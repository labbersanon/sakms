package usenet

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func registerFinalizeDL(t *testing.T, m *Manager, gid string) *dlState {
	t.Helper()
	sub := filepath.Join(m.StagingDir(), gid)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dl := &dlState{gid: gid, name: "test", stagingDir: sub, status: "active", cancel: cancel}
	m.mu.Lock()
	m.downloads[gid] = dl
	m.mu.Unlock()
	return dl
}

// writeJunkPar2 writes a file with PAR2 magic that fails Parse/Verify.
func writeJunkPar2(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("PAR2\x00PKT incomplete"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeAssembled_PAR2FailUnpackExtracts_MarksComplete(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})
	dl := registerFinalizeDL(t, m, "nzb-finalize-ok")

	par2 := filepath.Join(dl.stagingDir, "set.par2")
	rar := filepath.Join(dl.stagingDir, "set.part01.rar")
	writeJunkPar2(t, par2)
	if err := os.WriteFile(rar, []byte("rar"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldLook, oldCmd := lookPath, unpackCommand
	t.Cleanup(func() {
		lookPath = oldLook
		unpackCommand = oldCmd
	})
	lookPath = func(file string) (string, error) {
		if file == "unrar" {
			return "/bin/unrar-fake", nil
		}
		return "", exec.ErrNotFound
	}
	var phaseDuringUnpack string
	unpackCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		m.mu.Lock()
		phaseDuringUnpack = dl.phase
		m.mu.Unlock()
		dest := args[len(args)-1]
		dest = strings.TrimRight(dest, string(filepath.Separator))
		script := "#!/bin/sh\nprintf fake > \"$1/out.mkv\"\n"
		helper := filepath.Join(t.TempDir(), "fake-unrar.sh")
		if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return exec.CommandContext(ctx, helper, dest)
	}

	var (
		mu          sync.Mutex
		completeGID string
		errorFired  bool
		done        = make(chan struct{})
		once        sync.Once
	)
	m.SetOnComplete(func(gid string, _ []string) {
		mu.Lock()
		completeGID = gid
		mu.Unlock()
		once.Do(func() { close(done) })
	})
	m.SetOnError(func(string, error) {
		mu.Lock()
		errorFired = true
		mu.Unlock()
	})

	m.finalizeAssembled(context.Background(), dl.gid, dl, []string{par2, rar})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onComplete did not fire")
	}
	if dl.status != "complete" {
		t.Fatalf("status=%q want complete (err=%v)", dl.status, dl.err)
	}
	if phaseDuringUnpack != phaseUnpacking {
		t.Fatalf("phase during unpack=%q want %q", phaseDuringUnpack, phaseUnpacking)
	}
	if dl.phase != "" {
		t.Fatalf("phase after complete=%q want empty", dl.phase)
	}
	if dl.phaseDone != 0 || dl.phaseTotal != 0 || !dl.phaseStartedAt.IsZero() {
		t.Fatalf("phase progress after complete should be cleared, got %d/%d started=%v",
			dl.phaseDone, dl.phaseTotal, dl.phaseStartedAt)
	}
	mu.Lock()
	defer mu.Unlock()
	if completeGID != dl.gid {
		t.Fatalf("onComplete gid=%q", completeGID)
	}
	if errorFired {
		t.Fatal("onError must not fire when unpack extracts a video")
	}
	if errors.Is(dl.err, ErrContentUnusable) {
		t.Fatal("must not wrap ErrContentUnusable when unpack succeeds")
	}
}

func TestFinalizeAssembled_PAR2FailUnpackFails_ContentUnusable(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})
	dl := registerFinalizeDL(t, m, "nzb-finalize-unpack-fail")

	par2 := filepath.Join(dl.stagingDir, "set.par2")
	rar := filepath.Join(dl.stagingDir, "set.part01.rar")
	writeJunkPar2(t, par2)
	if err := os.WriteFile(rar, []byte("rar"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldLook, oldCmd := lookPath, unpackCommand
	t.Cleanup(func() {
		lookPath = oldLook
		unpackCommand = oldCmd
	})
	lookPath = func(file string) (string, error) {
		if file == "unrar" {
			return "/bin/unrar-fake", nil
		}
		return "", exec.ErrNotFound
	}
	unpackCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}

	var (
		mu       sync.Mutex
		gotErr   error
		complete bool
		fired    = make(chan struct{})
		once     sync.Once
	)
	m.SetOnComplete(func(string, []string) {
		mu.Lock()
		complete = true
		mu.Unlock()
	})
	m.SetOnError(func(_ string, failure error) {
		mu.Lock()
		gotErr = failure
		mu.Unlock()
		once.Do(func() { close(fired) })
	})

	m.finalizeAssembled(context.Background(), dl.gid, dl, []string{par2, rar})

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("onError did not fire")
	}
	if dl.status != "error" {
		t.Fatalf("status=%q want error", dl.status)
	}
	mu.Lock()
	defer mu.Unlock()
	if complete {
		t.Fatal("onComplete must not fire")
	}
	if !errors.Is(gotErr, ErrContentUnusable) {
		t.Fatalf("want ErrContentUnusable, got %v", gotErr)
	}
	if strings.Contains(gotErr.Error(), "par2:") && !strings.Contains(gotErr.Error(), "unrar") && !strings.Contains(gotErr.Error(), "false") && !strings.Contains(gotErr.Error(), "exit") {
		// Prefer unpack-shaped wrap; fake unrar exits via `false`.
		t.Logf("error text: %v", gotErr)
	}
}

func TestFinalizeAssembled_PAR2FailNoArchiveNoVideo_StillFailClosed(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})
	dl := registerFinalizeDL(t, m, "nzb-finalize-par2-only")

	par2 := filepath.Join(dl.stagingDir, "set.par2")
	writeJunkPar2(t, par2)

	var (
		mu       sync.Mutex
		gotErr   error
		complete bool
		fired    = make(chan struct{})
		once     sync.Once
	)
	m.SetOnComplete(func(string, []string) {
		mu.Lock()
		complete = true
		mu.Unlock()
	})
	m.SetOnError(func(_ string, failure error) {
		mu.Lock()
		gotErr = failure
		mu.Unlock()
		once.Do(func() { close(fired) })
	})

	m.finalizeAssembled(context.Background(), dl.gid, dl, []string{par2})

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("onError did not fire")
	}
	if dl.status != "error" {
		t.Fatalf("status=%q want error", dl.status)
	}
	mu.Lock()
	defer mu.Unlock()
	if complete {
		t.Fatal("onComplete must not fire for PAR2-only staging")
	}
	if !errors.Is(gotErr, ErrContentUnusable) {
		t.Fatalf("want ErrContentUnusable, got %v", gotErr)
	}
}

func TestFinalizeAssembled_NoPar2_Unchanged(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})
	dl := registerFinalizeDL(t, m, "nzb-finalize-flat")

	mkv := filepath.Join(dl.stagingDir, "a.mkv")
	if err := os.WriteFile(mkv, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	var (
		completeGID string
		errorFired  bool
		done        = make(chan struct{})
		once        sync.Once
		mu          sync.Mutex
	)
	m.SetOnComplete(func(gid string, _ []string) {
		mu.Lock()
		completeGID = gid
		mu.Unlock()
		once.Do(func() { close(done) })
	})
	m.SetOnError(func(string, error) {
		mu.Lock()
		errorFired = true
		mu.Unlock()
	})

	m.finalizeAssembled(context.Background(), dl.gid, dl, []string{mkv})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onComplete did not fire")
	}
	if dl.status != "complete" {
		t.Fatalf("status=%q want complete", dl.status)
	}
	mu.Lock()
	defer mu.Unlock()
	if completeGID != dl.gid || errorFired {
		t.Fatalf("completeGID=%q errorFired=%v", completeGID, errorFired)
	}
}
