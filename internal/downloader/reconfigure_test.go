package downloader

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"
)

// These tests exercise Reconfigure against a REAL anacrolix client, which is
// the only way the two-tier apply can be verified at all: the live tier mutates
// a *rate.Limiter and calls Torrent.SetMaxEstablishedConns, and the rebuild
// tier swaps the client out from under running watcher goroutines.
//
// Every client here is built with ListenPort 0 and DHT off. Port 0 is
// mandatory, not tidiness — the library's default is a FIXED 42069, so two
// clients in one test process either fail to bind or silently never connect.
// DHT off keeps the suite from reaching the network.

func testConfig(staging string) Config {
	return Config{
		StagingDir:      staging,
		MaxConc:         3,
		MaxConn:         4,
		ListenPort:      0,
		DHTEnabled:      false,
		PEXEnabled:      false,
		ObfuscationMode: "prefer",
	}
}

// startManager brings up a Manager on a real client and tears it down at test
// end. It waits for Start to install the client rather than sleeping.
func startManager(t *testing.T, cfg Config) *Manager {
	t.Helper()
	m := New(cfg, http.DefaultClient)
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

func (m *Manager) currentClient() any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tc == nil {
		return nil
	}
	return m.tc
}

// buildTestTorrent produces .torrent bytes for a small unique payload written
// OUTSIDE the manager's staging dir, so the client never finds the data and the
// torrent settles at "active" rather than completing instantly.
func buildTestTorrent(t *testing.T, seed byte) []byte {
	t.Helper()
	src := t.TempDir()
	payload := make([]byte, 64*1024)
	for i := range payload {
		payload[i] = byte(i) ^ seed
	}
	path := filepath.Join(src, "payload.bin")
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

// addTestTorrent serves a synthetic .torrent over httptest and adds it through
// the Manager's real AddTorrent path, so watchTorrent is genuinely running.
func addTestTorrent(t *testing.T, m *Manager, seed byte) string {
	t.Helper()
	body := buildTestTorrent(t, seed)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	gid, err := m.AddTorrent(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	waitStatus(t, m, gid, "active")
	return gid
}

func waitStatus(t *testing.T, m *Manager, gid, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if got := statusOf(m, gid); got == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("download %s status = %q, want %q", gid, got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func statusOf(m *Manager, gid string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[gid]; ok {
		return e.status
	}
	return ""
}

func handleOf(m *Manager, gid string) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[gid]
	if !ok || e.t == nil {
		return nil
	}
	return e.t
}

// T-2.3 — the riskiest case, written first. A rebuild-class change must swap the
// client, re-add every torrent under its ORIGINAL gid (the info-hash hex string
// is stable across a rebuild, which is what keeps grab rows keyed on
// download_gid intact), and leave a paused entry paused.
func TestReconfigure_RebuildClassChange_ReaddsTorrentsAndPreservesPause(t *testing.T) {
	cfg := testConfig(t.TempDir())
	m := startManager(t, cfg)

	activeGID := addTestTorrent(t, m, 1)
	pausedGID := addTestTorrent(t, m, 2)
	if activeGID == pausedGID {
		t.Fatal("test torrents collided on one info hash")
	}
	if err := m.Pause(pausedGID); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	oldClient := m.currentClient()
	oldActiveHandle := handleOf(m, activeGID)
	oldPausedHandle := handleOf(m, pausedGID)

	next := cfg
	next.PEXEnabled = true // rebuild-class: DisablePEX is fixed at NewClient
	if err := m.Reconfigure(context.Background(), next); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}

	newClient := m.currentClient()
	if newClient == nil {
		t.Fatal("no client after rebuild")
	}
	if newClient == oldClient {
		t.Fatal("client pointer unchanged — a rebuild-class change did not rebuild")
	}

	for _, tc := range []struct {
		gid       string
		oldHandle any
		want      string
	}{
		{activeGID, oldActiveHandle, "active"},
		{pausedGID, oldPausedHandle, "paused"},
	} {
		h := handleOf(m, tc.gid)
		if h == nil {
			t.Fatalf("gid %s lost its torrent handle across the rebuild", tc.gid)
		}
		if h == tc.oldHandle {
			t.Fatalf("gid %s still holds the pre-rebuild *Torrent", tc.gid)
		}
		if got := statusOf(m, tc.gid); got != tc.want {
			t.Fatalf("gid %s status after rebuild = %q, want %q", tc.gid, got, tc.want)
		}
	}

	// The status must STAY put: the old client's watchers wake asynchronously
	// on Closed() and, without the identity guard, would mark a freshly re-added
	// entry "removed" — freezing it out of pollSnapshot's refresh for good.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := statusOf(m, activeGID); got != "active" {
			t.Fatalf("active entry was clobbered to %q by a stale watcher", got)
		}
		if got := statusOf(m, pausedGID); got != "paused" {
			t.Fatalf("paused entry was clobbered to %q by a stale watcher", got)
		}
		time.Sleep(20 * time.Millisecond)
	}

	m.mu.Lock()
	torrents := len(m.tc.Torrents())
	m.mu.Unlock()
	if torrents != 2 {
		t.Fatalf("new client holds %d torrents, want 2", torrents)
	}
}

// T-2.1 — a save that only touches the rate limit must re-rate the EXISTING
// limiter in place and must not tear down in-flight downloads.
func TestReconfigure_RateLimitOnly_NoRebuild(t *testing.T) {
	cfg := testConfig(t.TempDir())
	m := startManager(t, cfg)

	oldClient := m.currentClient()

	m.mu.Lock()
	oldLimiter := m.rateLimiter
	m.mu.Unlock()
	if oldLimiter == nil {
		t.Fatal("buildClient left DownloadRateLimiter nil — SetLimit would panic on the first live save")
	}
	if oldLimiter.Limit() != rate.Inf {
		t.Fatalf("default limiter Limit() = %v, want rate.Inf (0 means unlimited, never rate.Limit(0))", oldLimiter.Limit())
	}

	const want = 500 * 1024
	next := cfg
	next.DownloadRateLimit = want
	if err := m.Reconfigure(context.Background(), next); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}

	if m.currentClient() != oldClient {
		t.Fatal("client pointer changed — a rate-limit-only save must not rebuild")
	}

	m.mu.Lock()
	newLimiter := m.rateLimiter
	m.mu.Unlock()
	if newLimiter != oldLimiter {
		t.Fatal("limiter POINTER was swapped; every read site inside the library caches the deref, so the running client would stay on the old limiter")
	}
	if newLimiter.Limit() != rate.Limit(want) {
		t.Fatalf("Limit() = %v, want %v", newLimiter.Limit(), rate.Limit(want))
	}
	if newLimiter.Burst() != 1<<20 {
		t.Fatalf("Burst() = %d, want %d — a finite limit with a zero burst issues no tokens and stalls every download", newLimiter.Burst(), 1<<20)
	}

	// And back to unlimited.
	next.DownloadRateLimit = 0
	if err := m.Reconfigure(context.Background(), next); err != nil {
		t.Fatalf("Reconfigure back to unlimited: %v", err)
	}
	if newLimiter.Limit() != rate.Inf {
		t.Fatalf("Limit() = %v after clearing the limit, want rate.Inf", newLimiter.Limit())
	}
}

// T-2.2 — a max-connections change must reach every LIVE torrent, not just
// future adds (EstablishedConnsPerTorrent is read once, at torrent-add).
func TestReconfigure_MaxConnections_AppliedToLiveTorrents(t *testing.T) {
	cfg := testConfig(t.TempDir())
	m := startManager(t, cfg)

	addTestTorrent(t, m, 3)
	addTestTorrent(t, m, 4)

	oldClient := m.currentClient()

	const want = 9
	next := cfg
	next.MaxConn = want
	if err := m.Reconfigure(context.Background(), next); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}
	if m.currentClient() != oldClient {
		t.Fatal("client pointer changed — a max-connections change is live-applicable")
	}

	m.mu.Lock()
	torrents := m.tc.Torrents()
	m.mu.Unlock()
	if len(torrents) != 2 {
		t.Fatalf("client holds %d torrents, want 2", len(torrents))
	}
	for i, tor := range torrents {
		// SetMaxEstablishedConns returns the PREVIOUS max, so setting the same
		// value back reads out what Reconfigure left behind.
		if got := tor.SetMaxEstablishedConns(want); got != want {
			t.Fatalf("torrent %d max established conns = %d, want %d", i, got, want)
		}
	}

	// And FUTURE adds. ClientConfig.EstablishedConnsPerTorrent is read once, at
	// torrent-add, off the client's own config — which Reconfigure cannot
	// rewrite safely. Without an explicit per-add setter call, a torrent grabbed
	// after a live save silently keeps the boot-time value.
	newGID := addTestTorrent(t, m, 6)
	added, _ := handleOf(m, newGID).(interface{ SetMaxEstablishedConns(int) int })
	if added == nil {
		t.Fatal("newly added torrent has no handle")
	}
	if got := added.SetMaxEstablishedConns(want); got != want {
		t.Fatalf("torrent added AFTER the save has max established conns = %d, want %d", got, want)
	}
}

// T-2.4 — StagingDir cannot move out from under in-flight downloads, and a
// refusal must leave the stored config exactly as it was.
func TestReconfigure_StagingDirWithActiveEntry_Refused(t *testing.T) {
	cfg := testConfig(t.TempDir())
	m := startManager(t, cfg)

	addTestTorrent(t, m, 5)
	oldClient := m.currentClient()

	next := cfg
	next.StagingDir = t.TempDir()
	next.DownloadRateLimit = 123456 // would have applied, had the save been accepted

	err := m.Reconfigure(context.Background(), next)
	if err == nil {
		t.Fatal("Reconfigure accepted a StagingDir move with an active download")
	}
	if !errors.Is(err, ErrRebuildRefused) {
		t.Fatalf("error %v does not match ErrRebuildRefused; the HTTP layer keys on it for its 409", err)
	}

	if got := m.StagingDir(); got != cfg.StagingDir {
		t.Fatalf("StagingDir() = %q after a refusal, want the unchanged %q", got, cfg.StagingDir)
	}
	m.mu.Lock()
	storedRate := m.cfg.DownloadRateLimit
	m.mu.Unlock()
	if storedRate != 0 {
		t.Fatalf("DownloadRateLimit = %d after a refusal, want the config left wholly untouched", storedRate)
	}
	if m.currentClient() != oldClient {
		t.Fatal("client was rebuilt despite the refusal")
	}
}

// T-2.4, second case — a magnet that has not yet received its info has no
// metainfo to snapshot, so the rebuild is refused rather than silently
// stranding the download.
func TestReconfigure_WaitingEntry_RefusesRebuild(t *testing.T) {
	cfg := testConfig(t.TempDir())
	m := startManager(t, cfg)

	m.mu.Lock()
	m.entries["pre-metadata"] = &entry{status: "waiting"}
	m.mu.Unlock()

	next := cfg
	next.PEXEnabled = true // rebuild-class, and StagingDir is untouched

	err := m.Reconfigure(context.Background(), next)
	if err == nil {
		t.Fatal("Reconfigure rebuilt while an entry was still fetching metadata")
	}
	if !errors.Is(err, ErrRebuildRefused) {
		t.Fatalf("error %v does not match ErrRebuildRefused", err)
	}
	m.mu.Lock()
	stored := m.cfg.PEXEnabled
	m.mu.Unlock()
	if stored {
		t.Fatal("config was mutated despite the refusal")
	}
}

// A live-tier-only save on a Manager whose engine was never started must store
// the config and return cleanly, so a settings save before Start (or against a
// test Manager) is not an error.
func TestReconfigure_BeforeStart_StoresConfig(t *testing.T) {
	cfg := testConfig(t.TempDir())
	m := New(cfg, http.DefaultClient)

	next := cfg
	next.DownloadRateLimit = 4096
	next.PEXEnabled = true
	if err := m.Reconfigure(context.Background(), next); err != nil {
		t.Fatalf("Reconfigure before Start: %v", err)
	}
	m.mu.Lock()
	got := m.cfg
	m.mu.Unlock()
	if got.DownloadRateLimit != 4096 || !got.PEXEnabled {
		t.Fatalf("config = %+v, want the new values stored for the next Start", got)
	}
}

func TestRebuildRequired_Classification(t *testing.T) {
	base := Config{StagingDir: "/s", ListenPort: 0, DHTEnabled: true, PEXEnabled: true, ObfuscationMode: "prefer"}

	live := []struct {
		name string
		mut  func(*Config)
	}{
		{"rate limit", func(c *Config) { c.DownloadRateLimit = 1024 }},
		{"max connections", func(c *Config) { c.MaxConn = 12 }},
		{"max concurrent", func(c *Config) { c.MaxConc = 7 }},
		{"seed ratio", func(c *Config) { c.SeedRatioLimit = 2 }},
		{"seed duration", func(c *Config) { c.SeedDurationMinutes = 60 }},
		{"stale threshold", func(c *Config) { c.StaleThresholdMinutes = 60 }},
		{"obfuscation alias", func(c *Config) { c.ObfuscationMode = "" }}, // "" == "prefer"
	}
	for _, tc := range live {
		next := base
		tc.mut(&next)
		if rebuildRequired(base, next) {
			t.Errorf("%s: wants a rebuild, but it is live-applicable", tc.name)
		}
	}

	rebuild := []struct {
		name string
		mut  func(*Config)
	}{
		{"staging dir", func(c *Config) { c.StagingDir = "/other" }},
		{"listen port", func(c *Config) { c.ListenPort = 6881 }},
		{"dht", func(c *Config) { c.DHTEnabled = false }},
		{"pex", func(c *Config) { c.PEXEnabled = false }},
		{"obfuscation", func(c *Config) { c.ObfuscationMode = "off" }},
		{"seeding off->on", func(c *Config) { c.SeedingEnabled = true }},
	}
	for _, tc := range rebuild {
		next := base
		tc.mut(&next)
		if !rebuildRequired(base, next) {
			t.Errorf("%s: is applied live, but the library fixes it at NewClient", tc.name)
		}
	}

	// Seeding ON -> OFF is genuinely live-safe: per-torrent DisallowDataUpload
	// short-circuits ahead of the reciprocation fallthrough.
	on := base
	on.SeedingEnabled = true
	if rebuildRequired(on, base) {
		t.Error("turning seeding off wants a rebuild, but it is live-applicable")
	}
}

func TestDownloadRateLimiter_AlwaysReal(t *testing.T) {
	if l := newDownloadRateLimiter(0); l == nil || l.Limit() != rate.Inf {
		t.Fatalf("unlimited limiter = %v, want a real limiter at rate.Inf", l)
	}
	// A finite limit must never pair with a zero burst.
	for _, bps := range []int{1, 1024, 1 << 20, 8 << 20} {
		l := newDownloadRateLimiter(bps)
		if l == nil {
			t.Fatalf("%d B/s: nil limiter", bps)
		}
		if l.Burst() <= 0 {
			t.Fatalf("%d B/s: burst = %d, want nonzero", bps, l.Burst())
		}
		if l.Limit() != rate.Limit(bps) {
			t.Fatalf("%d B/s: Limit() = %v", bps, l.Limit())
		}
	}
}

func TestObfuscationPolicy_OffIsStrict(t *testing.T) {
	if p := obfuscationPolicy("off"); !p.RequirePreferred || p.Preferred {
		t.Fatalf(`"off" = %+v, want {Preferred:false, RequirePreferred:true} — it REJECTS encrypted peers`, p)
	}
	if p := obfuscationPolicy("require"); !p.RequirePreferred || !p.Preferred {
		t.Fatalf(`"require" = %+v`, p)
	}
	for _, mode := range []string{"prefer", ""} {
		if p := obfuscationPolicy(mode); p.RequirePreferred || !p.Preferred {
			t.Fatalf("%q = %+v, want the library default {Preferred:true, RequirePreferred:false}", mode, p)
		}
	}
}
