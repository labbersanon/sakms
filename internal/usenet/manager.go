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
	"golang.org/x/sync/errgroup"
)

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
	// Err is the unflattened retrieval failure, Go-side only — it is never
	// serialised (the api layer maps this struct field-by-field into
	// apidto.Download, which carries ErrorMessage for the UI). Callers use
	// errors.Is to tell a permanent ErrArticleRemoved (451 DMCA takedown, never
	// retry) apart from a retryable ErrArticleNotFound (430 from every
	// subscription) or a transient dial failure. Nil unless Status == "error".
	Err error
}

// dlState is the mutable runtime state of one usenet download. All fields
// except the atomics are protected by Manager.mu.
type dlState struct {
	gid        string
	name       string
	stagingDir string
	resume     *resumeTracker // segment-resume sidecar writer; nil when disabled
	status     string
	errorMsg   string
	err        error // classified retrieval failure; surfaced as Download.Err
	files      []string
	cancel     context.CancelFunc

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
	startCtx   context.Context // set by Start under mu; nil until then

	// Claude 2026-09-11: segment resume policy (Phase 2 ARR-parity)
	// Reason: after restart RelaunchNZB must skip durable completed segments
	// Troubleshooting: settings usenet_segment_resume_enabled / _force_full
	segmentResume     bool
	forceFullDownload bool
	resumeMirror      ResumeMirror

	mu          sync.Mutex
	pools       []*pool // one per enabled subscription; swapped by SetSubscriptions
	downloads   map[string]*dlState
	subscribers map[int]chan []Download
	nextSubID   int
	// Claude 2026-09-01: job semaphore is MaxConcurrentDownloads, not Σ MaxConns.
	// Reason: operators need "how many NZBs at once" separate from per-server
	//   NNTP sockets; PAR2/unpack must not hold a download slot.
	// Troubleshooting: many NZBs each opening MaxConns and saturating the
	//   provider; Downloads stuck while one job repairs.
	// Review if: a separate repair-concurrency knob is added.
	// Related: SetMaxConcurrentDownloads; runDownload releases before PAR2.
	maxConcurrentDownloads int
	semaphore              chan struct{}
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
	// SegmentResume enables skip-completed-segment behavior using .sakms-resume.json.
	// Default false when unset by callers that predate Phase 2.
	SegmentResume bool
	// ForceFullDownload ignores/clears any resume sidecar (operator rollback).
	ForceFullDownload bool
	// ResumeMirror optionally persists a copy of the sidecar for UI/debug.
	ResumeMirror ResumeMirror
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
		resumeMirror:           cfg.ResumeMirror,
		downloads:              map[string]*dlState{},
		subscribers:            map[int]chan []Download{},
		maxConcurrentDownloads: maxDL,
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

// StagingDir returns the directory where assembled NZB files are written.
func (m *Manager) StagingDir() string { return m.stagingDir }

// SetResumePolicy updates segment-resume knobs at runtime (settings toggle /
// operator rollback without rebuilding the Manager).
func (m *Manager) SetResumePolicy(enabled, forceFull bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.segmentResume = enabled
	m.forceFullDownload = forceFull
}

// ResumePolicy returns the current (enabled, forceFull) resume knobs.
func (m *Manager) ResumePolicy() (enabled, forceFull bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.segmentResume, m.forceFullDownload
}

// ClearResumeMirror drops the optional DB resume row for gid after a successful
// import (or any other path that no longer needs the mirror). Staging sidecar
// cleanup is handled by RemoveOwnedStagingDir / ClearResumeArtifacts.
func (m *Manager) ClearResumeMirror(gid string) {
	if m == nil || gid == "" {
		return
	}
	m.mu.Lock()
	mirror := m.resumeMirror
	m.mu.Unlock()
	if mirror == nil {
		return
	}
	if err := mirror.ClearResume(gid); err != nil {
		log.Printf("usenet: clear resume mirror %s: %v", gid, err)
	}
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
	if name == "" {
		name = "usenet-download"
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
		total:      totalBytes,
		cancel:     cancel,
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
// Review if: true segment-resume lands (phase 2) and replaces this re-fetch
// Related: internal/api/downloadreconcile.go
//
// RelaunchNZB re-fetches the NZB at url and starts a download into the existing
// staging directory named gid. It is NOT true segment resume — phase 1 of the
// built-in-downloader ARR-parity plan: keep the same owned nzb-* tree (and any
// already-assembled video) instead of allocateStaging's fresh GID. Returns nil
// when gid is already in flight.
func (m *Manager) RelaunchNZB(ctx context.Context, gid, url, name string) error {
	if !IsOwnedStagingName(gid) {
		return fmt.Errorf("usenet: refusing relaunch into non-owned gid %q", gid)
	}
	if existing, err := m.FindByGID(gid); err != nil {
		return err
	} else if existing != nil {
		return nil
	}

	nzb, dnzb, err := fetchNZB(m.httpClient, url)
	if err != nil {
		return err
	}
	if name == "" {
		name = dnzb.Name
	}
	if name == "" {
		name = "usenet-download"
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
		total:      totalBytes,
		cancel:     cancel,
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

// Pause stops an active download by cancelling its context. The entry remains
// visible in List/FindByGID with status "paused". Re-submit via AddNZB to retry.
func (m *Manager) Pause(gid string) error {
	m.mu.Lock()
	dl, ok := m.downloads[gid]
	if ok {
		dl.status = "paused"
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("usenet: download not found: %s", gid)
	}
	dl.cancel()
	return nil
}

// Resume is not supported for usenet downloads in this implementation.
// Re-submit the NZB via AddNZB to restart.
func (m *Manager) Resume(_ string) error {
	return fmt.Errorf("usenet: resume is not supported; re-submit the NZB to restart")
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
		cancel: func() {},
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
	mirror := m.resumeMirror
	m.mu.Unlock()
	if forceFull {
		if err := ClearResumeArtifacts(dl.stagingDir); err != nil {
			log.Printf("usenet: clearing resume artifacts for %s: %v", gid, err)
		}
		if mirror != nil {
			_ = mirror.ClearResume(gid)
		}
		log.Printf("usenet: download %s (%s) full restart (force-full / rollback)", gid, dl.name)
	}
	dl.resume = loadResumeTracker(dl.stagingDir, gid, mirror, !resumeOn || forceFull)
	if resumeOn && !forceFull {
		if skipped := dl.resume.skippedSegments(); skipped > 0 {
			log.Printf("usenet: download %s (%s) resuming — %d segment(s) already complete on disk", gid, dl.name, skipped)
		} else {
			log.Printf("usenet: download %s (%s) starting (resume enabled, no prior segments)", gid, dl.name)
		}
	}

	files, err := m.downloadAll(ctx, gid, dl, nzb)
	<-sem
	released = true
	if err != nil {
		m.mu.Lock()
		if dl.status != "removed" && dl.status != "paused" {
			dl.status = "error"
			dl.errorMsg = err.Error()
			// Keep the wrapped error itself, not just its text, so a caller can
			// errors.Is a permanent ErrArticleRemoved apart from everything
			// else. Only ErrArticleRemoved is terminal downstream: api's
			// classifyDownloadState treats an ErrArticleNotFound AND any
			// unclassified error (a dial or decode failure) alike as
			// retryable, since neither proves the article is really gone.
			dl.err = err
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
		return
	}

	// Optional PAR2 verify + best-effort repair. Failure is non-fatal: the
	// download is marked complete with whatever files landed (the repair is
	// unvalidated against real-world par2cmdline output — see research notes).
	// Job slot already released above so another NZB can download meanwhile.
	repaired, repairErr := verifyAndRepair(dl.stagingDir, files)
	if repairErr != nil {
		log.Printf("usenet: par2 repair %s: %v (marking complete with unrepaired files)", gid, repairErr)
	} else {
		files = repaired
	}

	// Claude 2026-09-03: unpack rar/zip/7z after PAR2, before import.
	// Reason: most Usenet releases are multi-part RAR; import only resolves
	//   flat videos in the GID staging dir (non-recursive).
	// Troubleshooting: staging full of .partNN.rar, "no video files found".
	// Review if: password-protected archives need a setting.
	// Related: unpack.go; Dockerfile unrar + p7zip-full.
	unpacked, unpackErr := unpackArchives(dl.stagingDir, files)
	if unpackErr != nil {
		log.Printf("usenet: unpack %s: %v (marking complete with packed files)", gid, unpackErr)
	} else {
		files = unpacked
	}

	m.mu.Lock()
	if dl.status != "removed" && dl.status != "paused" {
		dl.status = "complete"
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
	var paths []string
	for _, nzbFile := range nzb.Files {
		path, err := m.assembleFile(ctx, gid, dl, nzbFile, maxConc)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", nzbFile.Subject, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// assembleFile downloads all segments of one NZB file and writes the assembled
// output to dl.stagingDir. Segments are downloaded concurrently up to maxConc,
// written to a pre-allocated file via io.WriterAt at the offsets in yEnc metadata.
//
// Claude 2026-09-11: skip segments recorded in .sakms-resume.json (Phase 2).
// Reason: RelaunchNZB after restart re-fetched every segment; ARR-parity needs
//         durable skip of completed MsgIDs while keeping the same nzb-* dir.
// Troubleshooting: journal "resuming — N segment(s)"; force-full clears sidecar.
// Review if: PAR2 repair requires invalidating specific MsgIDs after a bad write.
func (m *Manager) assembleFile(ctx context.Context, gid string, dl *dlState, nzbFile NZBFile, maxConc int) (string, error) {
	if len(nzbFile.Segs) == 0 {
		return "", fmt.Errorf("no segments")
	}

	segs := make([]NZBSegment, len(nzbFile.Segs))
	copy(segs, nzbFile.Segs)
	sort.Slice(segs, func(i, j int) bool { return segs[i].Number < segs[j].Number })

	resume := dl.resume
	firstMsg := strings.TrimSpace(segs[0].MsgID)

	filename, fileSize, priorDone := "", int64(0), 0
	if resume != nil && !resume.disabled {
		resume.mu.Lock()
		for name, rf := range resume.snap.Files {
			if rf == nil || len(rf.Done) == 0 {
				continue
			}
			if _, ok := rf.Done[firstMsg]; ok || len(resume.snap.Files) == 1 {
				filename = name
				fileSize = rf.Size
				priorDone = len(rf.Done)
				break
			}
		}
		resume.mu.Unlock()
	}

	var firstData []byte
	var firstOffset int64
	needFirst := filename == "" || resume == nil || resume.disabled || !resume.hasSegment(filename, firstMsg)
	if needFirst {
		first, err := m.fetchSegmentAny(segs[0].MsgID)
		if err != nil {
			return "", fmt.Errorf("segment 1: %w", err)
		}
		filename = preferredOutputName(first.filename, nzbFile.Subject)
		if first.fileSize > 0 {
			fileSize = first.fileSize
		}
		firstData = first.data
		firstOffset = first.offset
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

	if fileSize > 0 {
		fi, statErr := f.Stat()
		if statErr != nil || fi.Size() < fileSize {
			if err := f.Truncate(fileSize); err != nil {
				return "", fmt.Errorf("pre-allocating %s: %w", outPath, err)
			}
		}
	}

	writeSeg := func(msgID string, number int, data []byte, offset int64) error {
		if resume != nil && !resume.disabled && resume.hasSegment(filename, msgID) {
			m.addCompleted(gid, int64(len(data)))
			return nil
		}
		if _, err := f.WriteAt(data, offset); err != nil {
			return fmt.Errorf("writing segment %d: %w", number, err)
		}
		m.addCompleted(gid, int64(len(data)))
		if resume != nil {
			if err := resume.markSegment(filename, msgID, number, offset, len(data), fileSize); err != nil {
				log.Printf("usenet: resume persist %s seg %d: %v", gid, number, err)
			}
		}
		return nil
	}

	if needFirst {
		if err := writeSeg(firstMsg, segs[0].Number, firstData, firstOffset); err != nil {
			return "", err
		}
	} else if resume != nil {
		resume.mu.Lock()
		segLen := 0
		if rf := resume.snap.Files[filename]; rf != nil {
			segLen = rf.Done[firstMsg].Length
		}
		resume.mu.Unlock()
		if segLen <= 0 {
			segLen = int(segs[0].Bytes)
		}
		if segLen > 0 {
			m.addCompleted(gid, int64(segLen))
		}
	}

	if len(segs) > 1 {
		var g errgroup.Group
		g.SetLimit(maxConc)
		for _, seg := range segs[1:] {
			seg := seg
			g.Go(func() error {
				msgID := strings.TrimSpace(seg.MsgID)
				if resume != nil && !resume.disabled && resume.hasSegment(filename, msgID) {
					resume.mu.Lock()
					n := 0
					if rf := resume.snap.Files[filename]; rf != nil {
						n = rf.Done[msgID].Length
					}
					resume.mu.Unlock()
					if n <= 0 {
						n = int(seg.Bytes)
					}
					if n > 0 {
						m.addCompleted(gid, int64(n))
					}
					return nil
				}
				res, err := m.fetchSegmentAny(seg.MsgID)
				if err != nil {
					return fmt.Errorf("segment %d: %w", seg.Number, err)
				}
				return writeSeg(msgID, seg.Number, res.data, res.offset)
			})
		}
		if err := g.Wait(); err != nil {
			return "", err
		}
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
// Error precedence when every pool fails:
//   - every pool answered 430          -> ErrArticleNotFound (retryable)
//   - only 430/451, at least one 451   -> ErrArticleRemoved (permanent)
//   - anything else (dial, decode, …)  -> that error UNWRAPPED and
//     unclassified, since a server we could not reach tells us nothing about
//     whether it holds the article
//
// A classification mistake costs a retry rather than a lost download, but only
// because of what happens downstream: ErrArticleRemoved is the ONLY error api's
// classifyDownloadState treats as terminal. An unclassified error returned here
// is retried, not failed — so returning the raw transport error (rather than
// guessing 430 vs. 451) is the safe answer, including in the mixed case where
// one provider answered 451 and another was simply unreachable.
func (m *Manager) fetchSegmentAny(msgID string) (segmentResult, error) {
	pools := m.currentPools()
	if len(pools) == 0 {
		return segmentResult{}, ErrNoSubscriptions
	}

	allNotFound := true
	sawRemoved := false
	var otherErr error

	for _, p := range pools {
		conn, err := p.get()
		if err != nil {
			allNotFound = false
			if otherErr == nil {
				otherErr = err
			}
			continue
		}
		res, err := fetchSegment(conn, msgID)
		p.put(conn, err == nil)
		if err == nil {
			return res, nil
		}
		switch {
		case errors.Is(err, ErrArticleNotFound):
			// This provider does not carry it; try the next.
		case errors.Is(err, ErrArticleRemoved):
			allNotFound = false
			sawRemoved = true
		default:
			allNotFound = false
			if otherErr == nil {
				otherErr = err
			}
		}
	}

	switch {
	case allNotFound:
		return segmentResult{}, ErrArticleNotFound
	case otherErr != nil:
		return segmentResult{}, otherErr
	case sawRemoved:
		return segmentResult{}, ErrArticleRemoved
	default:
		return segmentResult{}, ErrArticleNotFound
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
			DownloadSpeed:   speed,
			Files:           dl.files,
			ErrorMessage:    dl.errorMsg,
			Err:             dl.err,
		})
	}
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
	status    string
	completed int64
}

func sameDownloads(a, b []Download) bool {
	if len(a) != len(b) {
		return false
	}
	ka := make(map[string]snapKey, len(a))
	for _, d := range a {
		ka[d.GID] = snapKey{d.Status, d.CompletedLength}
	}
	kb := make(map[string]snapKey, len(b))
	for _, d := range b {
		kb[d.GID] = snapKey{d.Status, d.CompletedLength}
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
func preferredOutputName(yencName, subject string) string {
	yencName = sanitizeName(yencName)
	if yencName != "" && knownOutputExt(yencName) {
		return yencName
	}
	if fromSub := filenameFromSubject(subject); fromSub != "" && knownOutputExt(fromSub) {
		return fromSub
	}
	if yencName != "" {
		return yencName
	}
	if fromSub := filenameFromSubject(subject); fromSub != "" {
		return fromSub
	}
	return sanitizeName(subject)
}

// verifyAndRepair runs PAR2 verification and best-effort repair on the files
// assembled in dir. If no .par2 files are present, files is returned unchanged.
// Repair failure is non-fatal — the caller logs and proceeds with unrepaired
// output (see research notes on interoperability caveat for go-newsgroups/par2).
func verifyAndRepair(dir string, files []string) ([]string, error) {
	var par2Paths, dataPaths []string
	for _, p := range files {
		if strings.HasSuffix(strings.ToLower(p), ".par2") {
			par2Paths = append(par2Paths, p)
		} else {
			dataPaths = append(dataPaths, p)
		}
	}
	if len(par2Paths) == 0 {
		return files, nil
	}

	blobs := make([][]byte, 0, len(par2Paths))
	for _, p := range par2Paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return files, fmt.Errorf("par2: reading %s: %w", p, err)
		}
		blobs = append(blobs, data)
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
	}

	result, err := rs.Verify(fileMap)
	if err != nil {
		return files, fmt.Errorf("par2: verify: %w", err)
	}
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
	}
	return files, nil
}

func countDamaged(r *par2lib.VerifyResult) int {
	n := 0
	for _, f := range r.Files {
		n += len(f.MissingSlices)
	}
	return n
}
