package usenet

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Claude 2026-09-11: ownership gate for staging deletes (stale-archive sweep).
// Reason: only remove dirs sakms minted under its staging root — never NZBGet/qbt/library trees.
// Troubleshooting: deletes no-op when IsOwnedStagingPath is false; look for .sakms-owned + nzb-* name.
// Review if: GID naming scheme changes or staging layout gains nested dirs.
// Related: allocateStaging, RemoveOwnedStagingDir, internal/stagingsweep.

// OwnedMarkerFile is written into every newly allocated staging directory so
// future layouts can prove sakms ownership even if naming rules change.
const OwnedMarkerFile = ".sakms-owned"

// ownedStagingNameRE matches sakms-minted GIDs: current "nzb-" + 16 hex, or
// legacy counter "nzb-" + digits (pre-random-GID era).
var ownedStagingNameRE = regexp.MustCompile(`^nzb-([0-9a-f]{16}|\d+)$`)

// IsOwnedStagingName reports whether base is a sakms-minted staging directory name.
func IsOwnedStagingName(base string) bool {
	return ownedStagingNameRE.MatchString(base)
}

// IsOwnedStagingPath reports whether dir is a sakms-owned staging directory:
// a direct child of stagingRoot whose basename is a sakms GID, or that contains
// the .sakms-owned marker (and is still a direct child of stagingRoot).
func IsOwnedStagingPath(stagingRoot, dir string) bool {
	if stagingRoot == "" || dir == "" {
		return false
	}
	root := filepath.Clean(stagingRoot)
	clean := filepath.Clean(dir)
	rel, err := filepath.Rel(root, clean)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	// Direct child only — never nested paths under a GID (or escape tricks).
	if strings.ContainsRune(rel, filepath.Separator) {
		return false
	}
	if IsOwnedStagingName(rel) {
		return true
	}
	// Marker-based ownership for any future direct-child naming, still contained.
	if _, err := os.Stat(filepath.Join(clean, OwnedMarkerFile)); err == nil {
		return true
	}
	return false
}

// RemoveOwnedStagingDir deletes dir only when IsOwnedStagingPath is true.
// Returns nil when the path is not owned (no-op) or already absent.
func RemoveOwnedStagingDir(stagingRoot, dir string) error {
	if !IsOwnedStagingPath(stagingRoot, dir) {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("usenet: removing owned staging %s: %w", dir, err)
	}
	return nil
}

// writeOwnedMarker best-effort creates OwnedMarkerFile inside dir.
func writeOwnedMarker(dir string) {
	_ = os.WriteFile(filepath.Join(dir, OwnedMarkerFile), []byte("sakms\n"), 0o644)
}
