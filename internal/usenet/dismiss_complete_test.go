package usenet

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFinalizeAssembled_DismissesCompleteAfterDelay(t *testing.T) {
	old := dismissCompleteAfter
	dismissCompleteAfter = 20 * time.Millisecond
	t.Cleanup(func() { dismissCompleteAfter = old })

	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})
	dl := registerFinalizeDL(t, m, "nzb-dismiss-complete")
	mkv := filepath.Join(dl.stagingDir, "movie.mkv")
	if err := os.WriteFile(mkv, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	m.SetOnComplete(func(string, []string) { close(done) })
	m.finalizeAssembled(context.Background(), dl.gid, dl, []string{mkv})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onComplete did not fire")
	}
	m.mu.Lock()
	_, stillQueued := m.downloads[dl.gid]
	m.mu.Unlock()
	if !stillQueued {
		t.Fatal("download should still be in queue during glance window")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		_, ok := m.downloads[dl.gid]
		m.mu.Unlock()
		if !ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("completed download was not dismissed from queue")
}

func TestForget_KeepsErrors(t *testing.T) {
	m := New(Config{StagingDir: t.TempDir(), MaxConcurrentDownloads: 1})
	dl := registerFinalizeDL(t, m, "nzb-keep-error")
	m.mu.Lock()
	dl.status = "error"
	m.mu.Unlock()

	old := dismissCompleteAfter
	dismissCompleteAfter = 5 * time.Millisecond
	t.Cleanup(func() { dismissCompleteAfter = old })
	m.scheduleDismissComplete(dl.gid)
	time.Sleep(30 * time.Millisecond)

	m.mu.Lock()
	_, stillThere := m.downloads[dl.gid]
	m.mu.Unlock()
	if !stillThere {
		t.Fatal("error download must not be dismissed by scheduleDismissComplete")
	}
	if !m.Forget(dl.gid) {
		t.Fatal("Forget should allow explicit error drop")
	}
}
