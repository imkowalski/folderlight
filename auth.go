package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const passwordIterations = 600000

type loginLimiter struct {
	mu       sync.Mutex
	window   time.Time
	attempts int
}

func (l *loginLimiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.window) > time.Minute {
		l.window = time.Now()
		l.attempts = 0
	}
	if l.attempts >= 10 {
		return false
	}
	l.attempts++
	return true
}
func (a *App) credential(r *http.Request) (string, error) {
	var value string
	err := a.store.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key='app_password'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}
func passwordHash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(salt) + ":" + base64.RawStdEncoding.EncodeToString(key), nil
}
func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, ":")
	if len(parts) != 2 {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[0])
	expected, e2 := base64.RawStdEncoding.DecodeString(parts[1])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(expected) != 32 {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	return err == nil && subtle.ConstantTimeCompare(key, expected) == 1
}
func sessionSignature(credential, payload string) string {
	mac := hmac.New(sha256.New, []byte(credential))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func loggedIn(r *http.Request, credential string) bool {
	if credential == "" {
		return true
	}
	cookie, err := r.Cookie("folderlight_session")
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 {
		return false
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() >= expiry || expiry > time.Now().Add(8*time.Hour+time.Minute).Unix() {
		return false
	}
	return hmac.Equal([]byte(parts[2]), []byte(sessionSignature(credential, parts[0]+"."+parts[1])))
}
func setSession(w http.ResponseWriter, r *http.Request, credential string) {
	cookie := &http.Cookie{Name: "folderlight_session", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"}
	if credential == "" {
		cookie.MaxAge = -1
	} else {
		nonce := make([]byte, 24)
		rand.Read(nonce)
		payload := strconv.FormatInt(time.Now().Add(8*time.Hour).Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
		cookie.Value = payload + "." + sessionSignature(credential, payload)
		cookie.MaxAge = 8 * 60 * 60
	}
	http.SetCookie(w, cookie)
}
func readPassword(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Password *string `json:"password"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || body.Password == nil || len(*body.Password) > 1024 {
		apiError(w, 400, "invalid password request")
		return "", false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		apiError(w, 400, "invalid password request")
		return "", false
	}
	return *body.Password, true
}
func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	credential, err := a.credential(r)
	if err != nil {
		apiError(w, 500, "could not read settings")
		return
	}
	out := map[string]any{"version": Version, "password_enabled": credential != "", "authenticated": loggedIn(r, credential)}
	if loggedIn(r, credential) {
		out["media_tools"] = mediaTools()
	}
	jsonResponse(w, 200, out)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !a.loginLimit.allow() {
		w.Header().Set("Retry-After", "60")
		apiError(w, 429, "too many login attempts; wait one minute")
		return
	}
	password, ok := readPassword(w, r)
	if !ok {
		return
	}
	credential, err := a.credential(r)
	if err != nil {
		apiError(w, 500, "could not read settings")
		return
	}
	if credential != "" && !verifyPassword(credential, password) {
		apiError(w, 401, "incorrect password")
		return
	}
	setSession(w, r, credential)
	jsonResponse(w, 200, map[string]bool{"authenticated": true})
}
func (a *App) savePassword(w http.ResponseWriter, r *http.Request) {
	old, err := a.credential(r)
	if err != nil {
		apiError(w, 500, "could not read settings")
		return
	}
	if !loggedIn(r, old) {
		apiError(w, 401, "sign in to change settings")
		return
	}
	password, ok := readPassword(w, r)
	if !ok {
		return
	}
	if password != "" && len([]rune(password)) < 8 {
		apiError(w, 400, "use at least 8 characters")
		return
	}
	credential := ""
	if password != "" {
		credential, err = passwordHash(password)
		if err != nil {
			apiError(w, 500, "could not secure password")
			return
		}
	}
	var result sql.Result
	if old != "" {
		result, err = a.store.db.ExecContext(r.Context(), `UPDATE settings SET value=? WHERE key='app_password' AND value=?`, credential, old)
	} else {
		result, err = a.store.db.ExecContext(r.Context(), `INSERT INTO settings(key,value) VALUES('app_password',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE settings.value=?`, credential, old)
	}
	if err != nil {
		apiError(w, 500, "could not save password")
		return
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		apiError(w, 409, "password changed; sign in again")
		return
	}
	setSession(w, r, credential)
	jsonResponse(w, 200, map[string]any{"version": Version, "password_enabled": credential != "", "authenticated": true, "media_tools": mediaTools()})
}
