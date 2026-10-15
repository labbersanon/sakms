package usenet

import (
	"context"
	"errors"
	"testing"
)

// TestStatArticleAll_PresentOnSecondPool proves a 223 anywhere wins even when
// an earlier pool answered 430.
func TestStatArticleAll_PresentOnSecondPool(t *testing.T) {
	srvA := newFakeNNTP(t)
	srvA.addStatus("miss@a", 430)
	srvB := newFakeNNTP(t)
	srvB.add("found@b", []byte("body"))

	m := New(Config{Servers: []ServerConfig{srvA.cfgWith(1), srvB.cfgWith(1)}, StagingDir: t.TempDir()})
	hs := m.HeaderSource()
	if hs == nil {
		t.Fatal("HeaderSource() returned nil")
	}
	v, err := hs.StatArticleAll(context.Background(), "found@b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != StatPresent {
		t.Fatalf("verdict = %v, want StatPresent", v)
	}
}

// TestStatArticleAll_AllPools430_IsMissing proves every pool answering 430
// (and no busy/transport problem anywhere) yields StatMissing.
func TestStatArticleAll_AllPools430_IsMissing(t *testing.T) {
	srvA := newFakeNNTP(t)
	srvA.addStatus("gone@x", 430)
	srvB := newFakeNNTP(t)
	srvB.addStatus("gone@x", 430)

	m := New(Config{Servers: []ServerConfig{srvA.cfgWith(1), srvB.cfgWith(1)}, StagingDir: t.TempDir()})
	hs := m.HeaderSource()
	v, err := hs.StatArticleAll(context.Background(), "gone@x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != StatMissing {
		t.Fatalf("verdict = %v, want StatMissing", v)
	}
}

// TestStatArticleAll_OnePool451_IsMissing proves a single 451 (removed/DMCA)
// with every other pool answering 430 is still a confirmed Missing — matching
// Manager.statArticleAny's "sawRemoved && otherErr==nil" semantics.
func TestStatArticleAll_OnePool451_IsMissing(t *testing.T) {
	srvA := newFakeNNTP(t)
	srvA.addStatus("dmca@x", 451)
	srvB := newFakeNNTP(t)
	srvB.addStatus("dmca@x", 430)

	m := New(Config{Servers: []ServerConfig{srvA.cfgWith(1), srvB.cfgWith(1)}, StagingDir: t.TempDir()})
	hs := m.HeaderSource()
	v, err := hs.StatArticleAll(context.Background(), "dmca@x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != StatMissing {
		t.Fatalf("verdict = %v, want StatMissing", v)
	}
}

// TestStatArticleAll_OneBusyOnePresent_IsPresent proves present still wins
// even when another pool is busy (present short-circuits before busy matters).
func TestStatArticleAll_OneBusyOnePresent_IsPresent(t *testing.T) {
	srvA := newFakeNNTP(t)
	pA := newPool(srvA.cfgWith(1))
	busyConn, err := pA.get() // holds the only live slot — tryGet on this pool returns ErrBusy
	if err != nil {
		t.Fatalf("priming busy pool: %v", err)
	}
	t.Cleanup(func() { pA.put(busyConn, false) })

	srvB := newFakeNNTP(t)
	srvB.add("found@b", []byte("body"))

	m := New(Config{Servers: []ServerConfig{srvB.cfgWith(1)}, StagingDir: t.TempDir()})
	m.pools = []*pool{pA, m.pools[0]}

	hs := m.HeaderSource()
	v, err := hs.StatArticleAll(context.Background(), "found@b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != StatPresent {
		t.Fatalf("verdict = %v, want StatPresent", v)
	}
}

// TestStatArticleAll_OneBusyOne430_IsInconclusive proves a busy pool mixed
// with a plain 430 elsewhere never resolves to Missing (provider load must
// never manifest as an index deletion).
func TestStatArticleAll_OneBusyOne430_IsInconclusive(t *testing.T) {
	srvA := newFakeNNTP(t)
	pA := newPool(srvA.cfgWith(1))
	busyConn, err := pA.get()
	if err != nil {
		t.Fatalf("priming busy pool: %v", err)
	}
	t.Cleanup(func() { pA.put(busyConn, false) })

	srvB := newFakeNNTP(t)
	srvB.addStatus("x@b", 430)

	m := New(Config{Servers: []ServerConfig{srvB.cfgWith(1)}, StagingDir: t.TempDir()})
	m.pools = []*pool{pA, m.pools[0]}

	hs := m.HeaderSource()
	v, err := hs.StatArticleAll(context.Background(), "x@b")
	if err == nil {
		t.Fatal("expected a non-nil error alongside StatInconclusive (ErrBusy)")
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if v != StatInconclusive {
		t.Fatalf("verdict = %v, want StatInconclusive", v)
	}
}

// TestStatArticleAll_TransportError_IsInconclusiveAndDiscardsConn proves a
// mid-STAT transport failure yields Inconclusive and the connection is
// discarded (keep=false), not returned to the idle pool.
func TestStatArticleAll_TransportError_IsInconclusiveAndDiscardsConn(t *testing.T) {
	srv := newFakeNNTPDropping(t, 1) // drops the first STAT/BODY mid-response
	m := New(Config{Servers: []ServerConfig{srv.cfgWith(1)}, StagingDir: t.TempDir()})
	hs := m.HeaderSource()
	v, err := hs.StatArticleAll(context.Background(), "whatever@x")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if v != StatInconclusive {
		t.Fatalf("verdict = %v, want StatInconclusive", v)
	}
	p := m.pools[0]
	select {
	case <-p.idle:
		t.Fatal("connection was returned to idle after a transport error — want discarded")
	default:
	}
}
