package downloader

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Fix 1's regression test. m.cfg used to be write-once-at-construction; once
// Reconfigure started assigning the whole struct under m.mu, every lock-free
// reader of it became a data race. This drives a config PUT concurrently with
// the Cancel/stopSeeding/StagingDir readers that run WITHOUT the lock by
// design, so -race sees the write/read pair directly.
//
// The varied field is DELIBERATELY a live-tier one (DownloadRateLimit), never
// StagingDir: a StagingDir change is rebuild-class, and rebuildRefusalLocked
// would return before `m.cfg = next` ever executes — the racing write would
// never happen and the test would pass vacuously. The race is against the
// WHOLE-STRUCT assignment, not against any particular field's value changing.
func TestReconfigure_ConcurrentWithLockFreeReaders_NoDataRace(t *testing.T) {
	staging := t.TempDir()
	m := NewForTesting(staging)

	const rounds = 300

	var wg sync.WaitGroup
	wg.Add(3)

	// Writer: a settings PUT loop.
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			next := Config{StagingDir: staging, DownloadRateLimit: i * 1024, MaxConn: i%8 + 1}
			if err := m.Reconfigure(context.Background(), next); err != nil {
				t.Errorf("Reconfigure: %v", err)
				return
			}
		}
	}()

	// Reader 1: Cancel -> deleteDownloadFiles, which reads the staging dir
	// lock-free (Cancel releases m.mu before the synchronous file I/O).
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			gid := "cancel-gid"
			file := filepath.Join(staging, "cancel.mkv")
			// Not the writeFile helper: it calls t.Fatalf, which is illegal from
			// a non-test goroutine.
			_ = os.WriteFile(file, []byte("data"), 0o644)
			m.SeedState(Download{GID: gid, Status: "complete", Dir: staging, Files: []string{file}})
			if err := m.Cancel(gid); err != nil {
				t.Errorf("Cancel: %v", err)
				return
			}
		}
	}()

	// Reader 2: stopSeeding (called by pollLoop with m.mu released) plus the
	// exported StagingDir accessor every api handler reaches through.
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			file := filepath.Join(staging, "seed.mkv")
			// Not the writeFile helper: it calls t.Fatalf, which is illegal from
			// a non-test goroutine.
			_ = os.WriteFile(file, []byte("data"), 0o644)
			m.stopSeeding(seedStop{gid: "seed-gid", seedPaths: []string{file}, reason: "test"})
			if got := m.StagingDir(); got != staging {
				t.Errorf("StagingDir() = %q, want %q", got, staging)
				return
			}
			m.sweepImportDir()
		}
	}()

	wg.Wait()
}
