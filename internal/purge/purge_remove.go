package purge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/labbersanon/sakms/internal/mode"
)

// Claude 2026-08-17: Purge must delete every copy of a title, not only the
// denormalized primary FilePath.
// Reason: ApplyLibrary used to os.Remove item.FilePath then libStore.Delete.
//   library_item_files rows CASCADE off the item, but their on-disk files
//   (Rename alternates, Dedup extra keepers, sibling encodes in the same
//   movie folder) stayed behind. Dedup Scan then rediscovered them as
//   orphans — live: The Last House (tmdb 1284041) purge applied on the
//   unsuffixed .mkv while - 2160p HEVC / - 1080p H264 / - 2160p HEVC.2
//   and an unmapped REPACK folder remained.
// Troubleshooting: Purge a title, Dedup rescan still lists every copy.
// Review if: library_item_files becomes the sole path source (no FilePath).

func uniqueNonEmptyPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// isSafeLibrarySubdir reports whether dir is a real subdirectory of root
// (never root itself, never a path that escapes via ".."). Used so Purge
// can RemoveAll a movie/scene folder without being able to wipe the
// library root if a file sits directly in it.
func isSafeLibrarySubdir(dir, root string) bool {
	if dir == "" || root == "" {
		return false
	}
	dir = filepath.Clean(dir)
	root = filepath.Clean(root)
	if dir == root {
		return false
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

// removeTrackedMedia deletes each unique path (ENOENT is success), then
// RemoveAlls each unique parent directory that is a subdirectory of
// rootFolderPath. Parents equal to the library root are left intact so a
// file sitting directly in the root cannot take the whole library with it.
func removeTrackedMedia(paths []string, rootFolderPath string) (changes []mode.PathChange, err error) {
	paths = uniqueNonEmptyPaths(paths)
	dirs := make(map[string]struct{})
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return changes, fmt.Errorf("deleting %q: %w", path, err)
		}
		changes = append(changes, mode.PathChange{Path: path, Kind: mode.Deleted})
		dir := filepath.Dir(path)
		if isSafeLibrarySubdir(dir, rootFolderPath) {
			dirs[dir] = struct{}{}
		}
	}
	for dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			return changes, fmt.Errorf("removing library folder %q: %w", dir, err)
		}
	}
	return changes, nil
}
