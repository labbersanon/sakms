package usenet

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// Claude 2026-09-11: durable usenet segment-resume sidecar (Phase 2)
// Reason: RelaunchNZB re-fetched every segment after restart; ARR-parity needs
//         skip-completed-segments using state that survives process death
// Troubleshooting: journal "usenet: resume"; file .sakms-resume.json under nzb-*
// Review if: true PAR2-aware resume or cross-host resume sync is required
// Related: assembleFile, RelaunchNZB, deleteArchiveMembers, downloadstate.Store

// ResumeFileName is the per-staging-dir sidecar that records completed segments.
// Source of truth for resume; the optional DB mirror is for UI/debug only.
// Must be removed by post-unpack / post-import cleanup (see deleteArchiveMembers).
const ResumeFileName = ".sakms-resume.json"

const resumeTmpName = ".sakms-resume.json.tmp"

// resumeSchemaVersion is the durable resume sidecar format.
// v1 wrote segments at yEnc begin offsets (leaving NUL gaps when decoded
// length < begin stride). v2 writes contiguously by decoded length.
// Claude 2026-09-15: bump for contiguous assembly fix.
// Reason: gap-polluted v1 resumes must not be reused after the offset change.
// Troubleshooting: journal "discarding legacy resume"; staging wiped + relaunch.
// Review if: a future schema needs another wipe policy.
const resumeSchemaVersion = 2

type ResumeSnapshot struct {
	Version int                    `json:"v"`
	GID     string                 `json:"gid"`
	Files   map[string]*ResumeFile `json:"files"`
}

type ResumeFile struct {
	Size int64                `json:"size,omitempty"`
	Done map[string]ResumeSeg `json:"done"` // key = MsgID
}

type ResumeSeg struct {
	Number int   `json:"n"`
	Offset int64 `json:"off"`
	Length int   `json:"len"`
}

// ResumeMirror is the optional DB copy of the staging sidecar (UI/debug).
// Implementations must be safe for concurrent use; Clear is called after import.
type ResumeMirror interface {
	SaveResume(gid string, snap ResumeSnapshot) error
	ClearResume(gid string) error
}

type resumeTracker struct {
	mu       sync.Mutex
	dir      string
	gid      string
	snap     ResumeSnapshot
	mirror   ResumeMirror
	disabled bool // force-full / resume off — never write
}

func loadResumeTracker(dir, gid string, mirror ResumeMirror, disabled bool) *resumeTracker {
	t := &resumeTracker{
		dir:      dir,
		gid:      gid,
		mirror:   mirror,
		disabled: disabled,
		snap: ResumeSnapshot{
			Version: resumeSchemaVersion,
			GID:     gid,
			Files:   map[string]*ResumeFile{},
		},
	}
	if disabled {
		return t
	}
	data, err := os.ReadFile(filepath.Join(dir, ResumeFileName))
	if err != nil {
		return t
	}
	var loaded ResumeSnapshot
	if err := json.Unmarshal(data, &loaded); err != nil {
		return t
	}
	if loaded.Files == nil {
		loaded.Files = map[string]*ResumeFile{}
	}
	// Claude 2026-09-15: refuse v1 (yEnc-offset) resumes — they encode gaps.
	// Reason: contiguous assembly (v2) cannot safely skip v1 ranges; leftover
	//         NUL holes fail PAR2 / unrar. Wipe sidecar + payloads so the
	//         next assemble is a clean contiguous write.
	// Troubleshooting: "discarding legacy resume"; hollow rar after upgrade.
	// Review if: resumeSchemaVersion bumps again and needs the same wipe.
	if loaded.Version != resumeSchemaVersion {
		log.Printf("usenet: discarding legacy resume v%d in %s (need v%d) — wiping staging payloads",
			loaded.Version, dir, resumeSchemaVersion)
		_ = ClearResumeArtifacts(dir)
		_ = wipeStagingPayloads(dir)
		return t
	}
	loaded.GID = gid
	t.snap = loaded
	return t
}

func (t *resumeTracker) disable() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.disabled = true
	t.mu.Unlock()
}

func (t *resumeTracker) skippedSegments() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, f := range t.snap.Files {
		n += len(f.Done)
	}
	return n
}

func (t *resumeTracker) hasSegment(filename, msgID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.snap.Files[filename]
	if f == nil {
		return false
	}
	_, ok := f.Done[msgID]
	return ok
}

// segmentCovered reports whether msgID is marked done, the on-disk file is
// large enough to contain that segment's recorded [offset, offset+length), AND
// that byte range looks populated (not a sparse hole / all-NUL fill).
// Skipping without these checks leaves hollow videos that still import.
// f may be nil — then only the size check runs (tests / callers without an FD).
func (t *resumeTracker) segmentCovered(f *os.File, filename, msgID string, fileBytes int64) bool {
	t.mu.Lock()
	rf := t.snap.Files[filename]
	var seg ResumeSeg
	ok := false
	if rf != nil {
		seg, ok = rf.Done[msgID]
	}
	t.mu.Unlock()
	if !ok || seg.Length <= 0 {
		return false
	}
	if fileBytes < seg.Offset+int64(seg.Length) {
		return false
	}
	if f == nil {
		return true
	}
	return rangeLooksPopulated(f, seg.Offset, int64(seg.Length))
}

func (t *resumeTracker) priorFile(firstMsg string) (name string, size int64, done int) {
	if t == nil || t.disabled {
		return "", 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for name, rf := range t.snap.Files {
		if rf == nil || len(rf.Done) == 0 {
			continue
		}
		if _, ok := rf.Done[firstMsg]; ok || len(t.snap.Files) == 1 {
			return name, rf.Size, len(rf.Done)
		}
	}
	return "", 0, 0
}

func (t *resumeTracker) skippedBytes(filename, msgID string, fallback int) int {
	n := 0
	if t != nil {
		t.mu.Lock()
		if rf := t.snap.Files[filename]; rf != nil {
			n = rf.Done[msgID].Length
		}
		t.mu.Unlock()
	}
	if n <= 0 {
		n = fallback
	}
	if n < 0 {
		return 0
	}
	return n
}

func (t *resumeTracker) setFileSize(filename string, size int64) {
	if t == nil || t.disabled || filename == "" || size <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.snap.Files[filename]
	if f == nil {
		f = &ResumeFile{Done: map[string]ResumeSeg{}}
		t.snap.Files[filename] = f
	}
	f.Size = size
	if err := t.persistLocked(); err != nil {
		log.Printf("usenet: resume persist size %s: %v", filename, err)
	}
}

func (t *resumeTracker) markSegment(filename, msgID string, number int, offset int64, length int, fileSize int64) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// Re-check under lock so SetResumePolicy(force-full) cannot race a persist
	// that recreates the sidecar after SweepResumeArtifacts.
	if t.disabled {
		return nil
	}
	f := t.snap.Files[filename]
	if f == nil {
		f = &ResumeFile{Done: map[string]ResumeSeg{}}
		t.snap.Files[filename] = f
	}
	if f.Done == nil {
		f.Done = map[string]ResumeSeg{}
	}
	if fileSize > 0 {
		f.Size = fileSize
	}
	f.Done[msgID] = ResumeSeg{Number: number, Offset: offset, Length: length}
	return t.persistLocked()
}

func (t *resumeTracker) persistLocked() error {
	path := filepath.Join(t.dir, ResumeFileName)
	tmp := filepath.Join(t.dir, resumeTmpName)
	data, err := json.MarshalIndent(t.snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if t.mirror != nil {
		if err := t.mirror.SaveResume(t.gid, cloneResumeSnapshot(t.snap)); err != nil {
			return fmt.Errorf("resume mirror: %w", err)
		}
	}
	return nil
}

func cloneResumeSnapshot(s ResumeSnapshot) ResumeSnapshot {
	out := ResumeSnapshot{Version: s.Version, GID: s.GID, Files: map[string]*ResumeFile{}}
	for name, f := range s.Files {
		if f == nil {
			continue
		}
		nf := &ResumeFile{Size: f.Size, Done: map[string]ResumeSeg{}}
		for id, seg := range f.Done {
			nf.Done[id] = seg
		}
		out.Files[name] = nf
	}
	return out
}

// ClearResumeArtifacts removes the staging sidecar (and tmp) from dir.
// Safe no-op when absent. Used by force-full relaunch and post-unpack cleanup.
// resumeFileVersion returns the sidecar schema version, or 0 when missing/invalid.
func resumeFileVersion(dir string) int {
	data, err := os.ReadFile(filepath.Join(dir, ResumeFileName))
	if err != nil {
		return 0
	}
	var loaded ResumeSnapshot
	if err := json.Unmarshal(data, &loaded); err != nil {
		return 0
	}
	return loaded.Version
}

// StagingHasLegacyOrOrphanPayloads reports whether dir has a pre-v2 resume sidecar (or
// payload files without a current-schema resume). Used at startup to wipe
// gap-damaged staging so reconcile can relaunch cleanly.
func StagingHasLegacyOrOrphanPayloads(dir string) bool {
	ver := resumeFileVersion(dir)
	if ver != 0 && ver != resumeSchemaVersion {
		return true
	}
	if ver == resumeSchemaVersion {
		return false
	}
	// No resume sidecar: any non-meta payload is treated as potentially
	// gap-damaged leftover from a pre-upgrade download.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if IsStagingMetaFile(e.Name()) {
			continue
		}
		return true
	}
	return false
}

// completedMsgIDsFromDir reads the resume sidecar so RelaunchNZB's precheck can
// skip articles already durable-complete. Returns nil when the sidecar is
// missing, unreadable, or written by an older schema version.
func completedMsgIDsFromDir(dir string) map[string]bool {
	data, err := os.ReadFile(filepath.Join(dir, ResumeFileName))
	if err != nil {
		return nil
	}
	var snap ResumeSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil
	}
	if snap.Version != resumeSchemaVersion {
		return nil
	}
	out := make(map[string]bool)
	for _, f := range snap.Files {
		if f == nil {
			continue
		}
		for id := range f.Done {
			if id != "" {
				out[id] = true
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func ClearResumeArtifacts(dir string) error {
	var first error
	for _, name := range []string{ResumeFileName, resumeTmpName} {
		err := os.Remove(filepath.Join(dir, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}

// IsStagingMetaFile reports names that must never be treated as download content
// (ownership marker, resume sidecar / tmp).
func IsStagingMetaFile(name string) bool {
	base := filepath.Base(name)
	return base == OwnedMarkerFile || base == ResumeFileName || base == resumeTmpName
}

// wipeStagingPayloads removes non-meta files under dir (videos/archives) while
// keeping ownership + resume markers for the caller to clear separately.
// Used by force-full so hollow ResolveVideoFile hits cannot be imported.
func wipeStagingPayloads(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var first error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if IsStagingMetaFile(name) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	return first
}
