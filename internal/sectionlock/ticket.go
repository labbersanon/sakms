package sectionlock

import (
	"encoding/json"
	"net/http"
	"time"
)

// CookieName is the unlock ticket cookie. Deliberately NOT the session
// cookie: two credentials, two names, two verifiers.
const CookieName = "sakms_unlock"

// HeaderName carries the PIN on X-Api-Key requests, which have no
// interactive unlock step.
//
// It travels in plaintext, accepted per the threat model — but in this
// deployment every container ships its logs to OpenObserve, so this header
// must NEVER be logged, and any future request-logging middleware must
// redact it by name.
const HeaderName = "X-Section-Pin"

// TicketTTL is ABSOLUTE and non-sliding. The app holds long-lived SSE
// streams (the notifications stream is opened at boot and held for the
// tab's lifetime), which would renew a sliding ticket forever — the
// operator would be permanently unlocked from one PIN entry.
//
// Non-sliding alone is not sufficient, though: it bounds a ticket's life,
// but does not re-lock an ALREADY-OPEN stream. That is what §4.5's
// per-stream re-check ticker is for.
const TicketTTL = 30 * time.Minute

// ticketType is the required "typ" discriminator.
const ticketType = "unlock"

// unlockAAD domain-separates the unlock ticket's ciphertext from every
// other ciphertext in the app under the same key.
//
// Why this is needed at all: internal/secrets seals with a nil AAD and no
// domain separation, and auth's sessionPayload is {Exp int64}. A naive
// unlock ticket of {exp} would be BYTE-INTERCHANGEABLE with a session
// cookie — copy sakms_session into sakms_unlock and the gate opens, with a
// 30-day expiry sailing straight through a 30-minute check.
//
// Versioned in the string so a future payload change can rotate the domain
// without touching key material.
const unlockAAD = "sakms-section-unlock-v1"

// AADEncryptor is the AAD-taking sibling of auth.TokenEncryptor — the same
// AES-256-GCM primitive, with additional authenticated data bound in.
//
// NOT YET IMPLEMENTED BY ANY PRODUCTION TYPE AS OF W0. W1 must add exactly
// these two methods to *secrets.Store, as NEW methods:
//
//	func (s *Store) EncryptWithAAD(plaintext string, aad []byte) (string, error)
//	func (s *Store) DecryptWithAAD(encoded string, aad []byte) (string, error)
//
// Each is a two-line delta from the existing Encrypt/Decrypt — pass aad as
// gcm.Seal/gcm.Open's final argument instead of nil. s.gcm is reachable
// from a method on *Store in that package, so the change is purely
// additive: no constructor change, no new field, no new key.
//
// Do NOT retrofit AAD onto the existing secrets.Store.Encrypt/Decrypt.
// Those two methods hold every already-stored ciphertext across eight
// packages (session cookies, the OIDC client secret, grabs, RSS feeds,
// connections, service connections, Trakt, webhooks); changing their AAD
// invalidates all of it at once — a forced logout plus a mid-upgrade OIDC
// redirect dance, which is the exact failure the type discriminator below
// is asymmetric to avoid.
type AADEncryptor interface {
	EncryptWithAAD(plaintext string, aad []byte) (string, error)
	DecryptWithAAD(encoded string, aad []byte) (string, error)
}

// ticketPayload is the unlock ticket's plaintext.
//
// Typ is what makes this NOT a session payload even before the AAD is
// considered. The discrimination is deliberately ASYMMETRIC:
//
//   - This verifier REQUIRES typ == "unlock".
//   - auth.ValidateToken REJECTS typ == "unlock" but treats an ABSENT typ
//     as a valid session (W1-a).
//
// Symmetric discrimination (requiring typ:"session") would invalidate
// every already-issued sakms_session on deploy.
type ticketPayload struct {
	Typ string `json:"typ"`
	Exp int64  `json:"exp"`
}

// IssueTicket mints an unlock ticket valid for TicketTTL from now,
// returning the opaque token and its absolute expiry.
func IssueTicket(enc AADEncryptor) (string, time.Time, error) {
	return issueTicketAt(enc, time.Now())
}

func issueTicketAt(enc AADEncryptor, now time.Time) (string, time.Time, error) {
	expiry := now.Add(TicketTTL)
	data, err := json.Marshal(ticketPayload{Typ: ticketType, Exp: expiry.Unix()})
	if err != nil {
		return "", time.Time{}, err
	}
	token, err := enc.EncryptWithAAD(string(data), []byte(unlockAAD))
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiry, nil
}

// ValidateTicket reports whether token is a genuine, unexpired unlock
// ticket, and returns its absolute expiry.
//
// Three independent things must all hold, and a failure of any one is
// indistinguishable to the caller:
//
//  1. It decrypts under the unlock AAD. A session cookie — sealed with a
//     nil AAD — fails here, at the crypto layer, before any field is read.
//  2. Its payload declares typ == "unlock".
//  3. It has not expired.
func ValidateTicket(enc AADEncryptor, token string) (time.Time, bool) {
	return validateTicketAt(enc, token, time.Now())
}

func validateTicketAt(enc AADEncryptor, token string, now time.Time) (time.Time, bool) {
	if token == "" {
		return time.Time{}, false
	}
	data, err := enc.DecryptWithAAD(token, []byte(unlockAAD))
	if err != nil {
		return time.Time{}, false
	}
	var payload ticketPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return time.Time{}, false
	}
	if payload.Typ != ticketType {
		return time.Time{}, false
	}
	expiry := time.Unix(payload.Exp, 0)
	if !now.Before(expiry) {
		return time.Time{}, false
	}
	return expiry, true
}

// SetUnlockCookie writes the unlock ticket cookie.
//
// It has NO Expires: the cookie dies when the browser closes, which is a
// second, independent bound on top of the ticket's own TTL. secure mirrors
// SetSessionCookie's per-mode logic — password mode passes false (a
// self-hosted instance on a trusted LAN is routinely plain HTTP), oidc
// mode passes true.
func SetUnlockCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	})
}

// ClearUnlockCookie drops the unlock cookie — manual lock, and logout.
func ClearUnlockCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: time.Unix(0, 0), MaxAge: -1,
	})
}

// TicketFrom returns the unlock ticket carried by r, or "" if absent.
func TicketFrom(r *http.Request) string {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// PinFrom returns the PIN header carried by r, or "" if absent.
func PinFrom(r *http.Request) string {
	return r.Header.Get(HeaderName)
}
