package usenet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Claude 2026-09-30: refuse hollow/unfinished videos even when the resume sidecar is gone.
// Reason: usenetStagingReadyForImport treated no-sidecar + ≥1MiB as complete; Truncate-up
//   sparse/all-NUL files imported as real movies. Expected size from the sidecar (when
//   present) AND sparse/zero sampling (when not).
// Troubleshooting: reconcile "hollow video"; Requests "no usable video".
// Review if: ffprobe duration is used as a third finished-signal.

// ResumeExpectedFileSize returns the sidecar's recorded size for filename, or 0
// when the sidecar is missing, unreadable, or has no matching file.
func ResumeExpectedFileSize(dir, filename string) int64 {
	data, err := os.ReadFile(filepath.Join(dir, ResumeFileName))
	if err != nil {
		return 0
	}
	var snap ResumeSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return 0
	}
	if snap.Version != resumeSchemaVersion {
		return 0
	}
	base := filepath.Base(filename)
	rf := snap.Files[base]
	if rf == nil {
		rf = snap.Files[filename]
	}
	if rf == nil {
		return 0
	}
	return rf.Size
}

// VideoLooksFinished reports whether path is a usable video payload, not a
// Truncate-up sparse hole or dense all-NUL fill. expectedSize 0 skips the
// size check (sidecar gone / torrents / tiny tests).
func VideoLooksFinished(path string, expectedSize int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%w: stat %s: %v", ErrNoVideoUnpacked, path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%w: %s is a directory", ErrNoVideoUnpacked, path)
	}
	size := info.Size()
	if size <= 0 {
		return fmt.Errorf("%w: empty %s", ErrNoVideoUnpacked, path)
	}
	if expectedSize > 0 && size < expectedSize {
		return fmt.Errorf("%w: %s is %d bytes, expected %d", ErrNoVideoUnpacked, path, size, expectedSize)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: open %s: %v", ErrNoVideoUnpacked, path, err)
	}
	defer f.Close()
	if !rangeHasDataExtents(f, 0, size) {
		return fmt.Errorf("%w: sparse %s", ErrNoVideoUnpacked, path)
	}
	for _, off := range finishedSampleOffsets(size) {
		n := size - off
		if n > 4096 {
			n = 4096
		}
		if !rangeLooksPopulated(f, off, n) {
			return fmt.Errorf("%w: hollow %s", ErrNoVideoUnpacked, path)
		}
	}
	return nil
}

func finishedSampleOffsets(size int64) []int64 {
	if size <= 0 {
		return nil
	}
	offs := []int64{0}
	if size > 8192 {
		offs = append(offs, size/2)
	}
	if size > 4096 {
		tail := size - 4096
		if tail > 0 && tail != offs[len(offs)-1] {
			offs = append(offs, tail)
		}
	}
	return offs
}
