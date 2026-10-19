package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageRevalidationWithoutDiskCacheOrNAS(t *testing.T) {
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
	request := func(headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/image?id=1&size=preview", nil)
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	first := request(nil)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	precondition := request(map[string]string{"If-Match": `"wrong"`, "If-None-Match": etag})
	if precondition.Code != http.StatusPreconditionFailed {
		t.Fatal("If-Match precedence", precondition.Code)
	}
	files, _ := os.ReadDir(cache.dir)
	for _, file := range files {
		if err := os.Remove(filepath.Join(cache.dir, file.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(cfg.Library, cfg.Library+"-offline"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{etag, "W/" + etag, `"other", W/` + etag} {
		w := request(map[string]string{"If-None-Match": value})
		if w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("ETag") != etag || !strings.Contains(w.Header().Get("Cache-Control"), "private") {
			t.Fatal("revalidation performed unnecessary I/O", w.Code, w.Header(), w.Body.String())
		}
	}
	if w := request(nil); w.Code != 422 {
		t.Fatal("missing client bytes incorrectly treated as cached", w.Code)
	}
	// Password protection must run before even the fastest conditional path.
	r := httptest.NewRequest("POST", "/api/password", strings.NewReader(`{"password":"test password"}`))
	r.Header.Set("X-Folderlight-Request", "1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(map[string]string{"If-None-Match": etag}); w.Code != 401 {
		t.Fatal("conditional response bypassed authentication", w.Code)
	}
}

func TestImageETagMatching(t *testing.T) {
	for _, value := range []string{`"yes"`, `W/"yes"`, `"no", W/"yes"`} {
		if !matchesImageETag(value, `"yes"`) {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"", "*", `"no"`, `"prefix-yes"`, `W/yes`, `yes`} {
		if matchesImageETag(value, `"yes"`) {
			t.Fatal(value)
		}
	}
}
