package rename

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/quality"
)

// Series alternate-version support — the Series counterpart to
// rename_alternates.go.
//
// FILE OWNERSHIP NOTE: this file currently holds ONLY §5.1's
// acceptDuplicatePendingEpisode (the Scan-side helper the seven softened
// `tracked` guard sites call). §6's applyLibrarySeriesAlternate lands
// ALONGSIDE it, in this same file, per the plan's own placement instruction —
// append to this file, never overwrite it.

// Claude 2026-08-07: Series Scan-time duplicate slots become Pending alternates
// Reason: deep-interview-sakms-series-parsing-accuracy-improvements §5.1 —
//   Series gains Movies-parity alternate-version support, so a file landing on
//   an episode slot that already holds a file is a legitimate alternate rather
//   than a conflict. Movies has resolved this at APPLY time all along
//   (acceptDuplicatePending, rename_alternates.go:178); Series now does the
//   same, and ApplyLibrarySeries' fold-in gate resolves the collision against
//   LIVE DB state instead of a Scan-time snapshot.
// Troubleshooting: a second same-slot file Applied and OVERWROTE the first's
//   library_episodes row instead of folding in as an alternate — that means the
//   Apply-side fold-in gate (existing.FilePath != "") did not fire; check §0.3.
// Review if: operators report unwanted alternates accumulating and want
//   same-slot duplicates declined at Scan time again.

// acceptDuplicatePendingEpisode fills a Pending proposal for an episode slot
// already holding a file — the Series counterpart to acceptDuplicatePending.
// It sets ONLY status and reason: unlike the Movies version it does NOT
// overwrite Title/TMDBID/Year/RootFolderPath, because Series' SEVEN call sites
// each populate those from a different source (an .nfo hint, a TMDB match, a
// TVDB anthology match, a pinned tracked row, ...) and clobbering them would
// corrupt the show folder — the exact failure
// autopilot-impl-episode-title-matching.md §0.3 records.
//
// This asymmetry with the Movies helper is deliberate and must be preserved.
// Movies' acceptDuplicatePending sets Title/TMDBID/Year/RootFolderPath because
// its four call sites reach it BEFORE those are assigned. Series' seven call
// sites each already have a correct pinned show. Do not "unify" the two.
//
// showTitle is bound PER SITE from that site's own in-scope title variable.
// Never hoist one shared identifier for all of them.
//
// Reason is written WHOLESALE, not wrapped. Two consequences worth knowing
// before reading a softened row's reason: Site 5's own
// episodeTitleMatchReasonPrefix and Site 7's aiEpisodeMatchReasonPrefix are
// both replaced rather than preserved (Site 5 reached via aiEpisodeMode1 is
// the exception — that caller wraps this helper's output afterwards, so those
// rows read "<aiPrefix> ...; alternate: ...").
func acceptDuplicatePendingEpisode(p *proposals.Proposal, showTitle string, season, episode int) {
	p.Status = proposals.Pending
	p.Reason = fmt.Sprintf(
		"alternate: %q S%02dE%02d already has a file in the library — apply will fold as primary or alternate by quality",
		showTitle, season, episode)
}

// Claude 2026-08-07: Series alternate fold-in (promote/demote by probed tier)
// Reason: deep-interview-sakms-series-parsing-accuracy-improvements — Movies parity for
//   episode slots; duplicates stay under the show, distinctly named, no Dedup/Purge involvement.
// Troubleshooting: the second same-slot Apply overwrote the first's library_episodes row
//   instead of folding — the caller's gate (existing.FilePath != "") did not fire.
// Review if: equal-tier policy changes from keep-existing-primary.

// applyLibrarySeriesAlternate folds sourcePath into an episode slot that already
// holds a file. Equal probed tier keeps the existing primary (the incoming file
// becomes the alternate) — identical policy to applyLibraryAlternate.
//
// existing is the library_episodes row for the FIRST occupied slot in the range
// (ApplyLibrarySeries' gate), already confirmed by the caller to have a non-empty
// FilePath (a fileless catalog row is NOT an occupied slot). It is the row whose
// file the promote/demote tier comparison is made against.
//
// resolved maps EVERY episode number in episodeNumbers to its library_episodes
// row, or to nil where that number has no row at all. The caller's gate builds it
// in one full pass with NO early break; the 5b write loop below consumes it to
// decide which alternate rows to write (a number mapping to nil is SKIPPED — no
// episode_id exists for it). It is a REQUIRED argument, not an optimisation:
// without it this function cannot distinguish "has a row" from "has no row" and
// would silently mis-handle a partially-catalogued range.
//
// resolved and existing are NOT redundant. existing answers "which file do I
// compare tiers against?" — exactly one row. resolved answers "which episode
// slots do I write file rows for?" — every number in the range. Collapsing them
// loses one or the other. resolved[firstOccupiedNumber] is existing.
//
// series is passed in (rather than re-fetched) because ApplyLibrarySeries already
// calls UpsertSeries before reaching this point and holds the row. episodeTitle is
// passed in because the caller already resolved it via resolveEpisodeMeta for the
// destination filename.
//
// Reuses probeFileMeta, moveUnique and fileMeta from rename_alternates.go
// unchanged — same package, deliberately not duplicated.
func applyLibrarySeriesAlternate(
	ctx context.Context,
	libStore *library.Store,
	series library.Series,
	existing *library.Episode,
	resolved map[int]*library.Episode,
	p proposals.Proposal,
	sourcePath string,
	episodeNumbers []int,
	episodeTitle string,
	preset naming.Preset,
	settingsTier string,
	prober Prober,
) (episodeID int64, changes []mode.PathChange, err error) {
	seasonDir := filepath.Join(p.RootFolderPath,
		naming.SeriesFolderName(preset, p.Title, p.Year, p.TMDBID),
		naming.SeasonDirName(p.SeasonNumber))
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		return 0, nil, fmt.Errorf("creating %q: %w", seasonDir, err)
	}

	orphanMeta := probeFileMeta(ctx, prober, sourcePath, settingsTier)
	primaryPath := existing.FilePath
	if primaryPath == "" {
		// Defensive only — the caller already gated on a non-empty FilePath.
		files, listErr := libStore.ListEpisodeFiles(ctx, existing.ID)
		if listErr == nil {
			for _, f := range files {
				if f.IsPrimary {
					primaryPath = f.FilePath
					break
				}
			}
		}
	}
	primaryMeta := probeFileMeta(ctx, prober, primaryPath, existing.QualityTier)
	if primaryMeta.Tier == "" {
		primaryMeta.Tier = existing.QualityTier
	}

	// Strictly greater: equal tier keeps the existing primary. Same policy,
	// same line, as applyLibraryAlternate.
	orphanWins := quality.RankString(orphanMeta.Tier) > quality.RankString(primaryMeta.Tier)

	// RANGE PROPOSALS NEVER PROMOTE. Promoting a range file would require
	// demoting a primary that may itself span a DIFFERENT set of episode
	// numbers, so "which slots get their file_path rewritten" has no single
	// correct answer. Refusing sidesteps the ambiguity and is strictly safe:
	// the existing primary is untouched and the incoming file still lands as a
	// correctly-named alternate. Movies has no analogue, so this is a decision
	// rather than a mirrored line.
	// Review if: range-vs-range promotion is ever specified.
	if len(episodeNumbers) > 1 {
		orphanWins = false
	}

	// REFUSE TO PROMOTE WHEN THE EXISTING PRIMARY IS A SHARED (BUNDLED) FILE.
	// Series-only hazard with no Movies analogue, so mirroring cannot catch it.
	// Reachable case: the tracked primary is a bundled "S01E01-E02" file and a
	// higher-tier single-episode file for E01 arrives. The promote branch would
	// moveUnique the bundled file to an alternate name (its path CHANGES) and
	// then call UpdateEpisodePrimaryPath for E01's row ONLY — leaving E02's
	// library_episodes.file_path pointing at a path that no longer exists,
	// silently. CountEpisodesByFilePath exists precisely to detect a shared path
	// and is already used this way by internal/dedup.
	//
	// The primaryPath != "" guard is load-bearing, not tidiness: fileless
	// catalog rows legitimately carry file_path = '', so counting the empty
	// string would return a large number on any catalog-synced show and refuse
	// every promotion outright.
	// Review if: cross-slot promotion of bundled primaries is ever specified.
	if orphanWins && primaryPath != "" {
		if n, cerr := libStore.CountEpisodesByFilePath(ctx, primaryPath); cerr == nil && n > 1 {
			orphanWins = false
		}
	}

	primaryDest := filepath.Join(seasonDir,
		naming.EpisodeRangeFileName(preset, p.Title, p.SeasonNumber, episodeNumbers, episodeTitle, filepath.Ext(sourcePath)))

	if orphanWins {
		// Move the existing primary to an alternate name first, to free the
		// primary filename. Unreachable for a range proposal (see the
		// len(episodeNumbers) > 1 refusal above), so both writes below are
		// correctly keyed on existing.ID — a single slot's primary is a
		// single-slot concept and nothing fans out across resolved here.
		altName := naming.EpisodeAlternateFileName(preset, p.Title, p.SeasonNumber, episodeNumbers, episodeTitle,
			quality.ResolutionLabel(primaryMeta.Height), quality.CodecLabel(primaryMeta.Codec),
			quality.BitrateLabel(primaryMeta.BitRate), filepath.Ext(primaryPath))
		movedPrimary, ch, moveErr := moveUnique(primaryPath, filepath.Join(seasonDir, altName))
		if moveErr != nil {
			return 0, changes, fmt.Errorf("demoting previous primary %q: %w", primaryPath, moveErr)
		}
		changes = append(changes, ch...)
		primaryPath = movedPrimary

		movedOrphan, ch, moveErr := moveUnique(sourcePath, primaryDest)
		if moveErr != nil {
			return 0, changes, fmt.Errorf("promoting orphan to primary %q: %w", primaryDest, moveErr)
		}
		changes = append(changes, ch...)

		if err := libStore.UpdateEpisodePrimaryPath(ctx, existing.ID, movedOrphan, orphanMeta.Tier, library.FileSize(movedOrphan)); err != nil {
			return 0, changes, err
		}
		// UpsertEpisodeFile's own demote-siblings step means the IsPrimary:true
		// write must come FIRST, exactly as in Movies.
		if _, err := libStore.UpsertEpisodeFile(ctx, library.EpisodeFile{
			EpisodeID: existing.ID, FilePath: movedOrphan, IsPrimary: true,
			QualityTier: orphanMeta.Tier, Size: library.FileSize(movedOrphan),
			Width: orphanMeta.Width, Height: orphanMeta.Height, VideoCodec: orphanMeta.Codec,
			BitRate: orphanMeta.BitRate, DurationSec: orphanMeta.Duration,
		}); err != nil {
			return 0, changes, err
		}
		if _, err := libStore.UpsertEpisodeFile(ctx, library.EpisodeFile{
			EpisodeID: existing.ID, FilePath: primaryPath, IsPrimary: false,
			QualityTier: primaryMeta.Tier, Size: library.FileSize(primaryPath),
			Width: primaryMeta.Width, Height: primaryMeta.Height, VideoCodec: primaryMeta.Codec,
			BitRate: primaryMeta.BitRate, DurationSec: primaryMeta.Duration,
		}); err != nil {
			return 0, changes, err
		}
		return existing.ID, changes, nil
	}

	// Orphan loses or ties — keep the existing primary; place the orphan as an
	// alternate.
	altName := naming.EpisodeAlternateFileName(preset, p.Title, p.SeasonNumber, episodeNumbers, episodeTitle,
		quality.ResolutionLabel(orphanMeta.Height), quality.CodecLabel(orphanMeta.Codec),
		quality.BitrateLabel(orphanMeta.BitRate), filepath.Ext(sourcePath))
	altDest := filepath.Join(seasonDir, altName)
	movedOrphan, ch, moveErr := moveUnique(sourcePath, altDest)
	if moveErr != nil {
		return 0, changes, fmt.Errorf("placing alternate %q: %w", altDest, moveErr)
	}
	changes = append(changes, ch...)

	// Refresh the primary row's probe labels if we have them.
	if primaryPath != "" {
		if _, err := libStore.UpsertEpisodeFile(ctx, library.EpisodeFile{
			EpisodeID: existing.ID, FilePath: primaryPath, IsPrimary: true,
			QualityTier: primaryMeta.Tier, Size: library.FileSize(primaryPath),
			Width: primaryMeta.Width, Height: primaryMeta.Height, VideoCodec: primaryMeta.Codec,
			BitRate: primaryMeta.BitRate, DurationSec: primaryMeta.Duration,
		}); err != nil {
			return 0, changes, err
		}
	}

	// ONE NON-PRIMARY ROW PER BUNDLED EPISODE THAT HAS ONE — never a single row
	// keyed on existing.ID. The singular form is a bug for range proposals: it
	// would leave every other bundled slot with no record of the alternate at
	// all. The single-episode case is this loop with exactly one iteration, so
	// there is no len(episodeNumbers) == 1 special case and none is wanted.
	//
	// A number with NO library_episodes row at all is SKIPPED, never minted.
	// Minting one would create an episode whose only file is an alternate — a
	// slot with a non-primary file and no primary — which is incoherent against
	// the one-primary-per-slot model the partial unique index encodes, and it
	// would fabricate library state for an episode nothing has ever tracked.
	// Skipping is strictly safe: the file is on disk under its alternate name
	// and the next Scan discovers it once the catalog syncs. So a range
	// alternate writes between 1 and N rows, not always N — at least one is
	// guaranteed, because this path is only reachable when some slot in the
	// range is occupied, and an occupied slot has a row by definition.
	orphanSize := library.FileSize(movedOrphan)
	for _, n := range episodeNumbers {
		ep := resolved[n]
		if ep == nil {
			log.Printf("rename: no library_episodes row for series %d S%02dE%02d; skipping alternate file row for %q",
				series.ID, p.SeasonNumber, n, movedOrphan)
			continue
		}
		if _, err := libStore.UpsertEpisodeFile(ctx, library.EpisodeFile{
			EpisodeID: ep.ID, FilePath: movedOrphan, IsPrimary: false,
			QualityTier: orphanMeta.Tier, Size: orphanSize,
			Width: orphanMeta.Width, Height: orphanMeta.Height, VideoCodec: orphanMeta.Codec,
			BitRate: orphanMeta.BitRate, DurationSec: orphanMeta.Duration,
		}); err != nil {
			return 0, changes, err
		}
	}
	return existing.ID, changes, nil
}

// fileExists reports whether path names something that currently stats. Used by
// ApplyLibrarySeries' fold-in gate so a stale library_episodes row pointing at a
// deleted file is not treated as an occupied slot. Movies' applyLibraryAlternate
// tolerates the same situation via its ListFiles fallback instead, which is
// weaker — this is an intentional divergence, not an inconsistency.
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
