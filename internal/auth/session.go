package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/sectionlock"
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

// unlockTicketType is the "typ" value sectionlock stamps on an unlock
// ticket. Duplicated here rather than imported (sectionlock keeps it
// unexported) — same precedent as sectionlock's own duplicated
// sessionCookieName. The two must stay in step; nothing enforces that
// beyond this comment and TestValidateToken_RejectsUnlockTicketPayload.
const unlockTicketType = "unlock"

// sessionPayload is the session cookie's plaintext.
//
// Typ is omitempty and IssueToken never sets it, so an issued session
// token's wire format is byte-identical to what it was before this field
// existed — {"exp":N}. That is deliberate: see ValidateToken.
type sessionPayload struct {
	Typ string `json:"typ,omitempty"`
	Exp int64  `json:"exp"`
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
// different key, past its expiry, or declaring itself an unlock ticket.
//
// # The type check is ASYMMETRIC, and that is the whole point
//
// It REJECTS typ == "unlock" but treats an ABSENT typ as a valid session.
// The symmetric rule — requiring typ == "session" — would invalidate every
// already-issued sakms_session the moment this ships, since none of them
// carry a typ field at all: a forced logout for every operator, and in oidc
// mode a redirect dance mid-upgrade. Do not "tidy" this into a symmetric
// check, and do not widen it to reject any non-empty typ.
//
// This is DEFENCE IN DEPTH, not the primary separation. A real unlock
// ticket is sealed with the section-unlock AAD and so fails in Decrypt
// above, at the crypto layer, before a field is read. This check is what
// catches the case that survives a future refactor swapping an AAD call
// back to the plain one for consistency — which is exactly why its test
// forges the payload with the NIL-AAD Encrypt rather than minting a real
// ticket.
func ValidateToken(enc TokenEncryptor, token string) bool {
	data, err := enc.Decrypt(token)
	if err != nil {
		return false
	}
	var payload sessionPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return false
	}
	if payload.Typ == unlockTicketType {
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

// MiddlewareOption configures Middleware's optional behaviour.
//
// Variadic rather than a widened signature on purpose: eight production
// call sites wrap Middleware (cmd/sakms/main.go's six mounts and
// internal/api/nodes_mux.go's two), and only the ones serving operator
// traffic want a section gate. A required parameter would force every one
// of them — plus every test that builds a middleware — to thread a nil.
type MiddlewareOption func(*middlewareConfig)

type middlewareConfig struct {
	// gate is nil when the section lock is not installed at all: no
	// SAKMS_SECTION_LOCK_DISABLE handling lives here, because a disarmed
	// instance simply never passes WithSectionGate. That keeps the env var
	// (and the boot-time decision to honour it) in cmd/sakms where the rest
	// of the wiring is.
	gate *sectionlock.Gate
}

// WithSectionGate installs the section PIN lock as Layer 1 — step 5 of
// Middleware's dispatch list. Without it Middleware behaves exactly as it
// did before the lock existed.
func WithSectionGate(gate *sectionlock.Gate) MiddlewareOption {
	return func(c *middlewareConfig) { c.gate = gate }
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
//     key-store hiccup can never 500 an explicitly no-auth mode. It is also
//     the ONLY exit that skips step 5's section gate, deliberately: the
//     lock is INERT in mode "none" (GATE-1), because the "none" banner
//     already declares the instance fully open and there is no
//     authenticated caller to bound a config write, an unlock, or the
//     failure counter. Adding a section check here would both reintroduce
//     the 500 this ordering exists to prevent and create an
//     unauthenticated remote-brick surface.
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
//  5. Only once primary auth has PASSED does the section PIN lock (Layer 1)
//     run — steps 3 and 4's three former success exits are collapsed into
//     the single gated exit this step guards, so no credential type can
//     route around it. The request path is classified into a set of
//     sections and denied with 403 section_locked if any member is locked
//     and no unlock ticket or PIN header was presented. Ordering matters:
//     primary auth answers 401 first, so an UNAUTHENTICATED caller never
//     learns anything about section-lock state. The resolved decision is
//     written into the request context for Layers 2 and 3 to read, so there
//     is no second decrypt/verify path anywhere. The GATE ITSELF is written
//     alongside it, for §4.5's one narrow exception — a long-lived SSE
//     handler re-checking whether its section was locked AFTER the stream
//     opened, which a frozen snapshot structurally cannot see. Only SSE
//     handlers may read it; see sectionlock.LiveLookup. Inactive unless
//     WithSectionGate installs a gate.
//
// # The lockout bucket is resolved between steps 4 and 5
//
// Once a credential has passed, the section lock's brute-force bucket is
// derived FROM THAT CREDENTIAL and attached to the request context
// (sectionlock.WithIdentity) — unconditionally, whether or not a gate is
// installed, because routes that verify a PIN outside the path gate read it
// too. sectionlock deliberately cannot re-derive it from the raw request:
// doing so was a real bypass, since a key-authenticated request carries an
// unvalidated session cookie the attacker chooses freely. See lockoutIdentity
// and sectionlock.Identity.
//
// A mode-read or credential-check store error fails CLOSED (500), never
// falls through to allow. So does a section-lock configuration read error,
// but only for a request a locked section could actually cover — a route
// that classifies as no section at all (the section-lock control surface
// itself, /api/auth/*, the SPA shell) still passes, because those are the
// routes the operator recovers THROUGH. The one other unrecoverable
// section-lock state — sections locked with no PIN in existence — answers
// 500 for the same reason, rather than the 403 a clearable denial gets.
func Middleware(enc TokenEncryptor, store *Store, next http.Handler, opts ...MiddlewareOption) http.Handler {
	var cfg middlewareConfig
	for _, opt := range opts {
		opt(&cfg)
	}
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
		// keyAuthed records WHICH credential passed, not merely that one did.
		// The section lock's brute-force bucket depends on it — see
		// lockoutIdentity.
		var allowed, keyAuthed bool
		presented := strings.TrimSpace(r.Header.Get("X-Api-Key"))
		if presented != "" {
			ok, err := store.VerifyAPIKey(r.Context(), presented)
			if err != nil {
				http.Error(w, "authentication error", http.StatusInternalServerError)
				return
			}
			allowed, keyAuthed = ok, ok
		}
		// Mode-specific credential, reached only if the key did not pass —
		// finalAllow is still keyOK || modeSpecificOK, unchanged.
		if !allowed {
			switch mode {
			case ModePassword, ModeOIDC:
				// Both are cookie-only: oidc's callback issues the same session
				// cookie password mode does, so there is no separate per-request
				// check to add here.
				allowed = passwordAuth(enc, r)
			default: // unknown/corrupt mode → fail closed
				allowed = false
			}
		}
		if !allowed {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		// THE ONE GATED SUCCESS EXIT. Every way of satisfying primary auth
		// converges here — the universal key and both cookie modes — so the
		// section-lock gate below cannot be routed around by adding a
		// credential type. The "none" return above is the deliberate fourth,
		// UNGATED exit; see step 2 of the dispatch list.
		//
		// The lockout bucket is resolved HERE, from whichever credential just
		// passed, and attached UNCONDITIONALLY — before the gate check, and
		// whether or not a gate is installed. Attaching it inside
		// serveSectionGated instead would be a silent hole: that function
		// returns early when no gate is configured, and the routes that verify
		// a PIN outside the path gate (the section-lock control surface, PUT
		// /api/auth/mode, POST /api/apikey/regenerate) would then fall back to
		// an IP bucket without anything failing.
		r = r.WithContext(sectionlock.WithIdentity(r.Context(), lockoutIdentity(enc, r, keyAuthed, presented)))
		serveSectionGated(cfg, w, r, next)
	})
}

// lockoutIdentity resolves the brute-force bucket from the credential that
// ACTUALLY authenticated r.
//
// # Branch on WHICH credential passed, never on what the request carries
//
// Middleware checks X-Api-Key first and skips the cookie check entirely when
// the key passes, so a key-authenticated request's cookie was never validated
// and may be pure attacker-chosen noise. Fingerprinting it anyway is the exact
// bug this function exists to prevent: it hands the caller a fresh bucket on
// every request and the failure counter never accumulates. keyAuthed is
// therefore the first branch, and the cookie is not consulted at all on it.
//
// The cookie is re-read (rather than threaded out of passwordAuth, which
// returns a bool) only on the branch where it is what authenticated — so by
// that line it has already passed ValidateToken.
func lockoutIdentity(enc TokenEncryptor, r *http.Request, keyAuthed bool, presented string) string {
	if keyAuthed {
		return sectionlock.KeyIdentity(presented)
	}
	if cookie, err := r.Cookie(CookieName); err == nil && ValidateToken(enc, cookie.Value) {
		return sectionlock.SessionIdentity(cookie.Value)
	}
	// Neither named credential is what let this request through. Unreachable
	// today (every branch above allowed is one of the two), but falling back to
	// the transport-level peer address keeps a future credential type bounded
	// by SOMETHING rather than silently unbucketed.
	return sectionlock.IPIdentity(r)
}

// serveSectionGated is step 5: the section PIN lock, running on the one
// success exit every primary-auth path converges to.
//
// # Classification comes from r.URL.Path, NEVER from r.PathValue
//
// http.ServeMux populates path values during its OWN ServeHTTP, which is
// after this middleware has run — so r.PathValue("mode") is "" here.
// Reading it would silently fail OPEN on every Adult route. Classify parses
// the URL's segments itself, and cleans them itself, so a traversal variant
// cannot classify differently from the path the mux will actually route.
func serveSectionGated(cfg middlewareConfig, w http.ResponseWriter, r *http.Request, next http.Handler) {
	if cfg.gate == nil {
		next.ServeHTTP(w, r)
		return
	}
	sections := sectionlock.Classify(r.URL.Path)
	decision, err := cfg.gate.Decide(r.Context(), sectionlock.Request{
		// Enforcing is unconditionally true here: mode "none" already
		// returned above, and a disarmed instance has no gate installed at
		// all — so by this line the lock is live by construction.
		Enforcing: true,
		Ticket:    sectionlock.TicketFrom(r),
		Pin:       sectionlock.PinFrom(r),
		Identity:  sectionlock.Identity(r),
	})
	if err != nil {
		// The lock configuration could not be read or parsed. Fail closed —
		// but only for a route a locked section could cover. An unclassified
		// route (the section-lock control surface, /api/auth/*, the SPA
		// shell) is exactly what the operator recovers THROUGH, so bricking
		// it here would remove the way out of the very state that produced
		// this error.
		//
		// 500, not 403 section_locked: no PIN can clear a corrupt stored
		// value, so answering section_locked would raise a frontend PIN
		// overlay that is guaranteed to fail forever.
		if len(sections) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "section lock unavailable", http.StatusInternalServerError)
		return
	}
	if !decision.Allows(sections) {
		// ErrNoPinSet is the same class of unrecoverable state as the read
		// error above, and gets the same answer for the same reason:
		// sections are locked with no PIN in existence, so NO credential can
		// clear it and a section_locked code would raise a frontend PIN
		// overlay guaranteed to fail forever. (Reachable only via a corrupt
		// bcrypt hash or a hand-edited database — the API refuses to create
		// it — and SAKMS_SECTION_LOCK_DISABLE=1 is the documented way out.)
		//
		// Every OTHER value of decision.Err — a wrong PIN, an active lockout
		// — IS clearable by presenting the right credential on a retry, so
		// those stay 403 section_locked.
		if errors.Is(decision.Err, sectionlock.ErrNoPinSet) {
			http.Error(w, "section lock unavailable", http.StatusInternalServerError)
			return
		}
		writeSectionLocked(w, decision.FirstLocked(sections))
		return
	}
	// The decision rides down in the context so Layers 2 (mode.Build) and 3
	// (the handlers Build never reaches) need no second verify path.
	// Forgetting WithContext here is a silent fail-open: those layers see an
	// absent decision, which ALLOWS by design.
	ctx := sectionlock.WithDecision(r.Context(), decision)
	// The gate itself rides down alongside it, for §4.5's ONE narrow
	// exception: a long-lived SSE handler re-checking whether its section
	// has been locked SINCE the stream opened, which a frozen snapshot
	// structurally cannot see. Attached on this line — including the
	// nothing-is-locked path — precisely because that is the state a
	// re-lock starts from. See sectionlock.LiveLookup for why this is not a
	// second verify path.
	ctx = sectionlock.WithLive(ctx, cfg.gate)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// writeSectionLocked renders the rejection body.
//
// 403 rather than 401 is a documented deviation from "rejected exactly as
// an unauthenticated one would be": the frontend reboots the SPA on any
// non-/api/auth/ 401, so a 401 here would log the operator out or
// boot-loop. The confidentiality property survives through ORDERING (step 5
// runs only after primary auth answered 401). The "code" field is what the
// frontend keys on to raise a PIN overlay instead — do not change it
// without changing client.ts.
func writeSectionLocked(w http.ResponseWriter, section string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   "section locked",
		"code":    "section_locked",
		"section": section,
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
