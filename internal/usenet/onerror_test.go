package usenet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// runFailingDownload drives runDownload for a download registered with the
// given status. No subscriptions are configured, so downloadAll always fails
// with ErrNoSubscriptions — no network, no fixtures.
func runFailingDownload(t *testing.T, m *Manager, gid, status string) {
	t.Helper()
	sub := filepath.Join(m.StagingDir(), gid)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dl := &dlState{gid: gid, name: "test", stagingDir: sub, status: status, cancel: cancel}
	m.mu.Lock()
	m.downloads[gid] = dl
	m.mu.Unlock()

	nzb := &NZB{Files: []NZBFile{{
		Subject: "test.mkv",
		Segs:    []NZBSegment{{Bytes: 10, Number: 1, MsgID: "<seg1@test>"}},
	}}}
	m.runDownload(context.Background(), gid, dl, nzb)
}

// TestSetOnError_FiresOnSegmentFailure proves the live fast-path callback runs
// when runDownload hits ErrNoSubscriptions.
func TestSetOnError_FiresOnSegmentFailure(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})

	var (
		mu     sync.Mutex
		gotGID string
		gotErr error
		fired  = make(chan struct{})
		once   sync.Once
	)
	m.SetOnError(func(gid string, failure error) {
		mu.Lock()
		gotGID = gid
		gotErr = failure
		mu.Unlock()
		once.Do(func() { close(fired) })
	})

	const gid = "nzb-onerror-test"
	runFailingDownload(t, m, gid, "active")

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("onError did not fire")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotGID != gid {
		t.Fatalf("gid = %q, want %q", gotGID, gid)
	}
	if !errors.Is(gotErr, ErrNoSubscriptions) {
		t.Fatalf("err = %v, want ErrNoSubscriptions", gotErr)
	}
}

func TestSetOnError_SkippedWhenPaused(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})

	fired := make(chan struct{}, 1)
	m.SetOnError(func(string, error) { fired <- struct{}{} })

	runFailingDownload(t, m, "nzb-paused", "paused")

	select {
	case <-fired:
		t.Fatal("onError must not fire for paused downloads")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSetOnError_NilCallbackNoPanic(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})
	runFailingDownload(t, m, "nzb-nil-cb", "active") // must not panic
}
