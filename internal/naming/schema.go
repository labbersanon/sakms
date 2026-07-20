package naming

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/labbersanon/sakms/internal/library"
)

// These are purely structural/shape checks — they confirm a path already
// LOOKS like it matches preset's convention, never that its tmdbid tag (or
// title) is actually correct for this content. That's deliberate: Rename's
// schema-conformance filter exists to skip proposing files that don't need
// its attention, not to re-verify an identification Rename would otherwise
// have to do anyway.
var (
	movieFolderJellyfin = regexp.MustCompile(`^.+(?: \(\d{4}\))? \[tmdbid-\d+\]$`)
	movieFolderLegacy   = regexp.MustCompile(`^.+ \(\d{4}\)$`)
	seasonDirPattern    = regexp.MustCompile(`^Season \d{2}$`)
	// [^-] before the space excludes a Legacy-shaped "Title - SxxExx" name —
	// RE2 has no lookbehind, so this is the plain-regex way to require the
	// character right before "SxxExx" isn't a dash. The optional (?:-E\d{2})?
	// right after SxxExx recognizes a logical-episode-split file's range
	// shape too (naming.EpisodeRangeFileName's "S03E05-E06") — without it, a
	// correctly-split, already-renamed file would never register as
	// schema-conformant and would be endlessly re-proposed on every Scan.
	episodeFileJellyfin  = regexp.MustCompile(`^.+[^-] S\d{2}E\d{2}(?:-E\d{2})?(?: .+)?$`)
	episodeFileLegacy    = regexp.MustCompile(`^.+ - S\d{2}E\d{2}(?:-E\d{2})?(?: - .+)?$`)
	seriesFolderJellyfin = regexp.MustCompile(`^.+(?: \(\d{4}\))? \[tmdbid-\d+\]$`)
	// adultPhashTag matches AdultFileName's embedded "[phash-HASH]" tag anywhere
	// in a filename — Adult has one fixed scheme, not a per-preset shape, so
	// this is the sole conformance marker MatchesAdultSchema checks for.
	adultPhashTag = regexp.MustCompile(`\[phash-[^\]]+\]`)
)

// MatchesMovieSchema reports whether entryPath — as found by
// library.ScanRootFolder, directly under a Movies root — is already
// organized per preset: a wrapping folder whose name matches preset's
// shape, containing exactly one resolvable video file whose own basename
// (minus extension) is identical to the folder name. A bare loose file
// never matches — both presets structurally require the wrapping folder,
// since that's the only way MovieFileName's name can be verified against
// MovieFolderName's.
func MatchesMovieSchema(entryPath string, preset Preset) bool {
	info, err := os.Stat(entryPath)
	if err != nil || !info.IsDir() {
		return false
	}
	folderName := filepath.Base(entryPath)
	pattern := movieFolderJellyfin
	if preset == Legacy {
		pattern = movieFolderLegacy
	}
	if !pattern.MatchString(folderName) {
		return false
	}
	videoPath, err := library.ResolveVideoFile(entryPath)
	if err != nil {
		return false
	}
	fileBase := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	return fileBase == folderName
}

// MatchesSeriesSchema reports whether videoPath — an individual episode
// file, already resolved via library.ResolveEpisodeVideoFiles — is already
// organized per preset: its own file name and its immediate "Season NN"
// parent both match the expected shape, and (Jellyfin only, since Legacy's
// series folder is a bare title with no fixed shape to check) its series
// folder grandparent does too.
func MatchesSeriesSchema(videoPath string, preset Preset) bool {
	fileBase := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	seasonDir := filepath.Base(filepath.Dir(videoPath))
	if !seasonDirPattern.MatchString(seasonDir) {
		return false
	}
	if preset == Legacy {
		return episodeFileLegacy.MatchString(fileBase)
	}
	seriesDir := filepath.Base(filepath.Dir(filepath.Dir(videoPath)))
	return episodeFileJellyfin.MatchString(fileBase) && seriesFolderJellyfin.MatchString(seriesDir)
}

// MatchesAdultSchema reports whether path's filename already carries the
// "[phash-HASH]" tag AdultFileName embeds — the Adult counterpart to
// MatchesMovieSchema's [tmdbid-N] check, wired into ScanLibraryAdult so a
// scene already named to SAK's fixed Adult scheme is never re-proposed. A
// scene is a flat one-file thing (no wrapping folder to verify against, unlike
// Movies), so this is a pure name check on the basename — structural only, it
// never confirms the embedded hash is actually correct for this content.
func MatchesAdultSchema(path string) bool {
	return adultPhashTag.MatchString(filepath.Base(path))
}
