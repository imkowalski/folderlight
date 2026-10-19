package main

import (
	"context"
	"image"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMobileImageProfiles(t *testing.T) {
	cfg := testConfig(t)
	store := testStore(t, cfg)
	writeImage(t, cfg, "photo.jpg", baseJPEG())
	scanNow(t, store, cfg, false)
	cache, err := newCache(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	app := &App{store: store, indexer: &Indexer{store: store, config: cfg}, cache: cache, ctx: context.Background()}
	handler := app.handler()
	p, _ := store.photo(context.Background(), 1)
	for profile, pixels := range map[string]int{"small": 240, "thumb": 480, "screen": 1024, "preview": 1600} {
		r := httptest.NewRequest("GET", "/api/image?id=1&size="+profile, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || !strings.Contains(w.Header().Get("Cache-Control"), "private") {
			t.Fatal(profile, w.Code, w.Header())
		}
		decoded, _, err := image.DecodeConfig(w.Body)
		if err != nil || decoded.Width > pixels || decoded.Height > pixels {
			t.Fatal(profile, decoded, err)
		}
		etag := w.Header().Get("ETag")
		if etag != `"`+cacheKey(p, pixels)+`"` {
			t.Fatal("profile cache collision", profile, etag)
		}
		r.Header.Set("If-None-Match", etag)
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 304 || w.Body.Len() != 0 {
			t.Fatal(profile, w.Code)
		}
	}
}
