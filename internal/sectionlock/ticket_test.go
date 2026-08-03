package sectionlock

import (
	"encoding/json"
	"testing"
	"time"
)

// SL-2: a session token is refused as an unlock ticket, and an unlock
// ticket is refused as a session cookie.
//
// # Why this test exists at all
//
// internal/secrets seals every ciphertext in the app with a NIL AAD under
// ONE key, and auth's session payload is {Exp int64}. A naive unlock
// ticket of {exp} would therefore be byte-interchangeable with a session
// cookie: copy sakms_session into sakms_unlock and the gate opens, with a
// 30-DAY expiry sailing straight through a 30-MINUTE check.
//
// Two independent mechanisms close that, and the sub-tests below assert
// each SEPARATELY on purpose. A single "the session token was rejected"
// assertion would pass if either mechanism were silently dropped — the
// likeliest such regression being a future refactor swapping
// DecryptWithAAD back to Decrypt for consistency with the rest of the app.
//
// # What is deferred to W1
//
// This file covers the sectionlock half in both directions. The second
// direction is covered here at the CRYPTO layer: an unlock ticket does not
// decrypt with the nil-AAD Decrypt that auth.ValidateToken calls first, so
// it is already unusable as a session cookie today. W1-a's amendment to
// auth.ValidateToken — reject typ=="unlock", treat an ABSENT typ as a
// valid session — is defence in depth on top of that, and its own test
// belongs in internal/auth. It cannot live here: W1-a makes internal/auth
// import this package, so importing auth from this test would be a cycle.
func TestUnlockTicketIsNotInterchangeableWithASessionCookie(t *testing.T) {
	crypto := newTestCrypto(t)

	// A session cookie exactly as auth.IssueToken produces one today:
	// nil AAD, payload {"exp":N}, 30 days out.
	sessionPayload, err := json.Marshal(struct {
		Exp int64 `json:"exp"`
	}{Exp: time.Now().Add(30 * 24 * time.Hour).Unix()})
	if err != nil {
		t.Fatalf("marshalling session payload: %v", err)
	}
	sessionToken, err := crypto.Encrypt(string(sessionPayload))
	if err != nil {
		t.Fatalf("sealing session token: %v", err)
	}

	t.Run("a session cookie is refused as an unlock ticket", func(t *testing.T) {
		if _, ok := ValidateTicket(crypto, sessionToken); ok {
			t.Fatal("a 30-day session cookie was accepted as a 30-minute unlock ticket")
		}
	})

	// The AAD binding, asserted on its own. This payload carries the
	// CORRECT typ, so the only thing that can reject it is the AAD — if
	// ValidateTicket ever calls the nil-AAD Decrypt, this fails and the
	// one above would not.
	t.Run("a correctly-typed payload sealed without the AAD is refused", func(t *testing.T) {
		data, err := json.Marshal(ticketPayload{Typ: ticketType, Exp: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatalf("marshalling: %v", err)
		}
		forged, err := crypto.Encrypt(string(data)) // nil AAD
		if err != nil {
			t.Fatalf("sealing: %v", err)
		}
		if _, ok := ValidateTicket(crypto, forged); ok {
			t.Fatal("a ticket-shaped payload sealed WITHOUT the unlock AAD was accepted — " +
				"ValidateTicket is not binding the AAD")
		}
	})

	// The type discriminator, asserted on its own. Correct AAD, wrong typ.
	t.Run("a wrongly-typed payload sealed WITH the AAD is refused", func(t *testing.T) {
		for _, typ := range []string{"", "session", "Unlock", "unlock2"} {
			data, err := json.Marshal(ticketPayload{Typ: typ, Exp: time.Now().Add(time.Hour).Unix()})
			if err != nil {
				t.Fatalf("marshalling: %v", err)
			}
			forged, err := crypto.EncryptWithAAD(string(data), []byte(unlockAAD))
			if err != nil {
				t.Fatalf("sealing: %v", err)
			}
			if _, ok := ValidateTicket(crypto, forged); ok {
				t.Errorf("a payload with typ=%q was accepted as an unlock ticket", typ)
			}
		}
	})

	// The reverse direction, at the crypto layer: what auth.ValidateToken
	// does first is a nil-AAD Decrypt, and that already fails.
	t.Run("an unlock ticket is refused as a session cookie", func(t *testing.T) {
		ticket, _, err := IssueTicket(crypto)
		if err != nil {
			t.Fatalf("IssueTicket: %v", err)
		}
		if _, err := crypto.Decrypt(ticket); err == nil {
			t.Fatal("an unlock ticket decrypted under the nil-AAD Decrypt that " +
				"auth.ValidateToken calls — it would be usable as a session cookie")
		}
	})
}

// A ticket minted now must verify now, and must report the same absolute
// expiry it was minted with.
func TestIssueAndValidateTicket(t *testing.T) {
	crypto := newTestCrypto(t)
	token, expiry, err := IssueTicket(crypto)
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}
	got, ok := ValidateTicket(crypto, token)
	if !ok {
		t.Fatal("a freshly issued ticket did not validate")
	}
	if !got.Equal(time.Unix(expiry.Unix(), 0)) {
		t.Fatalf("expiry = %v, want %v", got, expiry)
	}
	if d := time.Until(expiry); d > TicketTTL || d < TicketTTL-time.Minute {
		t.Fatalf("ticket TTL = %v, want ~%v", d, TicketTTL)
	}
}

// The TTL is ABSOLUTE and non-sliding: validating a ticket does not extend
// it. Without that, the notifications SSE stream — opened at boot and held
// for the tab's lifetime — would renew the ticket forever.
func TestTicketTTLIsAbsolute(t *testing.T) {
	crypto := newTestCrypto(t)
	issuedAt := time.Now()
	token, expiry, err := issueTicketAt(crypto, issuedAt)
	if err != nil {
		t.Fatalf("issueTicketAt: %v", err)
	}

	// Valid a minute before expiry, after many intervening validations.
	justBefore := expiry.Add(-time.Minute)
	for i := 0; i < 5; i++ {
		if _, ok := validateTicketAt(crypto, token, issuedAt.Add(time.Duration(i)*time.Minute)); !ok {
			t.Fatalf("ticket rejected %d minutes in, well inside its TTL", i)
		}
	}
	if _, ok := validateTicketAt(crypto, token, justBefore); !ok {
		t.Fatal("ticket rejected a minute before its expiry")
	}
	// Dead at expiry and after, no matter how often it was validated.
	if _, ok := validateTicketAt(crypto, token, expiry); ok {
		t.Fatal("ticket still valid exactly at its expiry")
	}
	if _, ok := validateTicketAt(crypto, token, expiry.Add(time.Second)); ok {
		t.Fatal("repeated validation slid the ticket's expiry forward")
	}
}

func TestValidateTicketRejectsGarbage(t *testing.T) {
	crypto := newTestCrypto(t)
	other := newTestCrypto(t)

	valid, _, err := IssueTicket(crypto)
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}

	// A ticket minted under a different key — a restored database without
	// its matching secret.key.
	if _, ok := ValidateTicket(crypto, mustIssue(t, other)); ok {
		t.Error("a ticket sealed under a different key validated")
	}
	for _, token := range []string{"", "not base64 at all", "AAAA", valid + "x"} {
		if _, ok := ValidateTicket(crypto, token); ok {
			t.Errorf("ValidateTicket(%q) accepted a malformed token", token)
		}
	}
}

func mustIssue(t *testing.T, enc AADEncryptor) string {
	t.Helper()
	token, _, err := IssueTicket(enc)
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}
	return token
}
