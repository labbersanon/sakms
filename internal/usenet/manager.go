package usenet

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	par2lib "github.com/go-newsgroups/par2"
	"golang.org/x/sync/errgroup"
)

// Download mirrors the downloader.Download shape so the api layer can build a
// unified queue from both torrent and usenet downloads without a shared
// interface. Fields with no usenet equivalent (Connections) are always zero.
type Download struct {
	GID             string
	Status          string   // "active" | "paused" | "error" | "complete" | "removed"
	Filename        string   // release name (from X-DNZB-Name or NZB first file)
	Dir             string   // staging subdirectory where assembled files land
	TotalLength     int64    // sum of NZB segment byte counts (approximate before download)
	CompletedLength int64    // decoded bytes written so far
	DownloadSpeed   int64    // bytes/sec (computed per 500 ms poll tick)
	Connections     int64    // always 0; reserved for interface parity with torrent downloads
	Files           []string // absolute paths of assembled files (populated on complete)
	ErrorMessage    string
}

// dlState is the mutable runtime state of one usenet download. All fields
// except the atomics are protected by Manager.mu.
type dlState struct {
	gid        string
	name       string
	stagingDir string
	status     string
	errorMsg   string
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
// process-lifetime singleton constructed once in cmd/sakms/main.go.
//
// Staged delivery: the Manager is built and can be exercised directly, but
// the dispatch path in internal/api/search.go still returns 400 for usenet
// grabs. The 400 stub is removed when the Manager is fully wired (Stage 2).
type Manager struct {
	pool       *pool
	httpClient *http.Client
	stagingDir string
	onComplete func(gid string, files []string)

	mu          sync.Mutex
	downloads   map[string]*dlState
	subscribers map[int]chan []Download
	nextSubID   int

	nextGID atomic.Int64 // monotonic counter for "nzb-N" GID strings
}

// Config parameterises a Manager.
type Config struct {
	Server     ServerConfig
	StagingDir string
	HTTPClient *http.Client
}

// New constructs a Manager for the given NNTP server configuration.
// The engine is not started until Start is called.
func New(cfg Config) *Manager {
	return &Manager{
		pool:        newPool(cfg.Server),
		httpClient:  cfg.HTTPClient,
		stagingDir:  cfg.StagingDir,
		downloads:   map[string]*dlState{},
		subscribers: map[int]chan []Download{},
	}
}

// SetOnComplete wires the completion callback. Safe to call before Start.
func (m *Manager) SetOnComplete(fn func(gid string, files []string)) {
	m.onComplete = fn
}

// StagingDir returns the directory where assembled NZB files are written.
func (m *Manager) StagingDir() string { return m.stagingDir }

// Start runs the 500 ms progress-poll loop and blocks until ctx is cancelled.
// Intended to run as `go m.Start(ctx)`.
func (m *Manager) Start(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var prev []Download
	for {
		select {
		case <-ctx.Done():
			m.pool.close()
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
// GID ("nzb-N") assigned to this download.
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

	gid := fmt.Sprintf("nzb-%d", m.nextGID.Add(1))

	dlDir := filepath.Join(m.stagingDir, sanitizeName(name))
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		return "", fmt.Errorf("usenet: creating staging dir %s: %w", dlDir, err)
	}

	var totalBytes int64
	for _, f := range nzb.Files {
		for _, s := range f.Segs {
			totalBytes += s.Bytes
		}
	}

	// Use a background context rather than the request context so the download
	// survives after the HTTP handler returns — same rationale as aria2c/torrent.
	dlCtx, cancel := context.WithCancel(context.Background())
	dl := &dlState{
		gid:        gid,
		name:       name,
		stagingDir: dlDir,
		status:     "active",
		total:      totalBytes,
		cancel:     cancel,
	}

	m.mu.Lock()
	m.downloads[gid] = dl
	m.mu.Unlock()

	go m.runDownload(dlCtx, gid, dl, nzb)
	return gid, nil
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

// Cancel removes a download entirely (stops it and removes it from the queue).
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
	return nil
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
// fire onComplete callback.
func (m *Manager) runDownload(ctx context.Context, gid string, dl *dlState, nzb *NZB) {
	files, err := m.downloadAll(ctx, gid, dl, nzb)
	if err != nil {
		m.mu.Lock()
		if dl.status != "removed" && dl.status != "paused" {
			dl.status = "error"
			dl.errorMsg = err.Error()
		}
		m.mu.Unlock()
		return
	}

	// Optional PAR2 verify + best-effort repair. Failure is non-fatal: the
	// download is marked complete with whatever files landed (the repair is
	// unvalidated against real-world par2cmdline output — see research notes).
	repaired, repairErr := verifyAndRepair(dl.stagingDir, files)
	if repairErr != nil {
		log.Printf("usenet: par2 repair %s: %v (marking complete with unrepaired files)", gid, repairErr)
	} else {
		files = repaired
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
	maxConc := m.pool.cfg.MaxConns
	if maxConc < 1 {
		maxConc = 4
	}
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
func (m *Manager) assembleFile(ctx context.Context, gid string, dl *dlState, nzbFile NZBFile, maxConc int) (string, error) {
	if len(nzbFile.Segs) == 0 {
		return "", fmt.Errorf("no segments")
	}

	segs := make([]NZBSegment, len(nzbFile.Segs))
	copy(segs, nzbFile.Segs)
	sort.Slice(segs, func(i, j int) bool { return segs[i].Number < segs[j].Number })

	// Fetch segment 1 first to learn the filename and total file size from the
	// yEnc =ybegin header. The connection is returned to the pool before we
	// proceed, so later concurrent fetches can reuse it.
	c, err := m.pool.get()
	if err != nil {
		return "", err
	}
	first, err := fetchSegment(c, segs[0].MsgID)
	m.pool.put(c, err == nil)
	if err != nil {
		return "", fmt.Errorf("segment 1: %w", err)
	}

	filename := first.filename
	if filename == "" {
		filename = sanitizeName(nzbFile.Subject)
	}
	outPath := filepath.Join(dl.stagingDir, filename)

	f, err := os.Create(outPath)
	if err != nil {
		return "", fmt.Errorf("creating %s: %w", outPath, err)
	}
	defer f.Close()

	if first.fileSize > 0 {
		if err := f.Truncate(first.fileSize); err != nil {
			return "", fmt.Errorf("pre-allocating %s: %w", outPath, err)
		}
	}
	if _, err := f.WriteAt(first.data, first.offset); err != nil {
		return "", fmt.Errorf("writing segment 1: %w", err)
	}
	m.addCompleted(gid, int64(len(first.data)))

	if len(segs) > 1 {
		var g errgroup.Group
		g.SetLimit(maxConc)
		for _, seg := range segs[1:] {
			seg := seg
			g.Go(func() error {
				conn, err := m.pool.get()
				if err != nil {
					return err
				}
				res, err := fetchSegment(conn, seg.MsgID)
				m.pool.put(conn, err == nil)
				if err != nil {
					return fmt.Errorf("segment %d: %w", seg.Number, err)
				}
				if _, werr := f.WriteAt(res.data, res.offset); werr != nil {
					return fmt.Errorf("writing segment %d: %w", seg.Number, werr)
				}
				m.addCompleted(gid, int64(len(res.data)))
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return "", err
		}
	}

	return outPath, nil
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
