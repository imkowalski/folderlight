package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

var staticOnce sync.Once
var staticHandler http.Handler

// Embedded assets are compressed once, rather than spending CPU compressing
// them per visitor. APIs and media keep their existing authenticated delivery.
func staticAssets() http.Handler {
	staticOnce.Do(func() {
		type asset struct {
			plain, compressed []byte
			contentType, etag string
		}
		files, _ := fs.Sub(assets, "web")
		fallback := http.FileServer(http.FS(files))
		saved := map[string]asset{}
		for _, name := range []string{"index.html", "app.js", "style.css", "favicon.svg"} {
			data, err := fs.ReadFile(files, name)
			if err != nil {
				continue
			}
			var output bytes.Buffer
			writer, _ := gzip.NewWriterLevel(&output, gzip.BestCompression)
			writer.Write(data)
			writer.Close()
			sum := sha256.Sum256(data)
			saved["/"+name] = asset{data, output.Bytes(), mime.TypeByExtension(path.Ext(name)), hex.EncodeToString(sum[:])}
		}
		staticHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.URL.Path
			if key == "/" {
				key = "/index.html"
			}
			file, ok := saved[key]
			if !ok {
				fallback.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Vary", "Accept-Encoding")
			w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
			w.Header().Set("Content-Type", file.contentType)
			body := file.plain
			suffix := "-identity"
			if acceptsGzip(r.Header.Get("Accept-Encoding")) {
				body = file.compressed
				suffix = "-gzip"
				w.Header().Set("Content-Encoding", "gzip")
			}
			w.Header().Set("ETag", `"`+file.etag+suffix+`"`)
			http.ServeContent(w, r, path.Base(key), time.Time{}, bytes.NewReader(body))
		})
	})
	return staticHandler
}
func acceptsGzip(value string) bool {
	wildcard := false
	for _, item := range strings.Split(strings.ToLower(value), ",") {
		parts := strings.Split(item, ";")
		name := strings.TrimSpace(parts[0])
		quality := 1.0
		for _, part := range parts[1:] {
			key, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if ok && key == "q" {
				q, err := strconv.ParseFloat(v, 64)
				if err != nil || q < 0 || q > 1 {
					quality = 0
				} else {
					quality = q
				}
			}
		}
		if name == "gzip" {
			return quality > 0
		}
		if name == "*" {
			wildcard = quality > 0
		}
	}
	return wildcard
}
