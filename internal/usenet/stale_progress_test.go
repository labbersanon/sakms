package usenet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func setStaleProgressTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := usenetStaleProgressTimeout
	usenetStaleProgressTimeout = d
	t.Cleanup(func() { usenetStaleProgressTimeout = orig })
}

func TestAbortStaleDownloads_CancelsHungDownloading(t *testing.T) {
	setStaleProgressTimeout(t, 30*time.Millisecond)

	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})
	var canceled atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dl := &dlState{
		gid:            "nzb-stale-hang",
		name:           "Ancient Aliens",
		status:         "active",
		phase:          phaseDownloading,
		lastProgressAt: time.Now().Add(-time.Second),
		cancel: func() {
			canceled.Store(true)
			cancel()
		},
		gate: newPauseGate(),
	}
	m.mu.Lock()
	m.downloads[dl.gid] = dl
	m.mu.Unlock()

	m.abortStaleDownloads()
	if !canceled.Load() {
		t.Fatal("stale downloading job was not cancelled")
	}
	if ctx.Err() == nil {
		t.Fatal("download ctx still live after stale abort")
	}
	m.mu.Lock()
	aborted := dl.staleAbort
	m.mu.Unlock()
	if !aborted {
		t.Fatal("staleAbort flag not set")
	}

	// Second pass must not cancel again.
	canceled.Store(false)
	m.abortStaleDownloads()
	if canceled.Load() {
		t.Fatal("stale abort cancelled twice")
	}
}

func TestAbortStaleDownloads_SkipsPausedWaitingAndFresh(t *testing.T) {
	setStaleProgressTimeout(t, 30*time.Millisecond)

	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})
	var n atomic.Int32
	cancel := func() { n.Add(1) }
	stale := time.Now().Add(-time.Minute)
	cases := []*dlState{
		{gid: "nzb-paused", status: "paused", phase: phaseDownloading, lastProgressAt: stale, cancel: cancel, gate: newPauseGate()},
		{gid: "nzb-wait", status: "active", phase: phaseWaiting, lastProgressAt: stale, cancel: cancel, gate: newPauseGate()},
		{gid: "nzb-pre", status: "active", phase: phasePrecheck, lastProgressAt: stale, cancel: cancel, gate: newPauseGate()},
		{gid: "nzb-fresh", status: "active", phase: phaseDownloading, lastProgressAt: time.Now(), cancel: cancel, gate: newPauseGate()},
	}
	m.mu.Lock()
	for _, dl := range cases {
		m.downloads[dl.gid] = dl
	}
	m.mu.Unlock()

	m.abortStaleDownloads()
	if n.Load() != 0 {
		t.Fatalf("cancelled %d jobs, want 0", n.Load())
	}
}

func TestAbortStaleDownloads_DisabledWhenTimeoutZero(t *testing.T) {
	setStaleProgressTimeout(t, 0)

	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})
	var n atomic.Int32
	m.mu.Lock()
	m.downloads["nzb-off"] = &dlState{
		gid:            "nzb-off",
		status:         "active",
		phase:          phaseDownloading,
		lastProgressAt: time.Now().Add(-time.Hour),
		cancel:         func() { n.Add(1) },
		gate:           newPauseGate(),
	}
	m.mu.Unlock()
	m.abortStaleDownloads()
	if n.Load() != 0 {
		t.Fatal("timeout 0 must not cancel")
	}
}

func TestFireOnError_ParksTransportAfterStaleCancel(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})
	var got error
	m.SetOnError(func(_ string, failure error) { got = failure })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.fireOnError(ctx, "nzb-stale", ErrStaleProgress)
	if !errors.Is(got, ErrTransport) {
		t.Fatalf("got %v, want ErrTransport (sync park after ctx cancel)", got)
	}
	if errors.Is(got, context.Canceled) {
		t.Fatal("ErrStaleProgress must not classify as shutdown Canceled")
	}
}

func TestRunDownload_StaleAbortParksAsTransportNotShutdown(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})
	var (
		mu     sync.Mutex
		gotErr error
		fired  = make(chan struct{})
		once   sync.Once
	)
	m.SetOnError(func(_ string, failure error) {
		mu.Lock()
		gotErr = failure
		mu.Unlock()
		once.Do(func() { close(fired) })
	})

	gid := "nzb-stale-remap"
	sub := filepath.Join(m.StagingDir(), gid)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dl := &dlState{
		gid:        gid,
		name:       "hung",
		stagingDir: sub,
		status:     "active",
		phase:      phaseDownloading,
		cancel:     func() {},
		gate:       newPauseGate(),
		staleAbort: true,
	}
	m.mu.Lock()
	m.downloads[gid] = dl
	m.mu.Unlock()
	nzb := &NZB{Files: []NZBFile{{
		Subject: "test.mkv",
		Segs:    []NZBSegment{{Bytes: 10, Number: 1, MsgID: "<seg1@test>"}},
	}}}
	m.runDownload(ctx, gid, dl, nzb)

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("onError did not fire")
	}
	mu.Lock()
	defer mu.Unlock()
	if !errors.Is(gotErr, ErrTransport) {
		t.Fatalf("err = %v, want ErrTransport", gotErr)
	}
	if errors.Is(gotErr, context.Canceled) {
		t.Fatalf("err %v still Is Canceled — would 0s-resume into the hang", gotErr)
	}
	if !errors.Is(gotErr, ErrStaleProgress) {
		t.Fatalf("err = %v, want ErrStaleProgress", gotErr)
	}
}
