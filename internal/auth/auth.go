// Package auth manages SAK's single local login — one username +
// bcrypt-hashed password gating access to the API (and therefore every
// review workflow) — plus stateless signed session tokens (see session.go)
// so a browser doesn't need to resend credentials on every request.
//
// Single login, not a user table: SAK is a self-hosted, single-operator
// tool (see the design's trust model), so there is exactly one account, the
// same way Settings has exactly one AI provider — no per-user permissions
// to model.
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/crypto/bcrypt"

	"github.com/labbersanon/sakms/internal/oidcauth"
	"github.com/labbersanon/sakms/internal/settings"
)

const (
	usernameKey     = "auth_username"
	passwordHashKey = "auth_password_hash"
	authModeKey     = "auth_mode"
)

// The three auth strategies a first-run install can pick from and switch
// between later (see GET/PUT /api/auth/mode). The earlier "forward"
// (reverse-proxy shared secret) and "authentik" (RFC 7662 bearer
// introspection) modes were removed in favor of "oidc" — a real
// provider-agnostic OpenID Connect Authorization Code flow with PKCE where
// SAK is the Relying Party (see internal/oidcauth).
const (
	ModePassword = "password"
	ModeOIDC     = "oidc"
	ModeNone     = "none"
)

// ErrNotConfigured is returned by Verify when no login has been set up yet.
var ErrNotConfigured = errors.New("auth: no login configured yet")

// Store persists the single login's credentials in internal/settings' flat
// KV store — a username and a bcrypt hash are just two more scalar values,
// no schema of their own needed (the hash is already safe to store as
// plaintext-in-DB; that's the entire point of a one-way hash).
type Store struct {
	settings *settings.Store

	// enc decrypts the OIDC client secret (auth_oidc_client_secret_enc) — the
	// secret is encrypted at the API handler layer (which already holds the
	// same secretStore instance, see internal/api's authSetupHandler/
	// oidcPutHandler) and stored as ciphertext through settings.Set; this
	// Store only needs to decrypt it, at the point OIDCClient builds an
	// internal/oidcauth.Client. It is the same TokenEncryptor shape session
	// tokens already use (see session.go), not a second crypto primitive.
	enc TokenEncryptor

	// httpClient bounds every outbound call this Store makes (OIDC discovery,
	// token exchange, and JWKS fetch, via OIDCClient) — the caller (cmd/sakms)
	// supplies one wrapping the program's shared outboundTimeout, same
	// convention as every other external client in this program.
	httpClient *http.Client

	// envKeyHash/envKeySuffix hold an externally-supplied API key
	// (SAKMS_API_KEY) for this process's lifetime only — see
	// UseEnvAPIKey in apikey.go for why these are never persisted.
	// envKeyHash is nil unless UseEnvAPIKey has been called.
	envKeyHash   []byte
	envKeySuffix string

	// oidcCacheMu/oidcCache memoize the discovered *oidcauth.Client so the
	// public, unauthenticated /api/auth/oidc/{login,callback} routes don't
	// perform a live OIDC discovery (well-known + JWKS) fetch against the
	// IdP on every single hit — an attacker looping the public login
	// endpoint would otherwise be able to flood the configured IdP and
	// exhaust SAK's own outbound connections (Finding 1, 2026-07-11 OIDC
	// security review). Keyed by a fingerprint of the four config fields;
	// OIDCClient rebuilds automatically whenever they change (no separate
	// invalidation call needed from SetOIDCConfig — a changed fingerprint
	// is a cache miss on its own).
	oidcCacheMu sync.Mutex
	oidcCache   *oidcClientCache
}

// oidcClientCache pairs a discovered *oidcauth.Client with the exact config
// fingerprint it was built from (see OIDCClient).
type oidcClientCache struct {
	fingerprint string
	client      *oidcauth.Client
}

// New builds a Store. enc decrypts the OIDC client secret (pass the same
// secretStore already used elsewhere for at-rest encryption) and httpClient
// bounds the outbound OIDC calls (discovery, token exchange, JWKS) — both
// wired once in cmd/sakms/main.go. Middleware's own signature is unaffected
// by this — it still takes its own TokenEncryptor argument for session-cookie
// validation, orthogonal to this Store-level one.
func New(settingsStore *settings.Store, enc TokenEncryptor, httpClient *http.Client) *Store {
	return &Store{settings: settingsStore, enc: enc, httpClient: httpClient}
}

// Configured reports whether a login has been created yet — the API layer
// uses this to refuse a second SetCredentials call (see internal/api's
// setup handler): once an instance has an owner, a later unauthenticated
// visitor must not be able to silently take it over by "setting up" a new
// login of their own.
//
// Defined as auth_mode set OR auth_username set — NOT auth_mode alone. An
// auth_mode-only definition would be a migration/instance-takeover
// regression: every pre-existing install has auth_username set but no
// auth_mode row (that setting didn't exist yet), so it would report
// Configured=false, re-show "Create your login" on next boot, AND make the
// setup handler's already-configured 409 guard stop firing — letting an
// unauthenticated visitor re-POST /api/auth/setup and overwrite the owner's
// credentials. The OR keeps existing password installs correctly
// "configured" (effective mode defaults to "password", see AuthMode) while
// still marking a fresh none/oidc first-run choice as configured too, since
// those write auth_mode without ever writing auth_username.
func (s *Store) Configured(ctx context.Context) (bool, error) {
	if _, err := s.settings.Get(ctx, authModeKey); err == nil {
		return true, nil
	} else if !errors.Is(err, settings.ErrNotFound) {
		return false, err
	}
	_, err := s.settings.Get(ctx, usernameKey) // legacy/password path
	if errors.Is(err, settings.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// AuthMode returns the effective auth mode: the stored auth_mode value, or
// "password" when unset (settings.ErrNotFound) — every pre-existing install
// has no auth_mode row yet, and "password" is exactly what it was already
// doing, so this default requires no migration. Any OTHER read error is
// propagated as-is (fail-closed per G1): the caller (Middleware) must never
// treat "the store couldn't tell us" as "assume password" or any other
// passing default.
func (s *Store) AuthMode(ctx context.Context) (string, error) {
	v, err := s.settings.Get(ctx, authModeKey)
	if errors.Is(err, settings.ErrNotFound) {
		return ModePassword, nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// SetAuthMode persists the active auth mode. This is a raw write — the
// switch-into preconditions (a password hash must exist before switching to
// "password", oidc must have its config, "none" needs an explicit
// acknowledgement) live in the API handler layer
// (internal/api/authmode.go), not here, mirroring SetCredentials/Verify's
// existing split between storage and validation.
func (s *Store) SetAuthMode(ctx context.Context, mode string) error {
	return s.settings.Set(ctx, authModeKey, mode)
}

// PasswordConfigured reports whether a password hash exists, independent of
// which mode is currently active — used by the mode-switch handler's G4
// precondition for switching INTO "password" (switching away from password
// never clears the hash, so switching back doesn't require re-entering
// credentials).
func (s *Store) PasswordConfigured(ctx context.Context) (bool, error) {
	_, err := s.settings.Get(ctx, passwordHashKey)
	if errors.Is(err, settings.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// SetCredentials creates or replaces the single login.
func (s *Store) SetCredentials(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return errors.New("auth: username and password are both required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	if err := s.settings.Set(ctx, usernameKey, username); err != nil {
		return fmt.Errorf("saving username: %w", err)
	}
	if err := s.settings.Set(ctx, passwordHashKey, string(hash)); err != nil {
		return fmt.Errorf("saving password hash: %w", err)
	}
	return nil
}

// Verify reports whether username/password match the configured login.
// Username is compared in constant time so a failed login can't leak
// anything about the real username via response timing; the password check
// is bcrypt's own constant-time comparison. Returns ErrNotConfigured (not a
// false negative) when no login exists yet — a caller must be able to tell
// "wrong password" apart from "there's nothing to check against."
func (s *Store) Verify(ctx context.Context, username, password string) (bool, error) {
	wantUsername, err := s.settings.Get(ctx, usernameKey)
	if errors.Is(err, settings.ErrNotFound) {
		return false, ErrNotConfigured
	}
	if err != nil {
		return false, err
	}
	hash, err := s.settings.Get(ctx, passwordHashKey)
	if err != nil {
		return false, err
	}

	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(wantUsername)) == 1
	passwordMatch := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	return usernameMatch && passwordMatch, nil
}
