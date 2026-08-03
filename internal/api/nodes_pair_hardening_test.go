package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/labbersanon/sakms/internal/nodes"
)

// nodes_pair_hardening_test.go — GET /api/nodes/pair is unauthenticated BY
// DESIGN (a node holds no credential until it pairs), so everything it accepts
// is attacker-controlled.
//
// These run against a real httptest.Server rather than a ResponseRecorder: the
// handler is a long-lived SSE stream, so the property under test is what
// happens while one stream is STILL OPEN — which a recorder, which only
// observes a finished handler, cannot express.

// One remote host may hold only ONE pending pairing. Without this an
// unauthenticated caller fills the five-slot global table and denies every
// genuine node enrolment for as long as it keeps its streams open.
func TestPairStreamCapsPendingPairingsPerHost(t *testing.T) {
	srv := newPairServer(t)

	// The first stream is opened and LEFT OPEN, holding the slot.
	openPairStream(t, srv, "attacker")

	// A second attempt from the same host (httptest connects over loopback, so
	// every request in this test shares one remote host) is refused.
	resp, err := http.Get(srv.URL + "/api/nodes/pair?name=second")
	if err != nil {
		t.Fatalf("second pair request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a second concurrent pairing from one host = %d, want 429 — one "+
			"unauthenticated caller can otherwise hold every slot in the global pending "+
			"table and deny real node enrolment", resp.StatusCode)
	}
}

// The slot is released when the stream ends, so a node that reconnects after a
// dropped connection is not locked out of its own enrolment.
func TestPairStreamReleasesTheHostSlotOnDisconnect(t *testing.T) {
	srv := newPairServer(t)

	closeFirst := openPairStream(t, srv, "node")
	closeFirst()

	// The same host must be able to pair again. Retried briefly because the
	// server-side handler unwinds its defers asynchronously after the client
	// closes the connection.
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(srv.URL + "/api/nodes/pair?name=node-again")
		if err != nil {
			t.Fatalf("re-pair request: %v", err)
		}
		code := resp.StatusCode
		resp.Body.Close()
		if code == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a host could not re-pair after its own stream ended (last status %d) — "+
				"the slot is never released, so one dropped connection permanently locks a "+
				"node out of enrolment", code)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The cap must not consume a global slot: a refused caller must leave the
// registry untouched.
func TestRefusedPairingDoesNotConsumeAGlobalSlot(t *testing.T) {
	reg := nodes.NewPairingRegistry()
	srv := httptest.NewServer(PairStreamHandler(reg))
	t.Cleanup(srv.Close)

	openPairStream(t, srv, "first")
	for i := 0; i < 10; i++ {
		resp, err := http.Get(srv.URL + "/api/nodes/pair?name=spam")
		if err != nil {
			t.Fatalf("spam request %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("spam request %d = %d, want 429", i, resp.StatusCode)
		}
	}

	// The registry still has room: the refusals never reached Register. A
	// different host proves it, since the loopback host is legitimately capped.
	if _, _, _, _, ok := reg.Register("a-genuine-node"); !ok {
		t.Fatal("ten refused requests exhausted the global pending table — the per-host cap " +
			"is being applied AFTER Register, so a capped caller still burns a slot")
	}
}

// The unauthenticated ?name= must be length-bounded, and bounded by RUNE so a
// multi-byte name is never cut into invalid UTF-8.
func TestPairingNameIsBounded(t *testing.T) {
	t.Run("truncated to the cap", func(t *testing.T) {
		long := strings.Repeat("A", maxPairingNameLen*3)
		if got := truncatePairingName(long); len([]rune(got)) != maxPairingNameLen {
			t.Fatalf("truncated name is %d runes, want %d", len([]rune(got)), maxPairingNameLen)
		}
	})

	t.Run("a legitimate short name is untouched", func(t *testing.T) {
		if got := truncatePairingName("workstation-1"); got != "workstation-1" {
			t.Fatalf("truncatePairingName mangled a legitimate name: %q", got)
		}
	})

	t.Run("multi-byte runes are not cut in half", func(t *testing.T) {
		got := truncatePairingName(strings.Repeat("日", maxPairingNameLen+10))
		if len([]rune(got)) != maxPairingNameLen {
			t.Fatalf("truncated to %d runes, want %d", len([]rune(got)), maxPairingNameLen)
		}
		if !utf8.ValidString(got) {
			t.Fatal("truncation produced invalid UTF-8 — it is cutting bytes, not runes")
		}
	})
}

// A CRLF payload survives truncation (it is short), which is precisely why the
// log site must ESCAPE rather than rely on the length bound. %q is what does
// it; this pins the property that makes %q necessary.
func TestPairingNameCRLFIsEscapedNotStripped(t *testing.T) {
	forge := "ok\r\n2026/01/01 00:00:00 nodes/pair: admin approved"

	if got := truncatePairingName(forge); got != forge {
		t.Fatalf("truncatePairingName altered the value: %q — the length bound is not the "+
			"control that stops log forging, and must not be mistaken for it", got)
	}
	// %q renders the newlines as escapes, so the value cannot become a second
	// log line. %s would emit them raw.
	rendered := quoteForLog(forge)
	if strings.Contains(rendered, "\n") || strings.Contains(rendered, "\r") {
		t.Fatalf("the log rendering %q still contains a raw newline — an unauthenticated "+
			"caller can forge log entries attributed to this service", rendered)
	}
	if !strings.Contains(rendered, `\r\n`) {
		t.Fatalf("the log rendering %q does not escape the CRLF", rendered)
	}
}

// --- helpers ------------------------------------------------------------

// quoteForLog mirrors exactly what the handler's log.Printf %q verb does to the
// name, so the assertion is about the real rendering rather than a paraphrase.
func quoteForLog(s string) string { return fmt.Sprintf("%q", s) }

func newPairServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(PairStreamHandler(nodes.NewPairingRegistry()))
	t.Cleanup(srv.Close)
	return srv
}

// openPairStream opens a pairing stream and blocks until the server has emitted
// its "pending" event — which happens only AFTER the slot is held — so a
// follow-up request races nothing. The returned func closes the stream.
func openPairStream(t *testing.T, srv *httptest.Server, name string) func() {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dialing pair stream: %v", err)
	}
	closed := false
	closeFn := func() {
		if !closed {
			closed = true
			conn.Close()
		}
	}
	t.Cleanup(closeFn)

	if _, err := conn.Write([]byte("GET /api/nodes/pair?name=" + name + " HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatalf("writing pair request: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading pair stream (never saw the %q event): %v", nodes.EventPending, err)
		}
		if strings.Contains(line, "event: "+nodes.EventPending) {
			return closeFn
		}
	}
}
