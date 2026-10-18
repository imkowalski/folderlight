package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed web/*
var assets embed.FS

type App struct {
	store   *Store
	indexer *Indexer
	cache   *ImageCache
	ctx     context.Context
}

func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func apiError(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}
func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/browse", a.browse)
	mux.HandleFunc("GET /api/search", a.browse)
	mux.HandleFunc("GET /api/tags", a.tags)
	mux.HandleFunc("GET /api/photo", func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.requestPhoto(w, r)
		if ok {
			jsonResponse(w, 200, p)
		}
	})
	mux.HandleFunc("GET /api/image", a.image)
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, a.indexer.snapshot()) })
	mux.HandleFunc("POST /api/rescan", func(w http.ResponseWriter, r *http.Request) {
		// Custom header and strict Origin checks prevent cross-site scan triggers.
		origin := r.Header.Get("Origin")
		if origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				apiError(w, 403, "cross-origin request rejected")
				return
			}
		}
		if r.Header.Get("X-Folderlight-Request") != "1" {
			apiError(w, 403, "missing scan request header")
			return
		}
		if !a.indexer.start(a.ctx, r.URL.Query().Get("force") == "1") {
			apiError(w, 409, "a scan is already running")
			return
		}
		jsonResponse(w, 202, a.indexer.snapshot())
	})
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
	})
	web, _ := fs.Sub(assets, "web")
	files := http.FileServer(http.FS(web))
	mux.Handle("GET /", files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})
}
func (a *App) requestPhoto(w http.ResponseWriter, r *http.Request) (Photo, bool) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id < 1 {
		apiError(w, 400, "invalid image id")
		return Photo{}, false
	}
	p, err := a.store.photo(r.Context(), id)
	if err != nil {
		apiError(w, 404, "image not found")
		return p, false
	}
	return p, true
}
func (a *App) browse(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	q := Query{Folder: v.Get("folder"), Search: strings.TrimSpace(v.Get("q")), Scope: v.Get("scope"), Sort: v.Get("sort"), Tag: v.Get("tag"), Page: 1, Limit: 60}
	if !validPath(q.Folder) || len(q.Search) > 512 || len(q.Tag) > 512 {
		apiError(w, 400, "invalid folder or search")
		return
	}
	if q.Scope == "" {
		q.Scope = "tree"
	}
	if q.Scope != "tree" && q.Scope != "folder" && q.Scope != "all" {
		apiError(w, 400, "invalid search scope")
		return
	}
	if q.Sort == "" {
		q.Sort = "name"
	}
	if q.Sort != "name" && q.Sort != "date" {
		apiError(w, 400, "invalid sort order")
		return
	}
	if v.Has("page") {
		p, e := strconv.Atoi(v.Get("page"))
		if e != nil || p < 1 || p > 10_000_000 {
			apiError(w, 400, "invalid page")
			return
		}
		q.Page = p
	}
	if v.Has("limit") {
		n, e := strconv.Atoi(v.Get("limit"))
		if e != nil || n < 1 || n > 120 {
			apiError(w, 400, "limit must be 1–120")
			return
		}
		q.Limit = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out, err := a.store.list(ctx, q)
	if err != nil {
		if errors.Is(err, errSearchSyntax) {
			apiError(w, 400, err.Error())
			return
		}
		log.Printf("browse: %v", err)
		apiError(w, 500, "could not query library")
		return
	}
	jsonResponse(w, 200, out)
}

func (a *App) tags(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	folder, prefix, scope := v.Get("folder"), v.Get("prefix"), v.Get("scope")
	if scope == "" {
		scope = "tree"
	}
	if !validPath(folder) || len(prefix) > 512 || (scope != "tree" && scope != "folder" && scope != "all") {
		apiError(w, 400, "invalid folder, prefix, or scope")
		return
	}
	limit := 8
	if v.Has("limit") {
		n, err := strconv.Atoi(v.Get("limit"))
		if err != nil || n < 1 || n > 20 {
			apiError(w, 400, "limit must be 1–20")
			return
		}
		limit = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tags, err := a.store.suggestTags(ctx, folder, scope, prefix, limit)
	if err != nil {
		log.Printf("tag suggestions: %v", err)
		apiError(w, 500, "could not load tags")
		return
	}
	jsonResponse(w, 200, map[string]any{"tags": tags})
}
func (a *App) image(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requestPhoto(w, r)
	if !ok {
		return
	}
	size := 480
	switch r.URL.Query().Get("size") {
	case "", "thumb":
	case "preview":
		size = 1600
	default:
		apiError(w, 400, "size must be thumb or preview")
		return
	}
	f, err := a.cache.get(r.Context(), p, size)
	if err != nil {
		status := 422
		if errors.Is(err, errQueueFull) {
			status = 503
			w.Header().Set("Retry-After", "2")
		}
		apiError(w, status, err.Error())
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		apiError(w, 500, "could not read cached image")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("ETag", `"`+cacheKey(p, size)+`"`)
	http.ServeContent(w, r, "preview.jpg", info.ModTime(), f)
}
