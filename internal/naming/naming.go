// Package naming formats and recognizes SAK's on-disk media naming
// conventions for Movies and Series — a small, fixed set of hand-built
// presets (Jellyfin/Emby's own documented convention, and SAK's original
// "Legacy" convention), never a free-form template engine.
//
// The Jellyfin/Emby shapes here are based on an AI-paraphrased summary of
// https://jellyfin.org/docs/general/server/media/movies/ and .../shows/,
// not verbatim doc text — per this project's honesty-about-unverified-
// assumptions convention, a spot-check against the live docs is worth doing
// if exactness against Jellyfin's actual parser ever matters more than it
// does today.
package naming

import (
	"fmt"
	"strings"
)

// Preset selects which on-disk naming convention MovieFolderName/
// MovieFileName/SeriesFolderName/EpisodeFileName produce.
type Preset string

const (
	// Jellyfin is the default: "Title (Year) [tmdbid-NNNN]" for a Movie's
	// folder and file; for Series, the same shape for the series folder,
	// "Season NN" for the season folder, and "Series Title SxxExx Episode
	// Title" (space-separated) for the episode file.
	Jellyfin Preset = "jellyfin"
	// Legacy is SAK's original convention: Movies get "Title (Year)" with
	// no tmdbid tag; Series keep the exact shape this project used before
	// Jellyfin/Emby alignment existed — a bare title folder (no year, no
	// tag), "Season NN", and "Series Title - SxxExx - Episode Title"
	// (dash-separated). An explicit opt-in, so an already-renamed library's
	// on-disk shape never silently changes after an upgrade.
	Legacy Preset = "legacy"
)

// Presets lists every recognized Preset, in the order a settings picker
// should offer them.
var Presets = []Preset{Jellyfin, Legacy}

// Valid reports whether p is one of Presets.
func Valid(p Preset) bool {
	for _, known := range Presets {
		if p == known {
			return true
		}
	}
	return false
}

// SafePathComponent rewrites a single path segment so it cannot introduce
// extra directory levels or null bytes when joined under a library root.
//
// Claude 2026-08-05: replace / \ and NUL in titles used for on-disk names
// Reason: TMDB titles like "9/11: …" made MovieFolderName/MovieFileName nest under Movies/9/… and Apply failed
// Troubleshooting: apply-batch 0/1 with leftover Movies/9/<title> folders — sanitize before Join
// Review if: Jellyfin's own path sanitization rules are adopted as a stricter shared helper
func SafePathComponent(s string) string {
	return strings.NewReplacer("/", "-", "\\", "-", "\x00", "_").Replace(s)
}

// MovieFolderName formats a movie's wrapping folder name. year/tmdbID of 0
// are omitted gracefully (e.g. TMDB reporting no release date, or a
// pre-registration call site that doesn't have the id yet) rather than
// rendering a placeholder like "(0)".
func MovieFolderName(preset Preset, title string, year, tmdbID int) string {
	name := SafePathComponent(title)
	if year != 0 {
		name = fmt.Sprintf("%s (%d)", name, year)
	}
	if preset == Jellyfin && tmdbID != 0 {
		name = fmt.Sprintf("%s [tmdbid-%d]", name, tmdbID)
	}
	return name
}

// MovieFileName formats a movie's target file name — Jellyfin/Emby's
// convention names the file identically to its wrapping folder.
func MovieFileName(preset Preset, title string, year, tmdbID int, ext string) string {
	return MovieFolderName(preset, title, year, tmdbID) + ext
}

// SeriesFolderName formats a series' wrapping folder name. Legacy keeps
// today's exact shape (a bare title, no year or tag); Jellyfin uses the
// same "Title (Year) [tmdbid-NNNN]" shape as a movie folder.
func SeriesFolderName(preset Preset, title string, year, tmdbID int) string {
	if preset == Legacy {
		return SafePathComponent(title)
	}
	return MovieFolderName(preset, title, year, tmdbID)
}

// SeasonDirName formats a season number the way episode files get
// organized on disk: "Season 03" — identical under either preset, since
// Jellyfin's own documented convention requires exactly this shape too.
func SeasonDirName(seasonNumber int) string {
	return fmt.Sprintf("Season %02d", seasonNumber)
}

// AdultFileName formats an Adult scene's target file name. Unlike Movies/
// Series, Adult has no user-chosen Preset — there's no external convention
// (like Jellyfin's) to align with, so it gets one fixed scheme:
// "Studio - Title (Date) [phash-HASH].ext", with the phash embedded directly
// per this project's documented intent (a filename-embedded phash for fast
// rescans; see CLAUDE.md). Optional fields are omitted gracefully rather than
// rendering placeholders — the same convention MovieFolderName follows for a
// zero year/tmdbID: an empty studio drops the "Studio - " prefix, an empty
// date drops the " (Date)" segment, and an empty phash drops the
// "[phash-...]" tag (so a scene that couldn't be hashed is still named, just
// without the tag, and is simply re-proposed on the next scan). ext is
// threaded and appended exactly as MovieFileName does.
func AdultFileName(studio, title, date, phash, ext string) string {
	name := SafePathComponent(title)
	if studio != "" {
		name = fmt.Sprintf("%s - %s", SafePathComponent(studio), name)
	}
	if date != "" {
		name = fmt.Sprintf("%s (%s)", name, date)
	}
	if phash != "" {
		name = fmt.Sprintf("%s [phash-%s]", name, phash)
	}
	return name + ext
}

// EpisodeFileName formats one episode's target file name: Jellyfin's
// documented convention is space-separated ("Series Title S03E05 Episode
// Title.ext"); Legacy keeps this project's original dash-separated shape
// ("Series Title - SxxExx - Episode Title.ext").
func EpisodeFileName(preset Preset, seriesTitle string, seasonNumber, episodeNumber int, episodeTitle, ext string) string {
	seriesTitle = SafePathComponent(seriesTitle)
	episodeTitle = SafePathComponent(episodeTitle)
	var base string
	if preset == Legacy {
		base = fmt.Sprintf("%s - S%02dE%02d", seriesTitle, seasonNumber, episodeNumber)
		if episodeTitle != "" {
			base = fmt.Sprintf("%s - %s", base, episodeTitle)
		}
	} else {
		base = fmt.Sprintf("%s S%02dE%02d", seriesTitle, seasonNumber, episodeNumber)
		if episodeTitle != "" {
			base = fmt.Sprintf("%s %s", base, episodeTitle)
		}
	}
	return base + ext
}

// EpisodeRangeFileName is EpisodeFileName's sibling for a logical-episode-
// split file — one physical file bundling more than one episode number
// (library.ParseEpisodeNumbers), e.g. a "S01E01-E02" release. Renders
// "Series Title S03E05-E06.ext" (Jellyfin) / "Series Title - S03E05-E06.ext"
// (Legacy) from episodeNumbers' first and last entries (expects an already
// ascending-sorted, deduped slice — ParseEpisodeNumbers' own contract).
// Falls straight through to EpisodeFileName's ordinary single-episode
// rendering when episodeNumbers has fewer than 2 entries, so a caller can
// use this unconditionally without a separate single-vs-multi branch of its
// own. schema.go's episodeFileJellyfin/episodeFileLegacy recognize this
// range shape too, so a correctly-split, already-renamed file is seen as
// schema-conformant and not endlessly re-proposed on a later Scan.
func EpisodeRangeFileName(preset Preset, seriesTitle string, seasonNumber int, episodeNumbers []int, episodeTitle, ext string) string {
	if len(episodeNumbers) < 2 {
		episodeNumber := 0
		if len(episodeNumbers) == 1 {
			episodeNumber = episodeNumbers[0]
		}
		return EpisodeFileName(preset, seriesTitle, seasonNumber, episodeNumber, episodeTitle, ext)
	}
	first, last := episodeNumbers[0], episodeNumbers[len(episodeNumbers)-1]
	seriesTitle = SafePathComponent(seriesTitle)
	episodeTitle = SafePathComponent(episodeTitle)
	var base string
	if preset == Legacy {
		base = fmt.Sprintf("%s - S%02dE%02d-E%02d", seriesTitle, seasonNumber, first, last)
		if episodeTitle != "" {
			base = fmt.Sprintf("%s - %s", base, episodeTitle)
		}
	} else {
		base = fmt.Sprintf("%s S%02dE%02d-E%02d", seriesTitle, seasonNumber, first, last)
		if episodeTitle != "" {
			base = fmt.Sprintf("%s %s", base, episodeTitle)
		}
	}
	return base + ext
}
