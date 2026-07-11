package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

const (
	apikeyHashKey   = "auth_apikey_hash"   // hex SHA-256 of the active persisted key
	apikeySuffixKey = "auth_apikey_suffix" // last 4 chars, for masked display
)

// apikeyRawBytes matches secret.key's 32-byte precedent (internal/secrets,
// keySize) — no reason for the API key to carry less entropy than the
// at-rest encryption key it lives alongside.
const apikeyRawBytes = 32

// ErrEnvManaged is returned by Regenerate when SAKMS_API_KEY is active:
// env precedence (see activeKeyHash) would make a freshly regenerated
// settings key a silent no-op while the env var is set, and it would also
// be discarded on the next boot — so Regenerate refuses outright instead of
// writing a key that can never actually take effect.
var ErrEnvManaged = errors.New("auth: API key is managed by the SAKMS_API_KEY environment variable")

// APIKeyStatus backs the status endpoint, mirroring connections.Summary's
// shape: enough to render a masked "current key" line without ever
// exposing the key itself.
type APIKeyStatus struct {
	HasKey    bool   `json:"hasKey"`
	KeySuffix string `json:"keySuffix,omitempty"`
	Source    string `json:"source"` // "env" | "settings" | "none"
}

// newRandomKey generates a fresh API key: 32 bytes of crypto/rand,
// base64url-encoded with no padding (safe to put straight in a header or a
// UI text field with no escaping concerns).
func newRandomKey() (string, error) {
	raw := make([]byte, apikeyRawBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// hashKey returns the SHA-256 hash of raw. sha256.Sum256 returns a [32]byte
// array, and Go arrays aren't addressable when returned directly from a
// function call — sha256.Sum256(x)[:] doesn't compile — so the array is
// bound to a local first, then sliced.
func hashKey(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// suffix returns the last 4 runes of raw for masked display, or "" if raw
// has fewer than 4 runes (never expected in practice — newRandomKey always
// produces far more — but a masked display with nothing to show beats a
// panic on a short string).
func suffix(raw string) string {
	r := []rune(raw)
	if len(r) < 4 {
		return ""
	}
	return string(r[len(r)-4:])
}

// UseEnvAPIKey records an externally-supplied key (SAKMS_API_KEY) for the
// lifetime of this process. In-memory only: never persisted to settings.
// The key is supplied fresh on every boot by whoever sets the env var, and
// SAK's own server1 deployment wipes its DB roughly every 15 minutes — so
// persisting it would be pointless at best, and at worst would blur the
// env-vs-settings precedence this whole design depends on (see
// activeKeyHash).
func (s *Store) UseEnvAPIKey(raw string) {
	s.envKeyHash = hashKey(raw)
	s.envKeySuffix = suffix(raw)
}

// EnsureAPIKey is the no-env boot path. If a key hash already exists in
// settings, it's reused (returns "", nil). If none exists yet, a new key is
// generated and persisted (hash + suffix only — the raw key is never
// stored), and returned once so the caller (main.go) can log it. A second
// call after a fresh generation returns "" again, since the hash is now
// present.
func (s *Store) EnsureAPIKey(ctx context.Context) (rawIfGenerated string, err error) {
	_, err = s.settings.Get(ctx, apikeyHashKey)
	if err == nil {
		return "", nil
	}
	if !errors.Is(err, settings.ErrNotFound) {
		return "", err
	}

	raw, err := newRandomKey()
	if err != nil {
		return "", err
	}
	if err := s.persistKey(ctx, raw); err != nil {
		return "", err
	}
	return raw, nil
}

// Regenerate mints and persists a new key, returning it once. Refused with
// ErrEnvManaged while an env key is active (see ErrEnvManaged's doc).
func (s *Store) Regenerate(ctx context.Context) (raw, keySuffix string, err error) {
	if s.envKeyHash != nil {
		return "", "", ErrEnvManaged
	}
	raw, err = newRandomKey()
	if err != nil {
		return "", "", err
	}
	if err := s.persistKey(ctx, raw); err != nil {
		return "", "", err
	}
	// keySuffix is derived from raw directly, not re-read from settings — a
	// successful rotation (persistKey already committed) must never be lost
	// to an unrelated, later read failure. See the Phase 4 review that
	// caught the prior re-read design's key-loss window.
	return raw, suffix(raw), nil
}

// persistKey writes raw's hash + suffix to settings, replacing whatever was
// there before — the single write path shared by EnsureAPIKey's fresh-gen
// branch and Regenerate, so a previously stored key is invalidated the
// instant a new one lands (AC4).
func (s *Store) persistKey(ctx context.Context, raw string) error {
	if err := s.settings.Set(ctx, apikeyHashKey, hex.EncodeToString(hashKey(raw))); err != nil {
		return err
	}
	return s.settings.Set(ctx, apikeySuffixKey, suffix(raw))
}

// EnvKeyActive reports whether SAKMS_API_KEY was supplied at boot (see
// UseEnvAPIKey). When true, a freshly minted settings key would be dead on
// arrival — env precedence (activeKeyHash) makes the env hash win every
// verify — which is exactly why Regenerate refuses with ErrEnvManaged in this
// state. Callers that would otherwise mint must branch on this first.
func (s *Store) EnvKeyActive() bool {
	return s.envKeyHash != nil
}

// activeKeyHash resolves the hash currently in effect, preferring the
// in-memory env key over whatever's persisted in settings (env precedence —
// see UseEnvAPIKey and ErrEnvManaged). configured=false means no key is set
// at all yet, distinct from a settings-store read error, which the caller
// must fail closed on rather than treat as "not configured."
func (s *Store) activeKeyHash(ctx context.Context) (hash []byte, configured bool, err error) {
	if s.envKeyHash != nil {
		return s.envKeyHash, true, nil
	}
	hexHash, err := s.settings.Get(ctx, apikeyHashKey)
	if errors.Is(err, settings.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	decoded, err := hex.DecodeString(hexHash)
	if err != nil {
		return nil, false, err
	}
	return decoded, true, nil
}

// VerifyAPIKey constant-time-compares presented against the active key's
// hash. Guardrail-critical order, do not reorder:
//  1. presented is trimmed and an empty result is treated as absent, not
//     compared — otherwise an unconfigured store (want == nil, a zero-length
//     slice) could let subtle.ConstantTimeCompare("", "") == 1 false-pass an
//     empty presented key through as a "match".
//  2. the active hash is looked up; a genuine store error is returned to the
//     caller (which must fail closed — see auth.Middleware) rather than
//     treated as "no key".
//  3. "not configured" short-circuits to false — a presented key must never
//     verify against nothing.
//  4. only then is the constant-time comparison performed, and it is
//     mandatory: never replace this with a plain == comparison.
func (s *Store) VerifyAPIKey(ctx context.Context, presented string) (bool, error) {
	presented = strings.TrimSpace(presented)
	if presented == "" {
		return false, nil
	}
	want, configured, err := s.activeKeyHash(ctx)
	if err != nil {
		return false, err
	}
	if !configured {
		return false, nil
	}
	got := hashKey(presented)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// APIKeyStatus backs the status endpoint (mirrors connections.Summary's
// shape for connection status).
func (s *Store) APIKeyStatus(ctx context.Context) (APIKeyStatus, error) {
	if s.envKeyHash != nil {
		return APIKeyStatus{HasKey: true, KeySuffix: s.envKeySuffix, Source: "env"}, nil
	}
	_, err := s.settings.Get(ctx, apikeyHashKey)
	if errors.Is(err, settings.ErrNotFound) {
		return APIKeyStatus{HasKey: false, Source: "none"}, nil
	}
	if err != nil {
		return APIKeyStatus{}, err
	}
	suffixVal, err := s.settings.Get(ctx, apikeySuffixKey)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return APIKeyStatus{}, err
	}
	return APIKeyStatus{HasKey: true, KeySuffix: suffixVal, Source: "settings"}, nil
}
