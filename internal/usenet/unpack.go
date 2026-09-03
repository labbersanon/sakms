package usenet

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/config"
)

// Claude 2026-09-03: post-PAR2 archive unpack before import.
// Reason: Usenet releases are usually multi-part RAR; import only sees flat
//   videos in the GID staging dir, and sakms had no unrar/7z step.
// Troubleshooting: staging full of .partNN.rar, import "no video files found".
// Review if: password-protected archives or nested-rar depth >2 become common.
// Related: runDownload (call site); Dockerfile unrar + p7zip-full.

const unpackTimeout = 30 * time.Minute

// unpackCommand is swappable in tests.
var unpackCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

var (
	lookPath = exec.LookPath
	// .partNN.rar — capture the part number; .r00-style volumes are separate.
	rarPartRE   = regexp.MustCompile(`(?i)\.part(\d+)\.rar$`)
	rarVolumeRE = regexp.MustCompile(`(?i)\.r\d{2}$`)
)

// unpackArchives extracts password-less rar/zip/7z sets under dir into dir
// (flat), then deletes the archive members on success. Failure is returned for
// the caller to log; the original files slice is still usable.
//
// If no archives are present, or no unpacker is on PATH, files is returned
// unchanged with a nil error.
func unpackArchives(dir string, files []string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return files, fmt.Errorf("unpack: reading %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	leaders := archiveLeaders(names)
	if len(leaders) == 0 {
		return listStagingFiles(dir, files), nil
	}

	unrarPath, unrarErr := lookPath("unrar")
	sevenPath, sevenErr := lookPath("7z")
	if unrarErr != nil && sevenErr != nil {
		return files, fmt.Errorf("unpack: no unrar or 7z on PATH (install unrar/p7zip-full in the image)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), unpackTimeout)
	defer cancel()

	beforeVideos := videoNamesInDir(dir)

	// Up to two passes: some releases nest a rar inside a rar.
	for pass := 0; pass < 2; pass++ {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return files, fmt.Errorf("unpack: reading %s: %w", dir, err)
		}
		names = names[:0]
		for _, e := range entries {
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		}
		leaders = archiveLeaders(names)
		if len(leaders) == 0 {
			break
		}
		for _, leader := range leaders {
			path := filepath.Join(dir, leader)
			var runErr error
			switch archiveKind(leader) {
			case "rar":
				if unrarErr != nil {
					runErr = fmt.Errorf("unrar not available for %s", leader)
				} else {
					runErr = runUnrar(ctx, unrarPath, path, dir)
				}
			case "zip", "7z":
				if sevenErr != nil {
					runErr = fmt.Errorf("7z not available for %s", leader)
				} else {
					runErr = run7z(ctx, sevenPath, path, dir)
				}
			default:
				continue
			}
			if runErr != nil {
				return files, fmt.Errorf("unpack %s: %w", leader, runErr)
			}
		}
	}

	afterVideos := videoNamesInDir(dir)
	if !gainedVideo(beforeVideos, afterVideos) {
		return files, fmt.Errorf("unpack: completed without producing a video file in %s", dir)
	}

	if err := deleteArchiveMembers(dir); err != nil {
		log.Printf("usenet: unpack: deleting archives in %s: %v", dir, err)
	}
	return listStagingFiles(dir, files), nil
}

func archiveKind(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".rar") || rarVolumeRE.MatchString(lower):
		return "rar"
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	case strings.HasSuffix(lower, ".7z"):
		return "7z"
	default:
		return ""
	}
}

// archiveLeaders returns one path basename per archive set (sorted), skipping
// RAR volume members that are not the first part (.part01.rar / bare .rar).
func archiveLeaders(names []string) []string {
	var out []string
	seen := map[string]bool{}
	sort.Strings(names)
	for _, name := range names {
		kind := archiveKind(name)
		if kind == "" {
			continue
		}
		if kind == "rar" && isRarVolumeNotLeader(name) {
			continue
		}
		key := archiveSetKey(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	return out
}

func isRarVolumeNotLeader(name string) bool {
	lower := strings.ToLower(name)
	if rarVolumeRE.MatchString(lower) {
		return true // .r00 / .r01 — leader is the sibling .rar
	}
	if m := rarPartRE.FindStringSubmatch(lower); m != nil {
		// part01 / part1 are leaders; any other part number is a volume.
		n := strings.TrimLeft(m[1], "0")
		if n == "" {
			n = "0"
		}
		return n != "1"
	}
	return false
}

func archiveSetKey(name string) string {
	lower := strings.ToLower(name)
	if m := rarPartRE.FindStringSubmatch(lower); m != nil {
		return lower[:len(lower)-len(m[0])]
	}
	if rarVolumeRE.MatchString(lower) {
		return strings.TrimSuffix(lower, filepath.Ext(lower))
	}
	return strings.TrimSuffix(lower, filepath.Ext(lower))
}

func runUnrar(ctx context.Context, bin, archive, dest string) error {
	// e = extract without paths (flat into dest). -o+ overwrite. -y assume yes.
	cmd := unpackCommand(ctx, bin, "e", "-o+", "-y", archive, dest+string(filepath.Separator))
	var stderr bytes.Buffer
	cmd.Stdout = nil
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if looksPasswordProtected(msg) {
			return fmt.Errorf("password-protected archive (unsupported): %s", msg)
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func run7z(ctx context.Context, bin, archive, dest string) error {
	// e = extract without paths. -y assume yes. -oDEST (no space).
	cmd := unpackCommand(ctx, bin, "e", "-y", "-o"+dest, archive)
	var stderr bytes.Buffer
	cmd.Stdout = nil
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if looksPasswordProtected(msg) {
			return fmt.Errorf("password-protected archive (unsupported): %s", msg)
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func looksPasswordProtected(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "password") ||
		strings.Contains(lower, "encrypted") ||
		strings.Contains(lower, "wrong password")
}

func videoNamesInDir(dir string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if config.IsVideoFile(e.Name()) {
			out[e.Name()] = true
		}
	}
	return out
}

func gainedVideo(before, after map[string]bool) bool {
	for name := range after {
		if !before[name] {
			return true
		}
	}
	// Already had a video before unpack (unusual for rar-only releases) —
	// still treat as success if any video remains after.
	return len(after) > 0 && len(before) > 0
}

func deleteArchiveMembers(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var first error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if archiveKind(name) == "" && !strings.HasSuffix(lower, ".sfv") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func listStagingFiles(dir string, fallback []string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fallback
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	if len(out) == 0 {
		return fallback
	}
	return out
}
