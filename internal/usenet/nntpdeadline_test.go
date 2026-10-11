package usenet

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

// TestFetchSegmentAny_HungBodyTimesOutAsTransport is the production hang:
// server accepts the TCP session and never sends the article. Without a
// socket deadline fetchSegmentAny blocks forever, dl.err stays nil, and the
// download slot never frees. With articleIOTimeout the read returns, the
// error classifies as ErrTransport, and parkUsenetTransportFailure can run.
func TestFetchSegmentAny_HungBodyTimesOutAsTransport(t *testing.T) {
	orig := articleIOTimeout
	articleIOTimeout = 200 * time.Millisecond
	t.Cleanup(func() { articleIOTimeout = orig })

	p := makePayload(t, 1, 256)
	srv := newFakeNNTP(t)
	srv.gateOn()
	srv.serveAll(p)

	m := New(Config{Servers: []ServerConfig{srv.cfgWith(1)}, StagingDir: t.TempDir()})
	start := time.Now()
	_, err := m.fetchSegmentAny(context.Background(), p.msgIDs[0])
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("hung BODY returned success, want transport timeout")
	}
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("got %v, want ErrTransport", err)
	}
	if !isTransportError(err) {
		t.Fatalf("isTransportError(%v) = false, want true so parkUsenetTransportFailure qualifies", err)
	}
	// 3 attempts + 250ms + 750ms retry sleeps ≈ 1.6s with 200ms deadline.
	if elapsed < 150*time.Millisecond {
		t.Fatalf("returned in %s — deadline did not engage", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("fetchSegmentAny still hung for %s after deadline", elapsed)
	}
}

// TestSetNNTPDeadline_AppliesToLiveConn proves the unexported-field helper
// actually reaches the net.Conn inside a real Tensai75/nntp session.
func TestSetNNTPDeadline_AppliesToLiveConn(t *testing.T) {
	srv := newFakeNNTP(t)
	p := newPool(srv.cfgWith(1))
	c, err := p.get()
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { p.put(c, false) })
	if err := setNNTPDeadline(c, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("setNNTPDeadline: %v", err)
	}
	if err := setNNTPDeadline(c, time.Time{}); err != nil {
		t.Fatalf("clear deadline: %v", err)
	}
}

// newBlackholePool returns a MaxConns=1 pool pointed at a listener that never
// Accepts, with nntpDialTimeout shortened to 200ms for the test.
func newBlackholePool(t *testing.T) *pool {
	t.Helper()
	orig := nntpDialTimeout
	nntpDialTimeout = 200 * time.Millisecond
	t.Cleanup(func() { nntpDialTimeout = orig })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port
	return newPool(ServerConfig{Host: "127.0.0.1", Port: port, MaxConns: 1})
}

func TestDial_UnacceptedTCPTimesOut(t *testing.T) {
	p := newBlackholePool(t)
	start := time.Now()
	_, err := p.dial(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("dial to a listener that never Accepts succeeded")
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("returned in %s — dial timeout did not engage", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("dial still hung for %s after timeout", elapsed)
	}
}

func TestGetCtx_DialTimeoutReleasesLiveToken(t *testing.T) {
	p := newBlackholePool(t)
	ctx := context.Background()
	if _, err := p.getCtx(ctx); err == nil {
		t.Fatal("first getCtx succeeded against a blackhole listener")
	}
	start := time.Now()
	if _, err := p.getCtx(ctx); err == nil {
		t.Fatal("second getCtx succeeded — live token was not released after dial timeout")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("second getCtx hung %s — live token leaked after dial timeout", time.Since(start))
	}
}

func TestIsTransportError_OSDeadlineExceeded(t *testing.T) {
	if !isTransportError(os.ErrDeadlineExceeded) {
		t.Fatal("os.ErrDeadlineExceeded must classify as transport (conn.SetDeadline)")
	}
	if isTransportError(context.DeadlineExceeded) {
		t.Fatal("context.DeadlineExceeded must stay non-transport (download cancel)")
	}
}
