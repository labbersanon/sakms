package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/labbersanon/sakms/internal/auth"
	"github.com/labbersanon/sakms/internal/sectionlock"
)

// NewAuthModeMux returns the routes for reading and switching the active
// auth mode (GET/PUT /api/auth/mode). Kept on its own small mux, mirroring
// NewAPIKeyMux's precedent: switching auth modes mutates security-relevant
// state, so — unlike NewAuthMux's setup/login/logout/status routes, which
// must stay reachable without a session — this mux must be wrapped in
// auth.Middleware by the caller (cmd/sakms). A logged-in operator (or
// holder of the universal API key) both reads and writes here; a first-run
// visitor with neither cookie nor key never reaches it, which is why
// first-run mode selection goes through the public /api/auth/setup body
// instead (see the plan's §0.7 first-run bootstrap fix).
// gate and disabled carry §4.4's PIN requirement onto PUT /api/auth/mode.
// gate must be the SAME *sectionlock.Gate cmd/sakms passes to
// auth.WithSectionGate and api.NewSectionLockMux — a second one would have
// its own settings cache and its own brute-force counter. Passing nil leaves
// the route behaving exactly as it did before the section lock existed, which
// is what a test with no lock configured wants.
func NewAuthModeMux(authStore *auth.Store, gate *sectionlock.Gate, disabled bool) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/mode", getAuthModeHandler(authStore))
	mux.HandleFunc("PUT /api/auth/mode", putAuthModeHandler(authStore, gate, disabled))
	return mux
}

type authModeResponse struct {
	Mode string `json:"mode"`
}

func getAuthModeHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode, err := authStore.AuthMode(r.Context())
		if err != nil {
			log.Printf("auth mode status: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authModeResponse{Mode: mode})
	}
}

type authModeRequest struct {
	Mode string `json:"mode"`
	// AcknowledgeInsecure must be true to switch INTO "none" — the second
	// of G2's two required entry points (the first is /api/auth/setup's
	// first-run "none" branch).
	AcknowledgeInsecure bool `json:"acknowledgeInsecure"`
}

// putAuthModeHandler switches the active auth mode, enforcing G4's
// switch-into preconditions before writing anything:
//   - "password": a password hash must already exist (PasswordConfigured) —
//     otherwise a switch could strand the instance with no way back in.
//   - "oidc": issuer/client id/client secret/redirect URL must already exist
//     (OIDCConfigured) — reachable post-setup because the operator is already
//     authenticated some other way (password or the universal API key) by the
//     time they're switching modes from Settings.
//   - "none": requires acknowledgeInsecure:true (G2).
//
// On success, SetAuthMode writes ONLY auth_mode — the departed mode's
// config (e.g. a password hash) is never wiped, so switching away and back
// requires no reconfiguration (G4, AC6).
//
// # This route is PIN-gated, and it is the section lock's own disarm surface
//
// §4.4's critical row. Switching to auth mode "none" makes the section lock
// INERT (GATE-1), and before this gate existed that switch required only
// acknowledgeInsecure:true — so one request from any session cookie or API
// key permanently disarmed the whole feature, and the repo ships a
// copy-pasteable curl for it in docs/break-glass-recovery.md.
//
// The whole route is gated, not just the switch to "none". §4.4's table row
// is unqualified, and a "none"-only check would be a rule with an exception
// for a reader to get wrong later.
func putAuthModeHandler(authStore *auth.Store, gate *sectionlock.Gate, disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var req authModeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if !authorizeAuthModeChange(w, r, gate, disabled, authStore) {
			return
		}

		switch req.Mode {
		case auth.ModePassword:
			ok, err := authStore.PasswordConfigured(ctx)
			if err != nil {
				log.Printf("auth mode switch (password precondition): %v", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if !ok {
				http.Error(w, "password auth is not configured yet — set a password before switching to it", http.StatusBadRequest)
				return
			}
		case auth.ModeOIDC:
			ok, err := authStore.OIDCConfigured(ctx)
			if err != nil {
				log.Printf("auth mode switch (oidc precondition): %v", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if !ok {
				http.Error(w, "oidc auth is not configured yet — set issuer/client id/client secret/redirect URL before switching to it", http.StatusBadRequest)
				return
			}
		case auth.ModeNone:
			if !req.AcknowledgeInsecure {
				http.Error(w, "acknowledgeInsecure must be true to switch to the none auth mode", http.StatusBadRequest)
				return
			}
		default:
			http.Error(w, "unknown auth mode", http.StatusBadRequest)
			return
		}

		if err := authStore.SetAuthMode(ctx, req.Mode); err != nil {
			log.Printf("auth mode switch: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// authorizeAuthModeChange enforces §4.4's PIN requirement on PUT
// /api/auth/mode, reporting true when the request may proceed.
//
// # Header, not body
//
// The PIN arrives as X-Section-Pin, unlike /api/section-lock/*'s currentPin
// body field. "Gated on the PIN" is about STRENGTH, not transport, and the
// header is what the break-glass runbook's curl and OidcLogin.tsx's
// pre-session recovery form can both actually send — the recovery form has no
// session and posts a fixed body shape.
//
// # Four skips, each of which would otherwise brick a recovery path
//
//  1. No gate installed — the section lock is not part of this build/mount.
//  2. SAKMS_SECTION_LOCK_DISABLE. Checked BEFORE any configuration read, so
//     it survives the corrupt stored value it exists to recover from. This is
//     the documented way out of a forgotten PIN, and §4.4 leans on it
//     explicitly when it argues this gate adds inconvenience rather than a
//     new access class.
//  3. No PIN set — nothing to re-authenticate against, matching
//     sectionLockHandlers.authorizeChange.
//  4. Auth mode is already "none". The lock is inert there (GATE-1), so there
//     is nothing to disarm; gating would only make switching OUT of "none" —
//     the direction that RESTORES security — impossible.
func authorizeAuthModeChange(w http.ResponseWriter, r *http.Request, gate *sectionlock.Gate, disabled bool, authStore *auth.Store) bool {
	if gate == nil || disabled {
		return true
	}
	ctx := r.Context()
	mode, err := authStore.AuthMode(ctx)
	if err != nil {
		log.Printf("auth mode switch (section lock, reading auth mode): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if mode == auth.ModeNone {
		return true
	}
	pinSet, err := gate.Store().PinSet(ctx)
	if err != nil {
		log.Printf("auth mode switch (section lock, reading pin state): %v", err)
		http.Error(w, "section lock configuration unavailable", http.StatusInternalServerError)
		return false
	}
	if !pinSet {
		return true
	}
	if err := gate.VerifyPin(ctx, sectionlock.Identity(r), sectionlock.PinFrom(r)); err != nil {
		writeSectionLockPinError(w, err)
		return false
	}
	return true
}
