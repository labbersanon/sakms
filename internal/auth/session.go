package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// CookieName is the session cookie the browser carries on every request
// after a successful login/setup.
const CookieName = "sakms_session"

// sessionTTL is long-lived on purpose: SAK is a single-operator,
// self-hosted tool where a forced re-login every few hours is friction with
// no real security benefit (the credential itself, not session length, is
// the actual boundary) — 30 days means "log in occasionally," not "log in
// every visit."
const sessionTTL = 30 * 24 * time.Hour

// TokenEncryptor is the same Encrypt/Decrypt shape internal/secrets.Store
// already provides for API-key-at-rest encryption — session tokens reuse it
// rather than a second crypto primitive: AES-256-GCM authenticated
// encryption means a tampered or expired token simply fails to decrypt (or
// fails its own expiry check after decrypting), with no separate
// signature-verification code path to get wrong.
type TokenEncryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(encoded string) (string, error)
}

type sessionPayload struct {
	Exp int64 `json:"exp"`
}

// IssueToken returns an opaque, tamper-evident session token (safe to use
// as a cookie value) valid for sessionTTL from now.
func IssueToken(enc TokenEncryptor) (string, error) {
	payload := sessionPayload{Exp: time.Now().Add(sessionTTL).Unix()}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return enc.Encrypt(string(data))
}

// ValidateToken reports whether token is a genuine, unexpired session
// token — false for anything tampered, corrupted, encrypted under a
// different key, or past its expiry.
func ValidateToken(enc TokenEncryptor, token string) bool {
	data, err := enc.Decrypt(token)
	if err != nil {
		return false
	}
	var payload sessionPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return false
	}
	return time.Now().Unix() < payload.Exp
}

// SetSessionCookie writes a fresh session cookie to w.
//
// Secure isn't set: SAK's primary deployment is a self-hosted instance
// on a trusted LAN, often reached over plain HTTP the same way Radarr/
// Sonarr/Whisparr themselves are — forcing Secure would silently break the
// cookie (and therefore all login) for that entirely normal setup. Anyone
// exposing SAK beyond a trusted network should put a TLS-terminating
// reverse proxy in front of it, same guidance as those apps.
func SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(sessionTTL),
	})
}

// ClearSessionCookie removes the session cookie (logout) — an Expires in
// the past is the standard way to tell the browser to drop a cookie
// immediately, since there's no server-side session state to invalidate.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: time.Unix(0, 0), MaxAge: -1,
	})
}

// Authenticated reports whether r carries a valid session cookie.
func Authenticated(enc TokenEncryptor, r *http.Request) bool {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return false
	}
	return ValidateToken(enc, cookie.Value)
}

// Middleware gates every request to next behind either a valid session
// cookie or a valid X-Api-Key header — meant to wrap the business-logic API
// mux only; the auth endpoints themselves (setup/login/logout/status) live
// on a separate, always-public mux that never passes through this (see
// internal/api.NewAuthMux), so there's no exemption list to keep in sync
// here.
//
// Cookie is checked first via the unchanged Authenticated — Authenticated
// itself is not modified, so authStatusHandler (internal/api/auth.go) stays
// cookie-only, unaware the key path exists. Only if there's no valid cookie
// does the X-Api-Key header get a look, verified via store.VerifyAPIKey.
//
// A store read error fails CLOSED (500), never falls through to allow —
// "the store couldn't tell us" must never be treated as "the key is fine".
func Middleware(enc TokenEncryptor, store *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Authenticated(enc, r) {
			next.ServeHTTP(w, r)
			return
		}
		presented := strings.TrimSpace(r.Header.Get("X-Api-Key"))
		if presented != "" {
			ok, err := store.VerifyAPIKey(r.Context(), presented)
			if err != nil {
				http.Error(w, "authentication error", http.StatusInternalServerError)
				return
			}
			if ok {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}
