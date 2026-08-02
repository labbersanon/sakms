package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/downloader"
)

// Tests for the apply half of PUT /api/downloader/config: the handler hands the
// saved document to Manager.Reconfigure and reports which apply path ran.
//
// HOW "DID IT REBUILD?" IS OBSERVED, since the Manager exposes no such flag:
// Reconfigure refuses ANY rebuild while a torrent is still in the pre-metadata
// "waiting" state (there is no metainfo to snapshot yet), and that refusal
// surfaces as a 409. So a save made with a seeded "waiting" entry that answers
// 200 is proof the engine did NOT take the rebuild path, and one that answers
// 409 is proof it did. That is a real behavioral probe rather than an assertion
// that the response agrees with itself.

// applyBaseline is the config every apply test starts from. Seeding is OFF so
// the false->true rebuild direction is reachable; everything else is
// non-default so a comparison can't pass by accident.
func applyBaseline() apidto.DownloaderConfig {
	cfg := validDownloaderConfig()
	cfg.SeedingEnabled = false
	return cfg
}

// downloaderConfigOf mirrors applyBaseline into the engine's own Config, so the
// Manager's idea of "current" matches what the store holds. Without this the
// handler's classification and Reconfigure's internal one would compare against
// different baselines and the tests would prove nothing.
func downloaderConfigOf(c apidto.DownloaderConfig) downloader.Config {
	return downloader.Config{
		StagingDir:            c.StagingDir,
		MaxConc:               c.MaxConcurrent,
		MaxConn:               c.MaxConnections,
		DownloadRateLimit:     c.DownloadRateLimitBytes,
		DHTEnabled:            c.DHTEnabled,
		PEXEnabled:            c.PEXEnabled,
		ListenPort:            c.ListenPort,
		ObfuscationMode:       c.ObfuscationMode,
		SeedingEnabled:        c.SeedingEnabled,
		SeedRatioLimit:        c.SeedRatioLimit,
		SeedDurationMinutes:   c.SeedDurationMinutes,
		StaleThresholdMinutes: c.StaleThresholdMinutes,
	}
}

// newApplyServer wires the config routes against a Manager whose live config is
// base, and persists base so store and engine agree before the test's own save.
// The Manager has no torrent client (Start is never called), which is exactly
// the shape the refusal checks run against — they read m.entries, not the
// client.
func newApplyServer(t *testing.T, base apidto.DownloaderConfig) (*httptest.Server, *downloader.Manager) {
	t.Helper()
	dl := downloader.New(downloaderConfigOf(base), nil)
	srv, _ := newDownloaderConfigServerWith(t, dl)
	if code := putDownloaderConfig(t, srv, base); code != http.StatusOK {
		t.Fatalf("seeding baseline config: expected 200, got %d", code)
	}
	return srv, dl
}

// putForApplyResult PUTs a config and decodes the apply-result body.
func putForApplyResult(t *testing.T, srv *httptest.Server, cfg apidto.DownloaderConfig) (int, apidto.DownloaderConfigApplyResult) {
	t.Helper()
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshaling config: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/downloader/config", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building PUT: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, apidto.DownloaderConfigApplyResult{}
	}
	var got apidto.DownloaderConfigApplyResult
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding apply result: %v", err)
	}
	return resp.StatusCode, got
}

// TestDownloaderConfigApply_LivePatchDoesNotRebuild covers every field the
// canonical settings table marks live-applicable. Each save is made while a
// torrent sits in the "waiting" state, which Reconfigure refuses to rebuild
// around — so a 200 here is a behavioral proof that no rebuild was attempted,
// not just a claim by the response body.
func TestDownloaderConfigApply_LivePatchDoesNotRebuild(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*apidto.DownloaderConfig)
	}{
		{"download rate limit", func(c *apidto.DownloaderConfig) { c.DownloadRateLimitBytes = 5 << 20 }},
		{"max connections", func(c *apidto.DownloaderConfig) { c.MaxConnections = 42 }},
		{"seed ratio limit", func(c *apidto.DownloaderConfig) { c.SeedRatioLimit = 9.5 }},
		{"seed duration", func(c *apidto.DownloaderConfig) { c.SeedDurationMinutes = 4320 }},
		{"stale threshold", func(c *apidto.DownloaderConfig) { c.StaleThresholdMinutes = 90 }},
		{"nothing at all", func(c *apidto.DownloaderConfig) {}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := applyBaseline()
			srv, dl := newApplyServer(t, base)
			dl.SeedState(downloader.Download{GID: "abc123", Status: "waiting"})

			next := base
			tc.mutate(&next)

			code, got := putForApplyResult(t, srv, next)
			if code != http.StatusOK {
				t.Fatalf("live-only save: expected 200, got %d — a rebuild was attempted and refused", code)
			}
			if got.Applied != "live" {
				t.Errorf("applied = %q, want %q", got.Applied, "live")
			}
			if got.RestartRequired {
				t.Error("restartRequired = true for a live-patchable change")
			}
			if got.Message == "" {
				t.Error("apply result carries no message; the save-status line renders it verbatim")
			}
		})
	}
}

// TestDownloaderConfigApply_SeedingOffIsLive pins the DIRECTIONAL half of the
// seeding classification. Turning seeding ON needs a client rebuild (the
// engine's upload gate is fixed at construction); turning it OFF is genuinely
// live, because the per-torrent DisallowDataUpload call short-circuits ahead of
// it. A mirror written as old != next would wrongly rebuild here.
func TestDownloaderConfigApply_SeedingOffIsLive(t *testing.T) {
	base := applyBaseline()
	base.SeedingEnabled = true
	srv, dl := newApplyServer(t, base)
	dl.SeedState(downloader.Download{GID: "abc123", Status: "waiting"})

	next := base
	next.SeedingEnabled = false

	code, got := putForApplyResult(t, srv, next)
	if code != http.StatusOK {
		t.Fatalf("disabling seeding: expected 200, got %d — turning seeding OFF must not rebuild", code)
	}
	if got.Applied != "live" || got.RestartRequired {
		t.Errorf("disabling seeding reported %+v, want applied=live restartRequired=false", got)
	}
}

// TestDownloaderConfigApply_RebuildClassRequiresRestart covers every field the
// canonical table marks rebuild — including seedingEnabled false->true, which
// that table originally listed as live and which was corrected during BE-2's
// execution once uploadAllowed's NoUpload check was read directly.
//
// No entries are seeded here: with nothing in flight the rebuild is permitted,
// so the assertion is about the CLASSIFICATION rather than the refusal.
func TestDownloaderConfigApply_RebuildClassRequiresRestart(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*apidto.DownloaderConfig)
	}{
		{"staging dir", func(c *apidto.DownloaderConfig) { c.StagingDir = "/tmp/sakms-elsewhere" }},
		{"listen port", func(c *apidto.DownloaderConfig) { c.ListenPort = 6881 }},
		{"dht", func(c *apidto.DownloaderConfig) { c.DHTEnabled = !c.DHTEnabled }},
		{"pex", func(c *apidto.DownloaderConfig) { c.PEXEnabled = !c.PEXEnabled }},
		{"obfuscation mode", func(c *apidto.DownloaderConfig) { c.ObfuscationMode = "off" }},
		{"seeding off to on", func(c *apidto.DownloaderConfig) { c.SeedingEnabled = true }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := applyBaseline()
			srv, _ := newApplyServer(t, base)

			next := base
			tc.mutate(&next)

			code, got := putForApplyResult(t, srv, next)
			if code != http.StatusOK {
				t.Fatalf("rebuild-class save: expected 200, got %d", code)
			}
			if got.Applied != "rebuilt" {
				t.Errorf("applied = %q, want %q", got.Applied, "rebuilt")
			}
			if !got.RestartRequired {
				t.Error("restartRequired = false for a rebuild-class change; the UI would promise a quiet instant save")
			}
		})
	}
}

// TestDownloaderConfigApply_RefusalIs409AndPersistsNothing is the ordering
// guarantee: Reconfigure runs BEFORE the first settings write, so a refused
// save leaves the stored document byte-for-byte as it was. Persisting first
// would strand a setting the engine rejected — the stored-and-ignored failure
// this feature exists to prevent.
//
// Two refusal shapes, both real:
//   - a staging-dir move while a download holds open file handles
//   - any rebuild-class change (here, enabling seeding) while a magnet-added
//     torrent is still fetching its metadata and has no metainfo to snapshot
func TestDownloaderConfigApply_RefusalIs409AndPersistsNothing(t *testing.T) {
	cases := []struct {
		name        string
		entryStatus string
		mutate      func(*apidto.DownloaderConfig)
		key         string
		wantStored  string
	}{
		{
			name:        "staging dir moved while a download is active",
			entryStatus: "active",
			mutate:      func(c *apidto.DownloaderConfig) { c.StagingDir = "/tmp/sakms-elsewhere" },
			key:         DownloaderStagingDirKey,
			wantStored:  "/tmp/sakms-staging",
		},
		{
			name:        "seeding enabled while a torrent awaits metadata",
			entryStatus: "waiting",
			mutate:      func(c *apidto.DownloaderConfig) { c.SeedingEnabled = true },
			key:         TorrentSeedingEnabledKey,
			wantStored:  "false",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := applyBaseline()
			dl := downloader.New(downloaderConfigOf(base), nil)
			srv, store := newDownloaderConfigServerWith(t, dl)
			if code := putDownloaderConfig(t, srv, base); code != http.StatusOK {
				t.Fatalf("seeding baseline config: expected 200, got %d", code)
			}
			dl.SeedState(downloader.Download{GID: "abc123", Status: tc.entryStatus})

			next := base
			tc.mutate(&next)

			if code := putDownloaderConfig(t, srv, next); code != http.StatusConflict {
				t.Fatalf("refused save: expected 409, got %d", code)
			}
			got, err := store.Get(context.Background(), tc.key)
			if err != nil {
				t.Fatalf("reading setting %q: %v", tc.key, err)
			}
			if got != tc.wantStored {
				t.Errorf("setting %q = %q after a refused save, want %q unchanged — the refusal partially applied", tc.key, got, tc.wantStored)
			}
			// The whole document, not just the one field, must be untouched.
			if stored := getDownloaderConfig(t, srv); stored != base {
				t.Errorf("config after a refused save:\n got %+v\nwant %+v", stored, base)
			}
		})
	}
}

// TestDownloaderConfigApply_BlankStagingDirIsNotAMove pins the one field where
// the store and the engine legitimately disagree. GET answers "" for an unset
// staging dir (this handler has no dataDir to build <dataDir>/downloads from)
// while the Manager holds the path boot resolved. A client that round-trips
// that "" back must not be read as moving the staging directory — on a fresh
// install that would make every single save rebuild-class, and would hand the
// torrent client an empty DataDir.
func TestDownloaderConfigApply_BlankStagingDirIsNotAMove(t *testing.T) {
	base := applyBaseline()
	base.StagingDir = "" // never configured; the engine resolved its own default
	live := downloaderConfigOf(base)
	live.StagingDir = "/var/lib/sakms/downloads" // what boot built from dataDir
	dl := downloader.New(live, nil)
	srv, store := newDownloaderConfigServerWith(t, dl)
	if code := putDownloaderConfig(t, srv, base); code != http.StatusOK {
		t.Fatalf("seeding baseline config: expected 200, got %d", code)
	}
	dl.SeedState(downloader.Download{GID: "abc123", Status: "waiting"})

	code, got := putForApplyResult(t, srv, base)
	if code != http.StatusOK {
		t.Fatalf("blank staging dir: expected 200, got %d — an empty staging dir was read as a move", code)
	}
	if got.Applied != "live" || got.RestartRequired {
		t.Errorf("blank staging dir reported %+v, want applied=live restartRequired=false", got)
	}
	// "" is what gets STORED, so the next boot keeps re-deriving the default
	// rather than freezing today's resolved path into the settings table.
	if stored, err := store.Get(context.Background(), DownloaderStagingDirKey); err != nil {
		t.Fatalf("reading staging dir: %v", err)
	} else if stored != "" {
		t.Errorf("stored staging dir = %q, want %q — the live path was frozen into settings", stored, "")
	}
}

// TestDownloaderConfigApply_ClearingStagingDirIsRebuildClass is the companion to
// BlankStagingDirIsNotAMove above, and the two look contradictory until you
// notice they differ in what was STORED beforehand. There: nothing was ever
// configured, "" -> "" is genuinely no change, live is correct. Here: a real
// path was stored and the operator cleared it, so the stored document really did
// change — and on the next boot buildDownloader re-derives <dataDir>/downloads,
// which need not be the path currently in use. Reporting that as a quiet live
// save is the "applies now, silently reverts on restart" failure class.
//
// The seeded "waiting" entry makes the deliberate handler/engine divergence
// visible rather than hypothetical: Reconfigure refuses any real rebuild while a
// torrent has no metainfo to snapshot, so the 200 proves the engine did NOT tear
// itself down (its resolved path is unchanged either way) even though the
// response correctly warns that a restart changes behavior. See
// downloaderRebuildClass's doc comment for why over-reporting is the right
// direction here.
func TestDownloaderConfigApply_ClearingStagingDirIsRebuildClass(t *testing.T) {
	base := applyBaseline()
	base.StagingDir = t.TempDir() // a real, configured path — not the boot default
	srv, dl := newApplyServer(t, base)
	dl.SeedState(downloader.Download{GID: "abc123", Status: "waiting"})

	next := base
	next.StagingDir = "" // operator clears it back to the boot-computed default

	code, got := putForApplyResult(t, srv, next)
	if code != http.StatusOK {
		t.Fatalf("clearing staging dir: expected 200, got %d", code)
	}
	if got.Applied != "rebuilt" {
		t.Errorf("applied = %q, want %q — clearing a stored staging dir was reported as a live no-op", got.Applied, "rebuilt")
	}
	if !got.RestartRequired {
		t.Error("restartRequired = false after clearing a stored staging dir; the next boot re-derives a possibly different path with no warning")
	}
	// "" is what gets STORED, so boot keeps re-deriving the default rather than
	// freezing today's resolved path into the settings table.
	if stored := getDownloaderConfig(t, srv).StagingDir; stored != "" {
		t.Errorf("stored staging dir = %q, want %q", stored, "")
	}
}
