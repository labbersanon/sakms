package auth

import (
	"context"
	"errors"

	"github.com/labbersanon/sakms/internal/oidcauth"
	"github.com/labbersanon/sakms/internal/settings"
)

// oidcFingerprint identifies exactly which config a cached *oidcauth.Client
// was discovered from. It's built from the same four raw fields
// SetOIDCConfig always writes together (the cipher, not the decrypted
// secret — a rotated secret changes the ciphertext too, so this still
// invalidates correctly without ever needing the plaintext). Not a security
// boundary, just a cheap cache key — a "\x00" separator is enough to avoid
// the ordinary ambiguous-concatenation cache-key bug (ab+c vs a+bc).
func oidcFingerprint(issuerURL, clientID, clientSecretCipher, redirectURL string) string {
	return issuerURL + "\x00" + clientID + "\x00" + clientSecretCipher + "\x00" + redirectURL
}

const (
	oidcIssuerURLKey       = "auth_oidc_issuer_url"
	oidcClientIDKey        = "auth_oidc_client_id"
	oidcClientSecretEncKey = "auth_oidc_client_secret_enc" // ciphertext (secretStore.Encrypt), NOT hashed — this is an outbound credential SAK presents to the IdP at token-exchange time, not a one-way local check like the password hash.
	oidcRedirectURLKey     = "auth_oidc_redirect_url"
)

// OIDCConfig returns the stored OIDC config: issuer URL, client id, the
// client secret's CIPHERTEXT (never decrypted here — see OIDCClient, the only
// caller that needs the plaintext, via this Store's own enc field), and the
// operator-supplied redirect URL. All fields are empty when nothing has been
// configured yet; a genuine settings-store error is returned as-is (fail
// closed per G1). Keyed off the issuer URL for the "unset" short-circuit,
// since all four fields are always written together by SetOIDCConfig.
func (s *Store) OIDCConfig(ctx context.Context) (issuerURL, clientID, clientSecretCipher, redirectURL string, err error) {
	issuerURL, err = s.settings.Get(ctx, oidcIssuerURLKey)
	if errors.Is(err, settings.ErrNotFound) {
		return "", "", "", "", nil
	} else if err != nil {
		return "", "", "", "", err
	}
	clientID, err = s.settings.Get(ctx, oidcClientIDKey)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return "", "", "", "", err
	}
	clientSecretCipher, err = s.settings.Get(ctx, oidcClientSecretEncKey)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return "", "", "", "", err
	}
	redirectURL, err = s.settings.Get(ctx, oidcRedirectURLKey)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return "", "", "", "", err
	}
	return issuerURL, clientID, clientSecretCipher, redirectURL, nil
}

// SetOIDCConfig persists all four fields, replacing whatever was there
// before. clientSecretCipher must already be encrypted (see internal/api's
// authSetupHandler/oidcPutHandler, which hold the secretStore this Store's
// own enc field decrypts with) — this Store never sees the plaintext at write
// time, only at OIDCClient's decrypt-to-exchange point.
func (s *Store) SetOIDCConfig(ctx context.Context, issuerURL, clientID, clientSecretCipher, redirectURL string) error {
	if err := s.settings.Set(ctx, oidcIssuerURLKey, issuerURL); err != nil {
		return err
	}
	if err := s.settings.Set(ctx, oidcClientIDKey, clientID); err != nil {
		return err
	}
	if err := s.settings.Set(ctx, oidcClientSecretEncKey, clientSecretCipher); err != nil {
		return err
	}
	return s.settings.Set(ctx, oidcRedirectURLKey, redirectURL)
}

// OIDCConfigured reports whether OIDC config has been set — the G4
// precondition for switching INTO "oidc" mode. Keyed off the client-secret
// ciphertext (one of the fields SetOIDCConfig always writes together).
func (s *Store) OIDCConfigured(ctx context.Context) (bool, error) {
	_, err := s.settings.Get(ctx, oidcClientSecretEncKey)
	if errors.Is(err, settings.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// OIDCClient returns an internal/oidcauth.Client for the stored, decrypted
// OIDC config — the one place the client secret's ciphertext
// (auth_oidc_client_secret_enc) is ever decrypted, using this Store's own enc
// field (see auth.go's New).
//
// Construction performs live discovery against the issuer (oidcauth.New), a
// real outbound call — but this method is reached from the PUBLIC,
// unauthenticated /api/auth/oidc/{login,callback} routes, so a fresh
// discovery fetch on every single request would let anyone loop the login
// endpoint to flood the configured IdP (Finding 1, 2026-07-11 security
// review). The discovered client is therefore memoized on the Store, keyed
// by an oidcFingerprint of the config it came from; a config change (new
// issuer, rotated secret, etc.) naturally produces a different fingerprint
// and forces a rebuild on the next call — no separate cache-invalidation
// call is needed from SetOIDCConfig.
//
// Return shape distinguishes two failure classes (mirroring the rest of this
// package): a nil client with a nil error means "oidc mode is active but has
// no config yet" — a legitimate not-configured state the caller turns into a
// plain client-facing error, not a 500. A non-nil error means a genuine fault
// (the settings store is broken, the stored ciphertext no longer decrypts, or
// discovery against the issuer failed).
func (s *Store) OIDCClient(ctx context.Context) (*oidcauth.Client, error) {
	issuerURL, clientID, cipher, redirectURL, err := s.OIDCConfig(ctx)
	if err != nil {
		return nil, err
	}
	if issuerURL == "" || clientID == "" || cipher == "" || redirectURL == "" {
		return nil, nil // not configured — not a store fault
	}

	fp := oidcFingerprint(issuerURL, clientID, cipher, redirectURL)

	s.oidcCacheMu.Lock()
	if s.oidcCache != nil && s.oidcCache.fingerprint == fp {
		client := s.oidcCache.client
		s.oidcCacheMu.Unlock()
		return client, nil
	}
	s.oidcCacheMu.Unlock()

	if s.enc == nil {
		return nil, errors.New("auth: no secret decryptor configured for oidc mode")
	}
	secret, err := s.enc.Decrypt(cipher)
	if err != nil {
		return nil, err
	}
	client, err := oidcauth.New(ctx, oidcauth.Config{
		IssuerURL:    issuerURL,
		ClientID:     clientID,
		ClientSecret: secret,
		RedirectURL:  redirectURL,
	}, s.httpClient)
	if err != nil {
		return nil, err
	}

	s.oidcCacheMu.Lock()
	s.oidcCache = &oidcClientCache{fingerprint: fp, client: client}
	s.oidcCacheMu.Unlock()

	return client, nil
}
