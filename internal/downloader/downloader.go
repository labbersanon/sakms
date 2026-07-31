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
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	torrentlib "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// pollInterval is how often the Manager's background loop re-reads torrent
// progress to detect changes and fan out to SSE subscribers.
const pollInterval = 500 * time.Millisecond

// Config parameterizes the Manager.
type Config struct {
	StagingDir string // torrent download directory (anacrolix DataDir)
	MaxConc    int    // reserved; all torrents download concurrently today
	MaxConn    int    // EstablishedConnsPerTorrent in the torrent client config
}

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
	Connections     int64
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
	files    []string // full absolute paths, populated after GotInfo
	dir      string   // per-torrent folder or stagingDir
	filename string   // display: files[0] when known

	// Speed (delta across poll ticks).
	prevBytes int64
	prevTime  time.Time
	speed     int64

	// Cached for terminal states after the handle may be dropped.
	cachedCompleted int64
	cachedTotal     int64
	cachedConns     int64
}

// Manager owns the anacrolix torrent client, in-memory download state, and the
// SSE subscriber hub. It is a process-lifetime singleton — see package doc.
type Manager struct {
	cfg        Config
	tc         *torrentlib.Client // nil before Start or in test mode
	httpClient *http.Client

	onComplete func(gid string, files []string)

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
		cachedConns:     d.Connections,
		speed:           d.DownloadSpeed,
	}
}

// SetOnComplete wires the completion callback. Safe to call before Start.
func (m *Manager) SetOnComplete(fn func(gid string, files []string)) {
	m.onComplete = fn
}

// StagingDir returns the directory where torrent files are written.
func (m *Manager) StagingDir() string { return m.cfg.StagingDir }

// Start creates the anacrolix torrent client, starts the poll loop, and blocks
// until ctx is cancelled. Intended to run as `go m.Start(ctx)`.
func (m *Manager) Start(ctx context.Context) error {
	if m.cfg.StagingDir != "" {
		if err := os.MkdirAll(m.cfg.StagingDir, 0o755); err != nil {
			log.Printf("downloader: creating staging dir %s: %v", m.cfg.StagingDir, err)
		}
	}

	cfg := torrentlib.NewDefaultClientConfig()
	cfg.DataDir = m.cfg.StagingDir
	cfg.NoUpload = true
	cfg.Seed = false
	if m.cfg.MaxConn > 0 {
		cfg.EstablishedConnsPerTorrent = m.cfg.MaxConn
	}

	tc, err := torrentlib.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("downloader: creating torrent client: %w", err)
	}

	m.mu.Lock()
	m.tc = tc
	m.mu.Unlock()

	go m.pollLoop(ctx)

	<-ctx.Done()

	m.mu.Lock()
	m.tc = nil
	m.mu.Unlock()
	tc.Close()
	return ctx.Err()
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
			m.entries[gid] = &entry{status: "active"}
		}
		m.mu.Unlock()
		return gid, nil
	}

	m.mu.Lock()
	tc := m.tc
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
		mi, err := m.fetchMetainfo(ctx, uri)
		if err != nil {
			return "", err
		}
		var addErr error
		t, addErr = tc.AddTorrent(mi)
		if addErr != nil {
			return "", fmt.Errorf("downloader: adding torrent: %w", addErr)
		}
	}

	gid := t.InfoHash().HexString()
	m.mu.Lock()
	m.entries[gid] = &entry{
		t:      t,
		status: "waiting",
		dir:    m.cfg.StagingDir,
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
	e.status = "paused"
	m.mu.Unlock()
	if t != nil {
		t.DisallowDataDownload()
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
	e.status = "active"
	m.mu.Unlock()
	if t != nil {
		t.AllowDataDownload()
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
func (m *Manager) Cancel(gid string) error {
	m.mu.Lock()
	e, ok := m.entries[gid]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("download not found: %s", gid)
	}
	t := e.t
	files := append([]string(nil), e.files...)
	delete(m.entries, gid)
	m.mu.Unlock()
	if t != nil {
		t.Drop()
	}
	m.deleteDownloadFiles(files)
	return nil
}

// deleteDownloadFiles removes each declared file path and best-effort prunes any
// now-empty parent directories up to (but never including) the staging dir. Only
// directories strictly under the staging dir are pruned, and only when empty, so
// a single file sitting directly in the shared staging dir never triggers a
// directory removal.
func (m *Manager) deleteDownloadFiles(files []string) {
	staging := filepath.Clean(m.cfg.StagingDir)
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
func (m *Manager) watchTorrent(t *torrentlib.Torrent, gid string) {
	select {
	case <-t.Closed():
		m.mu.Lock()
		if e, ok := m.entries[gid]; ok && e.status != "removed" {
			e.status = "removed"
		}
		m.mu.Unlock()
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
	if e, ok := m.entries[gid]; ok {
		e.status = "active"
		e.files = files
		e.dir = dir
		e.filename = filename
		e.cachedTotal = t.Length()
	}
	m.mu.Unlock()

	t.DownloadAll()

	select {
	case <-t.Closed():
		m.mu.Lock()
		if e, ok := m.entries[gid]; ok && e.status != "removed" {
			e.status = "removed"
		}
		m.mu.Unlock()
	case <-t.Complete().On():
		completed := t.BytesCompleted()
		total := t.Length()
		m.mu.Lock()
		if e, ok := m.entries[gid]; ok {
			e.status = "complete"
			e.cachedCompleted = completed
			e.cachedTotal = total
			e.speed = 0
		}
		m.mu.Unlock()
		filesCopy := append([]string(nil), files...)
		if m.onComplete != nil {
			go m.onComplete(gid, filesCopy)
		}
	}
}

// computeFiles derives absolute file paths from a torrent after GotInfo.
// f.Path() uses '/' separators and is relative to DataDir (StagingDir).
func (m *Manager) computeFiles(t *torrentlib.Torrent) []string {
	tf := t.Files()
	out := make([]string, 0, len(tf))
	for _, f := range tf {
		out = append(out, filepath.Join(m.cfg.StagingDir, filepath.FromSlash(f.Path())))
	}
	return out
}

// computeDir returns the appropriate download directory for a file list: the
// parent of files[0] when files are in a per-torrent subfolder, or stagingDir
// when a single file sits directly in staging.
func (m *Manager) computeDir(files []string) string {
	if len(files) == 0 {
		return m.cfg.StagingDir
	}
	return filepath.Dir(files[0])
}

// fetchMetainfo downloads and parses a .torrent file from uri.
func (m *Manager) fetchMetainfo(ctx context.Context, uri string) (*metainfo.MetaInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, fmt.Errorf("downloader: building torrent request: %w", err)
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloader: fetching torrent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloader: torrent URL returned %d", resp.StatusCode)
	}
	mi, err := metainfo.Load(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("downloader: parsing torrent metainfo: %w", err)
	}
	return mi, nil
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
			snap := m.pollSnapshot()
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
		Connections:     e.cachedConns,
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

// pollSnapshot builds the current Download list from all entries, advancing
// speed state (prevBytes/prevTime/speed) for active downloads. Only called by
// pollLoop — HTTP handlers use readSnapshot to avoid corrupting speed state.
func (m *Manager) pollSnapshot() []Download {
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
				var total int64
				if e.t.Info() != nil {
					total = e.t.Length()
				}
				conns := int64(e.t.Stats().ConnectedSeeders)
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
				e.cachedConns = conns
			}
		}
		out = append(out, m.buildEntry(gid, e))
	}
	return out
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
