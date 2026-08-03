package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/auth"
	"github.com/labbersanon/sakms/internal/sectionlock"
)

// NewSectionLockMux returns the section PIN lock's own control surface.
//
// # Why this is a separate mux, and why cmd/sakms mounts it TWICE
//
// Same precedent as NewAPIKeyMux/NewAuthModeMux: it must be wrapped in
// auth.Middleware (primary login/API-key auth still applies to every route
// here) but it needs the gate, a dependency NewMux does not carry. The
// caller must register BOTH "/api/section-lock" and "/api/section-lock/" on
// the top-level mux — without the subtree form every path under it falls
// through to the more general "/api/" pattern and 404s inside NewMux's mux,
// which is not a failure any test of this file would catch.
//
// # "Exempt" here means exempt from the SECTION gate, not from auth
//
// Per §4.4's table, GET /status, POST /unlock and POST /lock are exempt from
// the path gate — a chicken-and-egg exemption, since the panel that manages
// the lock lives behind the `settings` lock and the frontend must be able to
// read lock state to draw badges while `settings` is locked. That exemption
// is implemented in sectionlock.Classify (which deliberately classifies
// /api/section-lock/* as no section at all), NOT here. Every route on this
// mux still requires a session cookie or the universal X-Api-Key.
//
// PUT /pin, DELETE /pin and PUT /sections are the gated ones, and their gate
// is the explicit currentPin check below rather than the path classifier —
// gating them by path would make a forgotten PIN unrecoverable through the
// API.
//
// gate must be the SAME *sectionlock.Gate instance cmd/sakms passes to
// auth.WithSectionGate. A second Gate (or a second Store under it) would
// have its own settings cache and its own brute-force counter: a PIN change
// made here would not be visible to the middleware until restart, and the
// lockout threshold would effectively double.
//
// disabled mirrors SAKMS_SECTION_LOCK_DISABLE. The middleware is disarmed by
// simply not installing a gate at all; this mux still needs the gate to read
// and write configuration, so it takes the flag explicitly instead.
func NewSectionLockMux(gate *sectionlock.Gate, authStore *auth.Store, disabled bool) *http.ServeMux {
	h := &sectionLockHandlers{gate: gate, auth: authStore, disabled: disabled}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/section-lock/status", h.status)
	mux.HandleFunc("POST /api/section-lock/unlock", h.unlock)
	mux.HandleFunc("POST /api/section-lock/lock", h.lock)
	mux.HandleFunc("PUT /api/section-lock/pin", h.setPin)
	mux.HandleFunc("DELETE /api/section-lock/pin", h.clearPin)
	mux.HandleFunc("PUT /api/section-lock/sections", h.setSections)
	return mux
}

type sectionLockHandlers struct {
	gate     *sectionlock.Gate
	auth     *auth.Store
	disabled bool
}

// sectionLockStatusResponse is what the panel and the sidebar badges read.
// The PIN itself — and its hash — never cross this boundary; pinSet is the
// only thing said about it.
type sectionLockStatusResponse struct {
	PinSet         bool     `json:"pinSet"`
	LockedSections []string `json:"lockedSections"`
	Unlocked       bool     `json:"unlocked"`
	// EnforcementAvailable is false when the lock cannot enforce anything on
	// this instance: SAKMS_SECTION_LOCK_DISABLE is set, or the instance runs
	// auth mode "none" (GATE-1 — the "none" banner already declares the
	// instance fully open, and there is no authenticated caller to bound a
	// config write, an unlock, or the failure counter). The panel renders
	// disabled on false.
	EnforcementAvailable bool `json:"enforcementAvailable"`
}

type sectionLockUnlockRequest struct {
	Pin string `json:"pin"`
}

// sectionLockPinRequest serves both PUT /pin (both fields) and DELETE /pin
// (CurrentPin only). Plain strings, not *string: these are write-or-nothing
// values, with none of the preserve/clear/set three-state semantics
// internal/apidto's Guardrail #5 reserves pointers for.
type sectionLockPinRequest struct {
	CurrentPin string `json:"currentPin"`
	NewPin     string `json:"newPin"`
}

type sectionLockSectionsRequest struct {
	CurrentPin string   `json:"currentPin"`
	Sections   []string `json:"sections"`
}

// status is exempt from the section gate and must stay answerable while
// everything else is locked.
//
// It DOES answer 500 on a corrupt section_lock_sections value rather than
// reporting an empty locked set, because reporting "nothing is locked" while
// the middleware denies every classified route would be a lie the panel
// cannot act on.
//
// That state stays recoverable through DELETE /pin, but read the condition
// precisely: with SAKMS_SECTION_LOCK_DISABLE=1 that route reads NEITHER
// settings key (authorizeChange short-circuits before any read, and
// Store.ClearPin only writes), so it works no matter how corrupt the stored
// values are. ARMED, it still reads PinHashKey to decide whether currentPin
// is required — so a corrupt sections value is recoverable while armed, and a
// failing READ of either key is not. The env var is the documented way out of
// the second case.
func (h *sectionLockHandlers) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mode, err := h.auth.AuthMode(ctx)
	if err != nil {
		log.Printf("section lock status (auth mode): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	store := h.gate.Store()
	pinSet, err := store.PinSet(ctx)
	if err != nil {
		log.Printf("section lock status (pin hash): %v", err)
		http.Error(w, "section lock configuration unavailable", http.StatusInternalServerError)
		return
	}
	locked, err := store.LockedSections(ctx)
	if err != nil {
		log.Printf("section lock status (locked sections): %v", err)
		http.Error(w, "section lock configuration unavailable", http.StatusInternalServerError)
		return
	}
	// Read the ticket directly rather than the Decision the middleware left
	// in the context: Decide returns early — before it ever looks at the
	// ticket — when nothing is locked, so the context Decision reports
	// Unlocked:false for a genuinely unlocked operator on an unlocked
	// instance. One AES-GCM open is cheaper than that inconsistency.
	_, unlocked := h.gate.ValidTicket(sectionlock.TicketFrom(r))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sectionLockStatusResponse{
		PinSet:               pinSet,
		LockedSections:       locked.Sorted(),
		Unlocked:             unlocked,
		EnforcementAvailable: !h.disabled && mode != auth.ModeNone,
	})
}

// unlock exchanges the PIN for the sakms_unlock ticket cookie. Exempt from
// the gate for the obvious reason: it is the way out of the gate.
func (h *sectionLockHandlers) unlock(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req sectionLockUnlockRequest
	if !decodeSectionLockBody(w, r, &req) {
		return
	}
	mode, ok := h.enforcingMode(w, r)
	if !ok {
		return
	}
	if err := h.gate.VerifyPin(ctx, sectionlock.Identity(r), req.Pin); err != nil {
		writeSectionLockPinError(w, err)
		return
	}
	token, _, err := h.gate.IssueTicket()
	if err != nil {
		log.Printf("section lock unlock (issuing ticket): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// secure mirrors SetSessionCookie's per-mode logic exactly: password mode
	// is routinely reached over plain HTTP on a trusted LAN, oidc mode never
	// is.
	sectionlock.SetUnlockCookie(w, token, mode == auth.ModeOIDC)
	w.WriteHeader(http.StatusNoContent)
}

// lock drops the ticket cookie. Never privileged — giving up a credential
// needs no credential — so it is exempt from every check on this file,
// including the auth-mode one.
//
// # Clearing the cookie is NOT sufficient, and that is what Revoke is for
//
// The unlock ticket is stateless: this route can delete the browser's copy,
// but it cannot reach a copy that has already been captured elsewhere. An
// open SSE stream is exactly that case — its *http.Request froze the cookie
// header at open time, so its re-check ticker keeps re-validating the ticket
// it captured and keeps finding it cryptographically valid and unexpired.
// Without the Revoke below, "Re-lock now" is silently ignored by every stream
// already running (QA-GATE-1 step 7). See sectionlock.Gate.Revoke.
func (h *sectionLockHandlers) lock(w http.ResponseWriter, _ *http.Request) {
	sectionlock.ClearUnlockCookie(w)
	h.gate.Revoke()
	w.WriteHeader(http.StatusNoContent)
}

func (h *sectionLockHandlers) setPin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req sectionLockPinRequest
	if !decodeSectionLockBody(w, r, &req) {
		return
	}
	if _, ok := h.enforcingMode(w, r); !ok {
		return
	}
	if !h.authorizeChange(w, r, req.CurrentPin, true) {
		return
	}
	if err := h.gate.Store().SetPin(ctx, req.NewPin); err != nil {
		if errors.Is(err, sectionlock.ErrPinTooShort) {
			http.Error(w, "PIN must be at least "+strconv.Itoa(sectionlock.MinPinLength)+" characters", http.StatusBadRequest)
			return
		}
		log.Printf("section lock set pin: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Claude 2026-08-03: revoke outstanding tickets on a PIN CHANGE, matching
	// what lock and clearPin already did.
	// Reason: changing the PIN is the natural response to "someone else learned
	// it". Leaving every already-issued ticket valid for the rest of its 30
	// minutes means the person who learned the old PIN keeps their access for
	// exactly as long as they would have had it anyway — the change accomplishes
	// nothing against the one threat that motivates it.
	// Troubleshooting: lock and clearPin both called Revoke; setPin was the only
	// one of the three that did not.
	// Review if: tickets ever start recording the PIN generation they were
	// issued under, which would make this implicit.
	//
	// Note what this does and does not reach, per Gate.Revoke's own doc: it
	// terminates streams that are already open, and it does NOT invalidate a
	// ticket's cryptographic validity — a browser still holding the cookie can
	// still authorize a fresh request until the ticket's own TTL runs out.
	// Store.SetPin has already changed the hash fingerprint, so the bcrypt memo
	// is invalidated independently and the OLD PIN stops working immediately.
	h.gate.Revoke()
	w.WriteHeader(http.StatusNoContent)
}

// clearPin removes the PIN and, in the same call, the locked-section set —
// Store.ClearPin does both, because a locked set with no PIN in existence is
// the one state no credential can clear.
//
// The disarm short-circuit inside authorizeChange runs BEFORE any
// configuration read, deliberately: this route is the documented way out of
// a corrupt bcrypt hash, so it must not be blocked by a read of the very
// value that is corrupt. ClearPin itself only writes.
func (h *sectionLockHandlers) clearPin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req sectionLockPinRequest
	if !decodeSectionLockBody(w, r, &req) {
		return
	}
	if _, ok := h.enforcingMode(w, r); !ok {
		return
	}
	if !h.authorizeChange(w, r, req.CurrentPin, true) {
		return
	}
	if err := h.gate.Store().ClearPin(ctx); err != nil {
		log.Printf("section lock clear pin: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The PIN that authorized every outstanding ticket no longer exists, so
	// those tickets should not outlive it (§4.5 names DELETE /pin alongside
	// POST /lock). The observable effect is smaller here than it looks, and
	// deliberately so: ClearPin also clears the locked set, so a stream's
	// re-check returns on the no-intersection branch before it ever consults
	// the epoch. Incremented anyway, because "clearing the PIN leaves live
	// tickets behind" would be false the day the two writes stop being
	// coupled.
	h.gate.Revoke()
	w.WriteHeader(http.StatusNoContent)
}

// setSections replaces the locked set.
//
// The 400 below is a RECOVERY INVARIANT, not input validation: locking any
// section while no PIN exists would make the gate deny with no credential in
// existence that could ever satisfy it — a brick reachable in one request.
func (h *sectionLockHandlers) setSections(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req sectionLockSectionsRequest
	if !decodeSectionLockBody(w, r, &req) {
		return
	}
	if _, ok := h.enforcingMode(w, r); !ok {
		return
	}
	store := h.gate.Store()
	pinSet, err := store.PinSet(ctx)
	if err != nil {
		log.Printf("section lock set sections (pin hash): %v", err)
		http.Error(w, "section lock configuration unavailable", http.StatusInternalServerError)
		return
	}
	if len(req.Sections) > 0 && !pinSet {
		http.Error(w, "set a PIN before locking any section — a locked section with no PIN in existence can never be unlocked", http.StatusBadRequest)
		return
	}
	// allowDisarm is FALSE here, unlike the two /pin routes: §6 names only
	// PUT /pin and DELETE /pin as skipping currentPin under
	// SAKMS_SECTION_LOCK_DISABLE, because those two are the corrupt-hash
	// recovery path. Changing which sections are locked is not.
	if !h.authorizeChange(w, r, req.CurrentPin, false) {
		return
	}
	// Claude 2026-08-03: submitted ids are now validated against the canonical
	// set; §5.2's preserve-unknown rule applies to STORAGE, not to writes.
	// Reason: nothing validated this, so "adult_content" or "Discover" (a typo
	// or a miscasing) was accepted with 204 and locked NOTHING — classification
	// only ever emits canonical ids, so the stored value gated no route at all.
	// An operator who ticks a box and gets a success response is entitled to
	// assume the section is locked; silently storing an inert id is the worst
	// possible outcome for a security control.
	// Troubleshooting: sectionlock.KnownSection existed with zero callers.
	// Review if: section ids ever become operator-defined rather than a fixed
	// canonical list.
	//
	// This does NOT contradict §5.2. That rule is about what happens to an id
	// ALREADY IN STORAGE that this build does not recognise — Store.LockedSections
	// keeps it, so a temporarily-removed tab re-locks if it returns. Preserving
	// what an older version legitimately wrote is a different question from
	// accepting a new write of an id no version ever emitted. Validate at the
	// boundary, tolerate in storage.
	if invalid := unknownSections(req.Sections); len(invalid) > 0 {
		http.Error(w, "unknown section ids: "+strings.Join(invalid, ", "), http.StatusBadRequest)
		return
	}
	if err := store.SetSections(ctx, req.Sections); err != nil {
		log.Printf("section lock set sections: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unknownSections returns the submitted ids this build does not recognise, in
// submission order and deduplicated, for the 400 message. Empty means the whole
// request is canonical.
func unknownSections(ids []string) []string {
	var invalid []string
	seen := map[string]bool{}
	for _, id := range ids {
		if sectionlock.KnownSection(id) || seen[id] {
			continue
		}
		seen[id] = true
		invalid = append(invalid, id)
	}
	return invalid
}

// enforcingMode returns the active auth mode, answering 409 when the lock is
// inert because the instance runs auth mode "none" (GATE-1, Option A: the
// panel renders disabled and the endpoints refuse rather than accepting
// configuration that would never be honoured).
//
// SAKMS_SECTION_LOCK_DISABLE deliberately does NOT refuse here — a disarmed
// instance must still be able to clear a corrupt PIN.
func (h *sectionLockHandlers) enforcingMode(w http.ResponseWriter, r *http.Request) (string, bool) {
	mode, err := h.auth.AuthMode(r.Context())
	if err != nil {
		log.Printf("section lock (auth mode): %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return "", false
	}
	if mode == auth.ModeNone {
		http.Error(w, "the section lock is inert in auth mode \"none\" — this instance is already fully open", http.StatusConflict)
		return "", false
	}
	return mode, true
}

// authorizeChange enforces §4.4's "currentPin when a PIN already exists" on
// the three gated control routes.
//
// The PIN is required from the BODY, even from a browser already holding a
// live unlock ticket: the ticket says "the operator entered the PIN
// recently", which is the right bar for reading Adult posters and the wrong
// one for rewriting the lock's own configuration.
//
// allowDisarm marks the two routes §6 lets SAKMS_SECTION_LOCK_DISABLE skip.
// The skip returns before any configuration read, so it survives the corrupt
// stored value it exists to recover from.
func (h *sectionLockHandlers) authorizeChange(w http.ResponseWriter, r *http.Request, currentPin string, allowDisarm bool) bool {
	if allowDisarm && h.disabled {
		return true
	}
	ctx := r.Context()
	pinSet, err := h.gate.Store().PinSet(ctx)
	if err != nil {
		log.Printf("section lock (reading pin state): %v", err)
		http.Error(w, "section lock configuration unavailable", http.StatusInternalServerError)
		return false
	}
	if !pinSet {
		// Nothing to re-authenticate against. Setting the first PIN needs no
		// PIN.
		return true
	}
	if err := h.gate.VerifyPin(ctx, sectionlock.Identity(r), currentPin); err != nil {
		writeSectionLockPinError(w, err)
		return false
	}
	return true
}

// decodeSectionLockBody decodes a JSON body, treating an EMPTY body as an
// empty request rather than an error — DELETE /pin legitimately carries no
// body at all when no PIN is set (or when the instance is disarmed).
func decodeSectionLockBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

// sectionLockLockoutResponse is the brute-force refusal.
//
// 429 rather than one more 403 section_locked because sectionlock's own
// LockedOutError exists so this surface "can answer distinguishably rather
// than reporting a lockout as though it were one more wrong PIN" — an
// operator who is locked out needs to be told to wait, not to try harder.
// The code is deliberately NOT section_locked: the frontend raises its PIN
// overlay on that exact string, and a PIN overlay is precisely what must not
// appear here.
type sectionLockLockoutResponse struct {
	Error      string `json:"error"`
	Code       string `json:"code"`
	RetryAfter int    `json:"retryAfter"`
}

// writeSectionLockPinError renders a failed PIN verification.
//
// A WRONG pin gets the same 403 body the path gate uses (§6 defines exactly
// one rejection shape), with an EMPTY section: routes.go classifies
// /api/section-lock/* as no section at all, so there is no section id to
// name — the credential being refused is the one guarding the lock's own
// configuration, not any one locked section.
func writeSectionLockPinError(w http.ResponseWriter, err error) {
	var lockedOut *sectionlock.LockedOutError
	switch {
	case errors.As(err, &lockedOut):
		retry := int(math.Ceil(lockedOut.RetryAfter.Seconds()))
		if retry < 1 {
			retry = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(sectionLockLockoutResponse{
			Error:      "too many failed PIN attempts",
			Code:       "section_lock_lockout",
			RetryAfter: retry,
		})
	case errors.Is(err, sectionlock.ErrNoPinPresented):
		http.Error(w, "a PIN is required", http.StatusBadRequest)
	case errors.Is(err, sectionlock.ErrNoPinSet):
		http.Error(w, "no PIN is set", http.StatusConflict)
	case errors.Is(err, sectionlock.ErrWrongPin):
		writeSectionLocked(w, "")
	default:
		// A configuration read failure. Fail closed, same as the middleware.
		log.Printf("section lock pin verification: %v", err)
		http.Error(w, "section lock configuration unavailable", http.StatusInternalServerError)
	}
}

// adultLocked reports whether this request must not see Adult material —
// Layer 3's read of the context Decision Layer 1 left behind, with no second
// decrypt or verify path (see sectionlock.RequireSection).
//
// It returns FALSE for every request that carried no Decision at all: a
// background scheduler, a disarmed instance, auth mode "none", or a test mux
// with no middleware. That is the same deliberate absent-allows rule Layers 2
// and 3 share, and it is safe for the same reason — Layer 1 is independent
// and primary.
func adultLocked(ctx context.Context) bool {
	return errors.Is(sectionlock.RequireAdult(ctx), sectionlock.ErrSectionLocked)
}

// denyIfAdultLocked is Layer 3's guard for a handler whose Adult scope lives
// in a row or a request body rather than in the URL — the one thing Layer 1's
// path rule structurally cannot see.
//
// It reports true when it has already written the 403, so a call site reads
// as a plain early return.
func denyIfAdultLocked(w http.ResponseWriter, r *http.Request) bool {
	if !adultLocked(r.Context()) {
		return false
	}
	writeSectionLocked(w, sectionlock.SectionAdultContent)
	return true
}

// writeSectionLocked mirrors auth's own rejection body byte for byte. It is
// duplicated rather than exported from internal/auth on purpose: the shape
// is four literal strings, and exporting it would widen that package's API
// for one caller.
func writeSectionLocked(w http.ResponseWriter, section string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   "section locked",
		"code":    "section_locked",
		"section": section,
	})
}
