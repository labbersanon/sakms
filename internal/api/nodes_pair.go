package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/nodes"
)

const sseKeepaliveInterval = 30 * time.Second

// maxPairingNameLen bounds the operator-facing device name taken from the
// unauthenticated ?name= query parameter.
const maxPairingNameLen = 64

// Claude 2026-08-03: per-remote-host cap on pending pairings.
// Reason: GET /api/nodes/pair is unauthenticated BY DESIGN (a node has no
// credential until it pairs), and nodes.PairingRegistry caps the pending table
// globally at maxPendingPairings = 5 with no per-source bound. So any remote
// caller could open five concurrent pairing streams and hold the whole table,
// and every genuine node enrolment would then fail with 503 for as long as they
// kept the streams open — an unauthenticated denial of a real operator action.
// Troubleshooting: "too many pending pairings" on a first-time node setup with
// no other node enrolling.
// Review if: PairingRegistry gains its own per-source accounting, which would
// make this redundant.
//
// ONE pending pairing per host, not a larger quota: a host pairs one node at a
// time by construction — the agent opens a single stream and waits for the
// operator to approve it — so anything above one is already anomalous. Keyed on
// the transport-level peer address only; X-Forwarded-For is deliberately NOT
// consulted, exactly as sectionlock.Identity documents, because a
// caller-controlled value would let one source present unlimited distinct
// identities and evade the cap entirely.
//
// This bounds CONCURRENT pending entries, not the rate of attempts. A caller
// that opens and closes streams in a loop is not stopped by it — but it also
// never holds a slot, so it cannot deny enrolment, which is the property being
// protected.
type pairingHostLimiter struct {
	mu    sync.Mutex
	hosts map[string]int
}

func newPairingHostLimiter() *pairingHostLimiter {
	return &pairingHostLimiter{hosts: map[string]int{}}
}

// acquire reserves the slot for host, reporting false when one is already held.
func (l *pairingHostLimiter) acquire(host string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hosts[host] > 0 {
		return false
	}
	l.hosts[host]++
	return true
}

// release frees host's slot, deleting the entry so the map cannot grow without
// bound across many one-off callers.
func (l *pairingHostLimiter) release(host string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hosts[host] <= 1 {
		delete(l.hosts, host)
		return
	}
	l.hosts[host]--
}

// pairingRemoteHost is the transport-level peer address, with the ephemeral
// port stripped so repeated connections from one machine share a key.
func pairingRemoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// truncatePairingName bounds the unauthenticated device name.
//
// Length only — it does NOT sanitize the contents, deliberately. Every consumer
// escapes at its own boundary instead: the log line below uses %q (which
// escapes CR, LF and every other control character, so a crafted name cannot
// forge a second log entry), and the SSE payload is JSON-encoded by
// json.Marshal. Sanitizing here as well would mangle legitimate non-ASCII
// device names for no additional safety.
//
// Truncates by RUNE, not by byte, so a multi-byte character is never cut in
// half into invalid UTF-8 that a downstream JSON encoder would replace.
func truncatePairingName(name string) string {
	runes := []rune(name)
	if len(runes) <= maxPairingNameLen {
		return name
	}
	return string(runes[:maxPairingNameLen])
}

// PairStreamHandler handles GET /api/nodes/pair (no auth required).
// It opens a pre-auth SSE stream: emits "pending" immediately (pairing code +
// device name), then blocks until the operator approves (→ emits "config" and
// closes), the TTL expires, the operator rejects, or the client disconnects.
func PairStreamHandler(pairingReg *nodes.PairingRegistry) http.HandlerFunc {
	// One limiter per handler, captured in the closure — the same lifetime as
	// the registry it guards, and per-instance so tests do not share state.
	limiter := newPairingHostLimiter()
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		// Claimed BEFORE pairingReg.Register, so a capped caller never consumes
		// one of the five global slots even briefly.
		host := pairingRemoteHost(r)
		if !limiter.acquire(host) {
			http.Error(w, "a pairing is already pending for this host", http.StatusTooManyRequests)
			return
		}
		defer limiter.release(host)

		name := truncatePairingName(r.URL.Query().Get("name"))
		if name == "" {
			name = "unknown"
		}

		id, code, configCh, done, registered := pairingReg.Register(name)
		if !registered {
			http.Error(w, "too many pending pairings", http.StatusServiceUnavailable)
			return
		}
		defer pairingReg.Disconnect(id)

		// %q, never %s: name is an unauthenticated caller-controlled query
		// parameter, and %s would write its bytes verbatim — a name containing
		// CR/LF forges arbitrary extra lines into the log, which in this
		// deployment ships straight to OpenObserve and would let an attacker
		// fabricate entries attributed to this service. %q escapes them.
		log.Printf("nodes/pair: %q registered (code=%s)", name, code)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")

		pendingData, err := json.Marshal(struct {
			PairingCode string `json:"pairingCode"`
			DeviceName  string `json:"deviceName"`
		}{PairingCode: code, DeviceName: name})
		if err != nil {
			log.Printf("nodes/pair: marshal pending: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", nodes.EventPending, pendingData)
		flusher.Flush()

		ticker := time.NewTicker(sseKeepaliveInterval)
		defer ticker.Stop()
		ctx := r.Context()

		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				// TTL expired or operator rejected — close stream without config.
				return
			case cfg := <-configCh:
				cfgData, err := json.Marshal(cfg)
				if err != nil {
					log.Printf("nodes/pair: marshal config: %v", err)
					return
				}
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", nodes.EventConfig, cfgData)
				flusher.Flush()
				log.Printf("nodes/pair: %q approved (id=%s)", name, id)
				return
			case <-ticker.C:
				fmt.Fprintf(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	}
}
