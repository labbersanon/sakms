package downloader

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	torrentlib "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// Seeding tests for the copy-then-move inversion: the ORIGINAL staged files
// stay where the torrent client wrote them and keep seeding, while a fresh
// copy under <staging>/.import/<gid>/ is what the import flow consumes.
//
// Every real client here is built with ListenPort 0 (the library's default is
// a FIXED 42069, so two clients in one process collide), DHT off, and
// noPortForwarding on — a hermetic test must never broadcast to the LAN.

func seedingTestConfig(staging string) Config {
	cfg := testConfig(staging)
	cfg.SeedingEnabled = true
	cfg.noPortForwarding = true
	return cfg
}

func leechingTestConfig(staging string) Config {
	cfg := testConfig(staging)
	cfg.noPortForwarding = true
	return cfg
}

// runManager starts an already-constructed Manager (so a test can wire
// SetOnComplete first, which startManager's own construction cannot) and tears
// it down at test end.
func runManager(t *testing.T, m *Manager) *Manager {
	t.Helper()
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

// stagedTorrent writes a payload INSIDE staging and returns .torrent bytes for
// it. The sibling helper buildTestTorrent deliberately writes OUTSIDE staging
// so the torrent never completes; a seeding test needs the opposite — the data
// present, so the client hash-checks it and fires Complete immediately.
func stagedTorrent(t *testing.T, staging, name string, seed byte) []byte {
	t.Helper()
	payload := make([]byte, 64*1024)
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

	info := metainfo.Info{PieceLength: 32 * 1024}
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

// addTorrentBody serves .torrent bytes over httptest and adds them through the
// Manager's real AddTorrent path, so watchTorrent genuinely runs.
func addTorrentBody(t *testing.T, m *Manager, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	gid, err := m.AddTorrent(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	return gid
}

func torrentOf(m *Manager, gid string) *torrentlib.Torrent {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[gid]; ok {
		return e.t
	}
	return nil
}

func clientOf(m *Manager) *torrentlib.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tc
}

// waitLeechComplete waits for the transfer and, on timeout, dumps both sides'
// peer/stat state — a bare hang gives nothing to debug.
func waitLeechComplete(t *testing.T, leecher *Manager, leechGID string, seeder *Manager, seedGID string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if statusOf(leecher, leechGID) == "complete" {
			return
		}
		if time.Now().After(deadline) {
			lt, st := torrentOf(leecher, leechGID), torrentOf(seeder, seedGID)
			lStats, sStats := lt.Stats(), st.Stats()
			t.Fatalf("leecher never completed: leecher status=%q completed=%d peers=%d active=%d; seeder seeding=%v peers=%d active=%d writtenData=%d",
				statusOf(leecher, leechGID), lt.BytesCompleted(), lStats.TotalPeers, lStats.ActivePeers,
				st.Seeding(), sStats.TotalPeers, sStats.ActivePeers, sStats.BytesWrittenData.Int64())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitComplete(t *testing.T, ch <-chan []string) []string {
	t.Helper()
	select {
	case files := <-ch:
		return files
	case <-time.After(60 * time.Second):
		t.Fatal("onComplete never fired")
		return nil
	}
}

// T-3.1 — with seeding on, the completion flow hands the importer the COPY's
// paths while every ORIGINAL is still in place, and the copy exists by the
// time onComplete fires.
func TestSeeding_CompletionHandsImporterTheCopy_OriginalsUntouched(t *testing.T) {
	staging := t.TempDir()
	m := New(seedingTestConfig(staging), http.DefaultClient)
	done := make(chan []string, 1)
	m.SetOnComplete(func(_ string, files []string) { done <- files })
	runManager(t, m)

	body := stagedTorrent(t, staging, "payload.bin", 3)
	original := filepath.Join(staging, "payload.bin")
	gid := addTorrentBody(t, m, body)

	files := waitComplete(t, done)
	if len(files) != 1 {
		t.Fatalf("onComplete files = %v, want exactly one", files)
	}

	wantRoot := filepath.Join(staging, importDirName, gid)
	if got := filepath.Dir(files[0]); got != wantRoot {
		t.Errorf("import path %q is not under the per-gid import root %q", files[0], wantRoot)
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Errorf("the import copy must exist by the time onComplete fires: %v", err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Errorf("the ORIGINAL must stay where the client wrote it (it is what seeds): %v", err)
	}
	if got := m.ImportRoot(gid); got != wantRoot {
		t.Errorf("ImportRoot(%s) = %q, want %q", gid, got, wantRoot)
	}
	if got := m.ImportPaths(gid); len(got) != 1 || got[0] != files[0] {
		t.Errorf("ImportPaths(%s) = %v, want %v", gid, got, files)
	}
}

// T-3.3 — regression guard: with seeding OFF, no .import directory is created
// and onComplete receives the ORIGINAL paths, exactly as before this feature.
func TestSeedingDisabled_CompletionIsUnchanged(t *testing.T) {
	staging := t.TempDir()
	m := New(leechingTestConfig(staging), http.DefaultClient)
	done := make(chan []string, 1)
	m.SetOnComplete(func(_ string, files []string) { done <- files })
	runManager(t, m)

	body := stagedTorrent(t, staging, "payload.bin", 4)
	original := filepath.Join(staging, "payload.bin")
	gid := addTorrentBody(t, m, body)

	files := waitComplete(t, done)
	if len(files) != 1 || files[0] != original {
		t.Fatalf("onComplete files = %v, want the ORIGINAL path %q", files, original)
	}
	if _, err := os.Stat(filepath.Join(staging, importDirName)); !os.IsNotExist(err) {
		t.Errorf("no .import dir may be created with seeding off, stat err = %v", err)
	}
	if got := m.ImportPaths(gid); got != nil {
		t.Errorf("ImportPaths = %v, want nil when no copy was made", got)
	}
	if got := m.ImportRoot(gid); got != staging {
		t.Errorf("ImportRoot = %q, want the staging dir %q when no copy was made", got, staging)
	}
}

// T-3.5 — the only test that proves seeding actually works end to end, and the
// one that catches the naive (non-inverted) reading of copy-then-move: a
// leecher pulls the full payload from a seeder whose import copy has already
// been relocated away. Then the ratio limit trips and the ORIGINAL staging
// files are deleted while the relocated library copy survives.
func TestSeeding_LoopbackPair_LeecherPullsAfterImportRelocatesTheCopy(t *testing.T) {
	seedStaging := t.TempDir()
	seederCfg := seedingTestConfig(seedStaging)
	seeder := New(seederCfg, http.DefaultClient)
	done := make(chan []string, 1)
	seeder.SetOnComplete(func(_ string, files []string) { done <- files })
	runManager(t, seeder)

	body := stagedTorrent(t, seedStaging, "payload.bin", 7)
	original := filepath.Join(seedStaging, "payload.bin")
	want, err := os.ReadFile(original)
	if err != nil {
		t.Fatalf("reading the staged payload: %v", err)
	}
	seedGID := addTorrentBody(t, seeder, body)
	copies := waitComplete(t, done)
	if len(copies) != 1 {
		t.Fatalf("onComplete files = %v, want exactly one", copies)
	}

	// Stand in for the import: relocate the COPY into a library root, exactly
	// as rename.Relocate does. The originals must survive this.
	libRoot := t.TempDir()
	libPath := filepath.Join(libRoot, "payload.bin")
	if err := os.Rename(copies[0], libPath); err != nil {
		t.Fatalf("relocating the import copy: %v", err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("the import must not move the seeding originals: %v", err)
	}

	// Leecher: a second Manager with an empty staging dir.
	leechStaging := t.TempDir()
	leecher := New(leechingTestConfig(leechStaging), http.DefaultClient)
	runManager(t, leecher)
	leechGID := addTorrentBody(t, leecher, body)

	// No DHT, no tracker, and this library has no local peer discovery at
	// all — the two clients must be introduced explicitly or the transfer
	// simply never starts.
	// Wait for "active" before introducing the peer: that status is set after
	// watchTorrent's DownloadAll, and a torrent with no piece priorities yet
	// accepts a peer without ever dialling it.
	waitStatus(t, leecher, leechGID, "active")
	lt := torrentOf(leecher, leechGID)
	if lt == nil {
		t.Fatal("leecher has no torrent handle")
	}
	if n := lt.AddClientPeer(clientOf(seeder)); n == 0 {
		t.Fatal("AddClientPeer added 0 peers — the leecher can never find the seeder")
	}

	waitLeechComplete(t, leecher, leechGID, seeder, seedGID)
	got, err := os.ReadFile(filepath.Join(leechStaging, "payload.bin"))
	if err != nil {
		t.Fatalf("reading the leeched payload: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("leeched payload differs from the seeder's original (%d vs %d bytes)", len(got), len(want))
	}

	// The full payload is uploaded by now, so a 0.5 ratio limit trips on the
	// next poll tick. Applied live (the ratio is not a rebuild-class field).
	next := seederCfg
	next.SeedRatioLimit = 0.5
	if err := seeder.Reconfigure(context.Background(), next); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(original); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("seeding never stopped: the original staging file %s still exists", original)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(libPath); err != nil {
		t.Errorf("the relocated library copy must survive seed cleanup: %v", err)
	}
	if got := statusOf(seeder, seedGID); got == "" {
		t.Error("the seeding entry should still be listed after its seed window closes")
	}
}

// seedingEntry builds a Manager with one seeding entry whose original staging
// file exists on disk — the fixture the ratio/duration predicates need without
// a real client.
func seedingEntry(t *testing.T, m *Manager, gid string, startedAt time.Time) (*entry, string) {
	t.Helper()
	original := filepath.Join(m.cfg.StagingDir, gid+".mkv")
	writeFile(t, original)
	m.SeedState(Download{GID: gid, Status: "complete", Dir: m.cfg.StagingDir, Files: []string{original}})
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[gid]
	e.seedStartedAt = startedAt
	e.seedTotalBytes = 1024
	e.seedPaths = []string{original}
	return e, original
}

// T-3.6 — the duration limit trips on an injected clock, and the teardown
// deletes the ORIGINAL staging files.
func TestSeedStopReason_DurationLimitStopsSeeding(t *testing.T) {
	m := NewForTesting(t.TempDir())
	m.cfg.SeedDurationMinutes = 1
	started := time.Now()
	e, original := seedingEntry(t, m, "dur", started)

	if reason := m.seedStopReason(e, started.Add(30*time.Second)); reason != "" {
		t.Fatalf("seedStopReason before the limit = %q, want empty", reason)
	}
	reason := m.seedStopReason(e, started.Add(2*time.Minute))
	if reason == "" {
		t.Fatal("seedStopReason after the duration limit = empty, want a reason")
	}

	m.stopSeeding(seedStop{gid: "dur", seedPaths: []string{original}, reason: reason})
	if _, err := os.Stat(original); !os.IsNotExist(err) {
		t.Errorf("the original staging file should be deleted after seeding stops, stat err = %v", err)
	}
}

// T-3.7 — both limits at 0 means seeding is unbounded and never
// self-terminates, no matter how long it runs.
func TestSeedStopReason_BothLimitsZeroNeverStops(t *testing.T) {
	m := NewForTesting(t.TempDir())
	m.cfg.SeedRatioLimit = 0
	m.cfg.SeedDurationMinutes = 0
	started := time.Now()
	e, _ := seedingEntry(t, m, "unbounded", started)

	for _, after := range []time.Duration{time.Minute, 48 * time.Hour, 365 * 24 * time.Hour} {
		if reason := m.seedStopReason(e, started.Add(after)); reason != "" {
			t.Errorf("seedStopReason after %s = %q, want empty (both limits disabled)", after, reason)
		}
	}
}

// T-3.8a — Manager.Start sweeps a pre-existing .import tree left by an unclean
// shutdown.
func TestStart_SweepsOrphanedImportDir(t *testing.T) {
	staging := t.TempDir()
	orphan := filepath.Join(staging, importDirName, "deadgid", "leftover.mkv")
	writeFile(t, orphan)
	bystander := filepath.Join(staging, "in-flight.mkv")
	writeFile(t, bystander)

	runManager(t, New(leechingTestConfig(staging), http.DefaultClient))

	if _, err := os.Stat(filepath.Join(staging, importDirName)); !os.IsNotExist(err) {
		t.Errorf("the .import tree should be swept on Start, stat err = %v", err)
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Errorf("the sweep must not touch anything outside .import: %v", err)
	}
}

// T-3.8b — a successful import reclaims its per-gid directory immediately,
// without waiting for a restart.
func TestClearImportCopy_RemovesPerGIDDirAfterImport(t *testing.T) {
	staging := t.TempDir()
	m := NewForTesting(staging)
	root := filepath.Join(staging, importDirName, "g1")
	writeFile(t, filepath.Join(root, "movie.mkv"))
	m.SeedState(Download{GID: "g1", Status: "complete", Dir: staging})
	m.SeedImportCopy("g1", root, []string{filepath.Join(root, "movie.mkv")})

	m.ClearImportCopy("g1")

	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("the per-gid import dir should be removed after a successful import, stat err = %v", err)
	}
	if got := m.ImportPaths("g1"); got != nil {
		t.Errorf("ImportPaths after ClearImportCopy = %v, want nil", got)
	}
	if got := m.ImportRoot("g1"); got != staging {
		t.Errorf("ImportRoot after ClearImportCopy = %q, want the staging dir %q", got, staging)
	}
}

// T-3.8c — a stageImportCopy that fails partway removes its own partial tree,
// so a failed copy is not a silent disk leak.
func TestStageImportCopy_FailureRemovesPartialTree(t *testing.T) {
	staging := t.TempDir()
	m := NewForTesting(staging)
	present := filepath.Join(staging, "e01.mkv")
	writeFile(t, present)
	missing := filepath.Join(staging, "e02.mkv") // never created

	copied, root, err := m.stageImportCopy("g2", []string{present, missing})
	if err == nil {
		t.Fatalf("stageImportCopy(%v) succeeded, want an error for the missing file", []string{present, missing})
	}
	if copied != nil || root != "" {
		t.Errorf("a failed stageImportCopy must return no paths, got copied=%v root=%q", copied, root)
	}
	if _, err := os.Stat(filepath.Join(staging, importDirName, "g2")); !os.IsNotExist(err) {
		t.Errorf("the partial import tree should be removed, stat err = %v", err)
	}
}

// stageImportCopy must refuse a path that escapes staging: e.files carries the
// RAW bencoded torrent path, and a copy written outside the import root could
// never be reclaimed by the root's own cleanup.
func TestStageImportCopy_RefusesOutOfStagingPath(t *testing.T) {
	staging := t.TempDir()
	m := NewForTesting(staging)
	outside := filepath.Join(t.TempDir(), "evil.mkv")
	writeFile(t, outside)

	if _, _, err := m.stageImportCopy("g3", []string{outside}); err == nil {
		t.Fatal("stageImportCopy accepted an out-of-staging path, want a refusal")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the refused source file must be left alone: %v", err)
	}
}

// T-6.2 — with seeding ENABLED but the download still in progress, the torrent
// must not be seeding. This is the single test that fails if the post-add
// DisallowDataUpload call is replaced by AddTorrentOpts.DisallowDataUpload,
// which anacrolix v1.61.0 declares but never reads.
//
// Preconditions that make Seeding()==false mean what we want (it is a
// disjunction): the client is built Seed=true/NoUpload=false, the torrent is
// live, DisableAggressiveUpload is left at its default false, and the download
// is genuinely incomplete.
func TestAddTorrent_DisallowsUploadWhileDownloading(t *testing.T) {
	m := startManager(t, seedingTestConfig(t.TempDir()))
	// buildTestTorrent stages its payload OUTSIDE staging, so this torrent
	// never completes and stays in the in-progress state under test.
	gid := addTestTorrent(t, m, 11)

	tt := torrentOf(m, gid)
	if tt == nil {
		t.Fatal("no torrent handle")
	}
	if tt.Seeding() {
		t.Error("an in-progress download must not seed even with seeding enabled")
	}

	// Positive control: the seed window is what flips it.
	m.beginSeeding(gid, tt, nil, tt.Length())
	if !tt.Seeding() {
		t.Error("beginSeeding should re-allow upload, but Seeding() is still false")
	}
}
