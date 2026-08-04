package downloader

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// stagedTorrentSized is stagedTorrent (seeding_test.go) with a caller-chosen
// payload size instead of the fixed 64 KiB. G-1/G-2 need a payload bigger
// than the rate limiter's 1 MiB minimum burst (downloadRateBurst) — otherwise
// the whole transfer fits inside the initial burst and completes in a single
// poll tick, leaving no window in which an intermediate non-zero speed could
// ever be sampled.
func stagedTorrentSized(t *testing.T, staging, name string, seed byte, size int) []byte {
	t.Helper()
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i) ^ seed
	}
	path := filepath.Join(staging, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("writing payload: %v", err)
	}

	info := metainfo.Info{PieceLength: 256 * 1024}
	if err := info.BuildFromFilePath(path); err != nil {
		t.Fatalf("building info: %v", err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("bencoding info: %v", err)
	}
	mi := metainfo.MetaInfo{InfoBytes: infoBytes}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatalf("writing metainfo: %v", err)
	}
	return buf.Bytes()
}

// Upload-speed engine tests (implementing plan §4, G-1…G-7). Matches the
// existing repo harnesses exactly: seeding_test.go's real-loopback-pair
// fixture for the two cases that need genuinely growing BytesWrittenData
// (G-1, G-2), and stale_test.go's direct-entry-manipulation-under-m.mu plus
// severalPollTicks-sleep pattern for everything else, since a real network
// transfer is not required (and would be flaky) to exercise the delta-guard
// and reset logic.

// setupUploadLeechPair builds a real seeder (already "complete" and seeding,
// with its payload staged locally so Complete() fires the instant it is
// added) and a leecher that pulls the full payload from it over a loopback
// peer connection — the only way to produce a genuinely growing
// BytesWrittenData counter on the seeder, which pollSnapshot's upload pass
// reads. Mirrors seeding_test.go's TestSeeding_LoopbackPair fixture.
func setupUploadLeechPair(t *testing.T, seed byte) (seeder, leecher *Manager, seedGID, leechGID string) {
	t.Helper()
	seedStaging := t.TempDir()
	seederCfg := seedingTestConfig(seedStaging)
	seeder = New(seederCfg, http.DefaultClient)
	done := make(chan []string, 1)
	seeder.SetOnComplete(func(_ string, files []string) { done <- files })
	runManager(t, seeder)

	// 3 MiB: bigger than downloadRateBurst's 1 MiB floor burst, so the rate
	// limit below actually throttles the transfer instead of the whole
	// payload fitting inside the initial burst and completing in one tick.
	const payloadSize = 3 * 1024 * 1024
	body := stagedTorrentSized(t, seedStaging, "payload.bin", seed, payloadSize)
	seedGID = addTorrentBody(t, seeder, body)
	waitComplete(t, done)

	leechStaging := t.TempDir()
	leechCfg := leechingTestConfig(leechStaging)
	// 512 KB/s: under the 1 MiB burst floor, so the first ~1 MiB downloads
	// instantly and the remaining ~2 MiB is genuinely throttled at this rate
	// (~4s), spanning several 500ms poll ticks — the real window G-1 and G-2
	// need to sample an intermediate non-zero speed in.
	leechCfg.DownloadRateLimit = 512 * 1024
	leecher = New(leechCfg, http.DefaultClient)
	runManager(t, leecher)
	leechGID = addTorrentBody(t, leecher, body)

	waitStatus(t, leecher, leechGID, "active")
	lt := torrentOf(leecher, leechGID)
	if lt == nil {
		t.Fatal("leecher has no torrent handle")
	}
	if n := lt.AddClientPeer(clientOf(seeder)); n == 0 {
		t.Fatal("AddClientPeer added 0 peers — the leecher can never find the seeder")
	}
	return seeder, leecher, seedGID, leechGID
}

// findDownload returns the entry matching gid from a List() snapshot, or nil.
func findDownload(list []Download, gid string) *Download {
	for i := range list {
		if list[i].GID == gid {
			return &list[i]
		}
	}
	return nil
}

// G-1 — THE SINGLE MOST IMPORTANT TEST IN THIS PLAN. A seeding entry
// (status "complete", non-zero seedStartedAt) whose handle genuinely writes
// bytes to a peer must publish a non-zero UploadSpeed. If the upload pass
// were nested inside pollSnapshot's active||waiting guard (F-1's central
// risk), this fails loudly: a seeding entry's status is "complete", so that
// guard can never observe it and UploadSpeed stays structurally 0 forever.
func TestUploadSpeed_ComputedWhileSeeding(t *testing.T) {
	seeder, leecher, seedGID, leechGID := setupUploadLeechPair(t, 41)

	waitLeechComplete(t, leecher, leechGID, seeder, seedGID)

	// Sample the seeder's own snapshot repeatedly. setupUploadLeechPair rate-
	// limits the leecher so the transfer spans several poll ticks, giving a
	// real window in which to observe a non-zero rate.
	deadline := time.Now().Add(20 * time.Second)
	var maxSeen int64
	for time.Now().Before(deadline) {
		if d := findDownload(seeder.List(), seedGID); d != nil && d.UploadSpeed > maxSeen {
			maxSeen = d.UploadSpeed
		}
		if maxSeen > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if maxSeen <= 0 {
		t.Fatal("UploadSpeed never went non-zero while genuinely seeding — the upload pass " +
			"is not reachable for a complete-and-seeding entry (F-1)")
	}
}

// G-2 — the upload pass runs concurrently with the download-speed pass on an
// actively-downloading leecher (pollSnapshot's upload-pass guard includes
// "active"), and must not corrupt DownloadSpeed's delta base (F-2's shared-
// prevTime hazard). The leecher cannot reciprocate upload before it
// completes (DisallowDataUpload at add time), so UploadSpeed staying 0
// throughout is the expected, orthogonal control.
func TestUploadSpeed_DoesNotCorruptDownloadSpeed(t *testing.T) {
	seeder, leecher, seedGID, leechGID := setupUploadLeechPair(t, 42)

	deadline := time.Now().Add(20 * time.Second)
	var sawDownloadSpeed bool
	for time.Now().Before(deadline) {
		d := findDownload(leecher.List(), leechGID)
		if d == nil {
			break // completed and status changed away from what we're tracking
		}
		if d.Status != "active" {
			break
		}
		if d.DownloadSpeed > 0 {
			sawDownloadSpeed = true
		}
		if d.UploadSpeed != 0 {
			t.Fatalf("UploadSpeed = %d on an active (pre-complete) leecher — upload is "+
				"disallowed until completion, so this can only mean the two delta passes "+
				"are cross-contaminating (F-2)", d.UploadSpeed)
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitLeechComplete(t, leecher, leechGID, seeder, seedGID)
	if !sawDownloadSpeed {
		t.Fatal("DownloadSpeed never went non-zero while the leecher was actively pulling " +
			"data — the upload pass sharing prevTime may have corrupted its delta base")
	}
}

// G-3 — a fresh entry's first poll tick must publish UploadSpeed 0, not a
// divide-by-zero or a garbage rate, mirroring the download-speed pass's own
// !e.prevTime.IsZero() guard. addTestTorrent's torrent never completes and
// has no peers, so BytesWrittenData never grows — UploadSpeed staying 0
// across many ticks demonstrates the guard holds for the whole entry
// lifetime under test, not merely for one lucky tick.
func TestUploadSpeed_ZeroForFreshEntry(t *testing.T) {
	m := startManager(t, testConfig(t.TempDir()))
	gid := addTestTorrent(t, m, 43)

	time.Sleep(severalPollTicks)
	d := findDownload(m.List(), gid)
	if d == nil {
		t.Fatalf("no download entry for %s", gid)
	}
	if d.UploadSpeed != 0 {
		t.Fatalf("UploadSpeed = %d for a fresh entry with no real upload traffic, want 0", d.UploadSpeed)
	}
}

// G-4 — a prevUpBytes value beyond the handle's real (unchanging, zero)
// BytesWrittenData counter — the exact shape a handle swap leaves behind
// before F-4's reset — must publish 0, not a negative rate. Models the
// post-rebuild hazard directly against pollSnapshot's delta>0 guard without
// needing an actual client rebuild.
func TestUploadSpeed_CounterResetYieldsZeroNotNegative(t *testing.T) {
	m := startManager(t, testConfig(t.TempDir()))
	gid := addTestTorrent(t, m, 44)
	time.Sleep(severalPollTicks) // let prevUpTime become non-zero naturally

	m.mu.Lock()
	e, ok := m.entries[gid]
	if !ok {
		m.mu.Unlock()
		t.Fatalf("no entry for %s", gid)
	}
	e.prevUpBytes = 1 << 30 // far beyond the real (0) BytesWrittenData
	m.mu.Unlock()

	time.Sleep(severalPollTicks)
	d := findDownload(m.List(), gid)
	if d == nil {
		t.Fatalf("no download entry for %s", gid)
	}
	if d.UploadSpeed != 0 {
		t.Fatalf("UploadSpeed = %d after an injected counter-reset shape, want 0 (not negative)", d.UploadSpeed)
	}
}

// G-5 — once the seed window closes (seedStopReason trips), UploadSpeed must
// return to 0 rather than freezing at its last observed rate forever (T1.7).
// The entry's real handle never actually seeds real data; only the
// bookkeeping fields are forced, matching seeding_test.go's own
// direct-entry-manipulation fixture (seedingEntry) — the point under test is
// pollSnapshot's reset, not a real transfer.
func TestUploadSpeed_ResetsWhenSeedingStops(t *testing.T) {
	cfg := testConfig(t.TempDir())
	cfg.SeedDurationMinutes = 1 // armed so the duration limit trips immediately below
	m := startManager(t, cfg)
	gid := addTestTorrent(t, m, 45)

	m.mu.Lock()
	e, ok := m.entries[gid]
	if !ok {
		m.mu.Unlock()
		t.Fatalf("no entry for %s", gid)
	}
	e.status = "complete"
	e.seedStartedAt = time.Now().Add(-2 * time.Minute) // already past the 1-minute limit
	e.upSpeed = 555                                    // simulate "was reporting a rate"
	e.prevUpTime = time.Now()
	m.mu.Unlock()

	time.Sleep(severalPollTicks)
	d := findDownload(m.List(), gid)
	if d == nil {
		t.Fatalf("no download entry for %s", gid)
	}
	if d.UploadSpeed != 0 {
		t.Fatalf("UploadSpeed = %d after the seed window closed, want 0 (frozen at the last rate, not reset)", d.UploadSpeed)
	}
}

// G-6 — D-1's whole reason for existing: cachedSeeds (née cachedConns) must
// keep refreshing while an entry is complete-and-seeding, not freeze at its
// last active-phase reading. A stale 99 injected directly proves the refresh
// happened only if the observed value changes to the real (isolated,
// zero-peer) ConnectedSeeders reading.
func TestSeedCount_StaysLiveWhileSeeding(t *testing.T) {
	m := startManager(t, testConfig(t.TempDir()))
	gid := addTestTorrent(t, m, 46)

	m.mu.Lock()
	e, ok := m.entries[gid]
	if !ok {
		m.mu.Unlock()
		t.Fatalf("no entry for %s", gid)
	}
	e.status = "complete"
	e.seedStartedAt = time.Now() // unbounded seeding: both limits are 0 in testConfig
	e.cachedSeeds = 99           // stale value simulating a frozen last-active reading
	m.mu.Unlock()

	time.Sleep(severalPollTicks)
	d := findDownload(m.List(), gid)
	if d == nil {
		t.Fatalf("no download entry for %s", gid)
	}
	if d.SeedCount == 99 {
		t.Fatal("SeedCount is still the injected stale value while seeding — cachedSeeds was " +
			"not refreshed for a complete-and-seeding entry (D-1)")
	}
}

// G-7 — F-4's asymmetry, end to end through a real rebuild: readdTorrents
// must reset prevUpBytes/prevUpTime (measured off the OLD handle's counter,
// meaningless against the new one) but must NOT reset prevBytes
// (BytesCompleted() is derived from verified on-disk pieces, not a process
// counter, and survives a handle swap intact). Modeled directly on
// seeding_rebuild_test.go's TestReadd_ResetsSeedBaseline_RatioLimitStillReachable.
func TestReaddTorrents_ResetsUploadDeltaButNotDownloadDelta(t *testing.T) {
	staging := t.TempDir()
	cfg := seedingTestConfig(staging)
	m := New(cfg, http.DefaultClient)
	done := make(chan []string, 1)
	m.SetOnComplete(func(_ string, files []string) { done <- files })
	runManager(t, m)

	body := stagedTorrent(t, staging, "payload.bin", 47)
	gid := addTorrentBody(t, m, body)
	waitComplete(t, done)

	const injectedUp = 1 << 20
	const injectedDown = 1 << 21
	m.mu.Lock()
	e := m.entries[gid]
	if e == nil || e.seedStartedAt.IsZero() {
		m.mu.Unlock()
		t.Fatalf("entry %s is not seeding after completion", gid)
	}
	e.prevUpBytes = injectedUp
	e.prevUpTime = time.Now()
	e.prevBytes = injectedDown
	oldHandle := e.t
	m.mu.Unlock()

	// A rebuild-class change that is NOT a staging move, so it is allowed
	// while seeding and genuinely re-adds the torrent to a fresh client.
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
	gotUp, gotUpTime, gotDown, newHandle := e.prevUpBytes, e.prevUpTime, e.prevBytes, e.t
	m.mu.Unlock()

	if newHandle == oldHandle {
		t.Fatal("the torrent handle was not replaced — this was not a real rebuild")
	}
	if gotUp != 0 {
		t.Fatalf("prevUpBytes = %d after the rebuild, want 0 — it was measured off the CLOSED handle's counter (F-4)", gotUp)
	}
	if !gotUpTime.IsZero() {
		t.Fatalf("prevUpTime = %v after the rebuild, want the zero value", gotUpTime)
	}
	if gotDown != injectedDown {
		t.Fatalf("prevBytes = %d after the rebuild, want the injected %d left UNCHANGED — "+
			"BytesCompleted() survives a handle swap intact and resetting it would introduce "+
			"a download-speed glitch on every reconfigure", gotDown, injectedDown)
	}
}
