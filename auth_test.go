package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGalleryPassword(t *testing.T) {
	cfg := testConfig(t)
	store := testStore(t, cfg)
	ctx := context.Background()
	cache, err := newCache(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.close()
	app := &App{store: store, indexer: &Indexer{store: store, config: cfg}, cache: cache, ctx: ctx}
	handler := app.handler()
	request := func(method, path, password string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		if password != "NO_BODY" {
			json.NewEncoder(&body).Encode(map[string]string{"password": password})
		}
		r := httptest.NewRequest(method, path, &body)
		r.Header.Set("X-Folderlight-Request", "1")
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/settings", "NO_BODY", nil); w.Code != 200 || !strings.Contains(w.Body.String(), Version) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("POST", "/api/password", "short", nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
	enabled := request("POST", "/api/password", "secret Ærø 123", nil)
	if enabled.Code != 200 {
		t.Fatal(enabled.Code, enabled.Body.String())
	}
	cookies := enabled.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal(cookies)
	}
	session := cookies[0]
	for _, body := range []string{`{}`, `{"password":null}`, `{"password":"12345678"}{"password":""}`} {
		r := httptest.NewRequest("POST", "/api/password", strings.NewReader(body))
		r.Header.Set("X-Folderlight-Request", "1")
		r.AddCookie(session)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid password body accepted: %s (%d)", body, w.Code)
		}
	}
	var stored string
	if err := store.db.QueryRow(`SELECT value FROM settings WHERE key='app_password'`).Scan(&stored); err != nil || strings.Contains(stored, "secret") {
		t.Fatal("password not hashed", err)
	}
	for _, path := range []string{"/api/browse", "/api/search", "/api/tags", "/api/photo?id=1", "/api/image?id=1", "/api/video?id=1", "/api/status", "/api/rescan", "/api/password", "/api/logout"} {
		method := "GET"
		if path == "/api/rescan" || path == "/api/password" || path == "/api/logout" {
			method = "POST"
		}
		if w := request(method, path, "NO_BODY", nil); w.Code != 401 {
			t.Fatalf("unprotected %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := request("POST", "/api/login", "wrong", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	login := request("POST", "/api/login", "secret Ærø 123", nil)
	if login.Code != 200 {
		t.Fatal(login.Code, login.Body.String())
	}
	if w := request("GET", "/api/browse", "NO_BODY", session); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	changed := request("POST", "/api/password", "another password", session)
	if changed.Code != 200 {
		t.Fatal(changed.Code, changed.Body.String())
	}
	if w := request("GET", "/api/browse", "NO_BODY", session); w.Code != 401 {
		t.Fatal("old session survived password change")
	}
	current := changed.Result().Cookies()[0]
	if w := request("POST", "/api/password", "", current); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := request("GET", "/api/browse", "NO_BODY", nil); w.Code != 200 {
		t.Fatal("password removal did not unlock")
	}
	r := httptest.NewRequest("POST", "/api/password", strings.NewReader(`{"password":"malicious"}`))
	r.Header.Set("X-Folderlight-Request", "1")
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin password change allowed")
	}
	r = httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"malicious"}`))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("login accepted without custom header")
	}
}
func TestSessionTamperingAndReset(t *testing.T) {
	hash, err := passwordHash("correct password")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword(hash, "correct password") || verifyPassword(hash, "wrong") {
		t.Fatal("password verification")
	}
	if verifyPassword("corrupt", "anything") {
		t.Fatal("invalid hash accepted")
	}
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	setSession(w, r, hash)
	cookie := w.Result().Cookies()[0]
	r.AddCookie(cookie)
	if !loggedIn(r, hash) {
		t.Fatal("valid session rejected")
	}
	bad := *cookie
	bad.Value += "x"
	rr := httptest.NewRequest("GET", "/", nil)
	rr.AddCookie(&bad)
	if loggedIn(rr, hash) {
		t.Fatal("tampered session accepted")
	}
	expired := *cookie
	payload := "1.nonce"
	expired.Value = payload + "." + sessionSignature(hash, payload)
	rr = httptest.NewRequest("GET", "/", nil)
	rr.AddCookie(&expired)
	if loggedIn(rr, hash) {
		t.Fatal("expired session accepted")
	}
	if loggedIn(r, hash+"reset") {
		t.Fatal("old credential accepted")
	}
	limiter := loginLimiter{}
	for range 10 {
		if !limiter.allow() {
			t.Fatal("rate limit too early")
		}
	}
	if limiter.allow() {
		t.Fatal("login rate limit missing")
	}
	limiter.window = time.Now().Add(-2 * time.Minute)
	if !limiter.allow() {
		t.Fatal("rate limiter did not recover")
	}
}
func TestRecoveryConfigWithoutNAS(t *testing.T) {
	cfg := testConfig(t)
	filename := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(cfg)
	os.WriteFile(filename, data, 0600)
	if err := os.Remove(cfg.Library); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(filename); err == nil {
		t.Fatal("normal startup allowed absent library")
	}
	if _, err := loadConfig(filename, true); err != nil {
		t.Fatal("password recovery requires NAS", err)
	}
}

func TestPasswordSurvivesRestart(t *testing.T) {
	cfg := testConfig(t)
	store := testStore(t, cfg)
	hash, err := passwordHash("persistent password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO settings VALUES('app_password',?)`, hash); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/browse", nil)
	w := httptest.NewRecorder()
	setSession(w, r, hash)
	cookie := w.Result().Cookies()[0]
	store.db.Close()
	reopened := testStore(t, cfg)
	app := &App{store: reopened}
	r.AddCookie(cookie)
	stored, err := app.credential(r)
	if err != nil || stored != hash || !loggedIn(r, stored) {
		t.Fatal("password/session lost on restart", err)
	}
	rr := httptest.NewRequest("GET", "/", nil)
	rr.Header.Set("X-Forwarded-Proto", "https")
	ww := httptest.NewRecorder()
	setSession(ww, rr, hash)
	if !ww.Result().Cookies()[0].Secure {
		t.Fatal("HTTPS proxy cookie not Secure")
	}
}
