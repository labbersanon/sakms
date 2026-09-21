package usenet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	par2lib "github.com/go-newsgroups/par2"
	"github.com/labbersanon/sakms/internal/xferlimit"
	"golang.org/x/sync/errgroup"
)

// Forget drops a TERMINAL (error/complete/removed) download from the in-memory
// queue without touching disk. Returns false when the entry is active or paused
// (callers must not Forget a live download).
//
// Claude 2026-09-17: intentionally NOT Cancel.
// Reason: Cancel calls deleteDownloadDir → os.RemoveAll, which would destroy the
//
//	staging dir and the .sakms-resume.json sidecar that resumeDueTransportRetries
//	needs for RelaunchNZB to resume from where the failed download left off.
//
// Review if: Cancel gains a "drop from queue, keep files" mode.
func (m *Manager) Forget(gid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	dl, ok := m.downloads[gid]
	if !ok {
		return false
	}
	switch dl.status {
	case "error", "complete", "removed":
		delete(m.downloads, gid)
		return true
	default:
		return false
	}
}

// Download mirrors the downloader.Download shape so the api layer can build a
// unified queue from both torrent and usenet downloads without a shared
// interface. Usenet has no seeder concept, so it carries neither a seed-count
// nor an upload-speed field at all — those are torrent-only in apidto.Download,
// and the frontend hides them entirely for this protocol rather than showing a
// zero.
type Download struct {
	GID             string
	Status          string   // "active" | "paused" | "error" | "complete" | "removed"
	Filename        string   // release name (from X-DNZB-Name or NZB first file)
	Dir             string   // staging subdirectory where assembled files land
	TotalLength     int64    // sum of NZB segment byte counts (approximate before download)
	CompletedLength int64    // decoded bytes written so far
	DownloadSpeed   int64    // bytes/sec (computed per 500 ms poll tick)
	Files           []string // absolute paths of assembled files (populated on complete)
	ErrorMessage    string
	// ResumeMode is how this job started: "resumed", "full", "forced-full", or "disabled".
	// Empty until runDownload sets it. Surfaced on the downloads SSE for operators.
	ResumeMode string
	// Claude 2026-09-21: Usenet postprocess phase for Downloads screen tags.
	// Reason: status stays "active" through PAR2/unpack; the UI needs a distinct
	//   wire value for fetching vs repairing vs unpacking.
	// Troubleshooting: Downloads showing raw "active" during PAR2/unrar.
	// Review if: torrents grow a comparable postprocess phase.
	// Wire values: "downloading" | "repairing" | "unpacking". Empty when
	// paused/error/complete/removed.
	Phase string
	// Claude 2026-09-21: Downloads postprocess progress (repairing/unpacking).
	// Reason: work units + phase start so the UI can show NN% and an elapsed timer
	//   instead of ↓ MB/s while downloadSpeed is 0.
	// Troubleshooting: SSE skipped ticks when only phaseDone changed (must be in snapKey).
	// Review if: torrents grow a comparable postprocess phase.
	PhaseDone      int64
	PhaseTotal     int64
	PhaseStartedAt time.Time
	// Claude 2026-09-20: queue order key — when this job entered the in-memory engine.
	// Reason: List()/SSE used map iteration and reshuffled the Downloads UI every tick.
	// Review if: durable queue restore should carry the original add time across restart.
	AddedAt time.Time
	// Err is the unflattened retrieval failure, Go-side only — it is never
	// serialised (the api layer maps this struct field-by-field into
	// apidto.Download, which carries ErrorMessage for the UI). Callers use
	// errors.Is to tell a permanent ErrArticleRemoved (451 DMCA takedown, never
	// retry) apart from a retryable ErrArticleNotFound (430 from every
	// subscription) or a transient dial failure. Nil unless Status == "error".
	Err error
}

// Claude 2026-09-21: Usenet download phase wire values (lowercase).
// Reason: Downloads tags distinguish fetch vs PAR2 vs unpack while status=active.
// Review if: a new postprocess step is added between assembly and complete.
const (
	phaseDownloading = "downloading"
	phaseRepairing   = "repairing"
	phaseUnpacking   = "unpacking"
)

// setPhase updates dl.phase under the caller's Manager.mu. Repairing/unpacking
// reset work-unit counters and stamp phaseStartedAt; any other phase (including
// empty on complete/error/paused) clears those fields so the DTO omitempties.
func (dl *dlState) setPhase(phase string) {
	dl.phase = phase
	dl.phaseDone = 0
	dl.phaseTotal = 0
	if phase == phaseRepairing || phase == phaseUnpacking {
		dl.phaseStartedAt = time.Now()
		return
	}
	dl.phaseStartedAt = time.Time{}
}

// dlState is the mutable runtime state of one usenet download. All fields
// except the atomics are protected by Manager.mu.
type dlState struct {
	gid        string
	name       string
	stagingDir string
	resume     *resumeTracker // sidecar writer; set in runDownload (disabled=true when resume is off)
	resumeMode string
	status     string
	// Claude 2026-09-21: mirrors Download.Phase; set under Manager.mu with status.
	phase          string
	phaseDone      int64
	phaseTotal     int64
	phaseStartedAt time.Time
	errorMsg       string
	err            error // classified retrieval failure; surfaced as Download.Err
	files          []string
	cancel         context.CancelFunc
	gate           *pauseGate // true-pause; never nil after construction
	// Claude 2026-09-20: set once when the job is inserted into m.downloads.
	addedAt time.Time

	// Progress fields — updated by download goroutines via Manager.addCompleted
	// and read + speed-computed by snapshot(), all under Manager.mu.
	total     int64
	completed int64
	prevBytes int64
	prevTime  time.Time
	speed     int64
}

// Manager is the Usenet download engine. It starts NZB downloads, tracks
// their progress, and fans out queue snapshots to SSE subscribers. It is a
// process-lifetime singleton constructed once in cmd/sakms/main.go; its set of
// NNTP server pools is reconfigurable at runtime via SetSubscriptions, so a
// subscription can be added or edited without restarting.
type Manager struct {
	httpClient *http.Client
	stagingDir string
	onComplete func(gid string, files []string)
	// Claude 2026-09-15: park grab on engine error without waiting for 24h sweep
	// Reason: sweepUsenetFailures only runs on usenet_retry_interval; live failures
	//   left rows at downloading until that tick (or a browser poll).
	// Troubleshooting: Requests stuck "Downloading" after journal "usenet: download … error"
	// Review if: RunUsenetRetry gains a short failure-only ticker of its own
	// Related: SetOnError; api.UsenetErrorHandler; sweepUsenetFailures
	onError  func(gid string, failure error)
	startCtx context.Context // set by Start under mu; nil until then

	// Claude 2026-09-11: segment resume policy (Phase 2 ARR-parity)
	// Reason: after restart RelaunchNZB must skip durable completed segments
	// Troubleshooting: settings usenet_segment_resume_enabled / _force_full
	segmentResume     bool
	forceFullDownload bool

	mu          sync.Mutex
	pools       []*pool // one per enabled subscription; swapped by SetSubscriptions
	downloads   map[string]*dlState
	subscribers map[int]chan []Download
	nextSubID   int
	// statUnreliable latches for the process lifetime once a backend answers 430
	// to STAT but still serves BODY, which makes the precheck gate unusable.
	statUnreliable bool
	// Claude 2026-09-01: job semaphore is MaxConcurrentDownloads, not Σ MaxConns.
	// Reason: operators need "how many NZBs at once" separate from per-server
	//   NNTP sockets; PAR2/unpack must not hold a download slot.
	// Troubleshooting: many NZBs each opening MaxConns and saturating the
	//   provider; Downloads stuck while one job repairs.
	// Review if: a separate repair-concurrency knob is added.
	// Related: SetMaxConcurrentDownloads; runDownload releases before PAR2.
	maxConcurrentDownloads int
	semaphore              chan struct{}
	rateCap                *xferlimit.Cap
}

// DefaultMaxConcurrentDownloads is used when Config.MaxConcurrentDownloads is
// unset (<=0). One NZB at a time is the safe default for provider fairness.
const DefaultMaxConcurrentDownloads = 1

// Config parameterises a Manager.
type Config struct {
	// Server is the legacy single-server field, kept so existing callers keep
	// working. When Servers is empty and Server has a Host, New treats it as a
	// one-element Servers. Prefer Servers for new code.
	Server ServerConfig
	// Servers is the set of enabled Usenet subscriptions. Segment retrieval
	// falls back across them in order.
	Servers []ServerConfig
	// MaxConcurrentDownloads caps how many NZBs may fetch segments at once.
	// It does not include PAR2/repair/import. <=0 means DefaultMaxConcurrentDownloads.
	// Per-server MaxConns still bound the NNTP pool and segment fan-out.
	MaxConcurrentDownloads int
	StagingDir             string
	HTTPClient             *http.Client
	// SegmentResume skips completed segments recorded in .sakms-resume.json.
	// Zero value is false so callers that predate Phase 2 keep full re-fetch.
	SegmentResume bool
	// ForceFullDownload ignores/clears any resume sidecar (operator rollback).
	ForceFullDownload bool
	// RateCap is the shared global download bandwidth limiter (torrent + Usenet).
	// Nil means unlimited.
	RateCap *xferlimit.Cap
}

// New constructs a Manager for the given NNTP server configuration(s).
// A Manager with zero servers is valid — it accepts no downloads until
// SetSubscriptions supplies one. The engine is not started until Start is called.
func New(cfg Config) *Manager {
	servers := cfg.Servers
	if len(servers) == 0 && cfg.Server.Host != "" {
		servers = []ServerConfig{cfg.Server}
	}
	maxDL := cfg.MaxConcurrentDownloads
	if maxDL < 1 {
		maxDL = DefaultMaxConcurrentDownloads
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	m := &Manager{
		httpClient:             httpClient,
		stagingDir:             cfg.StagingDir,
		segmentResume:          cfg.SegmentResume,
		forceFullDownload:      cfg.ForceFullDownload,
		downloads:              map[string]*dlState{},
		subscribers:            map[int]chan []Download{},
		maxConcurrentDownloads: maxDL,
		rateCap:                cfg.RateCap,
	}
	m.pools = newPools(servers)
	m.semaphore = make(chan struct{}, maxDL)
	return m
}

// newPools builds one pool per server config.
func newPools(servers []ServerConfig) []*pool {
	pools := make([]*pool, 0, len(servers))
	for _, s := range servers {
		pools = append(pools, newPool(s))
	}
	return pools
}

// concurrencyBudget is the summed per-server connection allowance across pools,
// with each server's MaxConns <= 0 substituted by defaultMaxConnsPerServer and
// the total clamped to at least 1. The clamp matters: a zero budget would make
// errgroup.SetLimit(0) block every segment fetch forever, and would give the
// download semaphore a capacity of zero, which deadlocks every download.
func concurrencyBudget(pools []*pool) int {
	total := 0
	for _, p := range pools {
		total += effectiveMaxConns(p.cfg)
	}
	if total < 1 {
		total = 1
	}
	return total
}

// SetSubscriptions swaps the Manager's pool set so an operator can add, edit or
// remove a Usenet subscription without restarting the process. Pools whose
// config is unchanged are reused (their idle connections survive); pools no
// longer configured are closed.
//
// Safe to call while downloads are in flight. A retired pool's close() only
// terminates connections sitting idle in it — a connection checked out by an
// in-flight fetch is untouched and is terminated by pool.put when that fetch
// returns it (see the comment on pool.put). An in-flight download that has
// already read m.pools continues against the old set for its current segment
// and picks up the new set on the next one.
//
// The job-download semaphore is NOT resized here — it is owned by
// MaxConcurrentDownloads / SetMaxConcurrentDownloads. Segment fan-out still
// follows concurrencyBudget(pools) inside downloadAll.
func (m *Manager) SetSubscriptions(cfgs []ServerConfig) {
	fresh := make([]*pool, 0, len(cfgs))

	m.mu.Lock()
	existing := m.pools
	keep := make([]bool, len(existing))
	for _, c := range cfgs {
		reused := false
		for i, p := range existing {
			if !keep[i] && p.cfg == c {
				fresh = append(fresh, p)
				keep[i] = true
				reused = true
				break
			}
		}
		if !reused {
			fresh = append(fresh, newPool(c))
		}
	}
	var retired []*pool
	for i, p := range existing {
		if !keep[i] {
			retired = append(retired, p)
		}
	}
	m.pools = fresh
	m.mu.Unlock()

	// close() does network I/O — never hold m.mu across it.
	for _, p := range retired {
		p.close()
	}
}

// SetMaxConcurrentDownloads replaces the NZB job semaphore capacity. Downloads
// already holding a token release into the channel they took it from, so during
// the overlap up to (held + newCapacity) NZBs can fetch concurrently. That is
// transient and benign. n < 1 is clamped to DefaultMaxConcurrentDownloads.
func (m *Manager) SetMaxConcurrentDownloads(n int) {
	if n < 1 {
		n = DefaultMaxConcurrentDownloads
	}
	m.mu.Lock()
	m.maxConcurrentDownloads = n
	m.semaphore = make(chan struct{}, n)
	m.mu.Unlock()
}

// Claude 2026-09-19: live StagingDir swap for Advanced off-data staging (A3).
// Reason: operators may point Usenet assemble at a non-data-volume path without
//
//	restarting; refusing while downloads are active avoids split-brain staging trees.
//
// Troubleshooting: PUT /api/settings/usenet-off-data-staging returns 409.
// Review if: drain-and-migrate of in-flight GIDs is added.
var ErrStagingDirBusy = errors.New("usenet: cannot change staging dir while downloads are active")

// SetStagingDir replaces the Usenet staging root. dir must be non-empty.
// Returns ErrStagingDirBusy if any download is tracked.
func (m *Manager) SetStagingDir(dir string) error {
	if m == nil {
		return nil
	}
	dir = filepath.Clean(dir)
	if dir == "" || dir == "." {
		return errors.New("usenet: staging dir must be non-empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.downloads) > 0 {
		return ErrStagingDirBusy
	}
	m.stagingDir = dir
	return nil
}

// SetRateCap replaces the shared download bandwidth cap (may be nil = unlimited).
func (m *Manager) SetRateCap(c *xferlimit.Cap) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.rateCap = c
	m.mu.Unlock()
}

// MaxConcurrentDownloads returns the current NZB job concurrency cap.
func (m *Manager) MaxConcurrentDownloads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxConcurrentDownloads
}

// HasSubscriptions reports whether any Usenet subscription is configured.
//
// This is the pre-flight guard the dispatch path uses before accepting a usenet
// grab. The Manager is constructed unconditionally at boot (so it is never nil
// and SetSubscriptions can configure it later), which means a nil check is no
// longer the right question — "are there any pools?" is.
func (m *Manager) HasSubscriptions() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pools) > 0
}

// currentPools returns a snapshot of the pool set. m.pools is mutable, so every
// read must go through here rather than touching the field directly.
func (m *Manager) currentPools() []*pool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*pool(nil), m.pools...)
}

// baseContext returns the context in-flight downloads derive from: Start's
// context once Start has run, so shutdown cancellation propagates, otherwise
// context.Background().
func (m *Manager) baseContext() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startCtx == nil {
		return context.Background()
	}
	return m.startCtx
}

// currentSemaphore returns the download semaphore in force right now.
func (m *Manager) currentSemaphore() chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.semaphore
}

// SetOnComplete wires the completion callback. Safe to call before Start.
func (m *Manager) SetOnComplete(fn func(gid string, files []string)) {
	m.onComplete = fn
}

// SetOnError wires the failure callback. Safe to call before Start. Fires when
// a download transitions to Status=="error" with a non-nil Err — the fast path
// for parking grabs without waiting on the usenet-retry sweep tick.
func (m *Manager) SetOnError(fn func(gid string, failure error)) {
	m.onError = fn
}

// fireOnError invokes onError asynchronously when set. Never call under m.mu.
//
// Claude 2026-09-15: ctx.Err guard keeps shutdown cancel from parking rows
// Reason: a cancelled ctx is a shutdown, not a retrieval failure — those rows
// are ReconcileInFlightDownloads' to relaunch. downloadAll rarely consults ctx
// today, so a cancelled download can still reach here with an error.
// Troubleshooting: grab parked for re-search across a restart instead of relaunching
// Review if: onError also covers AddNZB/RelaunchNZB sync failures
func (m *Manager) fireOnError(ctx context.Context, gid string, failure error) {
	if m.onError == nil || failure == nil || ctx.Err() != nil {
		return
	}
	go m.onError(gid, failure)
}

// StagingDir returns the directory where assembled NZB files are written.
func (m *Manager) StagingDir() string { return m.stagingDir }

// ResumeMode values surfaced on Download / downloads SSE.
const (
	ResumeModeResumed    = "resumed"
	ResumeModeFull       = "full"
	ResumeModeForcedFull = "forced-full"
	ResumeModeDisabled   = "disabled"
)

// ResumePolicy returns the live (enabled, forceFull) knobs.
func (m *Manager) ResumePolicy() (enabled, forceFull bool) {
	if m == nil {
		return true, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.segmentResume, m.forceFullDownload
}

// SetResumePolicy updates segment-resume knobs at runtime. When forceFull is
// true (or resume is disabled), in-flight sidecars are cleared immediately and
// force-full also sweeps every owned staging dir on disk — not only the next
// runDownload. Matches the Settings UI promise to clear sidecars on toggle.
func (m *Manager) SetResumePolicy(enabled, forceFull bool) {
	if m == nil {
		return
	}
	clearSidecars := forceFull || !enabled
	m.mu.Lock()
	m.segmentResume = enabled
	m.forceFullDownload = forceFull
	type live struct {
		gid, dir string
		resume   *resumeTracker
	}
	var lives []live
	if clearSidecars {
		for gid, dl := range m.downloads {
			lives = append(lives, live{gid: gid, dir: dl.stagingDir, resume: dl.resume})
			if forceFull {
				dl.resumeMode = ResumeModeForcedFull
			} else {
				dl.resumeMode = ResumeModeDisabled
			}
		}
	}
	m.mu.Unlock()

	if clearSidecars {
		for _, item := range lives {
			item.resume.disable()
			if err := ClearResumeArtifacts(item.dir); err != nil {
				log.Printf("usenet: clear resume artifacts %s: %v", item.dir, err)
			}
		}
	}
	if forceFull {
		// Claude 2026-09-11: cancel live jobs + wipe payloads, not just sidecars.
		// Reason: in-flight assembleFile already skipped holes; deleting only
		//         .sakms-resume.json left hollow video importable. Cancel drops
		//         the open FD + staging dir; idle dirs get payloads wiped so
		//         ResolveVideoFile fails and reconcile relaunches.
		// Troubleshooting: force-full still imports hollow mkv → wipe/cancel path
		liveGIDs := make([]string, 0, len(lives))
		for _, item := range lives {
			liveGIDs = append(liveGIDs, item.gid)
		}
		for _, gid := range liveGIDs {
			if err := m.Cancel(gid); err != nil {
				log.Printf("usenet: force-full cancel %s: %v", gid, err)
			}
		}
		n := m.SweepForceFull()
		log.Printf("usenet: force-full — swept %d owned staging dir(s) (sidecars + payloads)", n)
		// Claude 2026-09-11: force-full is one-shot, not sticky
		// Reason: after wipe/cancel, leave resume enabled for subsequent jobs;
		//         sticky force-full made every relaunch full until manually cleared
		// Troubleshooting: journal "force-full one-shot cleared"; settings key resets via PUT
		// Review if: UI needs an explicit "sticky force-full" mode
		m.mu.Lock()
		m.forceFullDownload = false
		m.mu.Unlock()
		log.Printf("usenet: force-full one-shot cleared — subsequent downloads may resume")
	}
}

// SweepResumeArtifacts removes resume sidecars under every
// sakms-owned staging directory. Safe to call with no downloads running.
func (m *Manager) SweepResumeArtifacts() int {
	return m.sweepOwnedStaging(false)
}

// SweepForceFull removes resume sidecars AND non-meta payloads
// under every owned staging dir so hollow videos cannot be imported.
func (m *Manager) SweepForceFull() int {
	return m.sweepOwnedStaging(true)
}

// InvalidateLegacyResumes wipes staging dirs that still carry a non-current
// resume sidecar (v1 yEnc-gap or v2 packed) or orphan payloads without a
// current resume. Call once at process start before ReconcileInFlightDownloads
// so damaged assemblies are discarded and relaunched with yEnc-begin writes.
//
// Claude 2026-09-18: upgrade wipe for yEnc-begin (v3) assembly.
// Reason: packed v2 and gap-polluted v1 staging must not resume after the layout change.
// Troubleshooting: journal "invalidated legacy usenet staging"; empty nzb-* dirs.
// Review if: resumeSchemaVersion bumps again — same wipe-non-current policy applies.
func (m *Manager) InvalidateLegacyResumes() int {
	if m == nil {
		return 0
	}
	root := m.StagingDir()
	if root == "" {
		return 0
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		log.Printf("usenet: invalidate legacy resumes: read %s: %v", root, err)
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if !IsOwnedStagingPath(root, dir) {
			continue
		}
		if !StagingHasLegacyOrOrphanPayloads(dir) {
			continue
		}
		log.Printf("usenet: invalidating legacy/gap-damaged staging %s", e.Name())
		if err := ClearResumeArtifacts(dir); err != nil {
			log.Printf("usenet: clear resume %s: %v", dir, err)
		}
		if err := wipeStagingPayloads(dir); err != nil {
			log.Printf("usenet: wipe payloads %s: %v", dir, err)
		}
		n++
	}
	if n > 0 {
		log.Printf("usenet: invalidated %d legacy usenet staging dir(s) for yEnc-begin reassemble", n)
	}
	return n
}

func (m *Manager) sweepOwnedStaging(wipePayloads bool) int {
	if m == nil {
		return 0
	}
	root := m.StagingDir()
	if root == "" {
		return 0
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		log.Printf("usenet: sweep staging: read %s: %v", root, err)
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if !IsOwnedStagingPath(root, dir) {
			continue
		}
		if err := ClearResumeArtifacts(dir); err != nil {
			log.Printf("usenet: sweep clear sidecars %s: %v", dir, err)
		}
		if wipePayloads {
			if err := wipeStagingPayloads(dir); err != nil {
				log.Printf("usenet: sweep wipe payloads %s: %v", dir, err)
			}
		}
		n++
	}
	return n
}

// Start runs the 500 ms progress-poll loop and blocks until ctx is cancelled.
// Intended to run as `go m.Start(ctx)`.
func (m *Manager) Start(ctx context.Context) {
	// Guarded: AddNZB reads startCtx from the caller's goroutine, and with
	// SetSubscriptions the Manager is now genuinely mutated while Start runs.
	m.mu.Lock()
	m.startCtx = ctx
	m.mu.Unlock()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var prev []Download
	for {
		select {
		case <-ctx.Done():
			for _, p := range m.currentPools() {
				p.close()
			}
			return
		case <-ticker.C:
			snap := m.snapshot()
			if !sameDownloads(prev, snap) {
				m.fanout(snap)
				prev = snap
			}
		}
	}
}

// AddNZB fetches the NZB at url, parses it, and starts a background download
// in the manager's staging directory. name is the display name; when empty,
// the X-DNZB-Name header value is used (or a generic fallback). Returns the
// GID ("nzb-" + 16 hex chars) assigned to this download.
func (m *Manager) AddNZB(ctx context.Context, url, name string) (string, error) {
	nzb, dnzb, err := fetchNZB(m.httpClient, url)
	if err != nil {
		return "", err
	}
	if name == "" {
		name = dnzb.Name
	}
	return m.AddArticleSet(ctx, nzb, name)
}

// AddArticleSet starts a download from an already-parsed (or synthesized) NZB
// article set — same path as AddNZB after fetch. Used by the native NNTP
// discovery backend so sakms-nntp: locators never hit fetchNZB.
//
// Claude 2026-09-17: split from AddNZB for native discovery.
// Reason: engine is NZB-struct-driven; native candidates synthesize *NZB in-memory.
// Troubleshooting: dispatch of sakms-nntp: URLs must call this, not AddNZB.
// Review if: AddNZB gains non-HTTP sources beyond native search.
func (m *Manager) AddArticleSet(ctx context.Context, nzb *NZB, name string) (string, error) {
	if nzb == nil {
		return "", errors.New("usenet: nil article set")
	}
	if name == "" {
		name = "usenet-download"
	}

	// Claude 2026-09-15: pre-download STAT gate before staging.
	// Reason: abort dead NZBs before allocateStaging so no dir is left behind and
	//   RunAutoGrab can try the next ranked candidate in the same cycle.
	// Troubleshooting: AddNZB returns ErrArticlesUnavailable; journal "usenet precheck:".
	// Review if: precheck moves behind a settings toggle (currently always on).
	if _, err := m.precheckNZB(ctx, nzb, nil); err != nil {
		return "", err
	}

	// Claude 2026-08-29: opaque nzb-<16 hex> GIDs, not a process-local nzb-N counter
	// Reason: nextGID reset to 0 on every container restart, so AddNZB reused nzb-1
	//   and ActiveByDownloadGID treated a new series as already grabbing (Furious
	//   vs Ultimatum). Indexer/ntfy can fire on the NZB fetch before that guard.
	// Troubleshooting: air-date logs "already being downloaded" with 0 grab rows
	// Review if: GIDs are minted from a durable store (DB sequence) instead
	gid, dlDir, err := m.allocateStaging()
	if err != nil {
		return "", err
	}

	var totalBytes int64
	for _, f := range nzb.Files {
		for _, s := range f.Segs {
			totalBytes += s.Bytes
		}
	}

	// Derive from startCtx so shutdown cancellation propagates to in-flight
	// downloads. Fall back to context.Background() if Start hasn't been called.
	base := m.baseContext()
	dlCtx, cancel := context.WithCancel(base)
	dl := &dlState{
		gid:        gid,
		name:       name,
		stagingDir: dlDir,
		status:     "active",
		phase:      phaseDownloading,
		total:      totalBytes,
		cancel:     cancel,
		gate:       newPauseGate(),
		addedAt:    time.Now(),
	}

	m.mu.Lock()
	if _, taken := m.downloads[gid]; taken {
		m.mu.Unlock()
		cancel()
		_ = os.RemoveAll(dlDir)
		return "", fmt.Errorf("usenet: gid %s already in flight", gid)
	}
	m.downloads[gid] = dl
	m.mu.Unlock()

	go m.runDownload(dlCtx, gid, dl, nzb)
	return gid, nil
}

// Claude 2026-09-11: RelaunchNZB — post-restart attach into an existing nzb-* dir
// Reason: ARR-parity queue reconcile must not mint a new GID (and orphan partial
//
//	staging) when the in-memory engine forgot a grab after reboot
//
// Troubleshooting: reconcile logs "relaunch"; dir must pass IsOwnedStagingPath
// Review if: PAR2-aware invalidation or cross-host resume replaces this relaunch
// Related: internal/api/downloadreconcile.go
//
// RelaunchNZB re-fetches the NZB at url and starts a download into the existing
// staging directory named gid (same owned nzb-* tree, not allocateStaging's
// fresh GID). assembleFile skips segments already recorded in .sakms-resume.json.
// Returns nil when gid is already in flight.
func (m *Manager) RelaunchNZB(ctx context.Context, gid, url, name string) error {
	nzb, dnzb, err := fetchNZB(m.httpClient, url)
	if err != nil {
		return err
	}
	if name == "" {
		name = dnzb.Name
	}
	return m.RelaunchArticleSet(ctx, gid, nzb, name)
}

// RelaunchArticleSet is RelaunchNZB without the HTTP fetch — used when the
// article set was synthesized from the native header index (sakms-nntp:).
//
// Claude 2026-09-17: split from RelaunchNZB for native discovery resume.
// Reason: transport-resume and reconcile must re-arm without fetchNZB on locators.
// Troubleshooting: relaunch of sakms-nntp: must call this path.
// Review if: locator re-resolution moves entirely into the API layer.
func (m *Manager) RelaunchArticleSet(ctx context.Context, gid string, nzb *NZB, name string) error {
	if !IsOwnedStagingName(gid) {
		return fmt.Errorf("usenet: refusing relaunch into non-owned gid %q", gid)
	}
	if existing, err := m.FindByGID(gid); err != nil {
		return err
	} else if existing != nil {
		return nil
	}
	if nzb == nil {
		return errors.New("usenet: nil article set")
	}
	if name == "" {
		name = "usenet-download"
	}

	// Claude 2026-09-15: precheck the still-needed articles on relaunch.
	// Reason: aged NZBs often fall out of retention; fail closed before burning
	//   the download slot again. Segments already resumable are not re-STATed.
	// Troubleshooting: reconcile parks on ErrArticlesUnavailable.
	var skip map[string]bool
	if enabled, forceFull := m.ResumePolicy(); enabled && !forceFull {
		skip = completedMsgIDsFromDir(filepath.Join(m.stagingDir, gid))
	}
	if _, err := m.precheckNZB(ctx, nzb, skip); err != nil {
		return err
	}

	if err := os.MkdirAll(m.stagingDir, 0o755); err != nil {
		return fmt.Errorf("usenet: creating staging dir %s: %w", m.stagingDir, err)
	}
	dlDir := filepath.Join(m.stagingDir, gid)
	if !IsOwnedStagingPath(m.stagingDir, dlDir) {
		return fmt.Errorf("usenet: refusing relaunch path %s", dlDir)
	}
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		return fmt.Errorf("usenet: ensuring staging dir %s: %w", dlDir, err)
	}
	writeOwnedMarker(dlDir)

	var totalBytes int64
	for _, f := range nzb.Files {
		for _, s := range f.Segs {
			totalBytes += s.Bytes
		}
	}

	base := m.baseContext()
	dlCtx, cancel := context.WithCancel(base)
	dl := &dlState{
		gid:        gid,
		name:       name,
		stagingDir: dlDir,
		status:     "active",
		phase:      phaseDownloading,
		total:      totalBytes,
		cancel:     cancel,
		gate:       newPauseGate(),
		addedAt:    time.Now(),
	}

	m.mu.Lock()
	if _, taken := m.downloads[gid]; taken {
		m.mu.Unlock()
		cancel()
		return nil
	}
	m.downloads[gid] = dl
	m.mu.Unlock()

	go m.runDownload(dlCtx, gid, dl, nzb)
	return nil
}

const (
	// 8 random bytes → "nzb-" + 16 hex chars, a shape no historical nzb-1..n
	// counter value can collide with.
	nzbGIDBytes             = 8
	allocateStagingAttempts = 8
)

// allocateStaging mints a GID and creates the staging directory it owns,
// retrying with a fresh GID if that one is already on disk. Mkdir rather than
// MkdirAll is deliberate: fs.ErrExist is the collision signal.
//
// SECURITY: the per-download dir is keyed on the GID, never on sanitizeName(name).
// name is attacker-controlled (Prowlarr title / NZB X-DNZB-Name) and sanitizeName
// only strips "/\\\x00", not "." / ".." — a name of exactly ".." would resolve to
// the PARENT of the staging dir, which Cancel would then os.RemoveAll.
func (m *Manager) allocateStaging() (gid, dlDir string, err error) {
	if err := os.MkdirAll(m.stagingDir, 0o755); err != nil {
		return "", "", fmt.Errorf("usenet: creating staging dir %s: %w", m.stagingDir, err)
	}
	for i := 0; i < allocateStagingAttempts; i++ {
		var raw [nzbGIDBytes]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", "", fmt.Errorf("usenet: generating gid: %w", err)
		}
		gid = "nzb-" + hex.EncodeToString(raw[:])
		dlDir = filepath.Join(m.stagingDir, gid)

		if err := os.Mkdir(dlDir, 0o755); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return "", "", fmt.Errorf("usenet: creating staging dir %s: %w", dlDir, err)
		}
		// Claude 2026-09-11: ownership marker for safe staging sweeps/deletes
		// Reason: sweeper must only RemoveAll dirs sakms minted; name pattern alone
		//         is legacy-safe, marker proves ownership if naming ever changes
		writeOwnedMarker(dlDir)
		return gid, dlDir, nil
	}
	return "", "", fmt.Errorf("usenet: could not allocate a free nzb GID")
}

// Pause suspends an active download without cancelling its context.
//
// Claude 2026-09-20: true pause (no context cancel).
// Reason: cancel-based Pause stranded grabs.queued with a dead GID and blocked
//
//	freeUsenetSlots; Resume was a stub forcing NZB re-submit.
//
// Troubleshooting: Pause then Resume on Downloads; global Resume all downloads.
// Review if: pause should drop the job semaphore so another NZB can fetch.
func (m *Manager) Pause(gid string) error {
	m.mu.Lock()
	dl, ok := m.downloads[gid]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("usenet: download not found: %s", gid)
	}
	if dl.status != "active" {
		m.mu.Unlock()
		return fmt.Errorf("usenet: download %s is %s, not active", gid, dl.status)
	}
	dl.status = "paused"
	dl.setPhase("")
	gate := dl.gate
	m.mu.Unlock()
	if gate != nil {
		gate.Pause()
	}
	return nil
}

// Resume continues a paused download. Missing staging returns ErrStagingGone.
//
// Claude 2026-09-20: true resume + staging-gone → re-search.
// Reason: operator requirement — never pretend to resume a missing staging tree.
// Troubleshooting: Resume returns ErrStagingGone; Requests shows pending_retry.
// Review if: Resume should RelaunchNZB into a fresh dir when staging is missing.
func (m *Manager) Resume(gid string) error {
	m.mu.Lock()
	dl, ok := m.downloads[gid]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("usenet: download not found: %s", gid)
	}
	if dl.status != "paused" {
		m.mu.Unlock()
		return fmt.Errorf("usenet: download %s is %s, not paused", gid, dl.status)
	}
	staging := dl.stagingDir
	cancel := dl.cancel
	gate := dl.gate
	m.mu.Unlock()

	st, err := os.Stat(staging)
	if err != nil || !st.IsDir() {
		m.mu.Lock()
		if d2, ok := m.downloads[gid]; ok && d2.status == "paused" {
			d2.status = "error"
			d2.setPhase("")
			d2.err = ErrStagingGone
			d2.errorMsg = ErrStagingGone.Error()
		}
		m.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if gate != nil {
			gate.Resume() // wake waiters so they observe ctx cancel
		}
		m.fireOnError(m.baseContext(), gid, ErrStagingGone)
		return ErrStagingGone
	}

	m.mu.Lock()
	if d2, ok := m.downloads[gid]; ok && d2.status == "paused" {
		d2.status = "active"
		d2.setPhase(phaseDownloading)
	}
	m.mu.Unlock()
	if gate != nil {
		gate.Resume()
	}
	return nil
}

// Cancel removes a download entirely (stops it and removes it from the queue),
// AND deletes the downloaded/partial file(s) it wrote to disk.
//
// File-deletion safety: unlike the torrent engine, every usenet download gets
// its OWN per-download staging subdirectory, created by allocateStaging as
// filepath.Join(m.stagingDir, gid) — that whole directory is
// owned by this one download, so os.RemoveAll of it is safe and complete (it
// can't take a sibling download's files). Guarded to never remove the root
// staging dir or an empty path. cancel() is called first so the download
// goroutine stops writing before the directory is removed (a narrow write-after-
// remove race is benign — the goroutine errors out on the cancelled context).
func (m *Manager) Cancel(gid string) error {
	m.mu.Lock()
	dl, ok := m.downloads[gid]
	if ok {
		dl.status = "removed"
		dl.setPhase("")
		delete(m.downloads, gid)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("usenet: download not found: %s", gid)
	}
	dl.cancel()
	m.deleteDownloadDir(dl.stagingDir)
	return nil
}

// deleteDownloadDir best-effort removes a download's own per-download staging
// subdirectory. Defense in depth: it only ever removes a directory that is
// STRICTLY under the root staging dir — it refuses "", the staging root itself
// (rel "."), and any path that escapes staging (rel ".." or "../…"). Combined
// with the GID-keyed dir in AddNZB, this means no attacker-influenced value can
// ever make this RemoveAll a parent or sibling of the staging root.
func (m *Manager) deleteDownloadDir(dir string) {
	if dir == "" {
		return
	}
	staging := filepath.Clean(m.stagingDir)
	clean := filepath.Clean(dir)
	rel, err := filepath.Rel(staging, clean)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	if err := os.RemoveAll(clean); err != nil {
		log.Printf("usenet: deleting cancelled download dir %s: %v", clean, err)
	}
}

// List returns a point-in-time snapshot of all known downloads.
func (m *Manager) List() []Download { return m.snapshot() }

// FindByGID looks up one download by GID. Returns (nil, nil) when not found.
func (m *Manager) FindByGID(gid string) (*Download, error) {
	for _, d := range m.snapshot() {
		if d.GID == gid {
			return &d, nil
		}
	}
	return nil, nil
}

// InjectDownloadForTest registers a live download entry so API tests can
// simulate "engine still knows this GID" without driving a real NZB fetch.
// SetForceFullForTest sets the live force-full flag without sweeping.
// Production clears force-full via SetResumePolicy (one-shot); tests use this
// to exercise reconcile's skip-import branch while the flag is asserted.
func (m *Manager) SetForceFullForTest(v bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.forceFullDownload = v
	m.mu.Unlock()
}

func (m *Manager) InjectDownloadForTest(gid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.downloads == nil {
		m.downloads = map[string]*dlState{}
	}
	if _, ok := m.downloads[gid]; ok {
		return
	}
	m.downloads[gid] = &dlState{
		gid:    gid,
		name:   "test",
		status: "active",
		phase:  phaseDownloading,
		cancel: func() {},
		gate:   newPauseGate(),
	}
}

// Subscribe registers a new SSE subscriber. Returns a buffered channel (cap 1)
// that receives each queue snapshot, and a cancel func that unsubscribes.
// Stale pending snapshots are dropped (latest-wins), matching downloader.Manager.
func (m *Manager) Subscribe() (<-chan []Download, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextSubID
	m.nextSubID++
	ch := make(chan []Download, 1)
	m.subscribers[id] = ch
	return ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if c, ok := m.subscribers[id]; ok {
			delete(m.subscribers, id)
			close(c)
		}
	}
}

// runDownload is the per-download background goroutine. It drives the full
// pipeline: download all segments → assemble files → optional par2 repair →
// unpack archives → fire onComplete callback.
func (m *Manager) runDownload(ctx context.Context, gid string, dl *dlState, nzb *NZB) {
	// Claude 2026-09-01: job slot covers segment fetch only, not PAR2/import.
	// Reason: max concurrent downloads must not count unpacking; MaxConns stay
	//   on the pool/segment path (concurrencyBudget in downloadAll).
	// Troubleshooting: one NZB repairing blocked every other NZB from starting.
	// Review if: repair gets its own concurrency cap.
	sem := m.currentSemaphore()
	sem <- struct{}{}
	released := false
	defer func() {
		if !released {
			<-sem
		}
	}()

	m.mu.Lock()
	resumeOn, forceFull := m.segmentResume, m.forceFullDownload
	m.mu.Unlock()
	// Claude 2026-09-11: clear sidecars on force-full OR resume-disabled
	// Reason: disable→re-enable left a stale sidecar that skipped into a
	//         truncated file (hollow import). Force-full is the rollback.
	// Troubleshooting: journal "full restart" / "resume disabled — cleared"
	// Review if: PUT force-full also sweeps all nzb-* dirs immediately
	if forceFull || !resumeOn {
		if err := ClearResumeArtifacts(dl.stagingDir); err != nil {
			log.Printf("usenet: clearing resume artifacts for %s: %v", gid, err)
		}
		if forceFull {
			log.Printf("usenet: download %s (%s) full restart (force-full / rollback)", gid, dl.name)
		} else {
			log.Printf("usenet: download %s (%s) resume disabled — cleared sidecars", gid, dl.name)
		}
	}
	dl.resume = loadResumeTracker(dl.stagingDir, gid, !resumeOn || forceFull)
	skipped := dl.resume.skippedSegments()
	switch {
	case forceFull:
		dl.resumeMode = ResumeModeForcedFull
	case !resumeOn:
		dl.resumeMode = ResumeModeDisabled
	case skipped > 0:
		dl.resumeMode = ResumeModeResumed
		log.Printf("usenet: download %s (%s) resuming — %d segment(s) already complete on disk", gid, dl.name, skipped)
	default:
		dl.resumeMode = ResumeModeFull
		log.Printf("usenet: download %s (%s) starting (resume enabled, no prior segments)", gid, dl.name)
	}

	files, err := m.downloadAll(ctx, gid, dl, nzb)
	<-sem
	released = true
	if err != nil {
		failed := false
		m.mu.Lock()
		switch dl.status {
		case "removed", "paused":
			// True pause does not cancel. Staging-gone Resume sets error before cancel.
		case "error":
			// Already classified (e.g. ErrStagingGone on Resume) — do not re-fire.
		default:
			dl.status = "error"
			dl.setPhase("")
			dl.errorMsg = err.Error()
			// Keep the wrapped error itself, not just its text, so a caller can
			// errors.Is a permanent ErrArticleRemoved apart from everything
			// else. Only ErrArticleRemoved is terminal downstream: api's
			// classifyDownloadState treats an ErrArticleNotFound AND any
			// unclassified error (a dial or decode failure) alike as
			// retryable, since neither proves the article is really gone.
			dl.err = err
			failed = true
			// Claude 2026-09-01: mirror NZBGet ErrorTarget=both — UI alone was
			// losing the reason on every container restart (in-memory queue).
			// Reason: Downloads showed red errors, docker logs had none, and
			//   the retry sweep skips unknown GIDs after restart, so failures
			//   were undiagnosable from the host.
			// Troubleshooting: "downloads all erroring" with empty sakms logs.
			// Review if: per-NZB log files (NZBGet NzbLog) are added later.
			// Related: internal/usenet/pool.go live-socket hard cap.
			log.Printf("usenet: download %s (%s) error: %v", gid, dl.name, err)
		}
		m.mu.Unlock()
		if failed {
			m.fireOnError(ctx, gid, err)
		}
		return
	}

	m.finalizeAssembled(ctx, gid, dl, files)
}

func (m *Manager) waitGateOrAbort(ctx context.Context, gid string, dl *dlState) error {
	if dl.gate == nil {
		return nil
	}
	err := dl.gate.Wait(ctx)
	if err == nil {
		return nil
	}
	failed := false
	m.mu.Lock()
	switch dl.status {
	case "removed", "paused", "error":
	default:
		dl.status = "error"
		dl.setPhase("")
		dl.errorMsg = err.Error()
		dl.err = err
		failed = true
		log.Printf("usenet: download %s (%s) error: %v", gid, dl.name, err)
	}
	m.mu.Unlock()
	if failed {
		m.fireOnError(ctx, gid, err)
	}
	return err
}

// finalizeAssembled runs PAR2 then unpack on assembled staging files and marks
// the download complete or error. Extracted so tests can exercise the gate
// without NNTP.
//
// Claude 2026-09-18: PAR2 failure is a warning; unpack (or a flat video) is the
//
//	delivery gate (#62).
//
// Reason: even with yEnc-begin + FileSize truncate, go-newsgroups/par2 can still
//
//	report missing slices; fail-closing before unrar skipped archives NZBGet
//	still extracts. Naming uniquify (#59/#60) is independent of this gate.
//
// Troubleshooting: "par2: not repairable" with a healthy .part01.rar in staging
//
//	and no unpack attempt in the journal.
//
// Review if: go-newsgroups/par2 treats FileDesc-sized yEnc NUL windows as complete
//
//	so this warning path is unused.
//
// Related: verifyAndRepair; unpackArchives; docs/usenet-contiguous-assembly.md.
func (m *Manager) finalizeAssembled(ctx context.Context, gid string, dl *dlState, files []string) {
	if err := m.waitGateOrAbort(ctx, gid, dl); err != nil {
		return
	}
	// Claude 2026-09-21: phase repairing while PAR2 runs; status stays active.
	// Reason: Downloads tags need downloading vs repairing vs unpacking.
	// Review if: PAR2 is skipped for flat-video releases (still set, then unpacking).
	m.mu.Lock()
	if dl.status == "active" {
		dl.setPhase(phaseRepairing)
	}
	m.mu.Unlock()
	repaired, repairErr := verifyAndRepair(dl.stagingDir, files, func(done, total int64) {
		m.setPhaseProgress(gid, done, total)
	})
	if repairErr != nil {
		log.Printf("usenet: par2 repair %s: %v — continuing to unpack", gid, repairErr)
	}
	files = repaired

	if err := m.waitGateOrAbort(ctx, gid, dl); err != nil {
		return
	}

	// Claude 2026-09-03: unpack rar/zip/7z after PAR2, before import.
	// Reason: most Usenet releases are multi-part RAR; import only resolves
	//   flat videos in the GID staging dir (non-recursive).
	// Troubleshooting: staging full of .partNN.rar, "no video files found".
	// Review if: password-protected archives need a setting.
	// Related: unpack.go; Dockerfile unrar + p7zip-full.
	// Claude 2026-09-15: unpack failure is also fail-closed (no fake complete).
	m.mu.Lock()
	if dl.status == "active" {
		dl.setPhase(phaseUnpacking)
	}
	m.mu.Unlock()
	unpacked, unpackErr := unpackArchives(dl.stagingDir, files, func(done, total int64) {
		m.setPhaseProgress(gid, done, total)
	})
	if unpackErr != nil {
		failed := false
		m.mu.Lock()
		if dl.status != "removed" && dl.status != "paused" {
			dl.status = "error"
			dl.setPhase("")
			dl.errorMsg = unpackErr.Error()
			// Claude 2026-09-17: wrap unpack failure with ErrContentUnusable so
			//   applyUsenetFailure can route to a different-release park.
			// Reason: a release that won't unpack is a property of THIS NZB.
			// Exception: ErrUnpackToolMissing is an environment fault — a different
			//   release cannot fix a missing unrar/7z binary — so it passes through
			//   unwrapped and lands on the days ladder as before.
			// Review if: password-protected archives should be treated differently.
			if errors.Is(unpackErr, ErrUnpackToolMissing) {
				dl.err = unpackErr
			} else {
				dl.err = fmt.Errorf("%w: %w", ErrContentUnusable, unpackErr)
			}
			failed = true
			log.Printf("usenet: unpack %s: %v (failing download — not marking complete)", gid, unpackErr)
		}
		m.mu.Unlock()
		if failed {
			m.fireOnError(ctx, gid, dl.err)
		}
		return
	}
	files = unpacked

	// Claude 2026-09-18: PAR2-fail + no video still fail-closed.
	// Reason: without archives, unpack returns nil and would soft-complete a
	//   PAR2-only staging dir (the 2026-09-15 soft-complete regression).
	// Review if: flat non-archive payloads that are not videos need a path.
	if repairErr != nil && len(videoNamesInDir(dl.stagingDir)) == 0 {
		failed := false
		m.mu.Lock()
		if dl.status != "removed" && dl.status != "paused" {
			dl.status = "error"
			dl.setPhase("")
			dl.errorMsg = repairErr.Error()
			dl.err = fmt.Errorf("%w: %w", ErrContentUnusable, repairErr)
			failed = true
			log.Printf("usenet: par2 repair %s: %v (no video after unpack — not marking complete)", gid, repairErr)
		}
		m.mu.Unlock()
		if failed {
			m.fireOnError(ctx, gid, dl.err)
		}
		return
	}

	m.mu.Lock()
	if dl.status != "removed" && dl.status != "paused" {
		dl.status = "complete"
		dl.setPhase("")
		dl.files = files
	}
	m.mu.Unlock()

	if m.onComplete != nil {
		filesCopy := append([]string(nil), files...)
		go m.onComplete(gid, filesCopy)
	}
}

// downloadAll downloads every file in the NZB and returns the assembled paths.
func (m *Manager) downloadAll(ctx context.Context, gid string, dl *dlState, nzb *NZB) ([]string, error) {
	maxConc := concurrencyBudget(m.currentPools())
	// Claude 2026-09-18: track claimed output names across NZB files.
	// Reason: obfuscated multi-part releases reuse one yEnc filename for every
	//   RAR/PAR2 part; without uniquify, assembleFile smashed them into one path
	//   and merged resume Done maps (many MsgIDs at n:1/off:0) → PAR2
	//   "thousands of damaged/missing slices".
	// Troubleshooting: resume unique_n << done count; staging ~one-part size.
	// Review if: posters start emitting distinct yEnc names per part again.
	usedNames := map[string]struct{}{}
	var paths []string
	for _, nzbFile := range nzb.Files {
		if dl.gate != nil {
			if err := dl.gate.Wait(ctx); err != nil {
				return nil, err
			}
		}
		path, err := m.assembleFile(ctx, gid, dl, nzbFile, maxConc, usedNames)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", nzbFile.Subject, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// assembleFile downloads every segment of one NZB file and writes it at yEnc
// begin offsets under dl.stagingDir, then Truncates to =ybegin FileSize.
//
// Claude 2026-09-11: skip segments recorded in .sakms-resume.json (Phase 2).
// Reason: RelaunchNZB after restart re-fetched every segment; ARR-parity needs durable skip of completed MsgIDs while keeping the same nzb-* dir.
// Troubleshooting: journal "resuming — N segment(s)"; force-full clears sidecar.
// Review if: PAR2 repair requires invalidating specific MsgIDs after a bad write.
//
// Claude 2026-09-18: write at yEnc begin offsets; Truncate to =ybegin FileSize.
// Reason: stride posters (e.g. 768000) decode fewer bytes than the begin step;
//
//	packing by decoded length truncated ~1.5KB short of PAR2 FileDesc.
//	NZBGet DirectWrite uses WriteAt(yEnc Offset) and sizes to FileSize;
//	the 14–40 byte windows between parts are expected (not RAR damage).
//
// Troubleshooting: PAR2 "not repairable" with damage ≈ segment-boundary count
//
//	after a packed (v2) assemble; v1/v2 sidecars are wiped (resume v3).
//
// Review if: a poster emits overlapping yEnc ranges that need a different merge.
// Related: docs/usenet-contiguous-assembly.md; resume.go resumeSchemaVersion; NZBGet DirectWrite.
//
// Claude 2026-09-18: ordered pipeline write (concurrent fetch, serial commit).
// Reason: the previous "fetch-all into got[], then write" held an entire multi-GB
//
//	file in RAM and only markSegment'd at the end — restart lost mid-file progress
//	and drove sakms RSS to multi-GB. Fetchers take a slot before BODY; the writer
//	frees it after WriteAt+markSegment so peak buffered bodies ≤ maxConc.
//
// Troubleshooting: high eth0 RX with flat staging size mid-file → expected until
//
//	the ordered writer commits; after this change staging should grow steadily.
//
// Review if: markSegment persist rate needs batching for very large NZBs.
//
// Claude 2026-09-18: uniquify output names via usedNames (see downloadAll).
// Reason: same yEnc name across NZB files must not share one staging path/resume key.
// Troubleshooting: PAR2 not repairable after "complete" obfuscated RAR set.
// Review if: uniqueOutputName scheme (.partNNN) conflicts with a poster convention.
func (m *Manager) assembleFile(ctx context.Context, gid string, dl *dlState, nzbFile NZBFile, maxConc int, usedNames map[string]struct{}) (string, error) {
	if dl.gate != nil {
		if err := dl.gate.Wait(ctx); err != nil {
			return "", err
		}
	}
	if len(nzbFile.Segs) == 0 {
		return "", fmt.Errorf("no segments")
	}
	if maxConc < 1 {
		maxConc = 1
	}
	if usedNames == nil {
		usedNames = map[string]struct{}{}
	}

	segs := make([]NZBSegment, len(nzbFile.Segs))
	copy(segs, nzbFile.Segs)
	sort.Slice(segs, func(i, j int) bool { return segs[i].Number < segs[j].Number })

	type fetched struct {
		data     []byte
		offset   int64
		partSize int64
		fileSize int64
	}

	resume := dl.resume
	firstMsg := strings.TrimSpace(segs[0].MsgID)
	var filename string
	var priorDone int
	var yencSize int64
	if resume != nil {
		filename, yencSize, priorDone = resume.priorFile(firstMsg)
	}
	if filename != "" {
		// Resume hit — claim so a later NZB file cannot reuse this path.
		usedNames[filename] = struct{}{}
	}

	// Seed filename (+ optional first body) before open so the output path exists.
	var seed fetched
	var seedNum int
	if filename == "" || resume == nil || resume.disabled || !resume.hasSegment(filename, firstMsg) {
		first, err := m.fetchSegmentAny(ctx, segs[0].MsgID)
		if err != nil {
			return "", fmt.Errorf("segment 1: %w", err)
		}
		if filename == "" {
			filename = uniqueOutputName(preferredOutputName(first.filename, nzbFile.Subject), usedNames)
		}
		seed = fetched{data: first.data, offset: first.offset, partSize: first.partSize, fileSize: first.fileSize}
		seedNum = segs[0].Number
	}

	outPath := filepath.Join(dl.stagingDir, filename)
	flags := os.O_RDWR | os.O_CREATE
	if priorDone == 0 {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(outPath, flags, 0o644)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", outPath, err)
	}
	defer f.Close()

	fileBytes := int64(0)
	if fi, statErr := f.Stat(); statErr == nil {
		fileBytes = fi.Size()
	}

	// Hollow-file guard: sidecar said done but on-disk range is empty → re-fetch.
	if seed.data == nil {
		if resume == nil || resume.disabled || !resume.segmentCovered(f, filename, firstMsg, fileBytes) {
			first, err := m.fetchSegmentAny(ctx, segs[0].MsgID)
			if err != nil {
				return "", fmt.Errorf("segment 1: %w", err)
			}
			seed = fetched{data: first.data, offset: first.offset, partSize: first.partSize, fileSize: first.fileSize}
			seedNum = segs[0].Number
		}
	}

	covered := make([]bool, len(segs))
	for i, seg := range segs {
		msgID := strings.TrimSpace(seg.MsgID)
		if seed.data != nil && seg.Number == seedNum {
			covered[i] = false
			continue
		}
		if resume != nil && !resume.disabled && resume.segmentCovered(f, filename, msgID, fileBytes) {
			covered[i] = true
		}
	}

	got := make(map[int]fetched)
	var gotMu sync.Mutex
	gotCond := sync.NewCond(&gotMu)
	// nextWrite is the index into segs the ordered writer is waiting on.
	// Fetchers may only run for indices in [nextWrite, nextWrite+maxConc).
	nextWrite := 0

	fetchCtx, fetchCancel := context.WithCancel(ctx)
	defer fetchCancel()
	g, gctx := errgroup.WithContext(fetchCtx)
	doneWake := make(chan struct{})
	go func() {
		select {
		case <-gctx.Done():
			gotCond.Broadcast()
		case <-doneWake:
		}
	}()
	defer close(doneWake)

	// Seed first body into the pipeline.
	if seed.data != nil {
		gotMu.Lock()
		got[seedNum] = seed
		gotMu.Unlock()
	}

	// No errgroup.SetLimit: a limit here lets high-index workers hold worker
	// slots while blocked on the window, starving the indices the writer needs.
	// The sliding window alone caps concurrent BODY fetches at maxConc.
	for i, seg := range segs {
		if covered[i] {
			continue
		}
		if seed.data != nil && seg.Number == seedNum {
			continue
		}
		seg := seg
		idx := i
		msgID := strings.TrimSpace(seg.MsgID)
		g.Go(func() error {
			gotMu.Lock()
			for idx >= nextWrite+maxConc && gctx.Err() == nil {
				gotCond.Wait()
			}
			if gctx.Err() != nil {
				gotMu.Unlock()
				return gctx.Err()
			}
			gotMu.Unlock()

			if dl.gate != nil {
				if err := dl.gate.Wait(gctx); err != nil {
					return err
				}
			}
			res, err := m.fetchSegmentAny(gctx, msgID)
			if err != nil {
				return fmt.Errorf("segment %d: %w", seg.Number, err)
			}
			gotMu.Lock()
			got[seg.Number] = fetched{
				data:     res.data,
				offset:   res.offset,
				partSize: res.partSize,
				fileSize: res.fileSize,
			}
			gotMu.Unlock()
			gotCond.Broadcast()
			return nil
		})
	}

	var writeErr error
	var maxEnd int64
	for i, seg := range segs {
		msgID := strings.TrimSpace(seg.MsgID)
		if covered[i] {
			n := 0
			var off int64
			if resume != nil {
				n = resume.skippedBytes(filename, msgID, int(seg.Bytes))
				off = resume.skippedOffset(filename, msgID)
			}
			if n <= 0 {
				writeErr = fmt.Errorf("segment %d: missing decoded length for resume skip", seg.Number)
				break
			}
			m.addCompleted(gid, int64(n))
			if end := off + int64(n); end > maxEnd {
				maxEnd = end
			}
			gotMu.Lock()
			nextWrite = i + 1
			gotMu.Unlock()
			gotCond.Broadcast()
			continue
		}

		gotMu.Lock()
		for {
			if _, ok := got[seg.Number]; ok {
				break
			}
			if gctx.Err() != nil {
				gotMu.Unlock()
				writeErr = fmt.Errorf("segment %d: %w", seg.Number, gctx.Err())
				break
			}
			gotCond.Wait()
		}
		if writeErr != nil {
			break
		}
		item := got[seg.Number]
		delete(got, seg.Number)
		nextWrite = i + 1
		gotMu.Unlock()
		gotCond.Broadcast()

		if item.fileSize > yencSize {
			yencSize = item.fileSize
		}
		if _, err := f.WriteAt(item.data, item.offset); err != nil {
			writeErr = fmt.Errorf("writing segment %d: %w", seg.Number, err)
			break
		}
		m.addCompleted(gid, int64(len(item.data)))
		if resume != nil {
			if err := resume.markSegment(filename, msgID, seg.Number, item.offset, len(item.data), item.fileSize); err != nil {
				log.Printf("usenet: resume persist %s seg %d: %v", gid, seg.Number, err)
			}
		}
		if end := item.offset + int64(len(item.data)); end > maxEnd {
			maxEnd = end
		}
	}

	if writeErr != nil {
		fetchCancel()
		gotCond.Broadcast()
	}
	fetchErr := g.Wait()
	// Claude 2026-09-18: prefer real fetch failure over writer cancel mask.
	// Reason: errgroup cancels siblings on the first fetch error; the ordered
	//   writer then surfaces segment N: context.Canceled and previously won
	//   over fetchErr — UI/logs showed only "context canceled".
	// Troubleshooting: "segment N: context canceled" with no underlying cause.
	// Review if: errgroup is replaced with a model that preserves primary errors.
	if writeErr != nil {
		if fetchErr != nil && errors.Is(writeErr, context.Canceled) && !errors.Is(fetchErr, context.Canceled) {
			return "", fetchErr
		}
		return "", writeErr
	}
	if fetchErr != nil {
		return "", fetchErr
	}

	finalSize := maxEnd
	if yencSize > finalSize {
		finalSize = yencSize
	}
	if err := f.Truncate(finalSize); err != nil {
		return "", fmt.Errorf("truncating %s to yEnc size %d: %w", outPath, finalSize, err)
	}
	if resume != nil {
		resume.setFileSize(filename, finalSize)
	}
	return outPath, nil
}

// ErrNoSubscriptions is returned when a retrieval is attempted with no Usenet
// subscription configured.
var ErrNoSubscriptions = errors.New("usenet: no Usenet subscriptions are configured")

// fetchSegmentAny retrieves one article, falling back across the configured
// subscriptions until one of them has it.
//
// Fallback is per-SEGMENT and SEQUENTIAL. Per-segment (rather than picking one
// server for a whole download) is the only granularity that copes with partial
// retention across providers, which is the entire reason an operator configures
// more than one. Sequential (rather than probing every server at once) avoids
// downloading the same article body N times — N times the bandwidth and N times
// the per-provider connection consumption, for one usable copy.
//
// Each pool is tried up to maxSegmentAttemptsPerServer times before moving on.
// A transport failure (dropped socket, broken pipe) retries on a fresh connection
// because pool.put(conn, false) discards the bad socket and releases its live
// token, making a new dial possible. Article-level responses (430/451) are not
// retried within the same pool — only connection-level failures are.
//
// Error precedence when every pool fails:
//   - every pool answered 430          -> ErrArticleNotFound (retryable)
//   - only 430/451, at least one 451   -> ErrArticleRemoved (permanent)
//   - anything else (dial, decode, …)  -> classifySegmentFailure(otherErr),
//     which wraps with ErrTransport when the failure is connection-level
//
// A classification mistake costs a retry rather than a lost download, but only
// because of what happens downstream: ErrArticleRemoved is the ONLY error api's
// classifyDownloadState treats as terminal. An unclassified error returned here
// is retried, not failed — so returning the raw transport error (rather than
// guessing 430 vs. 451) is the safe answer, including in the mixed case where
// one provider answered 451 and another was simply unreachable.
//
// Claude 2026-09-17: ctx + per-server retry loop for transport failures.
// Reason: stale pooled connections failed whole NZBs on broken pipe; reconnect
//
//	(pool.put false + new get) recovers most segments.
//
// Claude 2026-09-18: invalidateIdle() on transport error — get() prefers idle,
//
//	so retries can exhaust maxSegmentAttemptsPerServer on stale siblings when
//	MaxConns exceeds that limit, never dialing fresh.
//
// Review if: maxSegmentAttemptsPerServer is exposed as a settings knob.
func (m *Manager) fetchSegmentAny(ctx context.Context, msgID string) (segmentResult, error) {
	pools := m.currentPools()
	if len(pools) == 0 {
		return segmentResult{}, ErrNoSubscriptions
	}

	allNotFound := true
	sawRemoved := false
	var otherErr error

	for _, p := range pools {
		for attempt := 1; attempt <= maxSegmentAttemptsPerServer; attempt++ {
			if ctx.Err() != nil {
				// Context cancelled — propagate immediately without wrapping as transport.
				return segmentResult{}, ctx.Err()
			}
			conn, err := p.getCtx(ctx)
			if err != nil {
				// Dial/auth failure for this server. If it looks like a transport
				// issue and we have retries left, wait and try again; otherwise
				// move to the next pool.
				if isTransportError(err) && attempt < maxSegmentAttemptsPerServer && ctx.Err() == nil {
					sleepWithCtx(ctx, transportRetryDelay(attempt))
					continue
				}
				allNotFound = false
				if otherErr == nil {
					otherErr = err
				}
				break // next pool
			}
			res, ferr := fetchSegment(conn, msgID)
			p.put(conn, ferr == nil)
			if ferr == nil {
				if attempt > 1 {
					log.Printf("usenet: segment recovered after %d attempt(s) on %s", attempt, p.cfg.Host)
				}
				// Account bytes against the shared global cap after a successful
				// BODY so torrent + Usenet share one Mbps budget.
				m.mu.Lock()
				cap := m.rateCap
				m.mu.Unlock()
				if err := cap.WaitN(ctx, len(res.data)); err != nil {
					return segmentResult{}, err
				}
				return res, nil
			}
			switch {
			case errors.Is(ferr, ErrArticleNotFound):
				// This provider does not carry it; try the next pool.
			case errors.Is(ferr, ErrArticleRemoved):
				allNotFound = false
				sawRemoved = true
			default:
				// Transport or decode failure. Retry on a fresh connection if
				// we have attempts left and the context is still live.
				if isTransportError(ferr) {
					p.invalidateIdle()
					if attempt < maxSegmentAttemptsPerServer && ctx.Err() == nil {
						sleepWithCtx(ctx, transportRetryDelay(attempt))
						continue
					}
				}
				allNotFound = false
				if otherErr == nil {
					otherErr = ferr
				}
			}
			break // move to next pool after any non-retry-eligible outcome
		}
	}

	switch {
	case allNotFound:
		return segmentResult{}, ErrArticleNotFound
	case otherErr != nil:
		return segmentResult{}, classifySegmentFailure(otherErr)
	case sawRemoved:
		return segmentResult{}, ErrArticleRemoved
	default:
		return segmentResult{}, ErrArticleNotFound
	}
}

// sleepWithCtx sleeps for d, returning early if ctx is cancelled.
func sleepWithCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// addCompleted adds n to dl.completed under Manager.mu.
func (m *Manager) addCompleted(gid string, n int64) {
	m.mu.Lock()
	if dl, ok := m.downloads[gid]; ok {
		dl.completed += n
	}
	m.mu.Unlock()
}

// setPhaseProgress records repairing/unpacking work-unit progress under m.mu
// so snapshot/SSE emit the new counts. done is clamped to total when total > 0.
func (m *Manager) setPhaseProgress(gid string, done, total int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dl, ok := m.downloads[gid]
	if !ok {
		return
	}
	if total < 0 {
		total = 0
	}
	if done < 0 {
		done = 0
	}
	if total > 0 && done > total {
		done = total
	}
	dl.phaseDone = done
	dl.phaseTotal = total
}

func reportPhaseProgress(onProgress func(done, total int64), done, total int64) {
	if onProgress == nil {
		return
	}
	if total < 0 {
		total = 0
	}
	if done < 0 {
		done = 0
	}
	if total > 0 && done > total {
		done = total
	}
	onProgress(done, total)
}

func (m *Manager) snapshot() []Download {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := make([]Download, 0, len(m.downloads))
	for _, dl := range m.downloads {
		var speed int64
		if dl.status == "active" && !dl.prevTime.IsZero() {
			if dt := now.Sub(dl.prevTime).Seconds(); dt > 0 {
				if delta := dl.completed - dl.prevBytes; delta > 0 {
					speed = int64(float64(delta) / dt)
				}
			}
		}
		dl.prevBytes = dl.completed
		dl.prevTime = now
		dl.speed = speed

		out = append(out, Download{
			GID:             dl.gid,
			Status:          dl.status,
			Filename:        dl.name,
			Dir:             dl.stagingDir,
			TotalLength:     dl.total,
			CompletedLength: dl.completed,
			ResumeMode:      dl.resumeMode,
			Phase:           dl.phase,
			PhaseDone:       dl.phaseDone,
			PhaseTotal:      dl.phaseTotal,
			PhaseStartedAt:  dl.phaseStartedAt,
			AddedAt:         dl.addedAt,
			DownloadSpeed:   speed,
			Files:           dl.files,
			ErrorMessage:    dl.errorMsg,
			Err:             dl.err,
		})
	}
	// Claude 2026-09-20: stable oldest-first order for Downloads SSE/List.
	// Reason: ranging m.downloads (a map) reshuffled the UI on every progress tick.
	// Troubleshooting: Downloads rows jumping; sort by AddedAt then GID.
	// Review if: operator-configurable sort lands.
	sort.SliceStable(out, func(i, j int) bool {
		if c := out[i].AddedAt.Compare(out[j].AddedAt); c != 0 {
			return c < 0
		}
		return out[i].GID < out[j].GID
	})
	return out
}

func (m *Manager) fanout(snap []Download) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.subscribers {
		select {
		case ch <- snap:
		default:
			// Drop stale pending snapshot (latest-wins), then try again.
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

type snapKey struct {
	status         string
	completed      int64
	resumeMode     string
	phase          string
	phaseDone      int64
	phaseTotal     int64
	phaseStartedAt time.Time
}

func sameDownloads(a, b []Download) bool {
	if len(a) != len(b) {
		return false
	}
	ka := make(map[string]snapKey, len(a))
	for _, d := range a {
		ka[d.GID] = snapKey{d.Status, d.CompletedLength, d.ResumeMode, d.Phase, d.PhaseDone, d.PhaseTotal, d.PhaseStartedAt}
	}
	kb := make(map[string]snapKey, len(b))
	for _, d := range b {
		kb[d.GID] = snapKey{d.Status, d.CompletedLength, d.ResumeMode, d.Phase, d.PhaseDone, d.PhaseTotal, d.PhaseStartedAt}
	}
	return reflect.DeepEqual(ka, kb)
}

// sanitizeName strips path separators and null bytes so a release name or
// yEnc filename can be used safely as a filesystem path component.
func sanitizeName(s string) string {
	return strings.NewReplacer("/", "_", "\\", "_", "\x00", "_").Replace(s)
}

// knownOutputExt reports whether name ends with a media/par2 extension the
// importer and PAR2 repair path recognize.
func knownOutputExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".mkv", ".mp4", ".avi", ".m4v", ".wmv", ".mov", ".ts", ".m2ts",
		".mpg", ".mpeg", ".iso", ".img", ".vob",
		".rar", ".zip", ".7z",
		".par2", ".nfo", ".srt", ".sub", ".idx", ".ass", ".ssa":
		return true
	default:
		return false
	}
}

// filenameFromSubject extracts the quoted base name from a typical NZB subject
// like `[9/9] "Show.S01E01.mkv" yEnc (1/100)`. Empty when no quoted token is
// present. NZBGet's subject-filename path does the same for posters that put a
// useless hash in the yEnc =ybegin name.
func filenameFromSubject(subject string) string {
	start := strings.Index(subject, "\"")
	if start < 0 {
		return ""
	}
	rest := subject[start+1:]
	end := strings.Index(rest, "\"")
	if end <= 0 {
		return ""
	}
	return sanitizeName(rest[:end])
}

// preferredOutputName chooses the on-disk name for an assembled NZB file.
//
// Claude 2026-09-01: fall back to the NZB subject when yEnc name lacks an ext.
// Reason: some posters put a bare hex hash in =ybegin; import then fails with
//
//	"no video files found" despite a complete Matroska sitting in staging.
//
// Troubleshooting: usenet download status=complete but import finds no video.
// Review if: import gains content-sniffing for extensionless files.
// Related: NZBGet subject-filename handling / NzbLog diagnostics.
//
// Claude 2026-09-18: prefer subject when yEnc is an obfuscated hex+.par2 hash.
// Reason: RiPER-style posts put the SAME hash.par2 in =ybegin for every RAR
//
//	part while the subject quotes distinct Show.part05.rar names. Using yEnc
//	first made uniqueOutputName invent hash.part002.par2 names that unrar
//	cannot join as a multi-volume set.
//
// Troubleshooting: staging has hash.part00N.rar instead of Show.partNN.rar.
// Review if: a poster uses hex yEnc names that must win over a bad subject.
func preferredOutputName(yencName, subject string) string {
	yencName = sanitizeName(yencName)
	fromSub := filenameFromSubject(subject)
	if fromSub != "" && knownOutputExt(fromSub) && yencLooksObfuscated(yencName) {
		return fromSub
	}
	if yencName != "" && knownOutputExt(yencName) {
		return yencName
	}
	if fromSub != "" && knownOutputExt(fromSub) {
		return fromSub
	}
	if yencName != "" {
		return yencName
	}
	if fromSub != "" {
		return fromSub
	}
	return sanitizeName(subject)
}

// yencLooksObfuscated reports a bare hex stem (32+ chars), with or without a
// known output extension — the usual obfuscated =ybegin pattern.
func yencLooksObfuscated(name string) bool {
	name = sanitizeName(name)
	if name == "" {
		return false
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if stem == "" {
		stem = name
	}
	if len(stem) < 32 {
		return false
	}
	for _, c := range stem {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// uniqueOutputName claims base in used (or base.part002, .part003, …) so each
// NZB file gets a distinct staging path when posters reuse one yEnc filename.
func uniqueOutputName(base string, used map[string]struct{}) string {
	if used == nil {
		used = map[string]struct{}{}
	}
	base = sanitizeName(base)
	if base == "" {
		base = "file.bin"
	}
	if _, taken := used[base]; !taken {
		used[base] = struct{}{}
		return base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s.part%03d%s", stem, i, ext)
		if _, taken := used[cand]; !taken {
			used[cand] = struct{}{}
			return cand
		}
	}
}

// Claude 2026-09-15: obfuscated payloads often keep a .par2 subject name while
// the body is Matroska/MP4/RAR. Magic-sniff before PAR2 so we do not fail-closed
// on a healthy video that only looks like a repair set by extension.
// Reason: Ancient Aliens E2E assembled a complete MKV named *.vol-01.par2;
//   par2.Parse returned "no main packet" and the download was marked error.
// Troubleshooting: journal "par2: skipping non-PAR2"; file(1) shows Matroska.
// Review if: go-newsgroups/par2 gains content-type sniffing of its own.
// Related: preferredOutputName; ResolveVideoFile extension gate.

// verifyAndRepair runs PAR2 verification and repair on assembled files.
// Files whose names end in .par2 but whose contents are not PAR2 packets are
// reclassified (and renamed when a video/archive magic matches) so obfuscated
// releases are not rejected. If no real .par2 sets remain, files is returned
// unchanged with a nil error. A non-nil error means verify/repair failed; the
// caller may still unpack (contiguous assemblies are often short of FileDesc
// length while the RAR payload is intact).
// Review if: go-newsgroups/par2 treats FileDesc length mismatch as trim/pad.
func verifyAndRepair(dir string, files []string, onProgress func(done, total int64)) ([]string, error) {
	files = normalizeObfuscatedPar2Names(files)

	var par2Paths, dataPaths []string
	for _, p := range files {
		if isPar2Payload(p) {
			par2Paths = append(par2Paths, p)
		} else {
			dataPaths = append(dataPaths, p)
		}
	}
	if len(par2Paths) == 0 {
		return files, nil
	}

	// Claude 2026-09-21: work-unit total known after path lists.
	// Reason: Downloads postprocess bar needs a stable denominator. Units are
	//   each PAR2 read + each data read + 1 verify + worst-case rewrite of all data.
	// Troubleshooting: percent stuck at 0 until first ReadFile callback.
	// Review if: par2 library gains streaming progress of its own.
	total := int64(len(par2Paths) + len(dataPaths) + 1 + len(dataPaths))
	var done int64
	step := func() {
		done++
		reportPhaseProgress(onProgress, done, total)
	}

	blobs := make([][]byte, 0, len(par2Paths))
	for _, p := range par2Paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return files, fmt.Errorf("par2: reading %s: %w", p, err)
		}
		blobs = append(blobs, data)
		step()
	}

	rs, err := par2lib.Parse(blobs...)
	if err != nil {
		return files, fmt.Errorf("par2: parse: %w", err)
	}

	// Known limitation: all data files are loaded into memory for par2 verify/repair.
	// For multi-GB releases this is a large allocation. Par2 repair is best-effort and
	// non-fatal on failure, so an OOM here degrades gracefully. A future improvement
	// would use the par2 library's streaming/file-handle API if available.
	fileMap := make(map[string][]byte, len(dataPaths))
	for _, p := range dataPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			return files, fmt.Errorf("par2: reading data file %s: %w", p, err)
		}
		fileMap[filepath.Base(p)] = data
		step()
	}

	result, err := rs.Verify(fileMap)
	if err != nil {
		return files, fmt.Errorf("par2: verify: %w", err)
	}
	step()
	if result.Complete {
		return files, nil
	}
	if !result.Repairable {
		return files, fmt.Errorf("par2: not repairable (%d damaged/missing slices exceed available recovery blocks)", countDamaged(result))
	}

	repaired, err := rs.Repair(fileMap)
	if err != nil {
		return files, fmt.Errorf("par2: repair: %w", err)
	}
	for name, data := range repaired {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return files, fmt.Errorf("par2: writing repaired %s: %w", name, err)
		}
		step()
	}
	return files, nil
}

// isPar2Payload reports whether path is a real PAR2 set (extension + magic).
func isPar2Payload(path string) bool {
	if !strings.HasSuffix(strings.ToLower(path), ".par2") {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 4)
	n, err := f.Read(head)
	if err != nil || n < 4 {
		return false
	}
	return string(head) == "PAR2"
}

// normalizeObfuscatedPar2Names renames .par2 files whose payload magic is a
// known video/archive type to a matching extension so import can see them.
//
// Claude 2026-09-15: dedupe + remap after rename
// Reason: assembleFile can emit the same outPath multiple times; after the
//
//	first rename, later copies hit a gone .par2 and logged "skipping non-PAR2"
//	~N times. Stale paths left in the slice also poison verifyAndRepair's
//	dataPaths ReadFile when a real PAR2 set exists.
//
// Troubleshooting: journal spam "skipping non-PAR2"; par2: reading data file …
// Claude 2026-09-18: assembleFile now uniquifies colliding yEnc names (.partNNN);
//
//	keep dedupe here for resume/legacy staging that still lists duplicates.
func normalizeObfuscatedPar2Names(files []string) []string {
	renamed := make(map[string]string)
	emitted := make(map[string]bool)
	out := make([]string, 0, len(files))
	emit := func(p string) {
		if emitted[p] {
			return
		}
		emitted[p] = true
		out = append(out, p)
	}
	// adopt re-points a gone .par2 at whatever a prior pass renamed it to.
	adopt := func(p string) bool {
		dest := findRenamedObfuscatedSibling(p)
		if dest == "" {
			return false
		}
		renamed[p] = dest
		emit(dest)
		return true
	}
	for _, p := range files {
		if dest, ok := renamed[p]; ok {
			emit(dest)
			continue
		}
		if !strings.HasSuffix(strings.ToLower(p), ".par2") || isPar2Payload(p) {
			if _, err := os.Stat(p); err != nil {
				continue // gone / unreadable — drop rather than poison dataPaths
			}
			emit(p)
			continue
		}
		if _, err := os.Stat(p); os.IsNotExist(err) {
			adopt(p)
			continue
		}
		ext := sniffMediaExt(p)
		if ext == "" {
			log.Printf("usenet: par2: skipping non-PAR2 payload %s (leaving name unchanged)", filepath.Base(p))
			emit(p)
			continue
		}
		newPath := strings.TrimSuffix(p, filepath.Ext(p)) + ext
		if _, err := os.Stat(newPath); err == nil {
			// Source is still present too — refuse to clobber the target.
			log.Printf("usenet: par2: rename obfuscated %s → %s: target exists", filepath.Base(p), filepath.Base(newPath))
			emit(p)
			continue
		}
		if err := os.Rename(p, newPath); err != nil {
			if os.IsNotExist(err) && adopt(p) {
				continue
			}
			log.Printf("usenet: par2: rename obfuscated %s → %s: %v", filepath.Base(p), filepath.Base(newPath), err)
			if _, srcErr := os.Stat(p); srcErr == nil {
				emit(p)
			}
			continue
		}
		log.Printf("usenet: par2: renamed obfuscated payload %s → %s", filepath.Base(p), filepath.Base(newPath))
		renamed[p] = newPath
		emit(newPath)
	}
	return out
}

// findRenamedObfuscatedSibling returns an existing sibling path for a gone
// .par2 that was renamed to a sniffed media/archive extension. The extension
// list must stay the same set sniffMediaExt can return.
func findRenamedObfuscatedSibling(par2Path string) string {
	base := strings.TrimSuffix(par2Path, filepath.Ext(par2Path))
	for _, ext := range []string{".mkv", ".mp4", ".rar", ".zip"} {
		cand := base + ext
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return ""
}

// sniffMediaExt returns a file extension for common payload magics, or "" when
// unrecognized (caller leaves the name alone).
func sniffMediaExt(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 12)
	n, err := f.Read(head)
	if err != nil || n < 4 {
		return ""
	}
	switch {
	case head[0] == 0x1A && head[1] == 0x45 && head[2] == 0xDF && head[3] == 0xA3:
		return ".mkv"
	case n >= 8 && string(head[4:8]) == "ftyp":
		return ".mp4"
	case string(head[:4]) == "Rar!":
		return ".rar"
	case n >= 2 && string(head[:2]) == "PK":
		return ".zip"
	default:
		return ""
	}
}

func countDamaged(r *par2lib.VerifyResult) int {
	n := 0
	for _, f := range r.Files {
		n += len(f.MissingSlices)
	}
	return n
}
