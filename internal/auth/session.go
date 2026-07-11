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
// secure controls the cookie's Secure attribute. Password mode passes
// false: SAK's primary deployment is a self-hosted instance on a trusted
// LAN, often reached over plain HTTP the same way Radarr/Sonarr/Whisparr
// themselves are — forcing Secure would silently break the cookie (and
// therefore all login) for that entirely normal setup. Anyone exposing SAK
// beyond a trusted network should put a TLS-terminating reverse proxy in
// front of it, same guidance as those apps. OIDC mode passes true
// unconditionally instead: a redirect URL an external IdP can reach is, in
// every real deployment, already HTTPS, so there's no equivalent
// plain-HTTP case to preserve for it (Finding 2, 2026-07-11 OIDC security
// review).
func SetSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
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

// Middleware gates every request to next according to the instance's
// active auth mode (password/oidc/none) — meant to wrap the business-logic
// API mux only; the auth endpoints themselves (setup/login/logout/status,
// plus the public OIDC login/callback redirects) live on a separate,
// always-public mux that never passes through this (see
// internal/api.NewAuthMux), so there's no exemption list to keep in sync
// here.
//
// Dispatch order (deliberate, do not reorder):
//  1. Read the effective mode via store.AuthMode. Any error (not the
//     unset→"password" default, a genuine store failure) fails CLOSED
//     (500) before anything else runs — G1, "the store couldn't tell us"
//     must never be treated as a passing default.
//  2. "none" short-circuits here, before the key check below — so a
//     key-store hiccup can never 500 an explicitly no-auth mode.
//  3. The universal X-Api-Key header is checked next, INDEPENDENT of which
//     mode is active (Human Decision #2: the key works in every mode, not
//     just password) — finalAllow = keyOK || modeSpecificOK. This check
//     lives here, in Middleware's own body, deliberately NOT inside any
//     per-mode helper, so no future mode addition can accidentally scope it
//     to one mode.
//  4. Only if the key didn't pass does a mode-specific credential get
//     checked. Both "password" and "oidc" are cookie-ONLY (passwordAuth):
//     oidc mode, after the operator completes the IdP redirect dance, issues
//     the exact same signed session cookie password mode does (see
//     internal/api's oidcCallbackHandler), so the ongoing per-request check
//     is identical — a valid session cookie. A session cookie is honored
//     ONLY in these two branches, never in "none" (which short-circuited
//     above) or an unknown/corrupt mode (which fails closed).
//
// A mode-read or credential-check store error fails CLOSED (500), never
// falls through to allow.
func Middleware(enc TokenEncryptor, store *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode, err := store.AuthMode(r.Context())
		if err != nil { // G1 fail-closed
			http.Error(w, "authentication error", http.StatusInternalServerError)
			return
		}
		// none short-circuits BEFORE the key check so a key-store hiccup
		// can't 500 an explicitly no-auth mode. Represented here directly
		// (no separate noneAuth function) so a reader sees the ordering
		// guarantee in one place rather than hunting for it.
		if mode == ModeNone {
			next.ServeHTTP(w, r)
			return
		}
		// UNIVERSAL X-Api-Key credential, checked here in the top-level
		// body, independent of the mode switch — Human Decision #2. Do NOT
		// move this into any per-mode helper.
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
		// Mode-specific credential.
		var allowed bool
		switch mode {
		case ModePassword, ModeOIDC:
			// Both are cookie-only: oidc's callback issues the same session
			// cookie password mode does, so there is no separate per-request
			// check to add here.
			allowed = passwordAuth(enc, r)
		default: // unknown/corrupt mode → fail closed
			allowed = false
		}
		if allowed {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}

// passwordAuth is the cookie-only check shared by "password" and "oidc" modes
// (a session cookie is honored ONLY in those two branches, never in "none" or
// an unknown mode). The X-Api-Key path is deliberately NOT here — it is
// universal and lives in Middleware's own body above, checked before this
// helper ever runs.
func passwordAuth(enc TokenEncryptor, r *http.Request) bool {
	return Authenticated(enc, r)
}
