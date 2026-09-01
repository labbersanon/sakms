// Package downloader manages SAK's unified download engine: an anacrolix/torrent
// in-process BitTorrent client plus a subscriber hub that fans out live
// download-queue snapshots for the Downloads screen's SSE stream.
//
// DELIBERATE, opt-in exception to this project's "manual by default, no
// background pollers" convention (CLAUDE.md): the Manager runs one background
// goroutine that polls torrent progress every pollInterval and, on a completed
// download, fires an onComplete callback that runs the auto-import. A download
// engine inherently needs to observe its progress; there's no human-triggered
// equivalent of "the download finished."
//
// Lifetime: a Manager owns a long-lived torrent client and goroutines, so it is
// a PROCESS-LIFETIME SINGLETON — constructed once in cmd/sakms/main.go and
// started with `go m.Start(ctx)` alongside the other background jobs, never
// per-request. The same pointer is injected wherever a grab needs to reach the
// download engine.
//
// Import discipline: this package imports only anacrolix/torrent + stdlib — NOT
// mode/grabs/library — so it never forms an import cycle with mode.Session
// (which references *Manager). The onComplete callback is a plain
// func(gid string, files []string) set at construction, closing over whatever
// stores the caller needs in main.go.
package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	torrentlib "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"golang.org/x/time/rate"
)

// pollInterval is how often the Manager's background loop re-reads torrent
// progress to detect changes and fan out to SSE subscribers.
const pollInterval = 500 * time.Millisecond

// Config parameterizes the Manager.
//
// Everything below StagingDir/MaxConc/MaxConn is an operator-tunable torrent
// engine knob. Two callers construct a Config and they MUST agree, key for key
// and default for default: buildDownloader (cmd/sakms/main.go) at boot, and the
// downloader-config PUT handler that calls Reconfigure while the process runs.
// A divergence means a setting silently changes behavior across a restart,
// which is worse than not applying it at all.
type Config struct {
	StagingDir string // torrent download directory (anacrolix DataDir)
	MaxConc    int    // max torrents actively downloading; extras wait in "waiting"
	MaxConn    int    // EstablishedConnsPerTorrent in the torrent client config

	// DownloadRateLimit caps aggregate download throughput in bytes/sec.
	// 0 means unlimited (mapped to rate.Inf, never rate.Limit(0), which would
	// issue no tokens at all and stall every download).
	DownloadRateLimit int

	// DHTEnabled / PEXEnabled default to TRUE — the library's own defaults.
	// Read them from settings with an unset-means-true idiom; a plain
	// zero-value read turns peer discovery off on every fresh install.
	DHTEnabled bool
	PEXEnabled bool

	// ListenPort is passed through verbatim. 0 means "let the OS assign an
	// ephemeral port" and is what tests need in order to run two clients in one
	// process. The 42069 default belongs to the config-construction layer, not
	// here, so an explicit 0 is never silently rewritten.
	ListenPort int

	// ObfuscationMode is "require" | "prefer" | "off". Empty means "prefer",
	// the library's un-overridden default. Note "off" is the STRICTEST mode:
	// it rejects encrypted peers outright rather than merely not preferring
	// encryption.
	ObfuscationMode string

	// SeedingEnabled is the master switch for post-completion seeding.
	// Defaults to false.
	SeedingEnabled bool

	// SeedRatioLimit / SeedDurationMinutes bound a seeding window; 0 disables
	// that particular limit. StaleThresholdMinutes is how long an active
	// download may make zero progress with zero peers before it is considered
	// dead; 0 disables stale detection entirely. All three are read per check
	// rather than at construction.
	SeedRatioLimit        float64
	SeedDurationMinutes   int
	StaleThresholdMinutes int

	// noPortForwarding disables the library's UPnP/NAT-PMP discovery. It is
	// unexported because it exists solely for same-package tests: a hermetic
	// test must never broadcast to the real LAN, and a two-client loopback
	// pair otherwise spawns two sets of port-mapping goroutines. Production
	// keeps the library's default (forwarding on).
	noPortForwarding bool
}

// ErrRebuildRefused is the sentinel behind every refusal Reconfigure returns
// when a rebuild-class settings change cannot be applied safely right now. The
// HTTP layer maps it to 409 and surfaces Error() to the operator, so the
// message must explain what to do about it.
var ErrRebuildRefused = errors.New("downloader: engine rebuild refused")

// RebuildRefusedError carries the specific reason a rebuild was refused.
// errors.Is(err, ErrRebuildRefused) matches it.
type RebuildRefusedError struct {
	Reason string
}

func (e *RebuildRefusedError) Error() string {
	return "downloader: engine rebuild refused: " + e.Reason
}

func (e *RebuildRefusedError) Unwrap() error { return ErrRebuildRefused }

// Download is one download's status. Fields are shaped to mirror the old
// aria2.Download surface so the api/apidto layers need no wire-format changes.
type Download struct {
	GID             string
	Status          string // "active" | "waiting" | "paused" | "error" | "complete" | "removed"
	Filename        string
	Dir             string
	TotalLength     int64
	CompletedLength int64
	DownloadSpeed   int64
	SeedCount       int64
	UploadSpeed     int64
	Files           []string
	ErrorMessage    string
}

type seenKey struct {
	status    string
	completed int64
}

type entry struct {
	t        *torrentlib.Torrent // nil in test-mode entries
	status   string
	errorMsg string
	// Claude 2026-09-01: metaReady distinguishes pre-metadata "waiting" from
	//   post-metadata queue waiting for a MaxConc slot. Rebuild refusal only
	//   blocks the former; admitWaiting promotes the latter FIFO by addedAt.
	// Reason: maxConcurrent was stored but never enforced.
	// Troubleshooting: too many torrents saturating the network at once.
	// Review if: a separate "queued" status is introduced in the wire DTO.
	metaReady bool
	addedAt   time.Time
	files    []string // full absolute paths, populated after GotInfo
	dir      string   // per-torrent folder or stagingDir
	filename string   // display: files[0] when known

	// Speed (delta across poll ticks).
	prevBytes int64
	prevTime  time.Time
	speed     int64

	// Claude 2026-08-04: upload-speed delta state, deliberately a SEPARATE
	// triple from prevBytes/prevTime/speed above.
	// Reason: the upload pass in pollSnapshot runs for complete-and-seeding
	// entries too (F-1 in the plan), which the download-speed pass above
	// does not — sharing prevTime would let the upload pass advance the
	// download pass's delta base on a tick where only upload ran, corrupting
	// download speed. Never cross-assign between the two triples.
	// Review if: the two passes are ever merged into one guard.
	prevUpBytes int64
	prevUpTime  time.Time
	upSpeed     int64

	// Cached for terminal states after the handle may be dropped.
	cachedCompleted int64
	cachedTotal     int64
	cachedSeeds     int64

	// Stale-detection clock. lastProgressAt is initialized in AddTorrent, reset
	// in Resume, and advanced by pollSnapshot whenever completed bytes grow —
	// only while the entry is active/waiting, so a paused download's clock
	// freezes rather than ticking toward the reaper. staleFired is the
	// fire-once guard. Maintained here; the predicate that reads them is
	// isStale.
	lastProgressAt time.Time
	staleFired     bool

	// Seeding and import-copy state, all in-memory (no table, no migration).
	// seedBaselineUp is Stats().BytesWrittenData at seed start, subtracted out
	// because tit-for-tat reciprocation during the download may already have
	// written bytes. seedTotalBytes is t.Length() — deliberately the ratio
	// DENOMINATOR rather than Stats().BytesReadData, which resets across a
	// restart and would make the ratio unbounded. seedPaths are the ORIGINAL
	// staging paths: those are what keeps seeding and what cleanup deletes,
	// because the torrent client's storage resolves by path and moving the
	// original would kill the seed silently.
	//
	// importRoot/importPaths describe the copy made under
	// <StagingDir>/.import/<gid>/ that the importer consumes instead. They are
	// written together and back the ImportRoot/ImportPaths accessors; a nil
	// importPaths means "there is no copy, use the entry's own files".
	seedStartedAt  time.Time
	seedBaselineUp int64
	seedTotalBytes int64
	seedPaths      []string
	importRoot     string
	importPaths    []string
}

// seedStop is one seeding torrent that has tripped a stop condition, collected
// under m.mu inside pollSnapshot and acted on by pollLoop only AFTER the lock
// is released. Dropping a torrent takes the client's own lock and deleting its
// files is synchronous I/O against staging (plausibly a network mount); doing
// either under m.mu would stall every downloads HTTP handler and the SSE
// fan-out for the duration.
type seedStop struct {
	gid       string
	t         *torrentlib.Torrent
	seedPaths []string
	reason    string
}

// Manager owns the anacrolix torrent client, in-memory download state, and the
// SSE subscriber hub. It is a process-lifetime singleton — see package doc.
type Manager struct {
	cfg        Config
	tc         *torrentlib.Client // nil before Start or in test mode
	httpClient *http.Client

	// rateLimiter is the live limiter instance handed to the current client.
	// Reconfigure mutates this pointer's rate in place and never swaps it —
	// every read site inside the library caches the dereference, so replacing
	// the pointer would leave the running client on the old limiter. Guarded by
	// mu, replaced only alongside tc.
	rateLimiter *rate.Limiter

	onComplete func(gid string, files []string)
	onStale    func(gid string)

	mu          sync.Mutex
	entries     map[string]*entry
	subscribers map[int]chan []Download
	nextSubID   int

	// testMode is set by NewForTesting; gates AddTorrent's fake-GID path.
	testMode    bool
	testNextGID string
	// testAutoIncrementGID makes AddTorrent return a unique GID per call
	// (testNextGID + an incrementing counter) instead of a fixed one — models a
	// download client assigning each distinct add its own handle, for multi-item
	// batch tests where every item is a genuinely distinct download.
	testAutoIncrementGID bool
	testGIDSeq           int
}

// New constructs a Manager. The engine is not started until Start is called.
func New(cfg Config, httpClient *http.Client) *Manager {
	return &Manager{
		cfg:         cfg,
		httpClient:  httpClient,
		entries:     map[string]*entry{},
		subscribers: map[int]chan []Download{},
	}
}

// NewForTesting builds a Manager with no real torrent client, for use in
// handler tests. State is pre-seeded via SeedState. AddTorrent returns the GID
// configured via SetTestNextGID. Start must NOT be called on a test Manager.
func NewForTesting(stagingDir string) *Manager {
	return &Manager{
		cfg:         Config{StagingDir: stagingDir},
		entries:     map[string]*entry{},
		subscribers: map[int]chan []Download{},
		testMode:    true,
	}
}

// SetTestNextGID configures what GID AddTorrent returns in test mode.
func (m *Manager) SetTestNextGID(gid string) { m.testNextGID = gid }

// EnableTestAutoGID makes AddTorrent return a unique GID per call in test mode
// (the configured prefix plus an incrementing counter) rather than a fixed one.
// Used by multi-item batch tests where every dispatched item is a distinct
// download that must each get its own tracking row.
func (m *Manager) EnableTestAutoGID() { m.testAutoIncrementGID = true }

// SeedState injects a pre-existing download entry for tests — immediately
// visible to List, FindByGID, and Subscribe.
func (m *Manager) SeedState(d Download) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[d.GID] = &entry{
		status:          d.Status,
		errorMsg:        d.ErrorMessage,
		files:           d.Files,
		dir:             d.Dir,
		filename:        d.Filename,
		cachedCompleted: d.CompletedLength,
		cachedTotal:     d.TotalLength,
		cachedSeeds:     d.SeedCount,
		speed:           d.DownloadSpeed,
		upSpeed:         d.UploadSpeed,
		// Start the stale clock, same as AddTorrent. A zero time.Time would
		// read as decades of no progress and make every seeded test entry look
		// instantly stale.
		lastProgressAt: time.Now(),
	}
}

// SetOnComplete wires the completion callback. Safe to call before Start.
func (m *Manager) SetOnComplete(fn func(gid string, files []string)) {
	m.onComplete = fn
}

// SetOnStale wires the stale-torrent callback, fired once per gid when a
// download trips isStale. Same seam and same reason as SetOnComplete: this
// package may not import grabs/mode/api, so the cancel-and-requeue policy lives
// in internal/api and reaches the Manager through a plain func.
//
// Call it BEFORE Start, exactly as SetOnComplete is called: pollLoop reads the
// field without holding m.mu.
func (m *Manager) SetOnStale(fn func(gid string)) {
	m.onStale = fn
}

// StagingDir returns the directory where torrent files are written.
func (m *Manager) StagingDir() string { return m.stagingRoot() }

// stagingRoot reads m.cfg.StagingDir under m.mu.
//
// m.cfg USED to be write-once-at-construction, which is why so much of this
// file reads it bare. Reconfigure now assigns the WHOLE struct (m.cfg = next)
// under m.mu while a config PUT is in flight, so every read on a goroutine that
// does not already hold the lock is a data race. This accessor is that lock for
// the lock-free readers.
//
// CALLERS MUST NOT HOLD m.mu. sync.Mutex is not reentrant, so routing an
// already-locked read (isStale, seedStopReason, buildEntry, watchTorrent's
// completion arm, AddTorrent's entry insert) through here would self-deadlock —
// those sites deliberately keep reading m.cfg directly and are safe precisely
// because their caller holds the lock.
func (m *Manager) stagingRoot() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.StagingDir
}

// obfuscationPolicy maps this package's mode string onto the library's policy
// struct. "off" is the strictest of the three: RequirePreferred means "the
// value of Preferred is a strict requirement", so {false, true} reads as
// "unobfuscated is REQUIRED" and drops every encrypted inbound peer.
func obfuscationPolicy(mode string) torrentlib.HeaderObfuscationPolicy {
	switch mode {
	case "require":
		return torrentlib.HeaderObfuscationPolicy{Preferred: true, RequirePreferred: true}
	case "off":
		return torrentlib.HeaderObfuscationPolicy{Preferred: false, RequirePreferred: true}
	default: // "prefer" and "" — the library's un-overridden default
		return torrentlib.HeaderObfuscationPolicy{Preferred: true, RequirePreferred: false}
	}
}

// downloadRateBurst mirrors the library's own default-burst formula
// (max(min(limit, MaxInt), 1<<20), rate.go). A finite limit paired with a zero
// burst issues no tokens at all, so every download stalls permanently — the
// library's helper that would prevent this is unexported, hence the copy.
func downloadRateBurst(bytesPerSec int) int {
	const minBurst = 1 << 20
	if bytesPerSec > minBurst && bytesPerSec < math.MaxInt {
		return bytesPerSec
	}
	return minBurst
}

// unlimitedBurst is the burst an unlimited (rate.Inf) download limiter carries.
//
// IT MUST NOT BE 0, and this is not a style preference — it is the difference
// between an engine that connects to peers and one that never does. Passing a
// zero-burst limiter to ClientConfig.DownloadRateLimiter makes the library
// fill in a default via setDefaultDownloadRateLimiterBurstIfZero (rate.go:27),
// which computes int(max(min(EffectiveDownloadRateLimit(l), math.MaxInt),
// 1<<20)). For an Inf limit that inner value is math.MaxFloat64, and the int()
// conversion OVERFLOWS to a NEGATIVE burst. A negative burst pins Tokens()
// negative forever, and Torrent.newConnsAllowed (torrent.go:2549) refuses
// every outgoing AND incoming connection while
// DownloadRateLimiter.Tokens() <= 0 — so an install with no rate limit
// configured (the default) would silently never connect to a single peer.
//
// An explicit, POSITIVE burst skips that defaulting entirely, and the exact
// value is otherwise inert: rate.Limiter short-circuits an Inf limit before
// burst is consulted on every Allow/Wait/Reserve path, and the library's own
// read-truncating site (ratelimitreader.go:29) returns early on Inf before
// reading Burst(). It matches downloadRateBurst's floor for consistency.
const unlimitedBurst = 1 << 20

// newDownloadRateLimiter always returns a REAL limiter, never nil.
// ClientConfig.DownloadRateLimiter has no default and the library tolerates the
// nil as "unlimited" for its own reads — but rate.Limiter.SetLimit on a nil
// receiver panics, so the live-apply tier would blow up inside the settings
// handler on any install that never set a limit.
func newDownloadRateLimiter(bytesPerSec int) *rate.Limiter {
	if bytesPerSec <= 0 {
		return rate.NewLimiter(rate.Inf, unlimitedBurst)
	}
	return rate.NewLimiter(rate.Limit(bytesPerSec), downloadRateBurst(bytesPerSec))
}

// applyRateLimit re-rates an existing limiter in place. Burst is set first so
// the limiter is never momentarily {finite limit, zero burst} — that state
// issues no tokens and would stall in-flight downloads.
func applyRateLimit(l *rate.Limiter, bytesPerSec int) {
	if l == nil {
		return
	}
	if bytesPerSec <= 0 {
		// unlimitedBurst, not 0: see its doc comment. Saving "unlimited" must
		// not leave the running client unable to open connections.
		l.SetBurst(unlimitedBurst)
		l.SetLimit(rate.Inf)
		return
	}
	l.SetBurst(downloadRateBurst(bytesPerSec))
	l.SetLimit(rate.Limit(bytesPerSec))
}

// buildClient constructs a torrent client from the Manager's current config.
// It returns the client's rate limiter alongside it rather than stashing it on
// the Manager, so the caller installs both under one critical section.
//
// Called by Start and by Reconfigure's rebuild path; it holds no lock across
// NewClient, which is slow.
func (m *Manager) buildClient() (*torrentlib.Client, *rate.Limiter, error) {
	m.mu.Lock()
	mc := m.cfg
	m.mu.Unlock()

	cfg := torrentlib.NewDefaultClientConfig()
	cfg.DataDir = mc.StagingDir
	// Seed/NoUpload are client-wide. Per-torrent upload allowance is layered on
	// top at add time so an in-progress download never reciprocates uploads
	// before its seed window opens.
	cfg.Seed = mc.SeedingEnabled
	cfg.NoUpload = !mc.SeedingEnabled
	if mc.MaxConn > 0 {
		cfg.EstablishedConnsPerTorrent = mc.MaxConn
	}
	cfg.NoDHT = !mc.DHTEnabled
	cfg.DisablePEX = !mc.PEXEnabled
	// Assigned unconditionally: an explicit 0 must reach the library as "bind an
	// ephemeral port", not be rewritten to the 42069 default.
	cfg.ListenPort = mc.ListenPort
	cfg.HeaderObfuscationPolicy = obfuscationPolicy(mc.ObfuscationMode)
	cfg.DownloadRateLimiter = newDownloadRateLimiter(mc.DownloadRateLimit)
	cfg.NoDefaultPortForwarding = mc.noPortForwarding

	tc, err := torrentlib.NewClient(cfg)
	if err != nil {
		return nil, nil, err
	}
	return tc, cfg.DownloadRateLimiter, nil
}

// Start creates the anacrolix torrent client, starts the poll loop, and blocks
// until ctx is cancelled. Intended to run as `go m.Start(ctx)`.
func (m *Manager) Start(ctx context.Context) error {
	if staging := m.stagingRoot(); staging != "" {
		if err := os.MkdirAll(staging, 0o755); err != nil {
			log.Printf("downloader: creating staging dir %s: %v", staging, err)
		}
	}
	m.sweepImportDir()

	tc, limiter, err := m.buildClient()
	if err != nil {
		return fmt.Errorf("downloader: creating torrent client: %w", err)
	}

	m.mu.Lock()
	m.tc = tc
	m.rateLimiter = limiter
	m.mu.Unlock()

	go m.pollLoop(ctx)

	<-ctx.Done()

	// Read the CURRENT client through the mutex rather than closing the one
	// captured above: Reconfigure may have swapped in a new instance since, and
	// closing the captured local would double-close the old client while
	// leaking the live one's sockets, DHT servers and per-torrent goroutines.
	// Nil m.tc before closing, same ordering as before.
	m.mu.Lock()
	current := m.tc
	m.tc = nil
	m.rateLimiter = nil
	m.mu.Unlock()
	if current != nil {
		current.Close()
	}
	return ctx.Err()
}

// rebuildRequired reports whether moving from old to next needs a whole new
// torrent client. Everything listed here is either consumed once during
// NewClient or read from an unsynchronized ClientConfig field on a hot path,
// so mutating it in place is a data race, not a supported API.
func rebuildRequired(old, next Config) bool {
	switch {
	case old.StagingDir != next.StagingDir:
		return true
	case old.ListenPort != next.ListenPort:
		return true
	case old.DHTEnabled != next.DHTEnabled:
		return true
	case old.PEXEnabled != next.PEXEnabled:
		return true
	case obfuscationPolicy(old.ObfuscationMode) != obfuscationPolicy(next.ObfuscationMode):
		return true
	}
	// Turning seeding ON needs a rebuild too, and this is a deliberate
	// deviation from the plan's table (which classes the seeding switch as
	// live/per-torrent). PeerConn.uploadAllowed checks config.NoUpload BEFORE
	// the per-torrent flag (peerconn.go:1159) and Torrent.seeding checks
	// config.Seed (torrent.go:2075); both are plain unsynchronized bools fixed
	// at NewClient. So a live AllowDataUpload against a client built with
	// NoUpload=true can never actually upload — a stored-and-ignored knob.
	// Turning seeding OFF genuinely IS live-safe (per-torrent
	// DisallowDataUpload short-circuits ahead of the reciprocation
	// fallthrough), so only the OFF->ON direction rebuilds.
	return next.SeedingEnabled && !old.SeedingEnabled
}

// Reconfigure applies a new Config to the running engine in two tiers.
//
// Live tier (always, no disruption): the download rate limit is re-rated on the
// existing limiter pointer, every live torrent's max established connections is
// updated, and turning seeding off disallows upload on every live torrent
// immediately.
//
// Rebuild tier (only when rebuildRequired says so): every live torrent's full
// metainfo is snapshotted, the old client is closed, a new one is built, and
// each torrent is re-added. The GID is the info-hash hex string and is
// therefore STABLE across a rebuild, so grab rows keyed on download_gid survive
// intact — that property is what makes the rebuild viable at all.
//
// Two refusals, both returning a *RebuildRefusedError (errors.Is
// ErrRebuildRefused; the HTTP layer maps it to 409) and both leaving the stored
// config untouched:
//   - StagingDir changed while any download is in flight OR any torrent is
//     still seeding — moving DataDir out from under open file handles corrupts
//     them, and a seeding torrent's files would be orphaned under the old root.
//   - any entry is still in the pre-metadata "waiting" state — there is no
//     metainfo to snapshot yet, and accepting the rebuild would silently strand
//     an in-flight download. The window is seconds-to-minutes, so retrying
//     costs the operator almost nothing.
func (m *Manager) Reconfigure(ctx context.Context, next Config) error {
	m.mu.Lock()
	old := m.cfg
	rebuild := rebuildRequired(old, next)
	if rebuild {
		if refusal := m.rebuildRefusalLocked(old, next); refusal != nil {
			m.mu.Unlock()
			return refusal
		}
	}
	m.cfg = next
	tc := m.tc
	limiter := m.rateLimiter
	var torrents []*torrentlib.Torrent
	if tc != nil && !rebuild {
		torrents = tc.Torrents()
	}
	m.mu.Unlock()

	if tc == nil {
		// Not started, or a test Manager with no real client. The config is
		// stored; the next Start picks it up.
		return nil
	}

	if rebuild {
		return m.rebuildClient(old)
	}

	applyRateLimit(limiter, next.DownloadRateLimit)
	if next.MaxConn > 0 {
		for _, t := range torrents {
			t.SetMaxEstablishedConns(next.MaxConn)
		}
	}
	if old.SeedingEnabled && !next.SeedingEnabled {
		for _, t := range torrents {
			t.DisallowDataUpload()
		}
	}
	// MaxConc is live: admit/demote without rebuilding the client.
	m.mu.Lock()
	toAllow, toDeny := m.syncConcurrencyLocked()
	m.mu.Unlock()
	for _, t := range toAllow {
		t.AllowDataDownload()
	}
	for _, t := range toDeny {
		t.DisallowDataDownload()
	}
	return nil
}

// rebuildRefusalLocked returns the refusal that blocks a rebuild right now, or
// nil. Caller must hold m.mu, and must not have mutated m.cfg yet — a refusal
// leaves the stored config exactly as it was.
func (m *Manager) rebuildRefusalLocked(old, next Config) error {
	if next.StagingDir != old.StagingDir {
		for gid, e := range m.entries {
			if e.status == "active" || e.status == "waiting" {
				return &RebuildRefusedError{Reason: fmt.Sprintf(
					"download %s is still in flight — the staging directory cannot be moved out from under an open file handle; pause or cancel it first", gid)}
			}
			// A SEEDING entry's status is "complete" (watchTorrent sets it the
			// moment Complete() fires, which is the event seeding hooks into),
			// so the status clause above cannot see it — seeding is tracked by
			// seedStartedAt, not by status. Without this clause the rebuild
			// proceeds, the re-added torrent points at the NEW staging root
			// while seedPaths still point under the OLD one, and when seeding
			// later stops deleteDownloadFiles correctly refuses those now-
			// out-of-staging paths — orphaning the seed files permanently with
			// no automatic recovery.
			if !e.seedStartedAt.IsZero() {
				return &RebuildRefusedError{Reason: fmt.Sprintf(
					"download %s is still seeding — the staging directory cannot be moved out from under the files it is serving; stop seeding or cancel it first", gid)}
			}
		}
	}
	// Deliberately scoped to the StagingDir-changed branch above: a seeding
	// entry does NOT block a port/DHT/PEX/obfuscation rebuild, which re-adds
	// the torrent under the same staging root and keeps seeding intact (see
	// readdTorrents' seedBaselineUp reset). Widening this to every rebuild
	// class would make that path unreachable.
	for gid, e := range m.entries {
		// Post-metadata "waiting" means queued for a MaxConc slot and HAS
		// metainfo to snapshot — only block pre-metadata waiting.
		if e.status == "waiting" && !e.metaReady {
			return &RebuildRefusedError{Reason: fmt.Sprintf(
				"torrent %s is still fetching its metadata — try again in a moment", gid)}
		}
	}
	return nil
}

// rebuildTorrent is one live torrent captured before the old client is closed.
// The full metainfo is snapshotted, not just the info hash: re-adding by hash
// alone drops back to a metadata-less magnet, discarding the tracker list and
// forcing a fresh metadata fetch for a torrent whose info this process already
// holds.
type rebuildTorrent struct {
	gid       string
	mi        metainfo.MetaInfo
	status    string
	metaReady bool
	addedAt   time.Time
}

// snapshotMetainfo captures a live torrent's metainfo in a form that can
// actually be re-added.
//
// Torrent.Metainfo() populates PieceLayers with an EMPTY, NON-NIL map for a
// plain BitTorrent v1 torrent (pieceLayers() allocates the map up front and
// then skips every file that has no v2 pieces root). AddTorrent treats a
// non-nil PieceLayers as "this is a v2 torrent" and fails with
// "no piece root set for file X" for every file spanning more than one piece —
// which is nearly every real torrent. Round-tripping Metainfo() straight back
// into AddTorrent therefore does not work; normalizing the empty map to nil,
// exactly as metainfo.Load leaves it when parsing a v1 .torrent file, does.
// Genuine v2 piece layers are non-empty and pass through untouched.
func snapshotMetainfo(t *torrentlib.Torrent) metainfo.MetaInfo {
	mi := t.Metainfo()
	if len(mi.PieceLayers) == 0 {
		mi.PieceLayers = nil
	}
	return mi
}

// rebuildClient tears down the current client and stands up a replacement from
// m.cfg, re-adding every snapshotted torrent. On a construction failure it
// rolls the config back to old and rebuilds from that, so a bad listen port
// degrades to "the change didn't take" rather than "the engine is down until
// the process restarts".
func (m *Manager) rebuildClient(old Config) error {
	m.mu.Lock()
	tc := m.tc
	var snaps []rebuildTorrent
	for gid, e := range m.entries {
		if e.t == nil {
			continue
		}
		switch e.status {
		case "active", "paused", "complete":
		case "waiting":
			if !e.metaReady {
				continue // no metainfo yet — rebuildRefusal should have blocked
			}
		default:
			// "removed"/"error" entries have nothing live to carry over.
			continue
		}
		snaps = append(snaps, rebuildTorrent{
			gid: gid, mi: snapshotMetainfo(e.t), status: e.status,
			metaReady: e.metaReady, addedAt: e.addedAt,
		})
	}
	m.tc = nil
	m.rateLimiter = nil
	m.mu.Unlock()

	if tc == nil {
		// Reconfigure saw a live client, but the shutdown path won the race for
		// it in the window between. Abandon the rebuild rather than standing up
		// a fresh client nobody is left to close; the stored config is already
		// updated, so the next Start picks it up.
		return nil
	}

	// Close before building: a rebuild that keeps the listen port would
	// otherwise fail to bind against its own predecessor.
	tc.Close()

	var rollbackErr error
	newTC, limiter, err := m.buildClient()
	if err != nil {
		rollbackErr = fmt.Errorf("downloader: rebuilding torrent client: %w", err)
		m.mu.Lock()
		m.cfg = old
		m.mu.Unlock()
		newTC, limiter, err = m.buildClient()
		if err != nil {
			log.Printf("downloader: %v; rollback to the previous config also failed (%v) — the engine is now stopped until restart", rollbackErr, err)
			return rollbackErr
		}
		log.Printf("downloader: %v; rolled back to the previous engine config", rollbackErr)
	}

	m.mu.Lock()
	m.tc = newTC
	m.rateLimiter = limiter
	m.mu.Unlock()

	m.readdTorrents(newTC, snaps)
	return rollbackErr
}

// readdTorrents re-adds each snapshotted torrent to newTC, restores paused
// state, and relaunches a watcher for the entries that still have a lifecycle
// left to watch.
func (m *Manager) readdTorrents(newTC *torrentlib.Client, snaps []rebuildTorrent) {
	for _, s := range snaps {
		mi := s.mi
		t, err := newTC.AddTorrent(&mi)
		if err != nil {
			log.Printf("downloader: re-adding %s after engine rebuild: %v", s.gid, err)
			m.mu.Lock()
			if e, ok := m.entries[s.gid]; ok {
				e.t = nil
				e.status = "error"
				e.errorMsg = fmt.Sprintf("re-adding after engine rebuild: %v", err)
			}
			m.mu.Unlock()
			continue
		}

		m.mu.Lock()
		e, ok := m.entries[s.gid]
		if ok {
			e.t = t
			// Restore the snapshotted status too, not just the handle. The old
			// client was closed BEFORE this re-add, so an old watcher may
			// already have woken on Closed() and marked the entry "removed"
			// while it still legitimately owned the handle — the identity guard
			// only protects against the other interleaving. Re-asserting the
			// pre-rebuild status covers both.
			e.status = s.status
			e.metaReady = s.metaReady
			if !s.addedAt.IsZero() {
				e.addedAt = s.addedAt
			}

			// Re-baseline a seeding entry against the NEW handle. seedBaselineUp
			// was measured off the OLD handle's BytesWrittenData counter, and
			// the fresh handle's counter starts at 0 — so carrying the old value
			// over makes seedStopReason's `uploaded := BytesWrittenData -
			// seedBaselineUp` go negative and the ratio limit unreachable
			// forever. Any rebuild-class change (listen port, DHT, PEX,
			// obfuscation) hits this; the staging-dir change is refused
			// separately by rebuildRefusalLocked.
			if !e.seedStartedAt.IsZero() {
				e.seedBaselineUp = 0
				// Claude 2026-08-04: reset the upload-speed delta base
				// alongside seedBaselineUp, same handle-swap hazard (F-4).
				// Reason: prevUpBytes is a snapshot of the OLD handle's
				// BytesWrittenData; the new handle's counter restarts at 0,
				// so an un-reset prevUpBytes makes the next delta negative
				// and (via the delta > 0 guard) publishes false 0 B/s until
				// the new counter climbs back past the old value — minutes
				// of wrong data for a long seed.
				// prevBytes (download) is deliberately NOT reset here:
				// BytesCompleted() is derived from verified on-disk pieces,
				// not a process counter, so it survives a handle swap intact
				// and resetting it would introduce a download-speed glitch
				// on every reconfigure. That asymmetry is intentional.
				// Review if: BytesCompleted() ever becomes a process counter
				// too.
				e.prevUpBytes = 0
				e.prevUpTime = time.Time{}
			}
		}
		m.mu.Unlock()
		if !ok {
			// Cancelled while the rebuild was in flight.
			t.Drop()
			continue
		}

		// Same per-torrent upload discipline AddTorrent applies: a re-added
		// entry that has not completed must not reciprocate uploads just
		// because the rebuilt client was constructed with NoUpload=false. A
		// "complete" entry keeps upload allowed — it either was seeding before
		// the rebuild or is about to be evaluated by the seed pass.
		if s.status != "complete" {
			t.DisallowDataUpload()
		}

		// Restore pause / queued-waiting before the watcher runs, so its
		// GotInfo arm observes the status and leaves data download disallowed.
		if s.status == "paused" || s.status == "waiting" {
			t.DisallowDataDownload()
		}
		// A "complete" entry's original watcher already returned when
		// Complete() fired. Relaunching one would re-fire Complete().On()
		// immediately and trigger a DUPLICATE import.
		if s.status == "active" || s.status == "paused" || s.status == "waiting" {
			go m.watchTorrent(t, s.gid)
		}
	}
}

// AddTorrent queues a download by magnet URI or .torrent file URL. Returns the
// assigned GID (the torrent's info-hash hex string).
func (m *Manager) AddTorrent(ctx context.Context, uri string) (string, error) {
	if m.testMode {
		m.mu.Lock()
		gid := m.testNextGID
		if gid == "" {
			gid = "test-gid"
		}
		if m.testAutoIncrementGID {
			m.testGIDSeq++
			gid = fmt.Sprintf("%s-%d", gid, m.testGIDSeq)
		}
		if _, exists := m.entries[gid]; !exists {
			m.entries[gid] = &entry{status: "active", lastProgressAt: time.Now()}
		}
		m.mu.Unlock()
		return gid, nil
	}

	m.mu.Lock()
	tc := m.tc
	maxConn := m.cfg.MaxConn
	m.mu.Unlock()
	if tc == nil {
		return "", fmt.Errorf("downloader: engine not running")
	}

	var t *torrentlib.Torrent
	if strings.HasPrefix(uri, "magnet:") {
		var err error
		t, err = tc.AddMagnet(uri)
		if err != nil {
			return "", fmt.Errorf("downloader: adding magnet: %w", err)
		}
	} else {
		// Claude 2026-08-11: resolve Prowlarr (and similar) HTTP→magnet redirects.
		// Reason: indexer download URLs often 301/302 to Location: magnet:…;
		// Go's default client follows that redirect and fails with
		// unsupported protocol scheme "magnet".
		// Troubleshooting: Adult Grab 502 whose UI error embeds &tr= tracker params.
		// Review if: fetch path gains an explicit magnet Content-Type body handler.
		mi, magnet, err := m.fetchMetainfoOrMagnet(ctx, uri)
		if err != nil {
			return "", err
		}
		if magnet != "" {
			t, err = tc.AddMagnet(magnet)
			if err != nil {
				return "", fmt.Errorf("downloader: adding magnet: %w", err)
			}
		} else {
			var addErr error
			t, addErr = tc.AddTorrent(mi)
			if addErr != nil {
				return "", fmt.Errorf("downloader: adding torrent: %w", addErr)
			}
		}
	}

	// Upload is disallowed on EVERY add, unconditionally, and only re-allowed
	// once the seed window opens (beginSeeding). Without this, a client built
	// with NoUpload=false reciprocates uploads DURING the download —
	// PeerConn.uploadAllowed falls through to BitTorrent tit-for-tat once
	// NoUpload is off, and the per-torrent flag is what short-circuits it.
	//
	// This post-add call is the ONLY working mechanism. AddTorrentOpts carries
	// a DisallowDataUpload field, but newTorrentOpt never reads it in
	// anacrolix/torrent v1.61.0 — wiring it compiles, reads plausibly, and
	// silently uploads forever. Do not "simplify" this into the opts field.
	//
	// KNOWN, ACCEPTED GAP: between the add returning and this call taking the
	// client lock, an already-connected peer could in principle be served
	// data. The window is two adjacent statements with no I/O between them,
	// and a torrent created microseconds ago has no dialled peers (and, for a
	// magnet, no pieces to serve at all). The opts field would not close this
	// window — it would replace it with a permanently open one.
	t.DisallowDataUpload()

	// Apply the current max-connections setting to this add. ClientConfig's
	// EstablishedConnsPerTorrent is read ONCE, at torrent-add time, off the
	// client's own config — which Reconfigure cannot rewrite (an unsynchronized
	// struct field on a running client is a data race, not a supported API). So
	// without this call, every torrent added after a live max-connections save
	// would silently keep the boot-time value until the process restarts.
	// SetMaxEstablishedConns is the supported, lock-guarded setter.
	if maxConn > 0 {
		t.SetMaxEstablishedConns(maxConn)
	}

	gid := t.InfoHash().HexString()
	m.mu.Lock()
	m.entries[gid] = &entry{
		t:      t,
		status: "waiting",
		dir:    m.cfg.StagingDir,
		addedAt: time.Now(),
		// Start the stale clock at add time: a torrent that never makes its
		// first byte of progress is exactly the case stale detection exists for.
		lastProgressAt: time.Now(),
	}
	m.mu.Unlock()

	go m.watchTorrent(t, gid)
	return gid, nil
}

// Pause pauses an active download.
func (m *Manager) Pause(gid string) error {
	m.mu.Lock()
	e, ok := m.entries[gid]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("download not found: %s", gid)
	}
	t := e.t
	wasActive := e.status == "active"
	e.status = "paused"
	var toAllow []*torrentlib.Torrent
	if wasActive {
		toAllow = m.admitWaitingLocked()
	}
	m.mu.Unlock()
	if t != nil {
		t.DisallowDataDownload()
	}
	for _, at := range toAllow {
		at.AllowDataDownload()
	}
	return nil
}

// Resume unpauses a paused download.
func (m *Manager) Resume(gid string) error {
	m.mu.Lock()
	e, ok := m.entries[gid]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("download not found: %s", gid)
	}
	t := e.t
	// Reset the stale clock: the entry made no progress while paused by
	// design, and that dead time must not count toward the stale threshold.
	e.lastProgressAt = time.Now()
	admitted := false
	if e.metaReady {
		admitted = m.tryAdmitLocked(e)
		if !admitted {
			e.status = "waiting"
		}
	} else {
		e.status = "waiting"
	}
	m.mu.Unlock()
	if t != nil {
		if admitted {
			t.AllowDataDownload()
		} else {
			t.DisallowDataDownload()
		}
	}
	return nil
}

// Cancel removes a download entirely from both the torrent client and the
// in-memory state map (it will no longer appear in List), AND deletes the
// downloaded/partial file(s) it wrote to disk.
//
// File-deletion safety: we delete each exact path this torrent declared
// (e.files, computed from t.Files() after GotInfo), never a whole directory.
// This is deliberately per-file rather than an os.RemoveAll of the download
// dir: a single-file torrent writes directly into the SHARED staging dir
// (removing that dir would take unrelated downloads with it), and a nested
// multi-folder torrent's computeDir only names the first file's parent (a
// RemoveAll there would both miss sibling folders and risk staging). Deleting
// the declared paths is complete regardless of nesting and touches nothing this
// torrent didn't own. Best-effort: a missing file (never started, already gone)
// is not an error. Empty parent dirs left behind by a multi-file torrent are
// pruned best-effort.
//
// The per-gid .import/<gid>/ copy (made for a seeding torrent) is reclaimed
// too — see the capture below.
func (m *Manager) Cancel(gid string) error {
	m.mu.Lock()
	e, ok := m.entries[gid]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("download not found: %s", gid)
	}
	t := e.t
	files := append([]string(nil), e.files...)
	// Captured BEFORE the delete: importRoot is only reachable through the
	// entry, so once the map key is gone nothing can locate this gid's
	// .import/<gid>/ tree again — ClearImportCopy becomes a no-op lookup miss.
	// Cancelling a completed, seeding torrent would otherwise leak a FULL-SIZE
	// copy of its content until the next process restart's sweep.
	importRoot := e.importRoot
	delete(m.entries, gid)
	toAllow := m.admitWaitingLocked()
	m.mu.Unlock()
	if t != nil {
		t.Drop()
	}
	for _, at := range toAllow {
		at.AllowDataDownload()
	}
	m.deleteDownloadFiles(files)
	m.removeImportRoot(importRoot)
	return nil
}

// deleteDownloadFiles removes each declared file path and best-effort prunes any
// now-empty parent directories up to (but never including) the staging dir. Only
// directories strictly under the staging dir are pruned, and only when empty, so
// a single file sitting directly in the shared staging dir never triggers a
// directory removal.
func (m *Manager) deleteDownloadFiles(files []string) {
	// stagingRoot, not a bare m.cfg read: this runs lock-free BY DESIGN (Cancel
	// releases m.mu first, stopSeeding is called by pollLoop with the lock
	// released) precisely because it does synchronous I/O against staging.
	staging := filepath.Clean(m.stagingRoot())
	for _, f := range files {
		if f == "" {
			continue
		}
		// SECURITY: e.files comes from anacrolix's File.Path(), which returns the
		// RAW, unsanitized bencoded torrent path field (anacrolix only sanitizes
		// at storage-WRITE time, not at this accessor). A malicious torrent can
		// declare a path with "../" components that, after filepath.Join+Clean,
		// escapes the staging dir. Refuse to os.Remove anything not STRICTLY under
		// staging (rel ".." or "../…") — the same containment check the prune loop
		// below uses — so a crafted torrent can never delete a file outside staging.
		cleanF := filepath.Clean(f)
		if rel, err := filepath.Rel(staging, cleanF); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			log.Printf("downloader: refusing to delete out-of-staging path %s", f)
			continue
		}
		if err := os.Remove(cleanF); err != nil && !os.IsNotExist(err) {
			log.Printf("downloader: deleting cancelled file %s: %v", cleanF, err)
		}
		// Prune now-empty per-torrent subfolders (a multi-file torrent's own
		// directory), never the shared staging dir itself. os.Remove only
		// succeeds on an empty dir, so a shared dir with other downloads is safe.
		dir := filepath.Dir(cleanF)
		for {
			clean := filepath.Clean(dir)
			if clean == staging || clean == "." || clean == string(filepath.Separator) {
				break
			}
			rel, err := filepath.Rel(staging, clean)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				break // not under staging — don't touch
			}
			if err := os.Remove(clean); err != nil {
				break // non-empty or gone — stop climbing
			}
			dir = filepath.Dir(clean)
		}
	}
}

// List returns a snapshot of all known downloads.
func (m *Manager) List() []Download { return m.readSnapshot() }

// FindByGID looks up one download by GID. Returns (nil, nil) when not found.
func (m *Manager) FindByGID(gid string) (*Download, error) {
	for _, d := range m.readSnapshot() {
		if d.GID == gid {
			return &d, nil
		}
	}
	return nil, nil
}

// Subscribe registers a new SSE subscriber. Returns a channel that receives
// each subsequent queue snapshot and a cancel func that unsubscribes. The
// channel is buffered by 1; a slow consumer gets the latest snapshot (stale
// ones are dropped).
func (m *Manager) Subscribe() (<-chan []Download, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextSubID
	m.nextSubID++
	ch := make(chan []Download, 1)
	m.subscribers[id] = ch
	cancel := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if c, ok := m.subscribers[id]; ok {
			delete(m.subscribers, id)
			close(c)
		}
	}
	return ch, cancel
}

// watchTorrent monitors one torrent's lifecycle. It waits for metadata, starts
// the download, then waits for completion or cancellation. t.Closed() fires on
// both Cancel() and client shutdown.
//
// IDENTITY DISCIPLINE: every mutation below is guarded by `e.t == t`, not by
// the gid alone. A client rebuild closes every old torrent at once, waking all
// their watchers, while the re-added torrents land at the SAME map keys (the
// gid is the info-hash hex string and is stable across a rebuild). Without the
// pointer check, a superseded watcher would mark a live, downloading entry
// "removed" — freezing it out of pollSnapshot's refresh for good — or fire a
// spurious duplicate import off a stale Complete(). NewClient+AddTorrent always
// allocates a fresh *Torrent, so pointer identity is sufficient here; apply it
// uniformly, do not mix it with a generation counter.
func (m *Manager) watchTorrent(t *torrentlib.Torrent, gid string) {
	select {
	case <-t.Closed():
		m.markRemoved(t, gid)
		return
	case <-t.GotInfo():
	}

	files := m.computeFiles(t)
	dir := m.computeDir(files)
	var filename string
	if len(files) > 0 {
		filename = files[0]
	}
	m.mu.Lock()
	admitted := false
	if e, ok := m.entries[gid]; ok && e.t == t {
		e.metaReady = true
		e.files = files
		e.dir = dir
		e.filename = filename
		e.cachedTotal = t.Length()
		// A rebuild re-adds a paused entry with data download already
		// disallowed; don't resurrect it as active behind the operator's back.
		if e.status != "paused" {
			admitted = m.tryAdmitLocked(e)
			if !admitted {
				e.status = "waiting"
			}
		}
	}
	m.mu.Unlock()

	t.DownloadAll()
	if !admitted {
		// Queued (or paused): register piece interest but don't pull data yet.
		t.DisallowDataDownload()
	}

	select {
	case <-t.Closed():
		m.markRemoved(t, gid)
	case <-t.Complete().On():
		completed := t.BytesCompleted()
		total := t.Length()
		m.mu.Lock()
		e, ok := m.entries[gid]
		if !ok || e.t != t {
			m.mu.Unlock()
			return
		}
		e.status = "complete"
		e.cachedCompleted = completed
		e.cachedTotal = total
		e.speed = 0
		e.upSpeed = 0
		seedingEnabled := m.cfg.SeedingEnabled
		toAllow := m.admitWaitingLocked()
		m.mu.Unlock()
		for _, at := range toAllow {
			at.AllowDataDownload()
		}

		// Copy-then-move INVERSION. The originals stay exactly where the
		// torrent client wrote them and are what keeps seeding — the storage
		// layer resolves by PATH, so relocating them (even leaving a hard
		// link behind) makes the client's registered paths stop resolving and
		// seeding dies silently. A fresh copy under .import/<gid>/ is what the
		// unchanged import flow consumes instead.
		//
		// Deliberately staged with m.mu RELEASED: this is a full-size file
		// copy against staging, and m.mu is the same mutex every downloads
		// handler and the SSE fan-out take.
		importPaths := files
		if seedingEnabled && len(files) > 0 {
			copied, importRoot, err := m.stageImportCopy(gid, files)
			if err != nil {
				// A copy failure NEVER blocks the import: fall through with
				// the originals — byte-for-byte today's behavior — and skip
				// seeding for this torrent. stageImportCopy has already
				// removed its own partial tree.
				log.Printf("downloader: staging import copy for %s: %v — importing the original files and not seeding", gid, err)
			} else {
				importPaths = copied
				m.setImportPaths(gid, importRoot, copied)
				m.beginSeeding(gid, t, files, total)
			}
		}

		filesCopy := append([]string(nil), importPaths...)
		if m.onComplete != nil {
			go m.onComplete(gid, filesCopy)
		}
	}
}

// markRemoved flips gid to "removed", but only if the entry still refers to the
// torrent this watcher was launched with.
func (m *Manager) markRemoved(t *torrentlib.Torrent, gid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[gid]; ok && e.t == t && e.status != "removed" {
		e.status = "removed"
	}
}

// computeFiles derives absolute file paths from a torrent after GotInfo.
// f.Path() uses '/' separators and is relative to DataDir (StagingDir).
//
// Called from watchTorrent with m.mu RELEASED, so the staging dir is read
// through the accessor.
func (m *Manager) computeFiles(t *torrentlib.Torrent) []string {
	staging := m.stagingRoot()
	tf := t.Files()
	out := make([]string, 0, len(tf))
	for _, f := range tf {
		out = append(out, filepath.Join(staging, filepath.FromSlash(f.Path())))
	}
	return out
}

// computeDir returns the appropriate download directory for a file list: the
// parent of files[0] when files are in a per-torrent subfolder, or stagingDir
// when a single file sits directly in staging. Same lock discipline as
// computeFiles: called from watchTorrent with m.mu released.
func (m *Manager) computeDir(files []string) string {
	if len(files) == 0 {
		return m.stagingRoot()
	}
	return filepath.Dir(files[0])
}

// torrentHTTPClient returns a per-request client that stops on magnet:
// redirects instead of following them (which would fail with
// unsupported protocol scheme "magnet"). HTTP(S)→HTTP(S) redirects still
// follow the usual 10-hop default. The shared Manager client is never mutated.
func (m *Manager) torrentHTTPClient() *http.Client {
	base := m.httpClient
	if base == nil {
		base = http.DefaultClient
	}
	return &http.Client{
		Transport: base.Transport,
		Timeout:    base.Timeout,
		Jar:        base.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL != nil && strings.EqualFold(req.URL.Scheme, "magnet") {
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}
}

// fetchMetainfoOrMagnet downloads a .torrent file from uri, or returns a magnet
// URI when the server redirects to Location: magnet:… (Prowlarr's common path
// for magnet-only indexer results). Exactly one of (mi, magnet) is set on success.
func (m *Manager) fetchMetainfoOrMagnet(ctx context.Context, uri string) (*metainfo.MetaInfo, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, "", fmt.Errorf("downloader: building torrent request: %w", err)
	}
	resp, err := m.torrentHTTPClient().Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("downloader: fetching torrent: %w", err)
	}
	defer resp.Body.Close()

	if loc := magnetLocation(resp); loc != "" {
		return nil, loc, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("downloader: torrent URL returned %d", resp.StatusCode)
	}
	mi, err := metainfo.Load(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("downloader: parsing torrent metainfo: %w", err)
	}
	return mi, "", nil
}

// magnetLocation returns a trimmed magnet: Location when resp is a redirect
// (or any response) whose Location header is a magnet URI.
func magnetLocation(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		// continue
	default:
		return ""
	}
	loc := strings.TrimSpace(resp.Header.Get("Location"))
	if strings.HasPrefix(strings.ToLower(loc), "magnet:") {
		return loc
	}
	return ""
}

func (m *Manager) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var prev []Download
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap, stopSeeds, staleGIDs := m.pollSnapshot()
			// Both passes were COLLECTED under m.mu and are acted on here,
			// with the lock released. stopSeeding drops a torrent and deletes
			// files; onStale's own body reaches back into the Manager. Either
			// under the lock would stall every downloads handler, and the
			// latter would deadlock outright the moment it stopped being async.
			for _, s := range stopSeeds {
				m.stopSeeding(s)
			}
			if m.onStale != nil {
				for _, gid := range staleGIDs {
					m.onStale(gid)
				}
			}
			if !sameSnapshot(prev, snap) {
				m.fanout(snap)
				prev = snap
			}
		}
	}
}

// buildEntry assembles a Download from an entry's cached/stable fields without
// touching prevBytes, prevTime, or speed. Caller must hold m.mu.
func (m *Manager) buildEntry(gid string, e *entry) Download {
	dir := e.dir
	if dir == "" {
		dir = m.cfg.StagingDir
	}
	return Download{
		GID:             gid,
		Status:          e.status,
		Filename:        e.filename,
		Dir:             dir,
		TotalLength:     e.cachedTotal,
		CompletedLength: e.cachedCompleted,
		DownloadSpeed:   e.speed,
		SeedCount:       e.cachedSeeds,
		UploadSpeed:     e.upSpeed,
		Files:           e.files,
		ErrorMessage:    e.errorMsg,
	}
}

// readSnapshot builds the current Download list from cached entry fields.
// It never mutates prevBytes, prevTime, or speed — safe to call from HTTP
// request handlers without corrupting the poll loop's speed calculation.
func (m *Manager) readSnapshot() []Download {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Download, 0, len(m.entries))
	for gid, e := range m.entries {
		out = append(out, m.buildEntry(gid, e))
	}
	return out
}

// isStale reports whether e has met the stale-torrent predicate: active (never
// paused, never pre-metadata), zero completed bytes, no connected peers or
// seeders, and no progress for longer than the configured threshold.
// StaleThresholdMinutes == 0 disables detection entirely.
//
// Caller holds m.mu. The fire-once guard is pollSnapshot's, not this
// function's, so this stays a pure predicate.
//
// Every clause here is an EXCLUSION that exists for a reason:
//
//   - status "active" excludes "paused". A paused entry's stats are never
//     refreshed, so its clock freezes rather than ticking toward the reaper —
//     without this, a 4h pause would delete the operator's partial files. It
//     also excludes "waiting", the pre-GotInfo state a magnet sits in before it
//     has any metadata: nothing about that is dead, it just hasn't started.
//   - Stats() is read live rather than from e.cachedSeeds, which caches only
//     ConnectedSeeders. A swarm of leechers with no seeders is still connected
//     peers and must not be reaped, so both counters are checked. Reading the
//     handle under m.mu is exactly what pollSnapshot already does one branch up.
//
// GLOBAL PAUSE needs no clause here, and deliberately has none. The Manager
// cannot read the settings store (package import discipline, see the package
// doc), but it does not need to: dispatchToDownloadClient (internal/api) is the
// ONLY production caller of AddTorrent and it refuses with 423 Locked while
// downloads_global_paused is set, so no entry is ever created during a global
// pause; entries that predate it were all moved to "paused" by pauseAllActive
// and are excluded by the status clause above. The one residual case — an add
// racing the flag write — is benign rather than a data-loss risk: such an entry
// was never actually paused (no DisallowDataDownload was called on it), so it
// really is downloading, and zero bytes with zero peers for the full threshold
// means it really is dead.
func (m *Manager) isStale(e *entry, now time.Time) bool {
	if m.cfg.StaleThresholdMinutes <= 0 {
		return false
	}
	if e.t == nil || e.status != "active" {
		return false
	}
	if e.cachedCompleted != 0 {
		return false
	}
	if e.lastProgressAt.IsZero() {
		return false
	}
	stats := e.t.Stats()
	if stats.ActivePeers != 0 || stats.ConnectedSeeders != 0 {
		return false
	}
	return now.Sub(e.lastProgressAt) >= time.Duration(m.cfg.StaleThresholdMinutes)*time.Minute
}

// seedStopReason reports why a seeding entry should stop — a ratio limit or a
// duration limit, whichever trips first, with "" meaning "keep seeding".
// Both limits are live simultaneously; a limit of 0 means that limit is not
// enforced, and both at 0 means seeding is unbounded.
//
// The ratio DENOMINATOR is seedTotalBytes (t.Length()), NOT
// Stats().BytesReadData: that counter is process-lifetime and resets to 0
// across a restart, which would make the computed ratio unbounded and never
// trip. t.Length() is the torrent's declared size and is what
// qBittorrent-class clients divide by. Uploaded bytes are measured from a
// baseline taken at seed start, because tit-for-tat reciprocation during the
// download may already have written bytes.
//
// Caller holds m.mu.
func (m *Manager) seedStopReason(e *entry, now time.Time) string {
	if e.seedStartedAt.IsZero() {
		return ""
	}
	if ratio := m.cfg.SeedRatioLimit; ratio > 0 && e.seedTotalBytes > 0 && e.t != nil {
		// Count.Int64 has a pointer receiver, so the stats value must land in
		// an addressable local first.
		stats := e.t.Stats()
		uploaded := stats.BytesWrittenData.Int64() - e.seedBaselineUp
		if target := int64(float64(e.seedTotalBytes) * ratio); uploaded >= target {
			return fmt.Sprintf("seed ratio limit %.2f reached (%d bytes uploaded of %d)", ratio, uploaded, e.seedTotalBytes)
		}
	}
	if mins := m.cfg.SeedDurationMinutes; mins > 0 {
		limit := time.Duration(mins) * time.Minute
		if elapsed := now.Sub(e.seedStartedAt); elapsed >= limit {
			return fmt.Sprintf("seed duration limit %s reached (seeded for %s)", limit, elapsed.Truncate(time.Second))
		}
	}
	return ""
}

// stopSeeding performs the actual teardown for one collected seedStop:
// disallow upload, drop the torrent, delete the ORIGINAL staging files (the
// library copy lives outside staging and deleteDownloadFiles structurally
// cannot reach it), and log the reason.
//
// Called by pollLoop with m.mu RELEASED — that is the whole point of the
// collect pass, since Drop takes the torrent client's own lock and the
// deletion is synchronous I/O against staging. The import-copy fields are
// deliberately NOT cleared here: seeding ending says nothing about whether the
// import consumed its copy yet.
func (m *Manager) stopSeeding(s seedStop) {
	if s.t != nil {
		s.t.DisallowDataUpload()
		s.t.Drop()
	}
	m.deleteDownloadFiles(s.seedPaths)

	m.mu.Lock()
	if e, ok := m.entries[s.gid]; ok {
		e.seedPaths = nil
		e.seedBaselineUp = 0
		e.seedTotalBytes = 0
	}
	m.mu.Unlock()

	log.Printf("downloader: stopped seeding %s: %s", s.gid, s.reason)
}

// importDirName is the staging subdirectory this feature exclusively owns.
// Every per-gid import copy lives at <StagingDir>/.import/<gid>/.
const importDirName = ".import"

// importRootFor is the per-gid import-copy directory path. Takes m.mu via
// stagingRoot, so callers must not already hold it.
func (m *Manager) importRootFor(gid string) string {
	return filepath.Join(m.stagingRoot(), importDirName, gid)
}

// stageImportCopy copies every original staged file into
// <StagingDir>/.import/<gid>/, mirroring the original's structure relative to
// the staging dir, and returns the copied paths plus the per-gid root. The
// copy — not the original — is what the import flow relocates (§3.2's
// inversion), so the originals stay put and keep seeding.
//
// On ANY error it removes its own partial tree before returning: a copy that
// failed halfway has already written real bytes that nothing will ever import
// or look at again, and leaving them is a silent disk leak indistinguishable
// from the ~2x seeding overhead. os.RemoveAll is safe HERE SPECIFICALLY, and
// only here, because .import/<gid>/ is a directory this feature created and
// exclusively owns — unlike the shared staging dir, which Cancel's doc comment
// explains can never be removed wholesale.
func (m *Manager) stageImportCopy(gid string, files []string) ([]string, string, error) {
	// Runs from watchTorrent's completion arm with m.mu released — it is a
	// full-size file copy — so staging is read through the accessor.
	staging := filepath.Clean(m.stagingRoot())
	root := m.importRootFor(gid)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, "", fmt.Errorf("creating import copy dir %s: %w", root, err)
	}

	copied := make([]string, 0, len(files))
	for _, f := range files {
		// SECURITY: same containment check deleteDownloadFiles documents —
		// e.files comes from anacrolix's File.Path(), the RAW unsanitized
		// bencoded path, which can carry "../" components. A path that escapes
		// staging would also escape the import root, so os.RemoveAll below
		// could never reclaim it.
		rel, err := filepath.Rel(staging, filepath.Clean(f))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			m.removeImportRoot(root)
			return nil, "", fmt.Errorf("refusing to copy out-of-staging path %s", f)
		}
		dst := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			m.removeImportRoot(root)
			return nil, "", fmt.Errorf("creating import copy dir %s: %w", filepath.Dir(dst), err)
		}
		if err := copyFile(f, dst); err != nil {
			m.removeImportRoot(root)
			return nil, "", fmt.Errorf("copying %s for import: %w", f, err)
		}
		copied = append(copied, dst)
	}
	return copied, root, nil
}

// copyFile writes src's contents to dst, creating or truncating dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// removeImportRoot deletes one per-gid import-copy tree, best-effort. A
// cleanup failure must never block or fail an import — same discipline as
// deleteDownloadFiles's best-effort parent pruning.
func (m *Manager) removeImportRoot(root string) {
	if root == "" {
		return
	}
	if err := os.RemoveAll(root); err != nil {
		log.Printf("downloader: removing import copy dir %s: %v", root, err)
	}
}

// sweepImportDir removes the whole <StagingDir>/.import/ tree at startup.
// Seeding state is in-memory only, so nothing under it survives a restart in
// any useful form; every subdirectory is an orphan of an unclean shutdown.
//
// Deliberate limitation, stated rather than hidden: orphaned SEEDING ORIGINALS
// are indistinguishable from a legitimately-resuming download, so they are
// left for the operator. Only .import/ — unambiguously this feature's — is
// swept.
func (m *Manager) sweepImportDir() {
	// Called from Start with m.mu released.
	staging := m.stagingRoot()
	if staging == "" {
		return
	}
	m.removeImportRoot(filepath.Join(staging, importDirName))
}

// setImportPaths records the import copy made for gid. Written together with
// the root because ImportRoot and ImportPaths must never disagree about
// whether a copy exists.
func (m *Manager) setImportPaths(gid, root string, paths []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[gid]; ok {
		e.importRoot = root
		e.importPaths = append([]string(nil), paths...)
	}
}

// beginSeeding opens the seed window for gid: it records the ratio/duration
// accounting baseline and re-allows upload on the torrent, which AddTorrent
// disallowed at add time so an in-progress download never reciprocated.
//
// seedPaths are the ORIGINAL staging paths, not the import copy — those are
// what the client keeps serving and what cleanup eventually deletes.
func (m *Manager) beginSeeding(gid string, t *torrentlib.Torrent, seedPaths []string, total int64) {
	var baseline int64
	if t != nil {
		stats := t.Stats()
		baseline = stats.BytesWrittenData.Int64()
	}

	m.mu.Lock()
	e, ok := m.entries[gid]
	current := ok && e.t == t
	if current {
		e.seedStartedAt = time.Now()
		e.seedBaselineUp = baseline
		e.seedTotalBytes = total
		e.seedPaths = append([]string(nil), seedPaths...)
	}
	m.mu.Unlock()

	if current && t != nil {
		t.AllowDataUpload()
	}
}

// ImportRoot is the per-gid import-copy directory, or StagingDir() when
// seeding is off, no copy was made, or the gid is unknown. It is what
// downloadContentPath must compare against to keep its single-file vs
// multi-file verdict correct once the import consumes a copy under
// .import/<gid>/ rather than staging itself.
func (m *Manager) ImportRoot(gid string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[gid]; ok && e.importRoot != "" {
		return e.importRoot
	}
	return m.cfg.StagingDir
}

// ImportPaths is the per-gid import-copy FILE paths recorded by
// stageImportCopy, or NIL when seeding is off, no copy was made, the copy
// failed and the completion flow fell through to the originals, or the gid is
// unknown. A nil return means "there is no copy — use the entry's own files",
// which is today's behavior exactly; every caller must branch on
// len(paths) > 0 and fall back to the originals.
//
// Returns a defensive copy, same discipline as Cancel's paths copy, so a
// caller cannot mutate entry state.
func (m *Manager) ImportPaths(gid string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[gid]
	if !ok || len(e.importPaths) == 0 {
		return nil
	}
	return append([]string(nil), e.importPaths...)
}

// ClearImportCopy removes gid's import-copy directory and forgets it, so
// ImportPaths reverts to nil. Called by the automatic import path once the
// import has fully succeeded: rename.Relocate moves the CONTENT out of
// .import/<gid>/ but leaves the (now empty) directory tree behind, and on a
// busy install those accumulate as thousands of empty directories.
//
// Best-effort and never an error: the Manager.Start sweep is the backstop for
// anything this misses (a crash mid-import, or a cancelled entry).
func (m *Manager) ClearImportCopy(gid string) {
	m.mu.Lock()
	var root string
	if e, ok := m.entries[gid]; ok {
		root = e.importRoot
		e.importRoot = ""
		e.importPaths = nil
	}
	m.mu.Unlock()
	m.removeImportRoot(root)
}

// SeedImportCopy injects import-copy state for an existing test entry, the
// same test-support role SeedState plays for the entry itself — handler tests
// live in internal/api and cannot reach setImportPaths. Test-mode only.
func (m *Manager) SeedImportCopy(gid, importRoot string, importPaths []string) {
	m.setImportPaths(gid, importRoot, importPaths)
}

// pollSnapshot builds the current Download list from all entries, advancing
// speed state (prevBytes/prevTime/speed) and the stale clock for active
// downloads. Only called by pollLoop — HTTP handlers use readSnapshot to avoid
// corrupting speed state.
//
// It also returns the work that must be performed AFTER m.mu is released:
// seeding entries that tripped a stop condition, and gids that tripped the
// stale predicate. Both are COLLECTED here (with the entry marked so the next
// tick cannot re-collect it) and ACTED ON by pollLoop. m.mu is the same mutex
// every downloads HTTP handler and the SSE fan-out take, so no torrent drop,
// file I/O, or callback may run inside this function.
func (m *Manager) pollSnapshot() (snap []Download, stopSeeds []seedStop, staleGIDs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := make([]Download, 0, len(m.entries))
	for gid, e := range m.entries {
		if e.t != nil && (e.status == "active" || e.status == "waiting") {
			// Guard against a closed torrent handle (race with client shutdown).
			alive := true
			select {
			case <-e.t.Closed():
				alive = false
			default:
			}
			// TOCTOU note: there's a narrow window between this alive check and the Stats/
			// BytesCompleted calls below. anacrolix/torrent's methods return zero values
			// after Drop() rather than panicking, so this race is benign in practice.
			if alive {
				completed := e.t.BytesCompleted()
				// Stale clock: any forward progress resets it. It lives inside
				// the active||waiting guard on purpose — a paused entry's
				// stats are never refreshed here, so its clock freezes rather
				// than ticking toward the reaper and deleting the operator's
				// files after a long pause.
				if completed > e.prevBytes {
					e.lastProgressAt = now
				}
				var total int64
				if e.t.Info() != nil {
					total = e.t.Length()
				}
				var speed int64
				if e.status == "active" && !e.prevTime.IsZero() {
					if dt := now.Sub(e.prevTime).Seconds(); dt > 0 {
						if delta := completed - e.prevBytes; delta > 0 {
							speed = int64(float64(delta) / dt)
						}
					}
				}
				e.prevBytes = completed
				e.prevTime = now
				e.speed = speed
				e.cachedCompleted = completed
				e.cachedTotal = total
			}

			// Stale collect pass. Marked fired under the lock so the next tick
			// cannot re-collect the same entry.
			if !e.staleFired && m.isStale(e, now) {
				e.staleFired = true
				staleGIDs = append(staleGIDs, gid)
			}
		}

		// Seeding collect pass — deliberately OUTSIDE the active||waiting
		// guard above. A seeding entry's status is "complete" (watchTorrent
		// sets it the moment Complete() fires, which is the very event seeding
		// hooks into), so a check nested inside that guard could never fire and
		// would be indistinguishable from "seeding is unbounded".
		if e.t != nil && e.status == "complete" && !e.seedStartedAt.IsZero() {
			if reason := m.seedStopReason(e, now); reason != "" {
				stopSeeds = append(stopSeeds, seedStop{
					gid: gid,
					t:   e.t,
					// Defensive copy, same discipline as Cancel: the deletion
					// happens after the lock is released and must not race a
					// concurrent mutation of the entry's slice.
					seedPaths: append([]string(nil), e.seedPaths...),
					reason:    reason,
				})
				// Clear the discriminator so the next tick cannot re-collect it.
				e.seedStartedAt = time.Time{}
				// Upload window just closed: publish 0 rather than freezing at
				// the last observed rate for the rest of this entry's lifetime.
				e.upSpeed = 0
				e.prevUpTime = time.Time{}
			}
		}

		// Claude 2026-08-04: upload-speed + seed-count pass — its OWN guard,
		// deliberately AFTER the seeding collect pass above and NOT nested
		// inside the active||waiting guard.
		// Reason: upload only happens once a torrent is complete-and-seeding
		// (per-torrent AllowDataUpload happens in beginSeeding), which is
		// exactly the state the active||waiting guard excludes — nesting this
		// there would make UploadSpeed structurally always 0. Seed count
		// (cachedSeeds, née cachedConns) is refreshed here too, not in the
		// active||waiting block above, so it keeps reporting a live value
		// while an entry is seeding rather than freezing at its last active
		// reading — always-showing a stale number is worse than hiding it.
		// Same benign-TOCTOU reasoning as the active||waiting block above
		// applies to the alive-handle check below.
		// Review if: upload accounting changes to something other than
		// BytesWrittenData, or seed count gains its own independent gate.
		if e.t != nil && (e.status == "active" || e.status == "waiting" || (e.status == "complete" && !e.seedStartedAt.IsZero())) {
			alive := true
			select {
			case <-e.t.Closed():
				alive = false
			default:
			}
			if alive {
				e.cachedSeeds = int64(e.t.Stats().ConnectedSeeders)

				// Count.Int64 has a pointer receiver (F-5), so Stats() must
				// land in an addressable local before .BytesWrittenData.Int64().
				stats := e.t.Stats()
				// Claude 2026-08-04: BytesWrittenData, not the spec-text
				// BytesWritten — deliberate deviation.
				// Reason: BytesWrittenData is payload-only (excludes wire
				// protocol overhead), matching how download speed already
				// measures via BytesCompleted(), and it is the exact counter
				// seedStopReason already uses for ratio accounting — keeping
				// the displayed rate arithmetically consistent with the number
				// that decides when seeding stops. Every other upload-counter
				// site in this file (seedStopReason, beginSeeding) uses
				// BytesWrittenData; using BytesWritten here would introduce a
				// second, subtly different upload counter with no precedent.
				// Review if: someone "corrects" this back to BytesWritten to
				// match the spec text — don't, see above.
				written := stats.BytesWrittenData.Int64()

				var upSpeed int64
				if !e.prevUpTime.IsZero() {
					if dt := now.Sub(e.prevUpTime).Seconds(); dt > 0 {
						if delta := written - e.prevUpBytes; delta > 0 {
							upSpeed = int64(float64(delta) / dt)
						}
					}
				}
				e.prevUpBytes = written
				e.prevUpTime = now
				e.upSpeed = upSpeed
			}
		}

		out = append(out, m.buildEntry(gid, e))
	}
	return out, stopSeeds, staleGIDs
}

// fanout delivers snap to every subscriber, dropping a stale pending snapshot
// for any subscriber whose buffer is full (latest-wins, never blocks).
func (m *Manager) fanout(snap []Download) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.subscribers {
		select {
		case ch <- snap:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- snap:
			default:
			}
		}
	}
}

// sameSnapshot reports whether two snapshots are equal by (GID, Status,
// CompletedLength) — the fields whose change the UI cares about.
func sameSnapshot(a, b []Download) bool {
	if len(a) != len(b) {
		return false
	}
	return reflect.DeepEqual(diffKeys(a), diffKeys(b))
}

func diffKeys(dls []Download) map[string]seenKey {
	out := make(map[string]seenKey, len(dls))
	for _, d := range dls {
		out[d.GID] = seenKey{status: d.Status, completed: d.CompletedLength}
	}
	return out
}

// maxConcCap returns the effective MaxConc floor of 1.
func (m *Manager) maxConcCap() int {
	n := m.cfg.MaxConc
	if n < 1 {
		return 1
	}
	return n
}

func (m *Manager) countActiveLocked() int {
	n := 0
	for _, e := range m.entries {
		if e.status == "active" {
			n++
		}
	}
	return n
}

// tryAdmitLocked promotes e to active if a MaxConc slot is free. Caller holds
// m.mu. e must already be metaReady and not paused. Returns whether it is
// (now) active.
func (m *Manager) tryAdmitLocked(e *entry) bool {
	if e.status == "paused" || e.status == "complete" || e.status == "error" || e.status == "removed" {
		return false
	}
	if !e.metaReady {
		return false
	}
	if e.status == "active" {
		return true
	}
	if m.countActiveLocked() >= m.maxConcCap() {
		return false
	}
	e.status = "active"
	return true
}

// admitWaitingLocked promotes waiting+metaReady entries FIFO by addedAt until
// MaxConc is full. Returns torrents that should AllowDataDownload. Caller holds m.mu.
func (m *Manager) admitWaitingLocked() []*torrentlib.Torrent {
	var ready []*entry
	for _, e := range m.entries {
		// t may be nil in tests; still flip status so MaxConc accounting works.
		if e.status == "waiting" && e.metaReady {
			ready = append(ready, e)
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		if ready[i].addedAt.Equal(ready[j].addedAt) {
			return ready[i].filename < ready[j].filename
		}
		return ready[i].addedAt.Before(ready[j].addedAt)
	})
	var out []*torrentlib.Torrent
	for _, e := range ready {
		if m.countActiveLocked() >= m.maxConcCap() {
			break
		}
		e.status = "active"
		if e.t != nil {
			out = append(out, e.t)
		}
	}
	return out
}

// syncConcurrencyLocked admits up to MaxConc and demotes newest active excess
// back to waiting. Returns (allow, deny) torrent handles. Caller holds m.mu.
func (m *Manager) syncConcurrencyLocked() (allow, deny []*torrentlib.Torrent) {
	allow = m.admitWaitingLocked()
	var active []*entry
	for _, e := range m.entries {
		if e.status == "active" {
			active = append(active, e)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		// Demote newest first so older downloads keep their slots.
		return active[i].addedAt.After(active[j].addedAt)
	})
	capN := m.maxConcCap()
	for len(active) > capN {
		e := active[0]
		active = active[1:]
		e.status = "waiting"
		if e.t != nil {
			deny = append(deny, e.t)
		}
	}
	return allow, deny
}
