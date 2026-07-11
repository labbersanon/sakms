package auth

import (
	"context"
	"net/http"
	"testing"
)

// crossmode_test.go — cross-mode hardening for the universal X-Api-Key.

// TestMiddleware_EnvAPIKeyUniversal_AcrossModes proves the env-supplied
// SAKMS_API_KEY works in every mode, exactly like a settings-generated key.
// TestMiddleware_APIKeyWorksRegardlessOfMode already proves this for a
// settings-generated key; this test proves it specifically for the env key (a
// distinct code path — UseEnvAPIKey/envKeyHash, not EnsureAPIKey/the persisted
// hash) across two genuinely active modes (none and oidc). In oidc mode no
// session cookie is presented, so a pass can only be explained by the
// universal key check, never by the mode's own credential.
func TestMiddleware_EnvAPIKeyUniversal_AcrossModes(t *testing.T) {
	enc := testEncryptor(t)
	store := newTestStore(t)
	ctx := context.Background()

	store.UseEnvAPIKey("env-supplied-key-value")

	for _, mode := range []string{ModeNone, ModeOIDC} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			if err := store.SetAuthMode(ctx, mode); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			srv, called := middlewareTestServer(t, enc, store)
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
			req.Header.Set("X-Api-Key", "env-supplied-key-value")
			// Deliberately present NO session cookie even in oidc mode — a
			// pass must come from the universal env key, not from also
			// satisfying the mode's own credential.
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected the env-supplied X-Api-Key to pass in mode %q, got %d", mode, resp.StatusCode)
			}
			if !*called {
				t.Error("expected the inner handler to run for a valid env-supplied key")
			}
		})
	}
}
