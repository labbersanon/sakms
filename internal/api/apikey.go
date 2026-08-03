package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/labbersanon/sakms/internal/auth"
	"github.com/labbersanon/sakms/internal/sectionlock"
)

// NewAPIKeyMux returns the API-key management routes (status + regenerate).
// Kept on its own dedicated mux, separate from NewMux, for the same reason
// NewAuthMux is separate: these must be session-protected (unlike
// NewAuthMux's routes), but NewMux has 20 existing test call sites and the
// house convention is "NewMux stays unaware auth exists" — adding these
// routes here instead of to NewMux keeps that convention intact. cmd/sakms
// wraps this mux in the SAME auth.Middleware as NewMux's, so either a
// session cookie or a valid API key reaches it.
// gate may be nil (a disarmed instance, or a test with no section lock); the
// regenerate route degrades to its pre-lock behaviour in that case. When
// non-nil it MUST be the same *sectionlock.Gate cmd/sakms shares everywhere
// else — a second Gate carries its own brute-force counter, which is exactly
// the state this route has to move rather than duplicate.
func NewAPIKeyMux(authStore *auth.Store, gate *sectionlock.Gate) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apikey", apikeyStatusHandler(authStore))
	mux.HandleFunc("POST /api/apikey/regenerate", apikeyRegenerateHandler(authStore, gate))
	return mux
}

func apikeyStatusHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, err := authStore.APIKeyStatus(r.Context())
		if err != nil {
			log.Printf("apikey status: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	}
}

// apikeyRegenerateResponse is the one place the full API key crosses the
// API boundary — shown once, never retrievable again afterward.
type apikeyRegenerateResponse struct {
	APIKey    string `json:"apiKey"`
	KeySuffix string `json:"keySuffix"`
}

// Claude 2026-08-03: rotation now carries the section-lock brute-force counter
// onto the new key's identity.
// Reason: the plan's §14 R-7 justified leaving this route exempt from section
// gating with "no escalation, because a fresh key still needs X-Section-Pin".
// That reasoning was INCOMPLETE, and is corrected here rather than in the plan
// file. The lockout is keyed on the authenticating credential, so a fresh key
// is a fresh bucket with a zeroed failure count. An attacker holding a valid
// key could therefore spend four PIN guesses, rotate, and repeat forever —
// never reaching failureThreshold, and reducing a 6-digit PIN to a
// bcrypt-cost-bounded search. "A fresh key still needs X-Section-Pin" is true
// and was never the point; what mattered was that a fresh key also got a fresh
// ALLOWANCE.
// Troubleshooting: closes the rotation half of the brute-force bypass; the
// other half was sectionlock.Identity deriving its bucket from an unvalidated
// session cookie.
// Review if: this route ever becomes section-gated, which would close the
// vector on its own and make the transfer redundant — but read
// docs/break-glass-recovery.md first, because gating it would break the
// documented recovery path that must stay reachable WITHOUT a section PIN.
//
// Gating this route was considered and rejected for exactly that reason: it is
// how an operator recovers an instance whose PIN or auth state is unusable.
// Rate-limiting the rotation itself was also considered; transferring the
// counter is strictly better, because a rate limit still hands out a fresh
// allowance on every permitted rotation, which is the property being closed.
func apikeyRegenerateHandler(authStore *auth.Store, gate *sectionlock.Gate) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Captured BEFORE the rotation: this is the bucket the caller has been
		// accumulating failures against, resolved by auth.Middleware from the
		// credential that authenticated THIS request.
		previous := sectionlock.Identity(r)

		raw, keySuffix, err := authStore.Regenerate(r.Context())
		if errors.Is(err, auth.ErrEnvManaged) {
			http.Error(w, "API key is managed by the SAKMS_API_KEY environment variable; unset it to manage the key here", http.StatusConflict)
			return
		}
		if err != nil {
			log.Printf("apikey regenerate: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Move the counter onto the identity the new key will present under.
		// Transfer is a no-op when the caller has no failure history, so an
		// operator recovering through this route is entirely unaffected.
		if gate != nil {
			gate.TransferLockout(previous, sectionlock.KeyIdentity(raw))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apikeyRegenerateResponse{APIKey: raw, KeySuffix: keySuffix})
	}
}
