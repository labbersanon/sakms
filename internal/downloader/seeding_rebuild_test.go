package downloader

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Fix 2's regression test. A seeding entry's status is "complete", not
// "active"/"waiting", so the staging-dir-change refusal used to look straight
// past it: the rebuild proceeded, the re-added torrent pointed at the NEW
// staging root while seedPaths still pointed under the OLD one, and when
// seeding later stopped deleteDownloadFiles refused those now-out-of-staging
// paths — orphaning the seed files permanently.
func TestReconfigure_StagingDirWhileSeeding_Refused(t *testing.T) {
	staging := t.TempDir()
	m := NewForTesting(staging)
	seedingEntry(t, m, "seeder", time.Now())

	next := Config{StagingDir: t.TempDir(), DownloadRateLimit: 123456}
	err := m.Reconfigure(context.Background(), next)
	if err == nil {
		t.Fatal("Reconfigure accepted a StagingDir move while a torrent was seeding")
	}
	if !errors.Is(err, ErrRebuildRefused) {
		t.Fatalf("error %v does not match ErrRebuildRefused; the HTTP layer keys on it for its 409", err)
	}
	if got := m.StagingDir(); got != staging {
		t.Fatalf("StagingDir() = %q after a refusal, want the unchanged %q", got, staging)
	}
	m.mu.Lock()
	storedRate := m.cfg.DownloadRateLimit
	m.mu.Unlock()
	if storedRate != 0 {
		t.Fatalf("DownloadRateLimit = %d after a refusal, want the config left wholly untouched", storedRate)
	}
}

// The refusal above must stay scoped to a StagingDir change. A seeding entry
// must NOT block a port/DHT/PEX/obfuscation rebuild — that path re-adds the
// torrent under the same staging root and keeps seeding intact, and refusing it
// would make Fix 3's re-baseline unreachable.
func TestReconfigure_NonStagingRebuildWhileSeeding_Allowed(t *testing.T) {
	staging := t.TempDir()
	m := NewForTesting(staging)
	seedingEntry(t, m, "seeder", time.Now())

	next := Config{StagingDir: staging, PEXEnabled: true} // rebuild-class, staging untouched
	if err := m.Reconfigure(context.Background(), next); err != nil {
		t.Fatalf("a non-staging rebuild while seeding must be allowed, got %v", err)
	}
	m.mu.Lock()
	stored := m.cfg.PEXEnabled
	m.mu.Unlock()
	if !stored {
		t.Fatal("config was not stored despite the save being accepted")
	}
}

// Fix 3's regression test. readdTorrents assigns a NEW *Torrent to e.t but used
// to leave e.seedBaselineUp at a value measured off the OLD handle's
// BytesWrittenData counter. The new handle's counter starts at 0, so
// seedStopReason's `uploaded := BytesWrittenData - seedBaselineUp` went deeply
// negative and the ratio limit could never be reached again — after ANY
// rebuild-class settings change mid-seed, only the duration limit still bounded
// that seed.
//
// The injected nonzero baseline is load-bearing: a torrent completed from local
// data has baseline 0, which hides the bug entirely. A real seed whose download
// reciprocated tit-for-tat bytes carries a positive one, which is what this
// models.
func TestReadd_ResetsSeedBaseline_RatioLimitStillReachable(t *testing.T) {
	staging := t.TempDir()
	cfg := seedingTestConfig(staging)
	m := New(cfg, http.DefaultClient)
	done := make(chan []string, 1)
	m.SetOnComplete(func(_ string, files []string) { done <- files })
	runManager(t, m)

	body := stagedTorrent(t, staging, "payload.bin", 21)
	original := filepath.Join(staging, "payload.bin")
	gid := addTorrentBody(t, m, body)
	waitComplete(t, done)

	// The seed window is open by the time onComplete fires (beginSeeding runs
	// immediately before it). Inject the baseline a tit-for-tat download would
	// have left behind.
	const injected = 1 << 30
	m.mu.Lock()
	e := m.entries[gid]
	if e == nil || e.seedStartedAt.IsZero() {
		m.mu.Unlock()
		t.Fatalf("entry %s is not seeding after completion", gid)
	}
	e.seedBaselineUp = injected
	oldHandle := e.t
	m.mu.Unlock()

	// A rebuild-class change that is NOT a staging move, so it is allowed while
	// seeding and genuinely re-adds the torrent to a fresh client.
	next := cfg
	next.PEXEnabled = !cfg.PEXEnabled
	if err := m.Reconfigure(context.Background(), next); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}

	m.mu.Lock()
	e = m.entries[gid]
	if e == nil {
		m.mu.Unlock()
		t.Fatalf("entry %s vanished across the rebuild", gid)
	}
	gotBaseline, newHandle, seeding := e.seedBaselineUp, e.t, !e.seedStartedAt.IsZero()
	m.mu.Unlock()

	if newHandle == oldHandle {
		t.Fatal("the torrent handle was not replaced — this was not a real rebuild")
	}
	if !seeding {
		t.Fatal("the entry stopped being a seeder across the rebuild")
	}
	if gotBaseline != 0 {
		t.Fatalf("seedBaselineUp = %d after the rebuild, want 0 — it was measured off the CLOSED handle's counter, so the ratio limit can never be reached", gotBaseline)
	}

	// End to end: with the baseline re-based, a ratio limit the new handle can
	// actually satisfy trips and the ORIGINAL staging file is reclaimed. With
	// the stale baseline this never fires — uploaded stays ~-1GiB forever.
	stop := next
	stop.SeedRatioLimit = 0.000001 // target rounds to 0 bytes against a 64KiB torrent
	if err := m.Reconfigure(context.Background(), stop); err != nil {
		t.Fatalf("Reconfigure ratio limit: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(original); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("seeding never stopped: the original staging file %s still exists, so the ratio limit was unreachable", original)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Fix 4's regression test. Cancel deleted the entry before reading its
// importRoot, so nothing could locate that gid's .import/<gid>/ tree
// afterwards — cancelling a completed, seeding torrent leaked a FULL-SIZE copy
// of its content until the next process restart's startup sweep.
func TestCancel_WhileSeeding_ReclaimsImportCopy(t *testing.T) {
	staging := t.TempDir()
	m := NewForTesting(staging)

	original := filepath.Join(staging, "payload.mkv")
	importRoot := filepath.Join(staging, importDirName, "g1")
	importFile := filepath.Join(importRoot, "payload.mkv")
	writeFile(t, original)
	writeFile(t, importFile)

	m.SeedState(Download{GID: "g1", Status: "complete", Dir: staging, Files: []string{original}})
	m.SeedImportCopy("g1", importRoot, []string{importFile})
	m.mu.Lock()
	m.entries["g1"].seedStartedAt = time.Now()
	m.entries["g1"].seedPaths = []string{original}
	m.mu.Unlock()

	if err := m.Cancel("g1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := os.Stat(importRoot); !os.IsNotExist(err) {
		t.Errorf("the per-gid import copy must be reclaimed by Cancel, stat err = %v", err)
	}
	if _, err := os.Stat(original); !os.IsNotExist(err) {
		t.Errorf("the original staging file should still be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("the staging root must survive, stat err = %v", err)
	}
}
