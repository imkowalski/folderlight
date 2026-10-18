package main

import (
	"context"
	"database/sql"
	"unicode/utf8"
)

type TagSuggestion struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// Migrate an existing index once; subsequent tag edits maintain counts inside
// the same SQLite transaction as image metadata. No source images are reread.
func (s *Store) initTagCatalog() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='tag_catalog'`).Scan(&exists); err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS tag_catalog(norm TEXT PRIMARY KEY,label TEXT NOT NULL,count INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS tag_popular ON tag_catalog(count DESC,norm);
CREATE TRIGGER IF NOT EXISTS tag_catalog_ai AFTER INSERT ON tags BEGIN
INSERT INTO tag_catalog(norm,label,count) VALUES(new.norm,new.label,1) ON CONFLICT(norm) DO UPDATE SET count=count+1; END;
CREATE TRIGGER IF NOT EXISTS tag_catalog_ad AFTER DELETE ON tags BEGIN
UPDATE tag_catalog SET count=count-1 WHERE norm=old.norm;
DELETE FROM tag_catalog WHERE norm=old.norm AND count<=0; END;`)
	if err != nil {
		return err
	}
	if exists == 0 {
		if _, err = tx.Exec(`INSERT INTO tag_catalog(norm,label,count) SELECT norm,min(label),count(*) FROM tags GROUP BY norm`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func prefixEnd(prefix string) string {
	r := []rune(prefix)
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] < utf8.MaxRune {
			r[i]++
			if r[i] >= 0xd800 && r[i] <= 0xdfff {
				r[i] = 0xe000
			}
			return string(r[:i+1])
		}
	}
	return ""
}

func (s *Store) suggestTags(ctx context.Context, folder, scope, prefix string, limit int) ([]TagSuggestion, error) {
	out := []TagSuggestion{}
	prefix = fold(prefix)
	global := scope == "all" || scope == "tree" && folder == ""
	column := "norm"
	if !global {
		column = "t.norm"
	}
	where := "1=1"
	args := []any{}
	if prefix != "" {
		where += " AND " + column + ">=?"
		args = append(args, prefix)
		if upper := prefixEnd(prefix); upper != "" {
			where += " AND " + column + "<?"
			args = append(args, upper)
		}
	}
	var rows *sql.Rows
	var err error
	if global {
		args = append(args, limit)
		rows, err = s.db.QueryContext(ctx, `SELECT label,count FROM tag_catalog WHERE `+where+` ORDER BY count DESC,norm LIMIT ?`, args...)
	} else {
		if scope == "folder" {
			where += ` AND t.image_id IN (SELECT id FROM images WHERE folder=?)`
		} else {
			where += ` AND t.image_id IN (SELECT image_id FROM ancestors WHERE folder=?)`
		}
		args = append(args, folder, limit)
		rows, err = s.db.QueryContext(ctx, `SELECT min(t.label),count(*) AS hits FROM tags t WHERE `+where+` GROUP BY t.norm ORDER BY hits DESC,t.norm LIMIT ?`, args...)
	}
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var t TagSuggestion
		if err = rows.Scan(&t.Tag, &t.Count); err != nil {
			return out, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
