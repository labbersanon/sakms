package downloader

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Stale-detection tests (§4.2 / T-4.1–T-4.5).
//
// They all run against a REAL anacrolix client, which is not incidental: the
// predicate reads t.Stats() off a live handle, and a SeedState entry has a nil
// torrent so pollSnapshot skips it entirely — no fake-Manager test could ever
// observe onStale fire. The synthetic torrent addTestTorrent adds has no
// trackers, and every client here runs with DHT/PEX off, so it settles at
// exactly the shape the reaper looks for: active, zero bytes, zero peers.
//
// The clock is driven by back-dating e.lastProgressAt rather than by injecting
// a time source. pollSnapshot computes its own now, and adding a clock seam to
// Manager would collide with the seeding work that owns one.

// staleConfig is testConfig plus an armed stale threshold and no port
// forwarding — a hermetic test must never broadcast a port mapping to the LAN.
func staleConfig(staging string, thresholdMinutes int) Config {
	cfg := testConfig(staging)
	cfg.StaleThresholdMinutes = thresholdMinutes
	cfg.noPortForwarding = true
	return cfg
}

// staleRecorder collects the gids onStale fires for. The callback runs on the
// poll goroutine, so every field is mutex-guarded.
type staleRecorder struct {
	mu   sync.Mutex
	gids []string
}

func (r *staleRecorder) record(gid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gids = append(r.gids, gid)
}

func (r *staleRecorder) countOf(gid string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, g := range r.gids {
		if g == gid {
			n++
		}
	}
	return n
}

func (r *staleRecorder) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.gids)
}

// startStaleManager mirrors startManager but installs the stale callback BEFORE
// Start. pollLoop reads m.onStale without holding m.mu (same as m.onComplete),
// so setting it afterwards is a data race the -race gate would catch.
func startStaleManager(t *testing.T, cfg Config, onStale func(gid string)) *Manager {
	t.Helper()
	m := New(cfg, http.DefaultClient)
	m.SetOnStale(onStale)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("Start did not return after context cancellation")
		}
	})

	deadline := time.Now().Add(20 * time.Second)
	for {
		if m.currentClient() != nil {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatal("torrent client never came up")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// backdateProgress rewinds an entry's stale clock by d, simulating a download
// that has sat still for that long.
func backdateProgress(t *testing.T, m *Manager, gid string, d time.Duration) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[gid]
	if !ok {
		t.Fatalf("no entry for gid %s", gid)
	}
	e.lastProgressAt = time.Now().Add(-d)
}

// waitForStale waits for gid to be reported stale at least once.
func waitForStale(t *testing.T, r *staleRecorder, gid string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if r.countOf(gid) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("onStale never fired for %s", gid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// severalPollTicks is long enough for the 500 ms poll loop to run repeatedly —
// what a "never fires" assertion needs in order to mean anything.
const severalPollTicks = 6 * pollInterval

// T-4.1 — the core detection case, plus the fire-once guard. A second fire
// would mean a second cancel+requeue for one download: the grab row would be
// parked twice (double-incrementing retry_count) and Cancel would run against
// a gid that no longer exists.
func TestIsStale_FiresOnceForADeadTorrent(t *testing.T) {
	rec := &staleRecorder{}
	m := startStaleManager(t, staleConfig(t.TempDir(), 1), rec.record)
	gid := addTestTorrent(t, m, 1)

	backdateProgress(t, m, gid, 5*time.Minute)
	waitForStale(t, rec, gid)

	// Keep ticking: the entry is untouched and still matches the predicate, so
	// only the staleFired guard can stop a re-fire.
	time.Sleep(severalPollTicks)
	if got := rec.countOf(gid); got != 1 {
		t.Fatalf("onStale fired %d times for %s, want exactly 1 — the fire-once guard is not holding", got, gid)
	}
}

// T-4.2 (engine half) — THE DATA-LOSS REGRESSION GUARD. A paused download makes
// zero progress with zero peers by definition; without the status clause, a
// 4-hour pause would auto-cancel it and delete the operator's partial files.
//
// The global-pause half of T-4.2 is asserted in internal/api
// (TestGlobalPause_ShieldsEveryTorrentFromTheStaleReaper): the global toggle
// pauses each in-flight entry through this very same per-item path, and blocks
// any new add, so both halves reduce to the clause proven here.
func TestIsStale_NeverFiresForAPausedTorrent(t *testing.T) {
	rec := &staleRecorder{}
	m := startStaleManager(t, staleConfig(t.TempDir(), 1), rec.record)
	gid := addTestTorrent(t, m, 2)

	if err := m.Pause(gid); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	backdateProgress(t, m, gid, 24*time.Hour)

	time.Sleep(severalPollTicks)
	if got := rec.total(); got != 0 {
		t.Fatalf("onStale fired %d times for a PAUSED download — that would delete the operator's partial files", got)
	}

	// And resuming must restart the clock, not inherit the paused dead time:
	// otherwise every long pause would be reaped the instant it is resumed.
	if err := m.Resume(gid); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	time.Sleep(severalPollTicks)
	if got := rec.total(); got != 0 {
		t.Fatalf("onStale fired %d times right after Resume — the paused dead time was counted toward the threshold", got)
	}
}

// T-4.3 — a magnet has no metadata until GotInfo returns, so it sits at
// "waiting" with zero bytes and zero peers. That is a torrent that has not
// started, not one that has died, and reaping it would break every magnet add
// on a slow swarm.
func TestIsStale_NeverFiresForAPreMetadataTorrent(t *testing.T) {
	rec := &staleRecorder{}
	m := startStaleManager(t, staleConfig(t.TempDir(), 1), rec.record)

	// DHT and PEX are off and the magnet carries no trackers, so GotInfo can
	// never resolve — the entry stays "waiting" for the whole test.
	gid, err := m.AddTorrent(context.Background(), "magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12")
	if err != nil {
		t.Fatalf("AddTorrent(magnet): %v", err)
	}
	backdateProgress(t, m, gid, 24*time.Hour)

	time.Sleep(severalPollTicks)
	if got := statusOf(m, gid); got != "waiting" {
		t.Fatalf("test precondition lost: status = %q, want %q (the magnet resolved, so this no longer covers the pre-metadata case)", got, "waiting")
	}
	if got := rec.total(); got != 0 {
		t.Fatalf("onStale fired %d times for a pre-metadata magnet", got)
	}
}

// T-4.4 — any forward progress resets the clock, so a download that is slow but
// alive is never reaped. Progress is injected by rewinding prevBytes below the
// torrent's real completed count, which makes pollSnapshot's own
// `completed > e.prevBytes` reset line fire on the next tick. That is the exact
// production line under test; nothing about the reset is stubbed.
func TestIsStale_ProgressResetsTheClock(t *testing.T) {
	rec := &staleRecorder{}
	m := startStaleManager(t, staleConfig(t.TempDir(), 1), rec.record)
	gid := addTestTorrent(t, m, 3)

	m.mu.Lock()
	e := m.entries[gid]
	e.lastProgressAt = time.Now().Add(-24 * time.Hour) // well past the threshold
	e.prevBytes = -1                                   // ... but bytes moved this tick
	m.mu.Unlock()

	time.Sleep(severalPollTicks)
	if got := rec.total(); got != 0 {
		t.Fatalf("onStale fired %d times for a torrent that made progress inside the threshold", got)
	}
}

// T-4.5 — threshold 0 is the documented "off" switch. It must disable detection
// outright, not degrade to "every torrent is instantly stale", which is what a
// naive `now.Sub(last) >= 0` comparison would do.
func TestIsStale_ThresholdZeroDisablesDetection(t *testing.T) {
	rec := &staleRecorder{}
	m := startStaleManager(t, staleConfig(t.TempDir(), 0), rec.record)
	gid := addTestTorrent(t, m, 4)

	backdateProgress(t, m, gid, 30*24*time.Hour)

	time.Sleep(severalPollTicks)
	if got := rec.total(); got != 0 {
		t.Fatalf("onStale fired %d times with the threshold set to 0 (detection off)", got)
	}
}
