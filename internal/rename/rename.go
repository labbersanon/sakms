// Package rename implements SAK's Rename workflow: propose relocating
// orphaned (unmapped) files into their mode's own SAK library under the
// active naming scheme, then — once a human approves a specific proposal —
// actually relocate and record it.
//
// Scan never mutates anything; it only reads and produces proposals.Proposal
// values. Apply is the only function that moves a file and writes the library
// record, and it only ever acts on one already-approved proposal at a time —
// there is no "apply everything" path, by design (see the design spec's
// staged-for-approval principle). Every mode runs its own libStore-backed
// ScanLibrary*/ApplyLibrary* sibling, dispatched at the API layer.
package rename

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/classify"
	"github.com/labbersanon/sakms/internal/config"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/nfo"
	"github.com/labbersanon/sakms/internal/place"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/searchterm"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/tvdb"
)

// yearFromReleaseDate parses the release year out of TMDB's normalized
// "YYYY-MM-DD" ReleaseDate string, returning 0 if it's empty or malformed —
// kept local to this package rather than shared with internal/dedup.
func yearFromReleaseDate(releaseDate string) int {
	if len(releaseDate) < 4 {
		return 0
	}
	year, err := strconv.Atoi(releaseDate[:4])
	if err != nil {
		return 0
	}
	return year
}

// classifyAdultMatch maps a completed Identify result to a proposal's
// identification-derived fields, or to an Unmatched reason. A match without a
// valid stash-box scene identifier (web_search-only, SceneID=="" || Box=="")
// is a correctness requirement to reject: it has no valid Whisparr ForeignID.
func classifyAdultMatch(res *identify.MatchResult, err error) (status proposals.Status, reason, title, foreignID, itemType string) {
	switch {
	case err != nil:
		return proposals.Unmatched, fmt.Sprintf("identification failed: %v", err), "", "", ""
	case res == nil:
		return proposals.Unmatched, "no confident identification", "", "", ""
	}
	foreignID, hasID := res.WhisparrForeignID()
	if !hasID {
		return proposals.Unmatched, "web-identified only (no scene ID) — needs manual review", "", "", ""
	}
	return proposals.Pending, "", res.Title, foreignID, res.Type
}

// submitFingerprintGiveBack submits p's phash back to whichever community box
// it was first fingerprint-matched against (from the GiveBackBox/GiveBackSceneID
// captured on the proposal when it was a phash-cascade hit). A no-op, not an
// error, whenever p
// wasn't a phash-cascade hit (GiveBackBox/GiveBackSceneID/PHash unset), give-back
// isn't configured, or the submission itself fails — the item is already
// genuinely registered by the time this runs, so a give-back failure must
// never surface as an Apply error. ok reports whether it actually succeeded.
func submitFingerprintGiveBack(ctx context.Context, sess *mode.Session, p proposals.Proposal) (ok bool) {
	if p.GiveBackBox == "" || p.GiveBackSceneID == "" || p.PHash == "" || p.DurationSeconds <= 0 {
		return false
	}
	if sess.Identify == nil || sess.Identify.GiveBack == nil {
		return false
	}
	err := sess.Identify.GiveBack.SubmitFingerprint(ctx, p.GiveBackBox, p.GiveBackSceneID, p.PHash, p.DurationSeconds)
	return err == nil
}

// SubmitDraft gives an Adult proposal's identification back to the community
// databases (TPDB preferred, StashDB as fallback — see identify.GiveBack) when
// AI+web-search confidently identified a file (Title/Studio present) but it
// matched no existing scene anywhere. This is a distinct, human-triggered
// action from Apply — unlike the original CLI, which submitted automatically
// during its scan, SAK never fires an outbound mutation without an
// explicit human decision (see the design spec's staged-for-approval
// principle). p must be Unmatched and not already have a DraftID — submitting
// a draft twice for the same proposal is refused rather than silently
// duplicating it on the remote database.
func SubmitDraft(ctx context.Context, sess *mode.Session, p proposals.Proposal) (string, error) {
	if p.Workflow != proposals.Rename {
		return "", fmt.Errorf("proposal %d is a %q proposal, not rename — cannot submit a draft", p.ID, p.Workflow)
	}
	if p.Status != proposals.Unmatched {
		return "", fmt.Errorf("proposal %d is %q, not unmatched — nothing to give back", p.ID, p.Status)
	}
	if p.DraftID != "" {
		return "", fmt.Errorf("proposal %d already has a draft (%s) — refusing to submit a duplicate", p.ID, p.DraftID)
	}
	if p.Title == "" {
		return "", fmt.Errorf("proposal %d has no identified title — nothing to give back", p.ID)
	}
	if sess.Identify == nil || sess.Identify.GiveBack == nil {
		return "", fmt.Errorf("give-back isn't configured — add a TPDB or StashDB connection in Settings")
	}
	return sess.Identify.GiveBack.SubmitDraft(ctx, p.Title, p.Studio, p.Date)
}

// Relocate physically moves sourcePath into destRoot, preserving its current
// basename, and returns the new path. filepath.Base already strips any
// directory components from sourcePath, so the destination Join is safe
// against a traversal-shaped source path by construction. Collision-checked
// via place.UniquePath — a Kids and general root can easily already contain
// something with the same name. Exported so the native search-and-grab
// import step (internal/api's check-import handler) can reuse the exact same
// move logic once a download completes, instead of duplicating it.
func Relocate(sourcePath, destRoot string) (string, error) {
	dest := filepath.Join(destRoot, filepath.Base(sourcePath))
	unique, err := place.UniquePath(dest, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	if err != nil {
		return "", err
	}
	if err := os.Rename(sourcePath, unique); err != nil {
		return "", fmt.Errorf("moving %q to %q: %w", sourcePath, unique, err)
	}
	return unique, nil
}

// ScanLibrary is Rename's Movies-library scan — used only for Movies mode,
// now that Radarr no longer sits between SAK and the filesystem/TMDB (see
// internal/library's package doc). It walks rootFolderPath (and
// sess.KidsRootPath, if configured and different) for files libStore doesn't
// already know about, resolves each via TMDB search instead of Servarr's
// Lookup, and skips anything whose TMDB id is already in the library.
//
// It does NOT audit already-tracked items for kids/general drift: TMDB's
// search response carries no certification/genre data the way *arr's own
// Lookup did, and fetching it per already-tracked item on every scan would
// be an expensive N+1 against
// TMDB. This is a deliberate v1 simplification — new orphans are still
// classified (via AI, using title+overview only, since certification/genre
// metadata isn't available here without an extra per-item call), just not
// already-tracked items.
func ScanLibrary(ctx context.Context, sess *mode.Session, libStore *library.Store, rootFolderPath string, preset naming.Preset, cfg MatchConfig, prober Prober) ([]proposals.Proposal, error) {
	cfg = cfg.Normalize()
	if sess.TMDB == nil {
		return nil, fmt.Errorf("tmdb isn't configured yet — add it in Settings first")
	}
	if rootFolderPath == "" {
		return nil, fmt.Errorf("no Movies library root folder configured yet — add one in Settings first")
	}

	existing, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		return nil, fmt.Errorf("loading library items: %w", err)
	}
	byTMDB := make(map[int]bool, len(existing))
	for _, item := range existing {
		byTMDB[item.TMDBID] = true
	}
	// Claude 2026-08-05: mark primary + alternate paths known so alts aren't re-proposed
	// Reason: deep-interview-rename-alts-ai-fallback — library_item_files holds non-primary paths
	// Troubleshooting: alternate files reappeared as Rename orphans after Apply
	// Review if: AllFilePaths stops covering library_item_files
	known := map[string]bool{}
	if paths, err := libStore.AllFilePaths(ctx, mode.Movies); err == nil {
		for _, p := range paths {
			known[p] = true
		}
	} else {
		for _, item := range existing {
			known[item.FilePath] = true
		}
	}

	roots := []string{rootFolderPath}
	if sess.KidsRootPath != "" && sess.KidsRootPath != rootFolderPath {
		roots = append(roots, sess.KidsRootPath)
	}

	var out []proposals.Proposal
	for _, root := range roots {
		entries, err := library.ScanRootFolder(root, known)
		if err != nil {
			return nil, fmt.Errorf("scanning %s: %w", root, err)
		}
		for _, entry := range entries {
			if config.SidecarExts[strings.ToLower(filepath.Ext(entry.Name))] {
				continue
			}
			// Claude 2026-08-05: resolve to allowlisted video before propose; silent omit non-video
			// Reason: Movies scan previously proposed folders/.plexmatch/trickplay tiles without ResolveVideoFile
			// Troubleshooting: Non-video Rename rows — next Organize scan rebuilds the list without them
			// Review if: ScanLibrary stops using ResolveVideoFile or silent-omit policy changes
			videoPath, err := library.ResolveVideoFile(entry.Path)
			if err != nil {
				continue
			}
			if searchterm.IsSampleVideo(videoPath) {
				// Claude 2026-08-05: skip release .sample. clips — not library features
				// Reason: sample.mp4 orphans pollute Movies Rename unmatched/pending
				// Troubleshooting: unmatched rows for *.sample.mp4 — IsSampleVideo gate
				// Review if: sample detection moves into ResolveVideoFile
				continue
			}
			// Schema check needs the atomic folder (or file) entry, not the resolved video path —
			// MatchesMovieSchema only recognizes organized movie directories.
			if naming.MatchesMovieSchema(entry.Path, preset) {
				continue // already organized under the active preset — nothing to propose
			}
			// Keep entry.Name for TMDB search (folder release names); Path is the video file.
			out = append(out, proposeOneLibrary(ctx, sess, byTMDB, rootFolderPath, root, library.UnmappedEntry{
				Name: entry.Name,
				Path: videoPath,
			}, cfg, prober))
		}
	}
	return out, nil
}

func proposeOneLibrary(
	ctx context.Context, sess *mode.Session, byTMDB map[int]bool,
	generalRoot, foundRoot string, entry library.UnmappedEntry, cfg MatchConfig, prober Prober,
) proposals.Proposal {
	cfg = cfg.Normalize()
	p := proposals.Proposal{
		Mode: mode.Movies, Workflow: proposals.Rename,
		SourceName: entry.Name, SourcePath: entry.Path, RootFolderPath: foundRoot,
	}

	// NFO fast-path: if a Kodi/Jellyfin .nfo sidecar is present and carries
	// a TMDB ID, use it directly — no fuzzy filename search, no drilldown
	// gate. The ID in the .nfo is authoritative; still run the duplicate and
	// kids-root checks that follow.
	if hint := nfo.ReadSidecar(entry.Path); hint.TMDBID != 0 {
		details, err := sess.TMDB.MovieDetails(ctx, hint.TMDBID)
		if err != nil {
			p.Status = proposals.Unmatched
			p.Reason = fmt.Sprintf(".nfo TMDB id %d: lookup failed: %v", hint.TMDBID, err)
			return p
		}
		targetRoot := generalRoot
		switch {
		case foundRoot == sess.KidsRootPath:
			targetRoot = sess.KidsRootPath
		case sess.KidsRootPath != "" && sess.MainstreamAI != nil:
			if result, err := classify.WithAI(ctx, sess.MainstreamAI, details.Title, details.Overview); err == nil && result.IsKids {
				targetRoot = sess.KidsRootPath
			}
		}
		year := yearFromReleaseDate(details.ReleaseDate)
		if byTMDB[hint.TMDBID] {
			// Claude 2026-08-05: NFO duplicate → Pending alternate, not Unmatched
			// Reason: deep-interview-rename-alts-ai-fallback
			// Troubleshooting: .nfo TMDB already tracked used to leave orphan forever
			// Review if: NFO duplicates should skip Apply entirely
			acceptDuplicatePending(&p, details.Title, details.ID, year, targetRoot)
			p.Genres = details.Genres
			if cast, err := sess.TMDB.MovieCredits(ctx, details.ID); err == nil {
				p.Cast = cast
			}
			return p
		}
		p.Status = proposals.Pending
		p.Title = details.Title
		p.TMDBID = details.ID
		p.Year = year
		p.RootFolderPath = targetRoot
		p.Genres = details.Genres
		if cast, err := sess.TMDB.MovieCredits(ctx, details.ID); err == nil {
			p.Cast = cast
		}
		return p
	}

	sig := ExtractFileSignals(ctx, entry.Name, entry.Path, prober)
	acceptMovie := func(match tmdb.Item, candYear int) proposals.Proposal {
		targetRoot := generalRoot
		switch {
		case foundRoot == sess.KidsRootPath:
			targetRoot = sess.KidsRootPath
		case sess.KidsRootPath != "" && sess.MainstreamAI != nil:
			if result, err := classify.WithAI(ctx, sess.MainstreamAI, match.Title, match.Overview); err == nil && result.IsKids {
				targetRoot = sess.KidsRootPath
			}
		}
		if byTMDB[match.ID] {
			// Claude 2026-08-05: duplicate TMDB → Pending alternate fold-in
			acceptDuplicatePending(&p, match.Title, match.ID, candYear, targetRoot)
		} else {
			p.Status = proposals.Pending
			p.Title = match.Title
			p.TMDBID = match.ID
			p.Year = candYear
			p.RootFolderPath = targetRoot
		}
		if det, err := sess.TMDB.MovieDetails(ctx, match.ID); err == nil {
			p.Genres = det.Genres
			if p.Year == 0 {
				p.Year = yearFromReleaseDate(det.ReleaseDate)
			}
		}
		if names, err := sess.TMDB.MovieCredits(ctx, match.ID); err == nil {
			p.Cast = names
		}
		return p
	}
	if !sig.HasAny() {
		// Claude 2026-08-05: no-signal orphans may still use GuessTitle / Brave / web-authority
		// Reason: deep-interview-rename-web-authority — HBO specials often lack year in the filename
		// Troubleshooting: "no year, actor, or duration" before web ran → Skelton Funny Faces unmatched
		// Review if: no-signal web Pending false-positives need a stricter gate
		guessed := ""
		if sess.MainstreamAI != nil {
			if g, gerr := identify.GuessTitle(ctx, sess.MainstreamAI, entry.Name); gerr == nil && g != "" {
				guessed = g
				if got := tryMovieQueries(ctx, sess, sig, cfg, acceptMovie, searchterm.SearchQueries(guessed)); got != nil {
					return *got
				}
			}
		}
		if got := bravePhase2Movie(ctx, sess, sig, cfg, acceptMovie, guessed, entry.Name); got != nil {
			return *got
		}
		if got := tryWebAuthorityMovie(ctx, sess, byTMDB, generalRoot, foundRoot, entry.Name, guessed, p); got != nil {
			return *got
		}
		p.Status = proposals.Unmatched
		if IsJunkRenameFilename(entry.Name) {
			p.Reason = fmt.Sprintf("filename too generic for web naming authority (%q) — needs manual review", entry.Name)
		} else {
			p.Reason = "no year, actor, or duration available to corroborate a match — needs manual review"
		}
		return p
	}

	queries := searchterm.SearchQueries(entry.Name)
	var lastTerm string
	var lastLimit int
	// Claude 2026-08-05: defer Weak (duration-overrode-year) until all queries scanned
	// Reason: Copacabana bare search ranked 1985 before 1947; Weak accepted first
	// Troubleshooting: Pending year ≠ file year while a later top-N hit matches year
	// Review if: Strong never appears and Weak false-positives need a harder gate
	type weakMovie struct {
		match    tmdb.Item
		candYear int
	}
	var weak *weakMovie
	for _, term := range queries {
		lastTerm = term
		items, err := sess.TMDB.SearchMovies(ctx, term)
		if err != nil {
			p.Status = proposals.Unmatched
			p.Reason = fmt.Sprintf("TMDB search failed for %q: %v", term, err)
			return p
		}
		if len(items) == 0 {
			continue
		}
		limit := pickLimit(cfg.CandidateN, len(items))
		lastLimit = limit
		for i := 0; i < limit; i++ {
			match := items[i]
			candYear, runtimeMin, cast := movieCandidateMeta(ctx, sess.TMDB, match.ID, match.ReleaseDate, sig)
			rank := SignalsCorroborate(sig, candYear, runtimeMin, cast, cfg.DurationTolerancePct)
			if rank == CorroborationNone {
				continue
			}
			if rank == CorroborationStrong {
				return acceptMovie(match, candYear)
			}
			if weak == nil {
				weak = &weakMovie{match: match, candYear: candYear}
			}
		}
	}
	if weak != nil {
		return acceptMovie(weak.match, weak.candYear)
	}

	if fb := tvdbFallbackMovie(ctx, sess, lastTerm, byTMDB, generalRoot, foundRoot, sig, cfg, p); fb != nil {
		return *fb
	}

	// Claude 2026-08-05: GuessTitle then Brave Phase 2 (deep-interview-rename-brave-phase2)
	// Reason: slight filename inaccuracy needs web grounding after GuessTitle near-miss
	// Troubleshooting: Jo Jo Dancer 2007 autobiography framing → 1986 film via Brave
	// Review if: Brave runs before GuessTitle
	guessed := ""
	if sess.MainstreamAI != nil {
		if g, gerr := identify.GuessTitle(ctx, sess.MainstreamAI, entry.Name); gerr == nil && g != "" {
			guessed = g
			if got := tryMovieQueries(ctx, sess, sig, cfg, acceptMovie, searchterm.SearchQueries(guessed)); got != nil {
				return *got
			}
		}
	}
	if got := bravePhase2Movie(ctx, sess, sig, cfg, acceptMovie, guessed, entry.Name); got != nil {
		return *got
	}
	if got := tryWebAuthorityMovie(ctx, sess, byTMDB, generalRoot, foundRoot, entry.Name, guessed, p); got != nil {
		return *got
	}

	unmatchedAfterWebAuthority(&p, entry.Name, lastLimit, lastTerm)
	return p
}

func tryMovieQueries(
	ctx context.Context, sess *mode.Session, sig FileSignals, cfg MatchConfig,
	acceptMovie func(tmdb.Item, int) proposals.Proposal, queries []string,
) *proposals.Proposal {
	cfg = cfg.Normalize()
	type weakHit struct {
		match    tmdb.Item
		candYear int
	}
	var weak *weakHit
	for _, term := range queries {
		items, err := sess.TMDB.SearchMovies(ctx, term)
		if err != nil || len(items) == 0 {
			continue
		}
		limit := pickLimit(cfg.CandidateN, len(items))
		for i := 0; i < limit; i++ {
			match := items[i]
			candYear, runtimeMin, cast := movieCandidateMeta(ctx, sess.TMDB, match.ID, match.ReleaseDate, sig)
			rank := SignalsCorroborate(sig, candYear, runtimeMin, cast, cfg.DurationTolerancePct)
			if rank == CorroborationNone {
				continue
			}
			if rank == CorroborationStrong {
				out := acceptMovie(match, candYear)
				return &out
			}
			if weak == nil {
				weak = &weakHit{match: match, candYear: candYear}
			}
		}
	}
	if weak != nil {
		out := acceptMovie(weak.match, weak.candYear)
		return &out
	}
	return nil
}

// bravePhase2Movie grounds via Brave then TMDB with Phase-2 year trust
// (filename year ignored when this path produced the candidate).
func bravePhase2Movie(
	ctx context.Context, sess *mode.Session, sig FileSignals, cfg MatchConfig,
	acceptMovie func(tmdb.Item, int) proposals.Proposal, guessed, entryName string,
) *proposals.Proposal {
	if sess.WebSearch == nil || sess.MainstreamAI == nil {
		return nil
	}
	query := strings.TrimSpace(guessed)
	if query == "" {
		query = entryName
	}
	grounded, err := identify.GroundTitleViaSearch(ctx, sess.WebSearch, sess.MainstreamAI, query)
	if err != nil || grounded.Title == "" {
		return nil
	}
	cfg = cfg.Normalize()
	for _, term := range searchterm.SearchQueries(grounded.Title) {
		items, err := sess.TMDB.SearchMovies(ctx, term)
		if err != nil || len(items) == 0 {
			continue
		}
		limit := pickLimit(cfg.CandidateN, len(items))
		for i := 0; i < limit; i++ {
			match := items[i]
			candYear, _, _ := movieCandidateMeta(ctx, sess.TMDB, match.ID, match.ReleaseDate, sig)
			year := candYear
			if year == 0 {
				year = grounded.Year
			}
			out := acceptMovie(match, year)
			return &out
		}
	}
	return nil
}

// RelocateMovie moves sourcePath into a preset-formatted wrapping folder
// under destRoot — Movies' counterpart to RelocateEpisode, giving Movies
// real renaming behavior for the first time (Relocate alone only ever moves
// a file, preserving whatever name it already had). If the computed
// destination already equals sourcePath (the file is already correctly
// placed and named), this is a no-op — comparing paths up front, rather
// than always calling os.Rename, avoids place.UniquePath mistaking a file
// for colliding with itself and needlessly appending a ".2" suffix.
func RelocateMovie(sourcePath, destRoot, title string, year, tmdbID int, preset naming.Preset) (string, error) {
	folder := filepath.Join(destRoot, naming.MovieFolderName(preset, title, year, tmdbID))
	dest := filepath.Join(folder, naming.MovieFileName(preset, title, year, tmdbID, filepath.Ext(sourcePath)))
	if dest == sourcePath {
		return dest, nil
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return "", fmt.Errorf("creating %q: %w", folder, err)
	}
	unique, err := place.UniquePath(dest, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	if err != nil {
		return "", err
	}
	if err := os.Rename(sourcePath, unique); err != nil {
		return "", fmt.Errorf("moving %q to %q: %w", sourcePath, unique, err)
	}
	return unique, nil
}

// ApplyLibrary is Rename's Movies-library apply. p must be Pending. There's
// no reconcile-drift case (ScanLibrary never produces one — see its doc
// comment), so this only ever handles a new orphan: resolve the actual video
// file (p.SourcePath may be a directory
// wrapping it, or the file itself), relocate just that file into a
// preset-formatted folder via RelocateMovie, then record it directly in
// libStore — no registration/rescan round trip needed, since
// libStore.Upsert itself IS the "now tracked" state, immediately.
//
// changes is a named return so a post-move failure (e.g. libStore.Upsert)
// still reports the committed file move to the caller for
// Session.NotifyPlayers — the physical relocate already happened by then
// and must not go unnotified (partial-success rule, player-rescan-notify).
//
// tier is the operator's configured quality tier for this mode, recorded on
// the library row as "the tier preference in force when this file entered the
// library". It's a plain string rather than a quality.Tier or a settings
// lookup because this package deliberately does not import internal/settings
// or internal/quality — the internal/api caller, which already holds the
// settings store, resolves it (see autoGrabTier).
func ApplyLibrary(ctx context.Context, libStore *library.Store, p proposals.Proposal, preset naming.Preset, tier string, prober Prober) (itemID int64, changes []mode.PathChange, err error) {
	if p.Status != proposals.Pending {
		return 0, nil, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}

	videoPath, err := library.ResolveVideoFile(p.SourcePath)
	if err != nil {
		return 0, nil, fmt.Errorf("resolving the video file under %q: %w", p.SourcePath, err)
	}

	// Claude 2026-08-05: skip GetByTMDBID when TMDBID==0 (would collide all zero-id rows).
	// Negative synthetic web-authority ids still use GetByTMDBID for same-title alternates.
	// Reason: deep-interview-rename-web-authority
	// Troubleshooting: second web Apply overwrote first library row → was using id 0
	// Review if: nullable tmdb_id unique constraint replaces synthetic negatives
	if p.TMDBID != 0 {
		existing, getErr := libStore.GetByTMDBID(ctx, mode.Movies, p.TMDBID)
		if getErr == nil && existing != nil {
			return applyLibraryAlternate(ctx, libStore, existing, p, videoPath, preset, tier, prober)
		}
		if getErr != nil && !errors.Is(getErr, library.ErrNotFound) {
			return 0, nil, fmt.Errorf("looking up existing library title: %w", getErr)
		}
	}

	destPath, err := RelocateMovie(videoPath, p.RootFolderPath, p.Title, p.Year, p.TMDBID, preset)
	if err != nil {
		return 0, nil, fmt.Errorf("relocating %q into %q: %w", videoPath, p.RootFolderPath, err)
	}
	// RelocateMovie's self-collision guard means destPath can equal videoPath
	// (file was already correctly placed — no os.Rename happened). Emitting
	// a Deleted+Created pair for the same unchanged path would be a bogus
	// notify, so only report a change when a move actually occurred.
	if destPath != videoPath {
		changes = []mode.PathChange{{Path: videoPath, Kind: mode.Deleted}, {Path: destPath, Kind: mode.Created}}
	}

	probedTier := tier
	meta := probeFileMeta(ctx, prober, destPath, tier)
	if meta.Tier != "" {
		probedTier = meta.Tier
	}

	item, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: p.TMDBID, Title: p.Title, Year: p.Year,
		FilePath: destPath, RootFolderPath: p.RootFolderPath,
		Size: library.FileSize(destPath), QualityTier: probedTier,
		Genres: p.Genres, Cast: p.Cast,
	})
	if err != nil {
		return 0, changes, fmt.Errorf("recording %q in the library: %w", p.Title, err)
	}
	if _, err := libStore.UpsertFile(ctx, library.ItemFile{
		ItemID: item.ID, FilePath: destPath, IsPrimary: true,
		QualityTier: probedTier, Size: library.FileSize(destPath),
		Width: meta.Width, Height: meta.Height, VideoCodec: meta.Codec,
		BitRate: meta.BitRate, DurationSec: meta.Duration,
	}); err != nil {
		return item.ID, changes, fmt.Errorf("recording primary file for %q: %w", p.Title, err)
	}
	return item.ID, changes, nil
}

// episodeKey identifies one already-tracked-with-a-file episode, for
// ScanLibrarySeries' duplicate-skip check.
type episodeKey struct {
	tmdbID, season, episode int
}

// ScanLibrarySeries is Rename's Series-library scan — the library-backed
// path used once Series stopped requiring Sonarr (see the plan this was
// built from, Stage 2). It walks rootFolderPath (and sess.KidsRootPath, if
// configured and different) for orphaned files or season-pack folders,
// resolves each file's season/episode from its own name, resolves the show
// via TMDB search, and skips anything already tracked WITH a file — an
// episode TMDB previously reported as missing (file_path == "") is NOT
// skipped, since finding its file is exactly what fills that gap in.
//
// One proposal per resolved episode file, never one per season-pack folder
// — same "surface everything individually" posture ScanLibrary (Movies)
// and every other workflow already follows.
func ScanLibrarySeries(ctx context.Context, sess *mode.Session, libStore *library.Store, rootFolderPath string, preset naming.Preset, cfg MatchConfig, prober Prober) ([]proposals.Proposal, error) {
	cfg = cfg.Normalize()
	if sess.TMDB == nil {
		return nil, fmt.Errorf("tmdb isn't configured yet — add it in Settings first")
	}
	if rootFolderPath == "" {
		return nil, fmt.Errorf("no Series library root folder configured yet — add one in Settings first")
	}

	allSeries, err := libStore.ListSeries(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading series: %w", err)
	}

	// Claude 2026-08-06: roots moved above the series/episode loop
	// Reason: autopilot-impl-title-collision-fix §3.3 — folderIDs' path feed
	//   (below) needs roots while walking allSeries, so roots must exist
	//   before that loop now, not only before the filesystem walk.
	// Troubleshooting: folder-pinned TMDB id guard never pins anything —
	//   confirm roots is populated before the allSeries loop runs.
	// Review if: library_series gains a real per-show folder-path column.
	roots := []string{rootFolderPath}
	if sess.KidsRootPath != "" && sess.KidsRootPath != rootFolderPath {
		roots = append(roots, sess.KidsRootPath)
	}

	known := map[string]bool{}
	// Claude 2026-08-07: mark primary + alternate episode paths known so alts aren't re-proposed
	// Reason: deep-interview-sakms-series-parsing-accuracy-improvements — library_episode_files
	//   holds non-primary paths; without this every applied alternate reappears as an orphan on
	//   the next Scan. Mirrors ScanLibrary's AllFilePaths feed (rename.go:172-186).
	// Troubleshooting: alternate files reappear as Rename orphans after Apply.
	// Review if: AllEpisodeFilePaths stops covering library_episode_files.
	if paths, err := libStore.AllEpisodeFilePaths(ctx); err == nil {
		for _, p := range paths {
			known[p] = true
		}
	}
	// The per-series loop below still sets known[ep.FilePath] unconditionally — that is the
	// graceful fallback if the query above errors, exactly as Movies falls back to
	// existing[i].FilePath. Keep BOTH; they are not redundant.
	tracked := map[episodeKey]bool{}
	// Claude 2026-08-06: folderIDs — show-folder-pinned TMDB id map (Check B)
	// Reason: autopilot-impl-title-collision-fix — "The Path" vs "The Oath"/
	//   "The Secret Path"/a duplicate "The Path" catalog entry all passed the
	//   old TMDB-search-only acceptance path; this pins a candidate to the
	//   TMDB id whose folder name this file's show folder resolves to. See
	//   series_folder_guard.go's package doc for the full mechanism.
	// Troubleshooting: a file lands Pending under the wrong show — check
	//   whether folderIDs[showFolderKey(...)] is 0 (unpinned/ambiguous) when
	//   it should be pinned.
	// Review if: library_series gains a real per-show folder-path column.
	folderIDs := map[string]int{} // normalized show-folder key -> TMDB id; 0 = ambiguous
	// Claude 2026-08-06: seriesByID — TMDB id -> tracked row, for episode-title matching
	// Reason: autopilot-impl-episode-title-matching §0.3/§4.2a — a proposal
	//   built from TMDB's own TVDetails instead of the tracked row corrupts
	//   the show's folder (TVDetails carries no year) and its library.Series
	//   row (UpsertSeries overwrites unconditionally on Apply). This map lets
	//   proposeOneEpisodeLibrary carry the pinned show's own Title/Year/
	//   RootFolderPath through as a pinnedShow instead. library_series has
	//   ON CONFLICT(tmdb_id), so tmdb_id is unique and this map cannot lose a
	//   row.
	// Troubleshooting: an episode-title match proposal has the wrong Title/
	//   Year, or lands under the wrong Kids/general root — confirm
	//   seriesByID[id] actually resolves before blaming pinnedShow itself.
	// Review if: library_series gains a real per-show folder-path column.
	seriesByID := make(map[int]library.Series, len(allSeries))
	// Claude 2026-08-07: seriesByTVDBID — TVDB id -> tracked row, for the
	//   anthology pass's established-pin shortcut (autopilot-impl.md §3.5)
	// Reason: after the first Apply an anthology show's files live under a
	//   folder named from TheTVDB ("Laurel & Hardy" -> key "laurelhardy")
	//   while un-applied stragglers are still under the original on-disk
	//   folder ("Laurel and Hardy" -> key "laurelandhardy"). Those keys
	//   DIFFER, so a folder-keyed lookup misses exactly when it is needed
	//   most; the TVDB series id is stable across both.
	// Troubleshooting: a rescan of an already-applied anthology show needs 3
	//   fresh corroborating files again instead of 1 — confirm this map is
	//   non-empty, i.e. that ApplyLibrarySeries actually persisted TVDBID.
	// Review if: library_series gains a real per-show folder-path column.
	seriesByTVDBID := make(map[int]library.Series, len(allSeries))
	// Claude 2026-08-08: trackedEpisodesByTVDBID — the established pin's second corroboration channel
	// Reason: the anthology-starvation plan §1.4
	//   (spec: deep-interview-sakms-anthology-established-pin-starvation-fix.md) —
	//   tvdbAnthologyPass' established-pin qualification can no longer depend on
	//   this scan's shrinking candidate group alone. `episodes` below is ALREADY
	//   fetched for every tracked series (the ListEpisodes call on the next line
	//   predates this change); retaining the slice for TVDB-tracked series costs
	//   zero extra queries and is what lets the pass stay free of *library.Store.
	// Troubleshooting: an established anthology rescan still declines whole —
	//   confirm this map is non-empty for the show's TVDB id, i.e. that
	//   ApplyLibrarySeries persisted TVDBID and the episodes carry titles.
	// Review if: tvdbAnthologyPass ever gains direct store access.
	// Context: .omc/specs/deep-interview-sakms-anthology-established-pin-starvation-fix.md
	trackedEpisodesByTVDBID := make(map[int][]library.Episode, len(allSeries))
	for _, series := range allSeries {
		episodes, err := libStore.ListEpisodes(ctx, series.ID)
		if err != nil {
			return nil, fmt.Errorf("loading episodes for %q: %w", series.Title, err)
		}
		seriesByID[series.TMDBID] = series
		if series.TVDBID > 0 {
			seriesByTVDBID[series.TVDBID] = series
			// ASSIGNMENT, not append, and it must stay in this same branch:
			// seriesByTVDBID is last-wins if two rows ever shared a tvdb_id
			// (library_series is UNIQUE on tmdb_id only), so appending here would
			// mix a SHADOWED row's episodes into corroboration for a row the pass
			// never sees. The two maps must describe the same series row.
			trackedEpisodesByTVDBID[series.TVDBID] = episodes
		}
		// Title feed: a tracked show's own title claims its normalized key,
		// even with zero tracked episode files on disk (§3.1).
		pinFolderID(folderIDs, showFolderTitleKey(series.Title), series.TMDBID)
		for _, ep := range episodes {
			if ep.FilePath == "" {
				continue // known from TMDB but not on disk yet — not a duplicate
			}
			// Marking just the file path is enough — ScanRootFolder's
			// recursive walk decides atomicity dynamically from known at
			// whatever depth it encounters a directory, so a new season
			// added later (or a new file dropped next to this one) is still
			// discovered rather than masked by pre-marking ancestor dirs.
			known[ep.FilePath] = true
			tracked[episodeKey{tmdbID: series.TMDBID, season: ep.SeasonNumber, episode: ep.EpisodeNumber}] = true
			// Path feed: the folder actually holding a tracked episode's
			// file claims the same key.
			pinFolderID(folderIDs, showFolderKey(ep.FilePath, roots), series.TMDBID)
		}
	}

	var out []proposals.Proposal
	// anthologyIdx holds indices into out for rows that came from
	// proposeOneEpisodeLibrary's season/episode PARSE-FAILURE branch, supplied
	// structurally by that function's second return value — never re-derived
	// by matching on Reason (autopilot-impl.md §3.3).
	var anthologyIdx []int
	for _, root := range roots {
		entries, err := library.ScanRootFolder(root, known)
		if err != nil {
			return nil, fmt.Errorf("scanning %s: %w", root, err)
		}
		for _, entry := range entries {
			if config.SidecarExts[strings.ToLower(filepath.Ext(entry.Name))] {
				continue
			}
			videoFiles, err := library.ResolveEpisodeVideoFiles(entry.Path)
			if err != nil {
				continue // silent omit — non-video / empty of video (Jellyfin VideoExts gate)
			}
			for _, videoPath := range videoFiles {
				if naming.MatchesSeriesSchema(videoPath, preset) {
					continue // already organized under the active preset — nothing to propose
				}
				pin := pinnedShow{}
				if key := showFolderKey(videoPath, roots); key != "" {
					if id := folderIDs[key]; id > 0 {
						pin.tmdbID = id
						if s, ok := seriesByID[id]; ok {
							pin.title, pin.year, pin.root = s.Title, s.Year, s.RootFolderPath
						}
					}
				}
				p, parseFailed := proposeOneEpisodeLibrary(ctx, sess, tracked, pin, rootFolderPath, root, videoPath, roots, cfg, prober)
				out = append(out, p)
				if parseFailed {
					anthologyIdx = append(anthologyIdx, len(out)-1)
				}
			}
		}
	}

	// Claude 2026-08-07: TVDB anthology pass (autopilot-impl.md §3)
	// Reason: a folder of theatrical shorts that TheTVDB catalogs as a
	//   chronological TV series (verified: "Laurel & Hardy") has NO tracked
	//   library_series row, so folderIDs pins nothing and the TMDB-sourced
	//   tryEpisodeTitleMatchSeries declines every file by precondition. This
	//   pass resolves the SHOW from multi-file self-corroboration — 3+ distinct
	//   files each matching a distinct episode of one candidate's TVDB catalog
	//   — and only then places the individual files.
	// Troubleshooting: files stay unmatched — check, in order: sess.TVDB
	//   non-nil; showFolderKey(...) non-empty; showFolderName(...) returning
	//   the SHOW folder's name and not a nested subdirectory's (§2.4 — they
	//   share one traversal, so a mismatch means the refactor was undone); the
	//   key ABSENT from folderIDs; the candidate clearing its own threshold in
	//   step 4 (3 for a new pin, 1 for an established one — NOT a group-size
	//   check in step 3, see §3.4); SeriesEpisodes returning a non-empty
	//   catalog.
	// ORDERING IS LOAD-BEARING: this MUST run before the `seen` guard below, so
	//   the Pending rows it creates are covered by the within-batch
	//   episodeKey-collision check for free (§3.2). Backwards, two files that
	//   resolved to the same slot would both stay Pending and the later Apply
	//   would silently overwrite the earlier one's library_episodes row.
	// Review if: library_series gains a real per-show folder-path column, which
	//   would replace showFolderKey's normalized-name heuristic outright.
	resolved, handled := tvdbAnthologyPass(ctx, sess, tracked, seriesByTVDBID, trackedEpisodesByTVDBID, folderIDs,
		roots, rootFolderPath, anthologyIdx, out)

	// Claude 2026-08-07: AI-assisted episode recovery post-pass (autopilot-impl.md §2.2)
	// Reason: a POST-pass, never an inline call inside proposeOneEpisodeLibrary.
	//   An inline attempt would return parseFailed == false for any file it
	//   resolved, removing that file from anthologyIdx — and tvdbAnthologyPass
	//   needs 3 DISTINCT files to clear its new-pin threshold, so one AI guess
	//   could drop a folder below 3 and pre-empt STRONGER multi-file
	//   deterministic evidence for every other file in it. Running here makes
	//   that inversion structurally impossible: deterministic first, model last.
	// Troubleshooting: rows the AI pass should have spoken for stay untouched —
	//   check, in order: sess.MainstreamAI non-nil (the AI-fallback gate); the
	//   row's index present in anthologyIdx; handled[i] FALSE (a row
	//   tvdbAnthologyPass already wrote is deliberately off-limits, so its
	//   precise ambiguity / already-in-the-library reason is not overwritten
	//   with a vaguer one); the folder key resolving to either folderIDs (mode
	//   1) or resolved (mode 2) — a key in neither is mode 3, DEFERRED scope.
	// ORDERING IS LOAD-BEARING in BOTH directions: it must run AFTER
	//   tvdbAnthologyPass (whose resolved/handled it consumes) and BEFORE the
	//   `seen` guard below (so the Pending rows it creates inherit the
	//   within-batch episodeKey-collision check for free).
	// Note the map argument differs from the line above deliberately: anthology
	//   takes seriesByTVDBID (it resolves TheTVDB shows); this takes seriesByID
	//   (TMDB-keyed) because mode 1 resolves against a TMDB-pinned folder.
	// Review if: mode 3 (§2.6) is ever implemented — it would widen the
	//   eligible set to folders resolved by neither map.
	aiEpisodeRecoveryPass(ctx, sess, tracked, seriesByID, folderIDs, resolved, handled,
		roots, rootFolderPath, anthologyIdx, out)

	// Claude 2026-08-07: within-batch episodeKey collision is now alternate-aware
	// Reason: deep-interview-sakms-series-parsing-accuracy-improvements — Series gained
	//   Movies-parity alternate-version support, so a second file claiming an
	//   already-claimed slot is a legitimate alternate, not a conflict. This replaces
	//   the previous blanket-Unmatch, per that guard's own retired `Review if:`.
	//   ApplyLibrarySeries resolves the collision against LIVE DB state (the same
	//   mechanism ApplyLibrary/GetByTMDBID uses for Movies), so both rows may stay
	//   Pending: whichever applies first becomes the tracked primary, and the second
	//   folds in via applyLibrarySeriesAlternate — promoted or demoted by probed tier,
	//   order-independently.
	// Troubleshooting: two same-slot Pending rows and the SECOND Apply overwrote the
	//   first's library_episodes row instead of folding — that means the Apply-side
	//   fold-in gate (existing.FilePath != "") did not fire; check §0.3.
	// Review if: Series Dedup gains alternate awareness, at which point the
	//   annotation below could be dropped entirely (see §11.2).
	seen := map[episodeKey]bool{}
	for i := range out {
		if out[i].Status != proposals.Pending || out[i].TMDBID == 0 {
			continue
		}
		key := episodeKey{tmdbID: out[i].TMDBID, season: out[i].SeasonNumber, episode: out[i].EpisodeNumber}
		if seen[key] {
			// Stays Pending. The reason is PREFIXED, never replaced, so the operator can
			// see at review time that this row will land as an alternate rather than as
			// the slot's primary — a signal Movies does not even offer.
			//
			// PREFIX, DO NOT CLOBBER. This codebase already has an explicit precedent
			// against overwriting a precise reason with a vaguer one: aiEpisodeRecoveryPass
			// skips handled[i] rows specifically so "its precise ambiguity /
			// already-in-the-library reason is not overwritten with a vaguer one"
			// (rename.go:801-803). A row that reached Pending via the TVDB anthology pass
			// carries a corroboration reason worth keeping.
			if !strings.HasPrefix(out[i].Reason, "alternate:") {
				out[i].Reason = fmt.Sprintf(
					"alternate: another file in this scan also claims %q S%02dE%02d — apply will fold as primary or alternate by quality. %s",
					out[i].Title, out[i].SeasonNumber, out[i].EpisodeNumber, out[i].Reason)
			}
			continue
		}
		seen[key] = true
	}
	// Chosen option, per plan §9.7.1(C)'s requirement to record it here explicitly:
	// option 2 (leave HasPrefix, accept double-annotation on Mode-1 rows). A
	// Mode-1-produced alternate row (Site 5 via aiEpisodeMode1) contains but does
	// not start with "alternate:", so the HasPrefix check above does not recognise
	// it and will prepend a second "alternate:" clause to such a row if it also
	// collides within the same scan. Accepted: the reason is verbose but not wrong,
	// both clauses are true, and this is a narrow, already-rare intersection (a
	// Mode-1 recovery that also collides with another file in the same scan). Do
	// not change this check to Contains without re-reading the alternative
	// tradeoff recorded in plan §9.7.1.

	return out, nil
}

// Claude 2026-08-07: second return value — "the season/episode PARSE failed"
// Reason: autopilot-impl.md §3.3 — structural eligibility for the TVDB
//
//	anthology post-pass. The caller must NEVER re-derive this by matching on
//	p.Reason: CLAUDE.md records a HIGH-severity bug in
//	series-monitoring-autograb where origin was inferred from row shape plus a
//	reason string, the inference misfired, and it destroyed an operator's own
//	grab — and Wade's 2026-08-02 decision to spend a real schema column
//	(grabs.hold_until) rather than repeat it. Here the information is already
//	available structurally at the call site, so it costs nothing.
//
// Troubleshooting: the anthology pass never sees a file it obviously should —
//
//	confirm this returns true for it (only the ParseEpisodeNumbersLoose !ok
//	branch's own Unmatched does; every other exit, including the TMDB
//	episode-title sibling's, returns false).
//
// Review if: a second parse-failure exit is ever added to this function.
func proposeOneEpisodeLibrary(
	ctx context.Context, sess *mode.Session, tracked map[episodeKey]bool, pin pinnedShow,
	generalRoot, foundRoot, videoPath string, roots []string, cfg MatchConfig, prober Prober,
) (proposals.Proposal, bool) {
	cfg = cfg.Normalize()
	name := filepath.Base(videoPath)
	p := proposals.Proposal{
		Mode: mode.Series, Workflow: proposals.Rename,
		SourceName: name, SourcePath: videoPath, RootFolderPath: foundRoot,
	}

	// Claude 2026-08-06: loose parent-dir fallback for opaque hash basenames
	// Reason: 34 real "The Path" library rows (diagnosis
	//   .omc/artifacts/series-parse-failures-20260806.psv) have a hash
	//   basename with zero recognizable content, while the immediate parent
	//   directory is a complete scene-release name the existing strict
	//   pattern already parses correctly — see
	//   library.ParseEpisodeNumbersLoose's own doc comment.
	// Troubleshooting: "could not determine season/episode from %q" for a
	//   hash-named file whose parent folder is a normal release name.
	// Review if: ParseEpisodeNumbers itself changes — this must keep
	//   delegating to it unmodified.
	// seasonEpisodeViaCompactCode records that the (season, episode) below was
	// recovered by library.ParseCompactEpisodeCodeLoose rather than by
	// ParseEpisodeNumbersLoose. It is read at exactly three places — the two
	// tryWebAuthoritySeries call sites and the terminal-reason branch that
	// attributes their refusal (plan §1.3a) — and nowhere else.
	//
	// NAME IT FOR THE PARSE, NOT FOR THE FILENAME. It means "the season/episode
	// came from ParseCompactEpisodeCodeLoose", NOT "this filename contains an
	// eNNNN token". The guard's correctness depends on the former: a file whose
	// SxxExx marker parsed normally is not blocked, even if its name also
	// happens to carry a compact code somewhere.
	seasonEpisodeViaCompactCode := false
	season, episodes, ok := library.ParseEpisodeNumbersLoose(name, filepath.Dir(videoPath))
	if !ok {
		// Claude 2026-08-07: compact eNNNN episode code, rename-path-only (plan §1.2/§1.3)
		// Reason: 13 real "The Red Skelton Show" rows name the episode as
		//   "eSSEE" ("RED SKELTON e1524.mp4", "e614_720x480.mp4") with no
		//   SxxExx marker anywhere in the basename OR the parent directory, so
		//   ParseEpisodeNumbersLoose cannot reach them. Kept OUT of
		//   ParseEpisodeNumbers so import.go / releasematch.go /
		//   dedup_phash_primary.go / autograb are provably unaffected.
		//   Runs BEFORE tryEpisodeTitleMatchSeries below so the deterministic
		//   parser strictly pre-empts the title-matching heuristic.
		// Troubleshooting: an eNNNN file still reports "could not determine
		//   season/episode" — check compactEpisodeCodePattern's terminator
		//   class before suspecting the search path.
		// Review if: ParseEpisodeNumbers itself learns the compact shape, at
		//   which point this second attempt becomes dead and must be deleted,
		//   not left.
		if cs, ce, cok := library.ParseCompactEpisodeCodeLoose(name, filepath.Dir(videoPath)); cok {
			season, episodes, ok = cs, []int{ce}, true
			seasonEpisodeViaCompactCode = true
		}
	}
	if !ok {
		// Claude 2026-08-06: episode-title matching for files with no season/episode marker
		// Reason: autopilot-impl-episode-title-matching §2 — a file whose show is
		//   ALREADY pinned by the folder guard can still be placed by matching its
		//   recovered title against that show's own TMDB episode names. Runs ONLY
		//   here, ONLY when pinned, and never as part of a show+episode guess.
		// Troubleshooting: an episode-title match landed on the wrong slot —
		//   check §1.4's residual/containment rule, not the parse gate.
		// Review if: ParseEpisodeNumbersLoose learns the digit-code shape (e1524),
		//   which would move the Red Skelton population out of this branch.
		if got := tryEpisodeTitleMatchSeries(ctx, sess, tracked, pin, generalRoot, foundRoot, name, p); got != nil {
			return *got, false
		}
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("could not determine season/episode from %q", name)
		return p, true
	}
	episode := episodes[0]
	extraEpisodes := episodes[1:]

	if hint := nfo.ReadSeriesSidecar(videoPath); hint.TMDBID != 0 {
		// Claude 2026-08-07: SITE 1 of 7 — tracked slot is now an alternate, not a decline (plan §5.2.2)
		// Reason: deep-interview-sakms-series-parsing-accuracy-improvements §5.2 —
		//   this branch used to set Unmatched (".nfo TMDB id %d appears to already
		//   be in the library ...") and return early. Series now has Movies-parity
		//   alternate-version support, so a second file on an already-filled slot
		//   is a legitimate alternate. This is the ONE site the spec's acceptance
		//   criteria name explicitly (the Beverly Hillbillies .nfo case).
		//   Structurally the odd one out of the seven: det is not in scope here,
		//   so we record the duplicate and FALL THROUGH to the TMDB-confirm block
		//   below, which populates Title/Year/RootFolderPath exactly as a
		//   non-duplicate's would — the only difference is the Reason.
		// Troubleshooting: a .nfo duplicate Applied and OVERWROTE the slot's
		//   library_episodes row instead of folding in — the Apply-side fold-in
		//   gate (existing.FilePath != "") did not fire; check §0.3.
		// Review if: operators report unwanted alternates accumulating and want
		//   same-slot duplicates declined again.
		isDuplicateSlot := tracked[episodeKey{tmdbID: hint.TMDBID, season: season, episode: episode}]
		// Claude 2026-08-06: NFO season-confirm miss falls through to TMDB/TVDB/web
		// Reason: wrong or incomplete TMDB seasons in sidecars (e.g. Monster S02)
		//   used to hard-unmatch before TVDB/search could recover.
		// Troubleshooting: still unmatched after TVDB configured → NFO path returned early.
		// Review if: NFO TVDB id field is preferred when TMDB season 404s.
		if _, err := sess.TMDB.SeasonDetails(ctx, hint.TMDBID, season); err == nil {
			det, err := sess.TMDB.TVDetails(ctx, hint.TMDBID)
			if err != nil {
				p.Status = proposals.Unmatched
				p.Reason = fmt.Sprintf(".nfo TMDB id %d: lookup failed: %v", hint.TMDBID, err)
				return p, false
			}
			targetRoot := generalRoot
			switch {
			case foundRoot == sess.KidsRootPath:
				targetRoot = sess.KidsRootPath
			case sess.KidsRootPath != "" && sess.MainstreamAI != nil:
				if result, err := classify.WithAI(ctx, sess.MainstreamAI, det.Title, ""); err == nil && result.IsKids {
					targetRoot = sess.KidsRootPath
				}
			}
			p.Status = proposals.Pending
			p.Title = det.Title
			p.TMDBID = hint.TMDBID
			p.Year = hint.Year
			p.SeasonNumber = season
			p.EpisodeNumber = episode
			if len(extraEpisodes) > 0 {
				p.ExtraEpisodeNumbers = extraEpisodes
			}
			p.RootFolderPath = targetRoot
			p.Genres = det.Genres
			if cast, err := sess.TMDB.TVAggregateCredits(ctx, hint.TMDBID); err == nil {
				p.Cast = cast
			}
			// Site 1's softened outcome — Status+Reason only, never Title/Year/Root.
			if isDuplicateSlot {
				acceptDuplicatePendingEpisode(&p, det.Title, season, episode)
			}
			return p, false
		}
		// Season missing on the NFO's TMDB id — continue into filename search /
		// TVDB / web-authority instead of hard-unmatching.
	}

	sig := ExtractFileSignals(ctx, name, videoPath, prober)
	// Claude 2026-08-06: loose title seed for GuessTitle/bravePhase2Series and the seriesGate, computed once
	// Reason: code-reviewer HIGH finding — once ParseEpisodeNumbersLoose lets
	//   an opaque hash basename past the parse gate, GuessTitle/bravePhase2Series
	//   (unlike tryWebAuthoritySeries, which gates on IsJunkRenameFilename/
	//   HasTitleTokenOverlap) had no title-overlap guard: GuessTitle could
	//   hallucinate a title from 32 hex chars, and bravePhase2Series accepts
	//   the first TMDB candidate whose season exists — combined with the
	//   parent-dir-derived season/episode, that's a confidently WRONG match
	//   landing on Pending instead of correctly staying Unmatched. Seeding
	//   both with the same parent-dir-recovered title StripEpisodeMarkerLoose
	//   already computes for the search-term path below closes the hole
	//   (a hash string search-guesses to nothing) and improves recall for the
	//   real "The Path" case (Brave/GuessTitle get "The Path", not a hash).
	//   The seriesGate below reuses this same value as its titleSeed rather
	//   than recomputing library.StripEpisodeMarker(name) (the strict,
	//   non-loose variant) — that would silently diverge from the actual
	//   search term used a few lines down
	//   (queries := searchterm.SearchQueries(looseTitle)), and gate Check A
	//   would then compare candidates against the wrong seed.
	// Troubleshooting: a hash-basename file lands on a confident Pending with
	//   a plausible-looking but wrong TMDB match instead of staying Unmatched.
	// Review if: StripEpisodeMarkerLoose's fallback shape changes.
	//
	// Claude 2026-08-07: three-step title seed for the compact-code population (plan §1.4)
	// Reason: StripEpisodeMarkerLoose is a NO-OP on an eNNNN basename (there is
	//   no SxxExx/NxNN marker to strip in the file OR its parent), so the seed
	//   keeps the "e1524"/"720x480" noise. For "e614_720x480.mp4" the surviving
	//   tokens are {e614, 720x480}, which share ZERO tokens with any real show
	//   title — seriesGate Check A then rejects every candidate and the file can
	//   never match. Falling back to the SHOW FOLDER's own name is what recovers
	//   it, and showFolderName is the SAME traversal showFolderKey uses
	//   (series_tvdb_episode_match.go:showFolderSegment), so the seed and the
	//   pin can never describe different directories.
	// Troubleshooting: an eNNNN file parses its season/episode but stays
	//   Unmatched with a gate-rejection reason — confirm this cascade reached
	//   step 3.
	// Review if: StripEpisodeMarkerLoose learns the compact shape itself.
	looseTitle := library.StripEpisodeMarkerLoose(name, filepath.Dir(videoPath))
	if looseTitle == name {
		if stripped := library.StripCompactEpisodeCode(name); stripped != name {
			looseTitle = stripped // "RED SKELTON e1524.mp4" -> "RED SKELTON .mp4"
		}
	}
	if !hasWordToken(looseTitle) {
		if folder := showFolderName(videoPath, roots); folder != "" {
			looseTitle = folder // "e614_720x480.mp4" -> "The Red Skelton Show"
		}
	}
	// Claude 2026-08-06: seriesGate — title-collision precision gate (Check A + B)
	// Reason: autopilot-impl-title-collision-fix — see series_folder_guard.go's
	//   package doc for the full mechanism this gate implements.
	// Troubleshooting: a wrong-show Pending appears despite this gate existing —
	//   confirm every acceptSeries-adjacent candidate loop calls gate.allows
	//   (or gate.withSeed(...).allows) before accepting, not just this one.
	// Review if: library_series gains a real per-show folder-path column.
	gate := &seriesGate{
		titleSeed:    looseTitle,
		pinnedTMDBID: pin.tmdbID, // <- the ONLY change in this literal
		rejected:     new(int),
	}
	// Claude 2026-08-06: gateRejectedReason — shared §4.3 distinct-reason helper
	// Reason: code review — this function has more than one Unmatched exit
	//   point (the !sig.HasAny() branch's own early return, and the primary-
	//   search-exhausted path below), and the gate-rejection reason was only
	//   wired into the latter — a file rejected only inside the
	//   !sig.HasAny() branch (via trySeriesQueries/bravePhase2Series there)
	//   silently fell through to that branch's own generic reason instead.
	// Troubleshooting: an operator reports "The Path" files unmatched with no
	//   clue why and p.Reason doesn't carry this message — confirm every
	//   Unmatched return in this function calls gateRejectedReason first.
	// Review if: library_series gains a real per-show folder-path column.
	gateRejectedReason := func() bool {
		if gate.rejected == nil || *gate.rejected == 0 {
			return false
		}
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("rejected %d candidate(s) that did not match this folder's tracked show or share a title token with %q", *gate.rejected, gate.titleSeed)
		return true
	}
	acceptSeries := func(match tmdb.Item, candYear int) proposals.Proposal {
		// Claude 2026-08-07: SITE 2 of 7 — tracked slot is now an alternate, not a decline (plan §5.2.3)
		// Reason: deep-interview-sakms-series-parsing-accuracy-improvements §5.2 —
		//   this closure used to set Unmatched ("appears to already be in the
		//   library as %q S%02dE%02d ...") and return before populating anything.
		//   Series now has Movies-parity alternate-version support (Movies'
		//   analogue is rename.go:300's acceptDuplicatePending call), so the
		//   proposal is built exactly as a non-duplicate's is and only
		//   Status+Reason are overwritten at the end. Callers
		//   (trySeriesQueries/bravePhase2Series) already returned on this
		//   closure's result whatever its status, so candidate-search
		//   continuation is unchanged.
		// Troubleshooting: a duplicate Applied and OVERWROTE the slot's
		//   library_episodes row instead of folding in — the Apply-side fold-in
		//   gate (existing.FilePath != "") did not fire; check §0.3.
		// Review if: operators report unwanted alternates accumulating and want
		//   same-slot duplicates declined again.
		duplicateSlot := tracked[episodeKey{tmdbID: match.ID, season: season, episode: episode}]
		targetRoot := generalRoot
		switch {
		case foundRoot == sess.KidsRootPath:
			targetRoot = sess.KidsRootPath
		case sess.KidsRootPath != "" && sess.MainstreamAI != nil:
			if result, err := classify.WithAI(ctx, sess.MainstreamAI, match.Title, match.Overview); err == nil && result.IsKids {
				targetRoot = sess.KidsRootPath
			}
		}
		p.Status = proposals.Pending
		p.Title = match.Title
		p.TMDBID = match.ID
		p.Year = candYear
		p.SeasonNumber = season
		p.EpisodeNumber = episode
		if len(extraEpisodes) > 0 {
			p.ExtraEpisodeNumbers = extraEpisodes
		}
		p.RootFolderPath = targetRoot
		if det, err := sess.TMDB.TVDetails(ctx, match.ID); err == nil {
			p.Genres = det.Genres
		}
		if names, err := sess.TMDB.TVAggregateCredits(ctx, match.ID); err == nil {
			p.Cast = names
		}
		// Site 2's softened outcome — Status+Reason only, never Title/Year/Root.
		if duplicateSlot {
			acceptDuplicatePendingEpisode(&p, match.Title, season, episode)
		}
		return p
	}
	if !sig.HasAny() {
		guessed := ""
		if sess.MainstreamAI != nil {
			if g, gerr := identify.GuessTitle(ctx, sess.MainstreamAI, looseTitle); gerr == nil && g != "" {
				guessed = g
				if got := trySeriesQueries(ctx, sess, sig, cfg, acceptSeries, gate.withSeed(guessed), season, episode, searchterm.SearchQueries(guessed)); got != nil {
					return *got, false
				}
			}
		}
		if got := bravePhase2Series(ctx, sess, sig, cfg, acceptSeries, gate, season, episode, guessed, looseTitle); got != nil {
			return *got, false
		}
		// Claude 2026-08-07: compact-code files are refused by the web-naming-authority path (plan §1.3a)
		// Reason: the eNNNN parser newly makes this population reachable here,
		//   and this path has NO season-existence check and mints a SYNTHETIC
		//   NEGATIVE TMDB id (WebAuthorityTMDBID). "The Red Skelton Show" has a
		//   real TMDB entry (10534), so a web-authority Pending would create a
		//   SECOND library_series row for a show already tracked under a
		//   positive id — the split-show corruption
		//   series_tvdb_episode_match.go documents from the other direction.
		//   HasTitleTokenOverlap does not stop it: "skelton" is a >= 4-rune
		//   shared token, so the overlap gate passes on its strong branch.
		//   DEFENSIVE at this site — a compact-code file is a real .mp4, so
		//   sig.HasAny() is true and this branch is unreachable for it today.
		//   Guarded anyway: an unprobeable or zero-byte compact-code file would
		//   land here, and an unguarded twin of a guarded call is unreadable to
		//   a future reviewer.
		// Troubleshooting: an eNNNN file parses correctly and stays Unmatched —
		//   check p.Reason for compactCodeWebAuthorityRefusedReason, which the
		//   MAIN site below sets and this defensive one deliberately does not.
		// Review if: tryWebAuthoritySeries gains a SeasonDetails existence check
		//   AND a real-positive-TMDB-id preference, at which point this guard
		//   can be opened up — delete it deliberately, do not leave it dead.
		if !seasonEpisodeViaCompactCode {
			if got := tryWebAuthoritySeries(ctx, sess, tracked, generalRoot, foundRoot, season, episode, extraEpisodes, name, guessed, p); got != nil {
				return *got, false
			}
		}
		if gateRejectedReason() {
			return p, false
		}
		p.Status = proposals.Unmatched
		if IsJunkRenameFilename(name) {
			p.Reason = fmt.Sprintf("filename too generic for web naming authority (%q) — needs manual review", name)
		} else {
			p.Reason = "no year, actor, or duration available to corroborate a match — needs manual review"
		}
		return p, false
	}

	// Claude 2026-08-06: StripEpisodeMarkerLoose lockstep with ParseEpisodeNumbersLoose above
	// Reason: for the hash-basename shape, StripEpisodeMarker(name) is a
	//   no-op (nothing to strip), so the search term would be the opaque
	//   hash itself and TMDB search would fail — converting a parse
	//   failure into a lookup failure instead of a real fix. Falling back
	//   to the parent directory's own stripped title recovers a usable
	//   search term the same way season/episode was recovered above.
	//   Reuses looseTitle (computed once above, alongside sig) rather than
	//   recomputing it — same value, same reasoning.
	// Troubleshooting: season/episode now parses via the parent dir but the
	//   file still ends up Unmatched with a search-failure reason.
	// Review if: StripEpisodeMarker itself changes — this must keep
	//   delegating to it unmodified.
	queries := searchterm.SearchQueries(looseTitle)
	var lastTerm string
	var lastLimit int
	type weakSeries struct {
		match    tmdb.Item
		candYear int
	}
	var weak *weakSeries
	for _, term := range queries {
		lastTerm = term
		items, err := sess.TMDB.SearchTV(ctx, term)
		if err != nil {
			p.Status = proposals.Unmatched
			p.Reason = fmt.Sprintf("TMDB search failed for %q: %v", term, err)
			return p, false
		}
		if len(items) == 0 {
			continue
		}
		limit := pickLimit(cfg.CandidateN, len(items))
		lastLimit = limit
		for i := 0; i < limit; i++ {
			match := items[i]
			candYear, runtimeMin, cast := seriesCandidateMeta(ctx, sess.TMDB, match.ID, season, episode, match.ReleaseDate, sig)
			rank := SignalsCorroborate(sig, candYear, runtimeMin, cast, cfg.DurationTolerancePct)
			if rank == CorroborationNone {
				continue
			}
			// Season must exist before we treat this as a real pick.
			if _, err := sess.TMDB.SeasonDetails(ctx, match.ID, season); err != nil {
				continue
			}
			// Claude 2026-08-06: title-collision gate before strong-accept/weak-stash
			// Reason: autopilot-impl-title-collision-fix §2.1 — placed after the
			//   season check (keeps cheap-signal ordering) and before the
			//   strong/weak block so a gate-rejected candidate can never reach
			//   the weak fallback either.
			// Troubleshooting: a wrong-show Pending appears — confirm this call
			//   actually rejects the collision (gate.pinnedTMDBID/titleSeed).
			// Review if: library_series gains a real per-show folder-path column.
			if !gate.allows(match.Title, match.ID) {
				continue
			}
			if rank == CorroborationStrong {
				return acceptSeries(match, candYear), false
			}
			if weak == nil {
				weak = &weakSeries{match: match, candYear: candYear}
			}
		}
	}
	if weak != nil {
		return acceptSeries(weak.match, weak.candYear), false
	}

	if fb := tvdbFallbackSeries(ctx, sess, lastTerm, tracked, gate, generalRoot, foundRoot, season, episode, extraEpisodes, sig, cfg, p); fb != nil {
		return *fb, false
	}

	// Claude 2026-08-05: Series GuessTitle + Brave Phase 2 (parity with Movies)
	// Reason: deep-interview-rename-brave-phase2
	// Troubleshooting: wrong show title with valid SxxExx stayed Unmatched
	// Review if: Series Brave should re-parse SxxExx from grounded title
	//
	// Claude 2026-08-06: seeded with looseTitle, not name — see looseTitle's
	// own doc comment above (code-reviewer HIGH finding: an opaque hash
	// basename must not reach GuessTitle/bravePhase2Series raw).
	guessed := ""
	if sess.MainstreamAI != nil {
		if g, gerr := identify.GuessTitle(ctx, sess.MainstreamAI, looseTitle); gerr == nil && g != "" {
			guessed = g
			if got := trySeriesQueries(ctx, sess, sig, cfg, acceptSeries, gate.withSeed(guessed), season, episode, searchterm.SearchQueries(guessed)); got != nil {
				return *got, false
			}
		}
	}
	if got := bravePhase2Series(ctx, sess, sig, cfg, acceptSeries, gate, season, episode, guessed, looseTitle); got != nil {
		return *got, false
	}
	// Claude 2026-08-07: compact-code files are refused by the web-naming-authority path (plan §1.3a)
	// Reason: the eNNNN parser newly makes this population reachable here, and
	//   this path has NO season-existence check and mints a SYNTHETIC NEGATIVE
	//   TMDB id (WebAuthorityTMDBID). "The Red Skelton Show" has a real TMDB
	//   entry (10534), so a web-authority Pending would create a SECOND
	//   library_series row for a show already tracked under a positive id — the
	//   split-show corruption series_tvdb_episode_match.go documents from the
	//   other direction. HasTitleTokenOverlap does not stop it: "skelton" is a
	//   >= 4-rune shared token, so the overlap gate passes on its strong branch.
	//   This is the LIVE hazard site (the main path, after bravePhase2Series).
	// Troubleshooting: an eNNNN file parses correctly and stays Unmatched —
	//   check p.Reason for compactCodeWebAuthorityRefusedReason below, which is
	//   the ONLY string that identifies this guard. A generic "no catalog/web
	//   title+year" reason means web authority genuinely declined, NOT that
	//   this fired.
	// Review if: tryWebAuthoritySeries gains a SeasonDetails existence check AND
	//   a real-positive-TMDB-id preference, at which point this guard can be
	//   opened up — delete it deliberately, do not leave it dead.
	if !seasonEpisodeViaCompactCode {
		if got := tryWebAuthoritySeries(ctx, sess, tracked, generalRoot, foundRoot, season, episode, extraEpisodes, name, guessed, p); got != nil {
			return *got, false
		}
	}

	// Claude 2026-08-06: distinct Unmatched reason when the gate rejected candidates
	// Reason: autopilot-impl-title-collision-fix §4.3 — without this, a
	//   gate-rejected file reports the generic "no catalog/web title+year"
	//   reason, which reads like a catalog miss instead of a folder collision.
	// Troubleshooting: an operator reports "The Path" files unmatched with no
	//   clue why — check p.Reason for this prefix before assuming a catalog gap.
	// Review if: library_series gains a real per-show folder-path column.
	if gateRejectedReason() {
		return p, false // unchanged — a real gate rejection is MORE specific, so it still wins
	}
	// Claude 2026-08-07: positive attribution for §1.3a's web-authority block
	// Reason: without a distinct terminal reason the block is INVISIBLE. Trace a
	//   blocked "e2516" to its end state: gateRejectedReason() does not fire
	//   (inside bravePhase2Series the SeasonDetails check returns before
	//   seeded.allows, so *gate.rejected stays 0), control reaches
	//   unmatchedAfterWebAuthority, and the row is emitted with "no catalog/web
	//   title+year after top %d candidates" — a string that ASSERTS web
	//   authority was consulted and found nothing. It was structurally refused.
	//   It also leaves an unwired guard indistinguishable from a
	//   GroundTitleViaSearch miss: byte-identical output, no positive detector.
	// Troubleshooting: this reason on a file with a real SxxExx marker means the
	//   flag is being set for the wrong parse — it must mean "the season/episode
	//   came from ParseCompactEpisodeCodeLoose", not "the name contains eNNNN".
	// Review if: the guard above is opened up — this reason must go with it.
	//
	// ORDERING IS LOAD-BEARING IN ONE DIRECTION ONLY: gateRejectedReason() must
	// keep winning when it fires, because "the gate rejected N candidates that
	// did not share a title token" is strictly more diagnostic than "web
	// authority was skipped." Putting this branch first would swallow it.
	if seasonEpisodeViaCompactCode {
		p.Status = proposals.Unmatched
		p.Reason = compactCodeWebAuthorityRefusedReason
		return p, false
	}

	unmatchedAfterWebAuthority(&p, name, lastLimit, lastTerm)
	return p, false
}

// compactCodeWebAuthorityRefusedReason marks a row that the compact-code
// web-authority guard refused, so an operator can tell "web authority was
// skipped by policy" apart from "web authority ran and declined". Without it the
// two are indistinguishable in the proposals table.
const compactCodeWebAuthorityRefusedReason = "season/episode came from a compact eNNNN code; " +
	"web naming authority is not consulted for these (it cannot verify the season exists and " +
	"would mint a synthetic show id) — needs manual review"

func trySeriesQueries(
	ctx context.Context, sess *mode.Session, sig FileSignals, cfg MatchConfig,
	acceptSeries func(tmdb.Item, int) proposals.Proposal, gate *seriesGate, season, episode int, queries []string,
) *proposals.Proposal {
	cfg = cfg.Normalize()
	type weakHit struct {
		match    tmdb.Item
		candYear int
	}
	var weak *weakHit
	for _, term := range queries {
		items, err := sess.TMDB.SearchTV(ctx, term)
		if err != nil || len(items) == 0 {
			continue
		}
		limit := pickLimit(cfg.CandidateN, len(items))
		for i := 0; i < limit; i++ {
			match := items[i]
			candYear, runtimeMin, cast := seriesCandidateMeta(ctx, sess.TMDB, match.ID, season, episode, match.ReleaseDate, sig)
			rank := SignalsCorroborate(sig, candYear, runtimeMin, cast, cfg.DurationTolerancePct)
			if rank == CorroborationNone {
				continue
			}
			if _, err := sess.TMDB.SeasonDetails(ctx, match.ID, season); err != nil {
				continue
			}
			// Claude 2026-08-06: title-collision gate before strong-accept/weak-stash
			// Reason: autopilot-impl-title-collision-fix §2.3 — same placement
			//   rule as the inline loop: after the season check, before the
			//   strong/weak block, so a gate-rejected candidate never reaches
			//   the weak fallback either.
			// Review if: library_series gains a real per-show folder-path column.
			if !gate.allows(match.Title, match.ID) {
				continue
			}
			if rank == CorroborationStrong {
				out := acceptSeries(match, candYear)
				return &out
			}
			if weak == nil {
				weak = &weakHit{match: match, candYear: candYear}
			}
		}
	}
	if weak != nil {
		out := acceptSeries(weak.match, weak.candYear)
		return &out
	}
	return nil
}

func bravePhase2Series(
	ctx context.Context, sess *mode.Session, sig FileSignals, cfg MatchConfig,
	acceptSeries func(tmdb.Item, int) proposals.Proposal, gate *seriesGate, season, episode int, guessed, entryName string,
) *proposals.Proposal {
	if sess.WebSearch == nil || sess.MainstreamAI == nil {
		return nil
	}
	query := strings.TrimSpace(guessed)
	if query == "" {
		query = entryName
	}
	grounded, err := identify.GroundTitleViaSearch(ctx, sess.WebSearch, sess.MainstreamAI, query)
	if err != nil || grounded.Title == "" {
		return nil
	}
	cfg = cfg.Normalize()
	// Claude 2026-08-06: seed on the GROUNDED title, not the file-derived one
	// Reason: autopilot-impl-title-collision-fix §2.4 — this function's whole
	//   value is recovering when the filename is unusable; gating on the
	//   filename-derived seed would neuter it. Check B still applies at full
	//   strength regardless of seed.
	// Review if: library_series gains a real per-show folder-path column.
	seeded := gate.withSeed(grounded.Title)
	for _, term := range searchterm.SearchQueries(grounded.Title) {
		items, err := sess.TMDB.SearchTV(ctx, term)
		if err != nil || len(items) == 0 {
			continue
		}
		limit := pickLimit(cfg.CandidateN, len(items))
		for i := 0; i < limit; i++ {
			match := items[i]
			if _, err := sess.TMDB.SeasonDetails(ctx, match.ID, season); err != nil {
				continue
			}
			if !seeded.allows(match.Title, match.ID) {
				continue
			}
			candYear, _, _ := seriesCandidateMeta(ctx, sess.TMDB, match.ID, season, episode, match.ReleaseDate, sig)
			year := candYear
			if year == 0 {
				year = grounded.Year
			}
			out := acceptSeries(match, year)
			return &out
		}
	}
	return nil
}

// tvdbFallbackMovie resolves via TheTVDB when TMDB search/drilldown finds nothing.
// Applies the same FileSignals corroboration against the cross-referenced TMDB movie.
func tvdbFallbackMovie(
	ctx context.Context, sess *mode.Session,
	term string, byTMDB map[int]bool,
	generalRoot, foundRoot string, sig FileSignals, cfg MatchConfig,
	base proposals.Proposal,
) *proposals.Proposal {
	if sess.TVDB == nil || sess.TMDB == nil {
		return nil
	}
	cfg = cfg.Normalize()
	results, err := sess.TVDB.SearchMovies(ctx, term)
	if err != nil || len(results) == 0 {
		return nil
	}
	limit := pickLimit(cfg.CandidateN, len(results))
	type weakTVDBMovie struct {
		details  tmdb.MovieDetails
		candYear int
		bestName string
	}
	var weak *weakTVDBMovie
	accept := func(details tmdb.MovieDetails, candYear int, bestName string) *proposals.Proposal {
		targetRoot := generalRoot
		switch {
		case foundRoot == sess.KidsRootPath:
			targetRoot = sess.KidsRootPath
		case sess.KidsRootPath != "" && sess.MainstreamAI != nil:
			if result, err := classify.WithAI(ctx, sess.MainstreamAI, details.Title, details.Overview); err == nil && result.IsKids {
				targetRoot = sess.KidsRootPath
			}
		}
		p := base
		if byTMDB[details.ID] {
			acceptDuplicatePending(&p, details.Title, details.ID, candYear, targetRoot)
			if p.Reason == "" || !strings.HasPrefix(p.Reason, "alternate:") {
				p.Reason = fmt.Sprintf("alternate: TVDB match %q already in library", bestName)
			}
		} else {
			p.Status = proposals.Pending
			p.Title = details.Title
			p.TMDBID = details.ID
			p.Year = candYear
			p.RootFolderPath = targetRoot
		}
		p.Genres = details.Genres
		if names, err := sess.TMDB.MovieCredits(ctx, details.ID); err == nil {
			p.Cast = names
		}
		return &p
	}
	for i := 0; i < limit; i++ {
		best := results[i]
		tmdbID, err := sess.TMDB.FindMovieByTVDBID(ctx, best.TVDBID)
		if err != nil || tmdbID == 0 {
			continue
		}
		details, err := sess.TMDB.MovieDetails(ctx, tmdbID)
		if err != nil {
			continue
		}
		candYear := yearFromReleaseDate(details.ReleaseDate)
		if candYear == 0 && best.Year > 0 {
			candYear = best.Year
		}
		cast := []string(nil)
		if sig.Actor != "" {
			cast, _ = sess.TMDB.MovieCredits(ctx, details.ID)
		}
		rank := SignalsCorroborate(sig, candYear, details.Runtime, cast, cfg.DurationTolerancePct)
		if rank == CorroborationNone {
			continue
		}
		if rank == CorroborationStrong {
			return accept(details, candYear, best.Name)
		}
		if weak == nil {
			weak = &weakTVDBMovie{details: details, candYear: candYear, bestName: best.Name}
		}
	}
	if weak != nil {
		return accept(weak.details, weak.candYear, weak.bestName)
	}
	return nil
}

// tvdbFallbackSeries mirrors tvdbFallbackMovie for TV shows.
func tvdbFallbackSeries(
	ctx context.Context, sess *mode.Session,
	term string, tracked map[episodeKey]bool, gate *seriesGate,
	generalRoot, foundRoot string,
	season, episode int, extraEpisodes []int,
	sig FileSignals, cfg MatchConfig,
	base proposals.Proposal,
) *proposals.Proposal {
	if sess.TVDB == nil || sess.TMDB == nil {
		return nil
	}
	cfg = cfg.Normalize()
	results, err := sess.TVDB.SearchSeries(ctx, term)
	if err != nil || len(results) == 0 {
		return nil
	}
	limit := pickLimit(cfg.CandidateN, len(results))
	type weakTVDBSeries struct {
		tmdbID   int
		candYear int
		bestName string
		det      tmdb.TVDetails
	}
	var weak *weakTVDBSeries
	for i := 0; i < limit; i++ {
		best := results[i]
		// Claude 2026-08-06: conditional gate placement — §2.5/§3.4b
		// Reason: autopilot-impl-title-collision-fix — the TMDB id isn't
		//   known until FindTVByTVDBID below, so an unconditional loop-top
		//   title gate would let the weakest evidence (this site's `term`
		//   seed, the LAST query variant tried) veto a folder-confirmed id
		//   before it's even known — exactly the paradox §3.4b exists to
		//   prevent. Unpinned folders gate here (and save two TMDB round
		//   trips per rejection); pinned folders defer to the unified
		//   allows() call right after the id is resolved, so a
		//   folder-confirmed id always short-circuits Check A.
		// Troubleshooting: a folder-pinned show's TVDB-fallback match gets
		//   rejected by an odd seed — confirm this branch, not the one below,
		//   is what ran.
		// Review if: library_series gains a real per-show folder-path column.
		if gate.pinnedTMDBID <= 0 {
			if !gate.allowsTitle(best.Name) {
				// Claude 2026-08-06: explicit reject() — allowsTitle alone doesn't count
				// Reason: code review — only gate.allows() increments the shared
				//   counter; this direct allowsTitle() call bypassed it, so a file
				//   rejected only via this branch reported the generic
				//   unmatchedAfterWebAuthority reason instead of §4.3's distinct one.
				// Review if: allowsTitle/allowsID gain their own counting.
				gate.reject()
				continue
			}
		}
		tmdbID, err := sess.TMDB.FindTVByTVDBID(ctx, best.TVDBID)
		if err != nil || tmdbID == 0 {
			continue
		}
		if gate.pinnedTMDBID > 0 {
			if !gate.allows(best.Name, tmdbID) {
				continue
			}
		}
		if _, err := sess.TMDB.SeasonDetails(ctx, tmdbID, season); err != nil {
			continue
		}
		dateStr := ""
		if best.Year > 0 {
			dateStr = fmt.Sprintf("%d-01-01", best.Year)
		}
		candYear, runtimeMin, cast := seriesCandidateMeta(ctx, sess.TMDB, tmdbID, season, episode, dateStr, sig)
		if candYear == 0 && best.Year > 0 {
			candYear = best.Year
		}
		rank := SignalsCorroborate(sig, candYear, runtimeMin, cast, cfg.DurationTolerancePct)
		if rank == CorroborationNone {
			continue
		}
		det, err := sess.TMDB.TVDetails(ctx, tmdbID)
		if err != nil {
			continue
		}
		accept := func(tmdbID, candYear int, bestName string, det tmdb.TVDetails) *proposals.Proposal {
			// Claude 2026-08-07: SITE 3 of 7 — tracked slot is now an alternate, not a decline (plan §5.2.3)
			// Reason: deep-interview-sakms-series-parsing-accuracy-improvements §5.2 —
			//   this closure used to set Unmatched ("TVDB match %q appears to
			//   already ...") and return before populating anything. Movies' analogue (tvdbFallbackMovie, rename.go:1452)
			//   already calls acceptDuplicatePending here, so this is parity, not
			//   invention. The reason deliberately names det.Title, NOT bestName:
			//   bestName is TheTVDB's series name while the row this path emits
			//   carries p.Title = det.Title (TMDB's), and naming bestName would
			//   show the operator two different show names on one row. bestName
			//   consequently goes unused in the duplicate case; it is kept as a
			//   parameter because the caller at the strong-corroboration branch
			//   still passes it.
			// Troubleshooting: a TVDB-fallback duplicate Applied and OVERWROTE the
			//   slot's library_episodes row instead of folding in — the Apply-side
			//   fold-in gate (existing.FilePath != "") did not fire; check §0.3.
			// Review if: operators report unwanted alternates accumulating and
			//   want same-slot duplicates declined again.
			duplicateSlot := tracked[episodeKey{tmdbID: tmdbID, season: season, episode: episode}]
			targetRoot := generalRoot
			switch {
			case foundRoot == sess.KidsRootPath:
				targetRoot = sess.KidsRootPath
			case sess.KidsRootPath != "" && sess.MainstreamAI != nil:
				if result, err := classify.WithAI(ctx, sess.MainstreamAI, det.Title, ""); err == nil && result.IsKids {
					targetRoot = sess.KidsRootPath
				}
			}
			p := base
			p.Status = proposals.Pending
			p.Title = det.Title
			p.TMDBID = tmdbID
			p.Year = candYear
			p.SeasonNumber = season
			p.EpisodeNumber = episode
			if len(extraEpisodes) > 0 {
				p.ExtraEpisodeNumbers = extraEpisodes
			}
			p.RootFolderPath = targetRoot
			p.Genres = det.Genres
			if names, err := sess.TMDB.TVAggregateCredits(ctx, tmdbID); err == nil {
				p.Cast = names
			}
			// Site 3's softened outcome — Status+Reason only, never Title/Year/Root.
			if duplicateSlot {
				acceptDuplicatePendingEpisode(&p, det.Title, season, episode)
			}
			return &p
		}
		if rank == CorroborationStrong {
			return accept(tmdbID, candYear, best.Name, det)
		}
		if weak == nil {
			weak = &weakTVDBSeries{tmdbID: tmdbID, candYear: candYear, bestName: best.Name, det: det}
		}
	}
	if weak != nil {
		// Claude 2026-08-07: SITE 4 of 7 — tracked slot is now an alternate, not a decline (plan §5.2.3)
		// Reason: deep-interview-sakms-series-parsing-accuracy-improvements §5.2 —
		//   the weak/uncorroborated twin of Site 3 above, carrying an identical
		//   retired reason string. It is a SEPARATE site, not a diff artifact:
		//   it fires when no candidate reached CorroborationStrong, which on a
		//   low-signal library is the MORE common path of the two and is exactly
		//   the target population. Same det.Title-not-bestName reasoning as
		//   Site 3 (the emitted row carries weak.det.Title).
		// Troubleshooting: a weak-branch duplicate Applied and OVERWROTE the
		//   slot's library_episodes row instead of folding in — the Apply-side
		//   fold-in gate (existing.FilePath != "") did not fire; check §0.3.
		// Review if: operators report unwanted alternates accumulating and want
		//   same-slot duplicates declined again.
		duplicateSlot := tracked[episodeKey{tmdbID: weak.tmdbID, season: season, episode: episode}]
		targetRoot := generalRoot
		switch {
		case foundRoot == sess.KidsRootPath:
			targetRoot = sess.KidsRootPath
		case sess.KidsRootPath != "" && sess.MainstreamAI != nil:
			if result, err := classify.WithAI(ctx, sess.MainstreamAI, weak.det.Title, ""); err == nil && result.IsKids {
				targetRoot = sess.KidsRootPath
			}
		}
		p := base
		p.Status = proposals.Pending
		p.Title = weak.det.Title
		p.TMDBID = weak.tmdbID
		p.Year = weak.candYear
		p.SeasonNumber = season
		p.EpisodeNumber = episode
		if len(extraEpisodes) > 0 {
			p.ExtraEpisodeNumbers = extraEpisodes
		}
		p.RootFolderPath = targetRoot
		p.Genres = weak.det.Genres
		if names, err := sess.TMDB.TVAggregateCredits(ctx, weak.tmdbID); err == nil {
			p.Cast = names
		}
		// Site 4's softened outcome — Status+Reason only, never Title/Year/Root.
		if duplicateSlot {
			acceptDuplicatePendingEpisode(&p, weak.det.Title, season, episode)
		}
		return &p
	}
	return nil
}

func RelocateEpisode(sourcePath, destRoot, seriesTitle string, seriesYear, tmdbID, seasonNumber, episodeNumber int, episodeTitle string, preset naming.Preset) (string, error) {
	return RelocateEpisodeRange(sourcePath, destRoot, seriesTitle, seriesYear, tmdbID, seasonNumber, []int{episodeNumber}, episodeTitle, preset)
}

// RelocateEpisodeRange is RelocateEpisode's logical-episode-split sibling:
// identical relocation mechanics, but names the destination file via
// naming.EpisodeRangeFileName's episodeNumbers list (e.g. "S03E05-E06")
// instead of a single episode number. RelocateEpisode is now a thin
// wrapper over this function with a 1-element slice, so the ordinary
// single-episode path's behavior (including its exact destination name) is
// unchanged — EpisodeRangeFileName falls straight through to
// EpisodeFileName's own rendering for fewer than 2 numbers.
//
// Claude 2026-08-06: episodeTitle threaded through for Jellyfin/Legacy names
// Reason: Apply was dropping titles → "Show S01E01.ext" only; operators need
//
//	identifiable Jellyfin names ("Show S01E01 Pilot.ext").
//
// Troubleshooting: applied filenames unreadable → episodeTitle was always "".
// Review if: proposals gain a persisted episode_title from Scan.
func RelocateEpisodeRange(sourcePath, destRoot, seriesTitle string, seriesYear, tmdbID, seasonNumber int, episodeNumbers []int, episodeTitle string, preset naming.Preset) (string, error) {
	seriesFolder := naming.SeriesFolderName(preset, seriesTitle, seriesYear, tmdbID)
	seasonDir := filepath.Join(destRoot, seriesFolder, naming.SeasonDirName(seasonNumber))
	dest := filepath.Join(seasonDir, naming.EpisodeRangeFileName(preset, seriesTitle, seasonNumber, episodeNumbers, episodeTitle, filepath.Ext(sourcePath)))
	if dest == sourcePath {
		return dest, nil
	}

	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		return "", fmt.Errorf("creating %q: %w", seasonDir, err)
	}
	unique, err := place.UniquePath(dest, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	if err != nil {
		return "", err
	}
	if err := os.Rename(sourcePath, unique); err != nil {
		return "", fmt.Errorf("moving %q to %q: %w", sourcePath, unique, err)
	}
	return unique, nil
}

// ApplyLibrarySeries is Rename's Series-library counterpart to ApplyLibrary.
// p must be Pending. Relocates the file via RelocateEpisode, then
// get-or-creates the series (by TMDB id) and upserts the episode row.
// Existing title/air-date metadata for this exact episode (e.g. from a
// prior Sonarr import or Scan reporting it as missing) is preserved rather
// than blanked out, since this Apply call only ever supplies a file path,
// never episode metadata of its own.
//
// Unlike ApplyLibrary (Movies), p.SourcePath here IS the file being moved
// directly — Series' ScanLibrarySeries never wraps the path in a directory
// indirection the way Movies' orphan-folder case can — so the Deleted side
// of changes is p.SourcePath itself, not a resolved video file. This
// asymmetry with ApplyLibrary is intentional, not an oversight.
//
// changes is a named return so a post-move failure (e.g. libStore.UpsertSeries)
// still reports the committed file move to the caller for
// Session.NotifyPlayers — the physical relocate already happened by then
// and must not go unnotified (partial-success rule, player-rescan-notify).
//
// tier is the operator's configured quality tier for this mode — see
// ApplyLibrary for why it's a plain string parameter rather than a lookup.
//
// tmdbClient is optional: when non-nil, Apply fetches SeasonDetails so the
// destination Jellyfin/Legacy file name can include the episode title (and
// so empty library episode metadata can be filled). A TMDB failure is soft —
// Apply still relocates with whatever title library already has (often "").
//
// tvdbClient is the same deal for the anthology path: when non-nil, and the
// proposal carries a real TheTVDB series id with a synthetic (non-positive)
// TMDB id, Apply fetches the show's episode catalog so the same two
// consumers — the destination file name and library_episodes.title — can be
// filled from TheTVDB instead. Also soft; see the branch below.
//
// Claude 2026-08-06: resolve episode title before RelocateEpisodeRange
// Reason: Jellyfin names need "Show S01E01 Pilot.ext"; title was never passed.
// Troubleshooting: Apply batch left bare SxxExx names (unidentifiable).
// Review if: Proposal gains a real EpisodeTitle column, which would make both the TMDB and TheTVDB fetches below redundant.
// prober is appended last, matching ApplyLibrary's trailing-prober convention.
// It is nil-safe (probeFileMeta returns the fallback-tier meta for a nil
// prober), and is used only by the alternate fold-in path and the primary
// library_episode_files write below.
func ApplyLibrarySeries(ctx context.Context, libStore *library.Store, tmdbClient *tmdb.Client, tvdbClient *tvdb.Client, p proposals.Proposal, preset naming.Preset, tier string, prober Prober) (episodeID int64, changes []mode.PathChange, err error) {
	if p.Status != proposals.Pending {
		return 0, nil, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}

	allEpisodeNumbers := append([]int{p.EpisodeNumber}, p.ExtraEpisodeNumbers...)

	// Series row first so GetEpisode can resolve titles for the destination
	// name before any os.Rename — UpsertSeries is idempotent.
	// Claude 2026-08-07: thread TVDBID into the series row
	// Reason: the anthology pass' established-pin shortcut keys on
	//   library_series.tvdb_id, so a newly-added short only stops being
	//   Unmatched forever if THIS write persists it. UpsertSeries does
	//   `tvdb_id = excluded.tvdb_id` unconditionally, which is safe here:
	//   every ordinary rename proposal carries TVDBID = 0 (matching every
	//   existing row), and all of one show's anthology proposals carry the
	//   same id, so repeated Applies are idempotent.
	// Troubleshooting: a pinned anthology show stops matching new files after
	//   a Series Dedup Apply — internal/dedup/dedup.go:412 builds its own
	//   library.Series literal WITHOUT TVDBID and blanks this column back to
	//   0 with no error and no log line (internal/api/import.go:239 has the
	//   same shape but is effectively unreachable). That is a KNOWN,
	//   ACCEPTED limitation, deliberately left out of this feature's scope:
	//   it is a recall regression only — no misplacement, no corruption — and
	//   needs an untracked Dedup winner on an anthology show specifically.
	//   See .omc/plans/autopilot-impl.md §6.3a and §9.
	// Review if: internal/dedup grows TVDBID threading, retiring the hazard.
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: p.TMDBID, TVDBID: p.TVDBID, Title: p.Title, Year: p.Year, RootFolderPath: p.RootFolderPath,
		Genres: p.Genres, Cast: p.Cast,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("recording series %q: %w", p.Title, err)
	}

	tmdbByEp := map[int]tmdb.SeasonEpisode{}
	if tmdbClient != nil && p.TMDBID > 0 {
		if eps, serr := tmdbClient.SeasonDetails(ctx, p.TMDBID, p.SeasonNumber); serr == nil {
			for _, ep := range eps {
				tmdbByEp[ep.EpisodeNumber] = ep
			}
		}
	}

	// TheTVDB counterpart to tmdbByEp above, for anthology matches. Gated on
	// the exact complement of the TMDB branch's precondition: p.TMDBID <= 0
	// means a synthetic negative id from the anthology pass, and p.TVDBID > 0
	// means that pass recorded a real TheTVDB series id. Both together are
	// the only shape this fires for — an ordinary Series proposal (TMDBID > 0,
	// TVDBID == 0) never reaches it, so no existing behaviour changes.
	//
	// FAIL-SOFT, deliberately — and this is the OPPOSITE of SeriesEpisodes'
	// own fail-closed contract, which is why it is spelled out rather than
	// left to judgement. Fail-closed is right at SCAN time, where a partial
	// catalog would produce a confidently wrong (season, episode). Here the
	// season and episode are ALREADY DECIDED and persisted on the proposal;
	// the catalog is consulted only for a display title. So the error is
	// swallowed exactly as the TMDB branch swallows serr, and the title falls
	// through to "" — which naming.EpisodeFileName already handles by
	// omitting the title segment entirely, i.e. today's behaviour.
	//
	// The fetch sits HERE, outside resolveEpisodeMeta, on purpose: the
	// closure runs once per episode number, and its second call site is
	// AFTER RelocateEpisodeRange. Fetching inside it would issue one request
	// per number and move part of the network work past the file relocation
	// the fail-soft argument above depends on happening last.
	//
	// The season filter is load-bearing: SeriesEpisodes returns the WHOLE
	// show's catalog across every season in one call, so indexing by Number
	// alone would collide episodes of the same number in different seasons.
	// tmdbByEp needs no equivalent because SeasonDetails is already
	// per-season.
	tvdbByEp := map[int]tvdb.Episode{}
	if tvdbClient != nil && p.TVDBID > 0 && p.TMDBID <= 0 {
		if catalog, terr := tvdbClient.SeriesEpisodes(ctx, p.TVDBID, tvdb.SeasonTypeOfficial); terr == nil {
			for _, ep := range catalog {
				if ep.SeasonNumber == p.SeasonNumber {
					tvdbByEp[ep.Number] = ep
				}
			}
		}
	}

	resolveEpisodeMeta := func(episodeNumber int) (title, airDate string, err error) {
		if existing, gerr := libStore.GetEpisode(ctx, series.ID, p.SeasonNumber, episodeNumber); gerr == nil {
			title, airDate = existing.Title, existing.AirDate
		} else if !errors.Is(gerr, library.ErrNotFound) {
			return "", "", fmt.Errorf("checking existing metadata for episode %d: %w", episodeNumber, gerr)
		}
		if se, ok := tmdbByEp[episodeNumber]; ok {
			if title == "" {
				title = se.Name
			}
			if airDate == "" {
				airDate = se.AirDate
			}
		}
		// Same "only if still empty" shape as the TMDB fill above, and for
		// the same reason: a library title always wins over a freshly-fetched
		// one (see the never-blank note on the toUpsert loop below). tvdbByEp
		// is empty unless the anthology gate fired, so this is inert on every
		// ordinary Series Apply.
		if ep, ok := tvdbByEp[episodeNumber]; ok {
			if title == "" {
				title = ep.Name
			}
			if airDate == "" {
				airDate = ep.Aired
			}
		}
		return title, airDate, nil
	}

	// Destination file name uses the primary episode's title (range names
	// share one title slot in naming.EpisodeRangeFileName).
	fileTitle, _, err := resolveEpisodeMeta(p.EpisodeNumber)
	if err != nil {
		return 0, nil, err
	}

	// Claude 2026-08-07: alternate fold-in for an already-occupied episode slot
	// Reason: deep-interview-sakms-series-parsing-accuracy-improvements — the Series
	//   counterpart to ApplyLibrary's GetByTMDBID/applyLibraryAlternate branch. This is
	//   what makes the softened Scan-time guards safe: the collision is resolved against
	//   LIVE DB state at Apply time, so two same-slot Pending rows converge
	//   order-independently on the higher-tier file as primary.
	//
	// EVERY NUMBER IN THE RANGE IS CHECKED, NOT JUST p.EpisodeNumber. A range proposal
	//   whose PRIMARY slot is free but whose SECOND slot is occupied would otherwise fall
	//   through to UpsertEpisodes, whose ON CONFLICT(series_id, season_number,
	//   episode_number) silently overwrites the occupied row and strands its file on disk
	//   untracked.
	//
	// THE FilePath == "" CHECK BELOW AND THE SEPARATE !fileExists(...) CHECK BELOW ARE
	//   JOINTLY LOAD-BEARING — do not simplify either to "a row exists". library_episodes
	//   carries FILELESS catalog rows by design (UpsertEpisodeCatalog) so that
	//   MissingEpisodes is a plain query; the FilePath == "" check skips those. Dropping
	//   THAT check (gating on row existence alone) would route EVERY ordinary Series
	//   Apply for any catalog-synced show through the alternate path. Separately, a row
	//   can carry a non-empty FilePath that points at a file no longer on disk — the
	//   !fileExists(...) check skips those. Dropping THAT check would misroute only an
	//   Apply landing on a stale row. ScanLibrarySeries' own `tracked` map applies the
	//   identical skip for the identical reason.
	//
	// THE LOOP MUST NOT BREAK EARLY. It has TWO jobs, and an early break serves only the
	//   first: (a) find the first occupied slot, which decides WHETHER to fold in and
	//   supplies the single row the tier comparison is made against; (b) build a COMPLETE
	//   map of every number in the range to its library_episodes row (or nil), which
	//   applyLibrarySeriesAlternate's write loop needs to decide which alternate file rows
	//   to write. Breaking on the first occupied slot leaves every LATER number unqueried
	//   and therefore indistinguishable from "no row at all", so a slot that genuinely has
	//   one would silently get no alternate row. Dropping the break costs at most
	//   len(allEpisodeNumbers)-1 extra GetEpisode calls on a path that is already about to
	//   move files on disk.
	//
	// Troubleshooting: an ordinary Apply produced an "- alternate" filename → this gate
	//   fired on a fileless catalog row; confirm the occupied row's FilePath is genuinely
	//   non-empty and the file is genuinely on disk.
	// Review if: library_episodes stops carrying fileless catalog rows.
	resolved := map[int]*library.Episode{}
	var occupied *library.Episode
	for _, n := range allEpisodeNumbers {
		existing, gerr := libStore.GetEpisode(ctx, series.ID, p.SeasonNumber, n)
		if gerr != nil {
			if errors.Is(gerr, library.ErrNotFound) {
				resolved[n] = nil // no row at all — the write loop skips this number
				continue
			}
			return 0, nil, fmt.Errorf("looking up existing episode file: %w", gerr)
		}
		// Assigned BEFORE the guards below on purpose: a self-collision or a
		// stale row still HAS an episode_id, so the alternate write loop must
		// still write a row for it.
		resolved[n] = existing
		if existing.FilePath == "" {
			continue
		}
		// A re-Apply of the SAME file must not fold itself in as its own
		// alternate, and a stale row pointing at a deleted file is not an
		// occupied slot — fall through so the new file simply becomes primary.
		if existing.FilePath == p.SourcePath || !fileExists(existing.FilePath) {
			continue
		}
		if occupied == nil {
			occupied = existing // FIRST occupied slot only — do NOT break; the map needs the rest
		}
	}
	if occupied != nil {
		return applyLibrarySeriesAlternate(ctx, libStore, series, occupied, resolved, p, p.SourcePath,
			allEpisodeNumbers, fileTitle, preset, tier, prober)
	}

	moved, err := RelocateEpisodeRange(p.SourcePath, p.RootFolderPath, p.Title, p.Year, p.TMDBID, p.SeasonNumber, allEpisodeNumbers, fileTitle, preset)
	if err != nil {
		return 0, nil, fmt.Errorf("relocating %q: %w", p.SourcePath, err)
	}
	// RelocateEpisodeRange's self-collision guard means moved can equal
	// p.SourcePath (file was already correctly placed — no os.Rename
	// happened). Emitting a Deleted+Created pair for the same unchanged
	// path would be a bogus notify, so only report a change when a move
	// actually occurred.
	if moved != p.SourcePath {
		changes = []mode.PathChange{{Path: p.SourcePath, Kind: mode.Deleted}, {Path: moved, Kind: mode.Created}}
	}

	// Logical episode-splitting: the file was relocated exactly ONCE above
	// (allEpisodeNumbers), so every bundled number (primary plus every
	// extra) gets its own Episode row pointing at that SAME moved path —
	// never a second relocate. Each row's existing title/air-date is looked
	// up and preserved BEFORE the write (a prior TMDB-seeded value must
	// never be silently blanked just because this Apply call only supplied
	// a file path); missing fields fill from TMDB when available. The writes
	// themselves go through UpsertEpisodes in ONE transaction: without that,
	// a failure partway through (e.g. episode 2's write failing after
	// episode 1's already committed) would leave the relocated file "known"
	// — masked from ever being reported as an orphan again by a later Scan —
	// with episode 2's row still missing and unrecoverable. Atomic writes
	// mean a partial failure commits nothing, so a re-Scan can still discover
	// and correctly resolve the file.
	// One physical file, so one stat: every Episode row a bundled multi-episode
	// file produces carries that file's FULL size, not a share of it. The
	// storage aggregation de-duplicates by file_path rather than dividing, so
	// splitting the bytes here would under-report the file.
	movedSize := library.FileSize(moved)
	toUpsert := make([]library.Episode, 0, 1+len(p.ExtraEpisodeNumbers))
	for _, episodeNumber := range allEpisodeNumbers {
		epTitle, epAirDate, merr := resolveEpisodeMeta(episodeNumber)
		if merr != nil {
			return 0, changes, merr
		}
		toUpsert = append(toUpsert, library.Episode{
			SeriesID: series.ID, SeasonNumber: p.SeasonNumber, EpisodeNumber: episodeNumber,
			Title: epTitle, AirDate: epAirDate, FilePath: moved,
			Size: movedSize, QualityTier: tier,
		})
	}
	upserted, err := libStore.UpsertEpisodes(ctx, toUpsert)
	if err != nil {
		return 0, changes, fmt.Errorf("recording episode(s): %w", err)
	}

	// Primary library_episode_files row for each upserted episode — the Series
	// counterpart to ApplyLibrary's UpsertFile write. WITHOUT THIS, a second
	// Apply for the same slot has no primary file row to demote and
	// ListEpisodeFiles returns empty for freshly-applied episodes, so alternate
	// support would work only for pre-existing (backfilled) episodes and
	// silently half-fail for new ones.
	//
	// Loops over `upserted`, so a range file writes one primary row per bundled
	// episode — consistent with migration 0005's backfill and with the alternate
	// path's per-slot fan-out. THE INVARIANT THAT MAKES THAT SAFE: this code is
	// reachable only when EVERY number in allEpisodeNumbers is free — the gate
	// above returns into the alternate path the moment any one of them holds a
	// file on disk. So no write here can collide with an existing primary. If
	// that gate is ever narrowed back toward a single-episode check, this loop
	// becomes unsafe; the two are a pair.
	//
	// The PROBED tier is used for the file row only. ApplyLibrarySeries still
	// records the raw settings `tier` on the episode row above — deliberately
	// unchanged, so this matches how Movies populates its file rows without
	// altering existing episode-row behaviour.
	probed := probeFileMeta(ctx, prober, moved, tier)
	for _, ep := range upserted {
		if _, ferr := libStore.UpsertEpisodeFile(ctx, library.EpisodeFile{
			EpisodeID: ep.ID, FilePath: moved, IsPrimary: true,
			QualityTier: probed.Tier, Size: movedSize,
			Width: probed.Width, Height: probed.Height, VideoCodec: probed.Codec,
			BitRate: probed.BitRate, DurationSec: probed.Duration,
		}); ferr != nil {
			return upserted[0].ID, changes, fmt.Errorf("recording primary file for %q S%02dE%02d: %w",
				p.Title, ep.SeasonNumber, ep.EpisodeNumber, ferr)
		}
	}
	return upserted[0].ID, changes, nil
}
