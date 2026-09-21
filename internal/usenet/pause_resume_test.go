package usenet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPauseGate_WaitBlocksUntilResume(t *testing.T) {
	g := newPauseGate()
	g.Pause()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- g.Wait(ctx) }()

	select {
	case <-done:
		t.Fatal("Wait returned while paused")
	case <-time.After(50 * time.Millisecond):
	}

	g.Resume()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait after Resume: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after Resume")
	}
}

func TestPauseGate_WaitRespectsContextCancel(t *testing.T) {
	g := newPauseGate()
	g.Pause()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Wait(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after cancel")
	}
}

func TestManager_PauseResume_NoCancel(t *testing.T) {
	dir := t.TempDir()
	m := New(Config{StagingDir: dir, MaxConcurrentDownloads: 1})

	sub := filepath.Join(dir, "nzb-pause1")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeOwnedMarker(sub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dl := &dlState{
		gid:        "nzb-pause1",
		name:       "test",
		stagingDir: sub,
		status:     "active",
		phase:      phaseDownloading,
		cancel:     cancel,
		gate:       newPauseGate(),
		addedAt:    time.Now(),
	}
	m.mu.Lock()
	m.downloads[dl.gid] = dl
	m.mu.Unlock()

	if err := m.Pause(dl.gid); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	got, err := m.FindByGID(dl.gid)
	if err != nil || got == nil || got.Status != "paused" {
		t.Fatalf("status after Pause = %+v err=%v", got, err)
	}
	if got.Phase != "" {
		t.Fatalf("phase after Pause = %q, want empty", got.Phase)
	}
	// Context must still be live (true pause).
	if ctx.Err() != nil {
		t.Fatalf("context cancelled on Pause: %v", ctx.Err())
	}

	if err := m.Resume(dl.gid); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	got, err = m.FindByGID(dl.gid)
	if err != nil || got == nil || got.Status != "active" {
		t.Fatalf("status after Resume = %+v err=%v", got, err)
	}
	if got.Phase != phaseDownloading {
		t.Fatalf("phase after Resume = %q, want %q", got.Phase, phaseDownloading)
	}
}

func TestManager_Resume_StagingGone(t *testing.T) {
	dir := t.TempDir()
	m := New(Config{StagingDir: dir, MaxConcurrentDownloads: 1})

	fired := make(chan error, 1)
	m.SetOnError(func(_ string, failure error) { fired <- failure })

	sub := filepath.Join(dir, "nzb-gone")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	dl := &dlState{
		gid:        "nzb-gone",
		name:       "test",
		stagingDir: sub, // never created
		status:     "paused",
		cancel:     cancel,
		gate:       newPauseGate(),
		addedAt:    time.Now(),
	}
	dl.gate.Pause()
	m.mu.Lock()
	m.downloads[dl.gid] = dl
	m.mu.Unlock()

	err := m.Resume(dl.gid)
	if !errors.Is(err, ErrStagingGone) {
		t.Fatalf("Resume = %v, want ErrStagingGone", err)
	}
	got, err := m.FindByGID(dl.gid)
	if err != nil || got == nil || got.Status != "error" {
		t.Fatalf("status after staging-gone Resume = %+v err=%v", got, err)
	}
	select {
	case onErr := <-fired:
		if !errors.Is(onErr, ErrStagingGone) {
			t.Fatalf("onError = %v, want ErrStagingGone", onErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onError did not fire for staging-gone Resume")
	}
}
