package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http/httptest"
	"testing"
)

func TestCompressedAssetsAndValidation(t *testing.T) {
	handler := staticAssets()
	request := func(encoding, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/app.js", nil)
		r.Header.Set("Accept-Encoding", encoding)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	plain := request("", "")
	compressed := request("br, gzip", "")
	if plain.Code != 200 || compressed.Code != 200 || compressed.Header().Get("Content-Encoding") != "gzip" || compressed.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatal(plain.Code, compressed.Code, compressed.Header())
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !bytes.Equal(data, plain.Body.Bytes()) {
		t.Fatal("compressed asset mismatch", err)
	}
	if compressed.Body.Len() >= plain.Body.Len()/2 {
		t.Fatal("asset compression ineffective")
	}
	notModified := request("gzip", compressed.Header().Get("ETag"))
	if notModified.Code != 304 || notModified.Body.Len() != 0 {
		t.Fatal("ETag validation", notModified.Code)
	}
	if request("gzip;q=0", "").Header().Get("Content-Encoding") != "" {
		t.Fatal("ignored gzip exclusion")
	}
	for value, want := range map[string]bool{"gzip": true, "gzip;q=0.5": true, "gzip;q=0, *;q=1": false, "*;q=0.5": true, "gzip;q=bad": false, "br": false} {
		if acceptsGzip(value) != want {
			t.Fatal(value)
		}
	}
}
