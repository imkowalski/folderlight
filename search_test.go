package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestTagSearchSyntax(t *testing.T) {
	for _, tc := range []struct {
		input, text string
		tags        []string
	}{
		{"holiday 2025", "holiday 2025", []string{}},
		{"tag:Ærø", "", []string{"Ærø"}},
		{`TAG:"Family holiday" lake`, "lake", []string{"Family holiday"}},
		{`photo tag:Łódź tag:Ærø`, "photo", []string{"Łódź", "Ærø"}},
		{`tag:"Say \"hello\"" tag:"a\\b"`, "", []string{`Say "hello"`, `a\b`}},
		{"tag:Ærø tag:ÆRØ", "", []string{"Ærø"}},
		{"filename-tag:foo", "filename-tag:foo", []string{}},
	} {
		t.Run(tc.input, func(t *testing.T) {
			text, tags, err := parseSearch(tc.input)
			if err != nil || text != tc.text || !reflect.DeepEqual(tags, tc.tags) {
				t.Fatalf("%q %q %v", text, tags, err)
			}
		})
	}
	for _, input := range []string{"tag:", `tag:""`, `tag:"unterminated`, `tag:"Family"suffix`, `tag:"bad\q"`} {
		if _, _, err := parseSearch(input); !errors.Is(err, errSearchSyntax) {
			t.Errorf("malformed query accepted: %s (%v)", input, err)
		}
	}
}

func TestTagCatalogScopesUpdatesAndSyntax(t *testing.T) {
	c := testConfig(t)
	s := testStore(t, c)
	ctx := context.Background()
	for _, p := range []Photo{
		{Path: "Trip/lake1.jpg", Folder: "Trip", Name: "lake1.jpg", Tags: []string{"Ærø", "Family holiday"}},
		{Path: "Trip/Nested/lake2.jpg", Folder: "Trip/Nested", Name: "lake2.jpg", Tags: []string{"ÆRØ", "Łódź"}},
		{Path: "Elsewhere/lake3.jpg", Folder: "Elsewhere", Name: "lake3.jpg", Tags: []string{"Family holiday", "Unique"}},
	} {
		if err := s.put(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		folder, scope, prefix string
		want                  []TagSuggestion
	}{
		{"", "all", "ÆR", []TagSuggestion{{"Ærø", 2}}},
		{"Trip", "folder", "ær", []TagSuggestion{{"Ærø", 1}}},
		{"Trip", "tree", "ær", []TagSuggestion{{"ÆRØ", 2}}},
		{"Trip", "tree", "Ł", []TagSuggestion{{"Łódź", 1}}},
		{"Trip", "folder", "Unique", []TagSuggestion{}},
	} {
		got, err := s.suggestTags(ctx, tc.folder, tc.scope, tc.prefix, 8)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%+v: %+v %v", tc, got, err)
		}
	}
	for _, tc := range []struct {
		query string
		total int
	}{{"tag:Ærø", 2}, {`tag:"Family holiday"`, 2}, {`lake1 tag:Ærø tag:"Family holiday"`, 1}, {`tag:Ærø tag:Łódź`, 1}, {"tag:Family", 0}} {
		got, err := s.list(ctx, Query{Search: tc.query, Scope: "all", Page: 1, Limit: 60})
		if err != nil || got.Total != tc.total {
			t.Errorf("%s: %+v %v", tc.query, got, err)
		}
	}
	p, _ := s.photo(ctx, 1)
	p.Tags = []string{"New"}
	if err := s.put(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.suggestTags(ctx, "", "all", "ær", 8)
	if err != nil || len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("stale cache after replacement: %+v %v", got, err)
	}
	if _, err = s.db.Exec(`DELETE FROM images WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	got, err = s.suggestTags(ctx, "", "all", "ær", 8)
	if err != nil || len(got) != 0 {
		t.Fatalf("cascade did not clear tag cache: %+v %v", got, err)
	}
	_, err = s.db.Exec(`CREATE TRIGGER fail_tags BEFORE INSERT ON tags BEGIN SELECT RAISE(ABORT,'test rollback'); END`)
	if err != nil {
		t.Fatal(err)
	}
	p.Tags = []string{"Replacement"}
	if err = s.put(ctx, p); err == nil {
		t.Fatal("expected rollback")
	}
	got, err = s.suggestTags(ctx, "", "all", "New", 8)
	if err != nil || len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("cache escaped rollback: %+v %v", got, err)
	}
}

func TestTagCatalogMigrationAndRestart(t *testing.T) {
	c := testConfig(t)
	s, err := openStore(c.State)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.put(ctx, Photo{Path: "test.jpg", Name: "test.jpg", Tags: []string{"Ærø"}}); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`DROP TRIGGER tag_catalog_ai; DROP TRIGGER tag_catalog_ad; DROP TABLE tag_catalog;`)
	if err != nil {
		t.Fatal(err)
	}
	s.db.Close()
	for range 2 {
		s, err = openStore(c.State)
		if err != nil {
			t.Fatal(err)
		}
		got, e := s.suggestTags(ctx, "", "all", "ær", 8)
		if e != nil || !reflect.DeepEqual(got, []TagSuggestion{{"Ærø", 1}}) {
			t.Fatalf("migration/restart: %+v %v", got, e)
		}
		s.db.Close()
	}
}
