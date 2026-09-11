package usenet

import (
	"encoding/json"
	"errors"
	"fmt"
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

// resumeTmpName is the atomic-replace sibling of ResumeFileName.
const resumeTmpName = ".sakms-resume.json.tmp"

// ResumeSnapshot is the JSON shape of ResumeFileName.
type ResumeSnapshot struct {
	Version int                    `json:"v"`
	GID     string                 `json:"gid"`
	Files   map[string]*ResumeFile `json:"files"`
}

// ResumeFile tracks one assembled output file inside a staging dir.
type ResumeFile struct {
	Size int64                `json:"size,omitempty"`
	Done map[string]ResumeSeg `json:"done"` // key = segment MsgID
}

// ResumeSeg is one completed segment write.
type ResumeSeg struct {
	Number int   `json:"n"`
	Offset int64 `json:"off"`
	Length int   `json:"len"`
}

// ResumeMirror is the optional DB mirror of the staging sidecar (UI/debug).
// Implementations must be safe for concurrent use; Clear is called after import.
type ResumeMirror interface {
	SaveResume(gid string, snap ResumeSnapshot) error
	ClearResume(gid string) error
}

// resumeTracker is the per-download in-memory + sidecar writer used by assembleFile.
type resumeTracker struct {
	mu       sync.Mutex
	dir      string
	gid      string
	snap     ResumeSnapshot
	mirror   ResumeMirror
	disabled bool // force-full / resume disabled — never write
}

func loadResumeTracker(dir, gid string, mirror ResumeMirror, disabled bool) *resumeTracker {
	t := &resumeTracker{
		dir:      dir,
		gid:      gid,
		mirror:   mirror,
		disabled: disabled,
		snap: ResumeSnapshot{
			Version: 1,
			GID:     gid,
			Files:   map[string]*ResumeFile{},
		},
	}
	if disabled {
		return t
	}
	path := filepath.Join(dir, ResumeFileName)
	data, err := os.ReadFile(path)
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
	loaded.Version = 1
	loaded.GID = gid
	t.snap = loaded
	return t
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

func (t *resumeTracker) fileSize(filename string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.snap.Files[filename]
	if f == nil {
		return 0
	}
	return f.Size
}

func (t *resumeTracker) markSegment(filename, msgID string, number int, offset int64, length int, fileSize int64) error {
	if t == nil || t.disabled {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
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
		snapCopy := cloneResumeSnapshot(t.snap)
		if err := t.mirror.SaveResume(t.gid, snapCopy); err != nil {
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
