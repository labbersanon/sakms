package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/settings"
)

// newDownloaderConfigServer wires just the two /api/downloader/config routes
// against a fresh settings store, returning both so a test can assert on the
// wire AND on what was actually persisted (the settings KEY STRINGS are part of
// the contract other tasks read — see T-1.1).
//
// No download Manager: these tests cover validation and storage, which the PUT
// handler performs identically whether or not an engine is attached (a nil
// Manager is a supported state in this package — see pauseAllActive). The
// apply-to-the-running-engine half is covered by downloads_config_apply_test.go,
// which wires a real Manager.
func newDownloaderConfigServer(t *testing.T) (*httptest.Server, *settings.Store) {
	t.Helper()
	return newDownloaderConfigServerWith(t, nil)
}

// newDownloaderConfigServerWith is newDownloaderConfigServer with an explicit
// download Manager for the apply-path tests.
func newDownloaderConfigServerWith(t *testing.T, dl *downloader.Manager) (*httptest.Server, *settings.Store) {
	t.Helper()
	store := newSettingsStore(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/downloader/config", getDownloaderConfigHandler(store))
	mux.HandleFunc("PUT /api/downloader/config", putDownloaderConfigHandler(store, dl))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, store
}

// getDownloaderConfig fetches the config as a typed DTO.
func getDownloaderConfig(t *testing.T, srv *httptest.Server) apidto.DownloaderConfig {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/downloader/config")
	if err != nil {
		t.Fatalf("GET config: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET config: expected 200, got %d", resp.StatusCode)
	}
	var cfg apidto.DownloaderConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decoding config: %v", err)
	}
	return cfg
}

// putDownloaderConfigRaw PUTs a raw JSON body, so a test can omit a field
// entirely — marshaling apidto.DownloaderConfig can never do that (no
// omitempty anywhere, deliberately), and omission is exactly what T-1.1 pins.
func putDownloaderConfigRaw(t *testing.T, srv *httptest.Server, body []byte) int {
	t.Helper()
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
	return resp.StatusCode
}

// validDownloaderConfig is a fully-populated, entirely non-default config. Every
// value differs from its documented default so a round-trip test cannot pass by
// accidentally re-reading a default.
func validDownloaderConfig() apidto.DownloaderConfig {
	return apidto.DownloaderConfig{
		StagingDir:             "/tmp/sakms-staging",
		MaxConcurrent:          7,
		MaxConnections:         9,
		DownloadRateLimitBytes: 1048576,
		DHTEnabled:             false,
		PEXEnabled:             false,
		ListenPort:             51413,
		ObfuscationMode:        "require",
		SeedingEnabled:         true,
		SeedRatioLimit:         2.5,
		SeedDurationMinutes:    60,
		StaleThresholdMinutes:  15,
	}
}

func putDownloaderConfig(t *testing.T, srv *httptest.Server, cfg apidto.DownloaderConfig) int {
	t.Helper()
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshaling config: %v", err)
	}
	return putDownloaderConfigRaw(t, srv, body)
}

// TestDownloaderConfig_UnsetKeysReturnDefaults is T-1.1's defaults half: a store
// with nothing written to it answers GET with the documented default for every
// key, so a fresh install behaves identically to one before these knobs existed.
//
// StagingDir is deliberately "" rather than <dataDir>/downloads — this handler
// has no data directory to build that path from; boot supplies the real one.
func TestDownloaderConfig_UnsetKeysReturnDefaults(t *testing.T) {
	srv, _ := newDownloaderConfigServer(t)
	got := getDownloaderConfig(t, srv)

	want := apidto.DownloaderConfig{
		StagingDir:             "",
		MaxConcurrent:          3,
		MaxConnections:         4,
		DownloadRateLimitBytes: 0,
		DHTEnabled:             true,
		PEXEnabled:             true,
		ListenPort:             42069,
		ObfuscationMode:        "prefer",
		SeedingEnabled:         false,
		SeedRatioLimit:         1.0,
		SeedDurationMinutes:    2880,
		StaleThresholdMinutes:  240,
	}
	if got != want {
		t.Errorf("unset config defaults:\n got %+v\nwant %+v", got, want)
	}
}

// TestDownloaderConfig_RoundTripsEveryKey is T-1.1's round-trip half: a PUT of
// every field in the canonical settings table (plus MaxConcurrent) comes back
// out of GET unchanged, and lands under the exact settings key strings the
// engine reads.
func TestDownloaderConfig_RoundTripsEveryKey(t *testing.T) {
	srv, store := newDownloaderConfigServer(t)
	want := validDownloaderConfig()

	if code := putDownloaderConfig(t, srv, want); code != http.StatusOK {
		t.Fatalf("PUT valid config: expected 200, got %d", code)
	}
	if got := getDownloaderConfig(t, srv); got != want {
		t.Errorf("config did not round-trip:\n got %+v\nwant %+v", got, want)
	}

	// The key strings themselves are contract: BE-2's buildDownloader reads
	// these exact names, so a rename here has to fail loudly rather than
	// silently stranding every stored value.
	ctx := context.Background()
	for key, want := range map[string]string{
		DownloaderStagingDirKey:         "/tmp/sakms-staging",
		DownloaderMaxConcurrentKey:      "7",
		DownloaderMaxConnectionsKey:     "9",
		TorrentDownloadRateLimitKey:     "1048576",
		TorrentDHTEnabledKey:            "false",
		TorrentPEXEnabledKey:            "false",
		TorrentListenPortKey:            "51413",
		TorrentObfuscationModeKey:       "require",
		TorrentSeedingEnabledKey:        "true",
		TorrentSeedRatioLimitKey:        "2.5",
		TorrentSeedDurationMinutesKey:   "60",
		TorrentStaleThresholdMinutesKey: "15",
	} {
		got, err := store.Get(ctx, key)
		if err != nil {
			t.Errorf("reading setting %q: %v", key, err)
			continue
		}
		if got != want {
			t.Errorf("setting %q = %q, want %q", key, got, want)
		}
	}
}

// TestDownloaderConfig_RoundTripsZeroAndOffValues covers the other end of every
// range: the values that mean "off"/"unlimited" must survive a round-trip
// exactly, not be mistaken for unset and silently replaced by a default. The
// getSetting* helpers fall back to the default on an EMPTY string, so a bug
// that stored "" instead of "0" would read back as 240/1.0/2880 here.
func TestDownloaderConfig_RoundTripsZeroAndOffValues(t *testing.T) {
	srv, _ := newDownloaderConfigServer(t)
	want := validDownloaderConfig()
	want.DownloadRateLimitBytes = 0 // unlimited
	want.SeedRatioLimit = 0         // no ratio limit
	want.SeedDurationMinutes = 0    // no duration limit
	want.StaleThresholdMinutes = 0  // stale detection disabled
	want.SeedingEnabled = false
	want.ObfuscationMode = "off"

	if code := putDownloaderConfig(t, srv, want); code != http.StatusOK {
		t.Fatalf("PUT zero-value config: expected 200, got %d", code)
	}
	if got := getDownloaderConfig(t, srv); got != want {
		t.Errorf("zero/off values did not round-trip:\n got %+v\nwant %+v", got, want)
	}
}

// TestDownloaderConfig_AcceptsEveryObfuscationMode pins all three enum members
// as valid, so a future tightening of the switch cannot silently drop one.
func TestDownloaderConfig_AcceptsEveryObfuscationMode(t *testing.T) {
	for _, mode := range []string{"require", "prefer", "off"} {
		t.Run(mode, func(t *testing.T) {
			srv, _ := newDownloaderConfigServer(t)
			cfg := validDownloaderConfig()
			cfg.ObfuscationMode = mode
			if code := putDownloaderConfig(t, srv, cfg); code != http.StatusOK {
				t.Fatalf("obfuscationMode %q: expected 200, got %d", mode, code)
			}
			if got := getDownloaderConfig(t, srv).ObfuscationMode; got != mode {
				t.Errorf("obfuscationMode round-trip = %q, want %q", got, mode)
			}
		})
	}
}

// TestDownloaderConfig_NoLPDKey is T-1.1's negative assertion. The vendored
// torrent library has no local-peer-discovery implementation at all, so there
// is nothing to wire a toggle to — shipping one would be a stored-and-ignored
// knob. Asserted against the raw wire JSON rather than the Go struct, since the
// wire shape is what the frontend and any external client actually see.
func TestDownloaderConfig_NoLPDKey(t *testing.T) {
	srv, _ := newDownloaderConfigServer(t)

	resp, err := http.Get(srv.URL + "/api/downloader/config")
	if err != nil {
		t.Fatalf("GET config: %v", err)
	}
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decoding raw config: %v", err)
	}

	lpd := regexp.MustCompile(`lpd|localpeer|local_peer|lsd|mdns`)
	for key := range raw {
		if lpd.MatchString(strings.ToLower(key)) {
			t.Errorf("config exposes a local-peer-discovery key %q; the torrent library has no LPD support to wire it to", key)
		}
	}
}

// TestDownloaderConfig_OmittedMaxConcurrentIs400 pins the frontend contract
// that §1.1 warns about, as a test rather than as a comment.
//
// MaxConcurrent renders no UI control (it's reserved/unimplemented — all
// torrents download concurrently today) but it is a plain int with no
// omitempty, so a client that simply leaves it out of the PUT body sends 0 and
// trips the >= 1 guard. Every save then fails with a 400 that appears to come
// from whichever field the operator actually touched. The frontend MUST read
// MaxConcurrent from GET and echo it back verbatim on every PUT.
func TestDownloaderConfig_OmittedMaxConcurrentIs400(t *testing.T) {
	srv, _ := newDownloaderConfigServer(t)

	full, err := json.Marshal(validDownloaderConfig())
	if err != nil {
		t.Fatalf("marshaling config: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(full, &fields); err != nil {
		t.Fatalf("unmarshaling to map: %v", err)
	}
	delete(fields, "maxConcurrent")
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("re-marshaling config: %v", err)
	}

	if code := putDownloaderConfigRaw(t, srv, body); code != http.StatusBadRequest {
		t.Errorf("PUT omitting maxConcurrent: expected 400, got %d — the frontend round-trip requirement is unenforced", code)
	}
}

// TestDownloaderConfig_RejectsInvalidValues is T-1.2: every out-of-range value
// is a 400, and a rejected request persists NOTHING (the handler validates the
// whole document before its first Set — a partial write would leave the engine
// reading a config the operator never successfully saved).
func TestDownloaderConfig_RejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*apidto.DownloaderConfig)
	}{
		{"privileged listen port", func(c *apidto.DownloaderConfig) { c.ListenPort = 80 }},
		{"listen port above range", func(c *apidto.DownloaderConfig) { c.ListenPort = 70000 }},
		{"zero max connections", func(c *apidto.DownloaderConfig) { c.MaxConnections = 0 }},
		{"zero max concurrent", func(c *apidto.DownloaderConfig) { c.MaxConcurrent = 0 }},
		{"negative download rate", func(c *apidto.DownloaderConfig) { c.DownloadRateLimitBytes = -1 }},
		{"unknown obfuscation mode", func(c *apidto.DownloaderConfig) { c.ObfuscationMode = "banana" }},
		{"empty obfuscation mode", func(c *apidto.DownloaderConfig) { c.ObfuscationMode = "" }},
		{"negative seed ratio", func(c *apidto.DownloaderConfig) { c.SeedRatioLimit = -0.5 }},
		{"negative seed duration", func(c *apidto.DownloaderConfig) { c.SeedDurationMinutes = -1 }},
		{"negative stale threshold", func(c *apidto.DownloaderConfig) { c.StaleThresholdMinutes = -1 }},

		// The overflow half. 200,000,000 minutes is past the int64-nanosecond
		// wrap point (153,722,867), so without an upper bound
		// `time.Duration(x) * time.Minute` in internal/downloader goes NEGATIVE
		// and the setting inverts rather than saturating — a stale threshold
		// meaning "effectively never" becomes "cancel and delete the partial
		// files of every active torrent on the next poll tick". 1e18 does the
		// same to the ratio via `int64(float64(totalBytes) * ratio)`, an
		// out-of-range float->int64 conversion that yields min-int64 on amd64.
		{"overflowing stale threshold", func(c *apidto.DownloaderConfig) { c.StaleThresholdMinutes = 200_000_000 }},
		{"overflowing seed duration", func(c *apidto.DownloaderConfig) { c.SeedDurationMinutes = 200_000_000 }},
		{"overflowing seed ratio", func(c *apidto.DownloaderConfig) { c.SeedRatioLimit = 1e18 }},

		// Staging dir. The engine creates, writes, and DELETES
		// torrent-declared filenames under this path, so an unvalidated value
		// hands that authority to whatever a .torrent file declares.
		{"relative staging dir", func(c *apidto.DownloaderConfig) { c.StagingDir = "downloads" }},
		{"traversal staging dir", func(c *apidto.DownloaderConfig) { c.StagingDir = "../../etc" }},
		{"root staging dir", func(c *apidto.DownloaderConfig) { c.StagingDir = "/" }},
		{"system staging dir", func(c *apidto.DownloaderConfig) { c.StagingDir = "/etc" }},
		// Cleaned before matching, so an unnormalized spelling of a denied
		// directory is denied too.
		{"unnormalized system staging dir", func(c *apidto.DownloaderConfig) { c.StagingDir = "/usr/local/.." }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, store := newDownloaderConfigServer(t)
			cfg := validDownloaderConfig()
			tc.mutate(&cfg)

			if code := putDownloaderConfig(t, srv, cfg); code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", code)
			}
			// Nothing was persisted: the staging dir is the handler's first
			// Set, so if validation ran too late it would already be stored.
			if v, err := store.Get(context.Background(), DownloaderStagingDirKey); err == nil && v != "" {
				t.Errorf("rejected request persisted stagingDir = %q; validation must complete before any Set", v)
			}
		})
	}
}

// TestDownloaderConfig_AcceptsLargeButReasonableValues is the other half of the
// overflow guard, and the reason the ceilings are set two orders of magnitude
// below the wrap point rather than right at it: an operator who genuinely wants
// a months-long stale threshold or a high seed ratio must not be told no. A
// bound tight enough to reject these would be a usability regression dressed up
// as a security fix.
func TestDownloaderConfig_AcceptsLargeButReasonableValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*apidto.DownloaderConfig)
	}{
		{"three-month stale threshold", func(c *apidto.DownloaderConfig) { c.StaleThresholdMinutes = 129600 }},
		{"six-month seed duration", func(c *apidto.DownloaderConfig) { c.SeedDurationMinutes = 259200 }},
		{"high seed ratio", func(c *apidto.DownloaderConfig) { c.SeedRatioLimit = 500 }},
		{"every ceiling exactly", func(c *apidto.DownloaderConfig) {
			c.StaleThresholdMinutes = torrentMinutesMax
			c.SeedDurationMinutes = torrentMinutesMax
			c.SeedRatioLimit = torrentSeedRatioMax
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newDownloaderConfigServer(t)
			cfg := validDownloaderConfig()
			tc.mutate(&cfg)

			if code := putDownloaderConfig(t, srv, cfg); code != http.StatusOK {
				t.Fatalf("expected 200, got %d — the overflow ceiling is rejecting a legitimate value", code)
			}
			if got := getDownloaderConfig(t, srv); got != cfg {
				t.Errorf("large values did not round-trip:\n got %+v\nwant %+v", got, cfg)
			}
		})
	}
}

// TestDownloaderConfig_StagingDirAcceptsCreatablePath pins the accept side of
// the staging-dir guard, including the part the denylist cannot express: a path
// that does not exist yet is CREATED at save time rather than failing silently
// later, deep in the engine's own log (downloader.go MkdirAlls and log.Printfs
// without returning the error, which is why this check has to live here).
//
// "Creatable", not "writable" — os.MkdirAll returns nil for a directory that
// already exists without probing it. Don't tighten the assertion past what the
// code actually promises.
func TestDownloaderConfig_StagingDirAcceptsCreatablePath(t *testing.T) {
	srv, store := newDownloaderConfigServer(t)
	cfg := validDownloaderConfig()
	cfg.StagingDir = filepath.Join(t.TempDir(), "staging", "nested")

	if code := putDownloaderConfig(t, srv, cfg); code != http.StatusOK {
		t.Fatalf("creatable staging dir: expected 200, got %d", code)
	}
	if fi, err := os.Stat(cfg.StagingDir); err != nil {
		t.Errorf("staging dir was accepted but not created: %v", err)
	} else if !fi.IsDir() {
		t.Errorf("staging dir path exists but is not a directory")
	}
	if got, err := store.Get(context.Background(), DownloaderStagingDirKey); err != nil {
		t.Fatalf("reading staging dir: %v", err)
	} else if got != cfg.StagingDir {
		t.Errorf("stored staging dir = %q, want %q", got, cfg.StagingDir)
	}
}

// TestDownloaderConfig_BlankStagingDirSkipsValidation guards the one legal
// non-absolute value. "" means "leave it at the boot-computed default" — a
// value this handler cannot resolve (it has no dataDir) and the one path
// guaranteed to already exist, since boot created it. A validator that ran
// filepath.Clean first would see "." and 400 the clear-to-default flow.
func TestDownloaderConfig_BlankStagingDirSkipsValidation(t *testing.T) {
	srv, _ := newDownloaderConfigServer(t)
	cfg := validDownloaderConfig()
	cfg.StagingDir = ""

	if code := putDownloaderConfig(t, srv, cfg); code != http.StatusOK {
		t.Fatalf("blank staging dir: expected 200, got %d — clearing to the boot default was rejected", code)
	}
}
