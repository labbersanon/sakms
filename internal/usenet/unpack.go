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
func unpackArchives(dir string, files []string, onProgress func(done, total int64)) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Claude 2026-09-17: ReadDir failure is an environment/filesystem fault.
		// Reason: a staging dir that can't be read is not a release quality problem;
		//   wrapping with ErrUnpackToolMissing keeps the api layer on the days ladder.
		return files, fmt.Errorf("%w: reading %s: %v", ErrUnpackToolMissing, dir, err)
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
		// Claude 2026-09-17: return ErrUnpackToolMissing (not ErrContentUnusable).
		// Reason: a missing unrar/7z is an environment fault — a different NZB cannot
		//   fix it. Returning this sentinel keeps the api layer on the days ladder
		//   instead of burning three full re-downloads on every release.
		// Review if: the image base gains unrar/7z by default (pre-flight check elsewhere).
		return files, ErrUnpackToolMissing
	}

	ctx, cancel := context.WithTimeout(context.Background(), unpackTimeout)
	defer cancel()

	beforeVideos := videoNamesInDir(dir)
	var firstErr error
	var done int64
	var total int64

	// Up to two passes: some releases nest a rar inside a rar.
	for pass := 0; pass < 2; pass++ {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return files, fmt.Errorf("%w: reading %s: %v", ErrUnpackToolMissing, dir, err)
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
		// Claude 2026-09-21: progress is archive leaders attempted this pass.
		// Reason: Downloads unpack percent is N of M sets, not bytes. Pass 2
		//   extends total by the new leaders still to run.
		// Troubleshooting: percent stuck at 0% until first leader finishes.
		// Review if: nested rar depth >2 becomes common.
		n := int64(len(leaders))
		if n < 1 {
			n = 1
		}
		if total == 0 {
			total = n
		} else {
			total = done + n
		}
		// Claude 2026-09-15: try every leader; obfuscated releases often ship a
		// pretty-named orphan part01 alongside a complete hash-named set.
		// Returning on the first failure skipped the working set (NZBGet tries
		// each RAR set independently).
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
			}
			if runErr != nil {
				log.Printf("usenet: unpack leader %s failed: %v — trying next set", leader, runErr)
				if firstErr == nil {
					firstErr = fmt.Errorf("unpack %s: %w", leader, runErr)
				}
			}
			done++
			reportPhaseProgress(onProgress, done, total)
		}
		if gainedVideo(beforeVideos, videoNamesInDir(dir)) {
			break
		}
	}

	afterVideos := videoNamesInDir(dir)
	if !gainedVideo(beforeVideos, afterVideos) {
		if firstErr != nil {
			return files, fmt.Errorf("unpack: no video produced in %s (last leader error: %w)", dir, firstErr)
		}
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

// archiveLeaders returns one path basename per archive set, skipping RAR volume
// members that are not the first part (.part01.rar / bare .rar).
//
// Claude 2026-09-15: rank complete RAR sets before orphan pretty-named part01s.
// Reason: obfuscated posts often include Show.Name.part01.rar (no part02) plus
//
//	a full AbCdEf.part01–N.rar set; lexicographic order tried the orphan
//	first and aborted before the hash set (see unpack loop).
//
// Troubleshooting: unpack "Bad archive" on pretty part01 while hash parts exist.
// Review if: .r00-style volume sets need the same completeness scoring.
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
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := rarSetScore(out[i], names), rarSetScore(out[j], names)
		if si != sj {
			return si > sj
		}
		return out[i] < out[j]
	})
	return out
}

// rarSetScore prefers contiguous part01..N sets. Orphan part01 (no part02) scores 0.
func rarSetScore(leader string, names []string) int {
	if archiveKind(leader) != "rar" {
		return 1 // zip/7z: single-file sets are fine
	}
	key := archiveSetKey(leader)
	parts := map[int]bool{}
	for _, name := range names {
		if archiveSetKey(name) != key {
			continue
		}
		lower := strings.ToLower(name)
		if m := rarPartRE.FindStringSubmatch(lower); m != nil {
			n := strings.TrimLeft(m[1], "0")
			if n == "" {
				n = "0"
			}
			var num int
			fmt.Sscanf(n, "%d", &num)
			parts[num] = true
			continue
		}
		if rarVolumeRE.MatchString(lower) {
			parts[0] = true // .rar + .r00 style — treat as present
		}
		if strings.HasSuffix(lower, ".rar") && !rarPartRE.MatchString(lower) {
			parts[1] = true
		}
	}
	if !parts[1] && !parts[0] {
		return 0
	}
	// Count contiguous run from 1.
	score := 0
	for i := 1; i <= len(names)+1; i++ {
		if !parts[i] {
			break
		}
		score++
	}
	if score == 1 && !parts[2] {
		// Lone part01 with no continuation — likely obfuscation decoy.
		return 0
	}
	return score
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
			return fmt.Errorf("%w: %s", ErrPasswordProtected, msg)
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
			return fmt.Errorf("%w: %s", ErrPasswordProtected, msg)
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
		// Claude 2026-09-11: also drop .par2 after successful unpack
		// Reason: import left PAR2 repair volumes behind after RAR delete; user
		//         wants archive-family cleanup complete once video exists
		if name == OwnedMarkerFile {
			continue
		}
		if !IsStagingMetaFile(name) && archiveKind(name) == "" &&
			!strings.HasSuffix(lower, ".sfv") &&
			!strings.HasSuffix(lower, ".par2") {
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
		if IsStagingMetaFile(e.Name()) {
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
