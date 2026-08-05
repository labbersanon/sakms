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
	if !sig.HasAny() {
		p.Status = proposals.Unmatched
		p.Reason = "no year, actor, or duration available to corroborate a match — needs manual review"
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

	if lastLimit == 0 {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("no TMDB match for queries derived from %q", entry.Name)
		return p
	}
	p.Status = proposals.Unmatched
	p.Reason = fmt.Sprintf("no TMDB candidate in top %d passed corroboration (last query %q)", lastLimit, lastTerm)
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

	existing, getErr := libStore.GetByTMDBID(ctx, mode.Movies, p.TMDBID)
	if getErr == nil && existing != nil {
		return applyLibraryAlternate(ctx, libStore, existing, p, videoPath, preset, tier, prober)
	}
	if getErr != nil && !errors.Is(getErr, library.ErrNotFound) {
		return 0, nil, fmt.Errorf("looking up existing library title: %w", getErr)
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

	known := map[string]bool{}
	tracked := map[episodeKey]bool{}
	for _, series := range allSeries {
		episodes, err := libStore.ListEpisodes(ctx, series.ID)
		if err != nil {
			return nil, fmt.Errorf("loading episodes for %q: %w", series.Title, err)
		}
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
			videoFiles, err := library.ResolveEpisodeVideoFiles(entry.Path)
			if err != nil {
				continue // silent omit — non-video / empty of video (Jellyfin VideoExts gate)
			}
			for _, videoPath := range videoFiles {
				if naming.MatchesSeriesSchema(videoPath, preset) {
					continue // already organized under the active preset — nothing to propose
				}
				out = append(out, proposeOneEpisodeLibrary(ctx, sess, tracked, rootFolderPath, root, videoPath, cfg, prober))
			}
		}
	}
	return out, nil
}

func proposeOneEpisodeLibrary(
	ctx context.Context, sess *mode.Session, tracked map[episodeKey]bool,
	generalRoot, foundRoot, videoPath string, cfg MatchConfig, prober Prober,
) proposals.Proposal {
	cfg = cfg.Normalize()
	name := filepath.Base(videoPath)
	p := proposals.Proposal{
		Mode: mode.Series, Workflow: proposals.Rename,
		SourceName: name, SourcePath: videoPath, RootFolderPath: foundRoot,
	}

	season, episodes, ok := library.ParseEpisodeNumbers(name)
	if !ok {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("could not determine season/episode from %q", name)
		return p
	}
	episode := episodes[0]
	extraEpisodes := episodes[1:]

	if hint := nfo.ReadSeriesSidecar(videoPath); hint.TMDBID != 0 {
		if tracked[episodeKey{tmdbID: hint.TMDBID, season: season, episode: episode}] {
			p.Status = proposals.Unmatched
			p.Reason = fmt.Sprintf(".nfo TMDB id %d appears to already be in the library as S%02dE%02d — leaving in place for manual review", hint.TMDBID, season, episode)
			return p
		}
		if _, err := sess.TMDB.SeasonDetails(ctx, hint.TMDBID, season); err != nil {
			p.Status = proposals.Unmatched
			p.Reason = fmt.Sprintf(".nfo TMDB id %d: could not confirm season %d: %v", hint.TMDBID, season, err)
			return p
		}
		det, err := sess.TMDB.TVDetails(ctx, hint.TMDBID)
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
		return p
	}

	sig := ExtractFileSignals(ctx, name, videoPath, prober)
	if !sig.HasAny() {
		p.Status = proposals.Unmatched
		p.Reason = "no year, actor, or duration available to corroborate a match — needs manual review"
		return p
	}

	queries := searchterm.SearchQueries(library.StripEpisodeMarker(name))
	var lastTerm string
	var lastLimit int
	type weakSeries struct {
		match    tmdb.Item
		candYear int
	}
	var weak *weakSeries
	acceptSeries := func(match tmdb.Item, candYear int) proposals.Proposal {
		if tracked[episodeKey{tmdbID: match.ID, season: season, episode: episode}] {
			p.Status = proposals.Unmatched
			p.Reason = fmt.Sprintf("appears to already be in the library as %q S%02dE%02d — leaving in place for manual review", match.Title, season, episode)
			return p
		}
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
		return p
	}
	for _, term := range queries {
		lastTerm = term
		items, err := sess.TMDB.SearchTV(ctx, term)
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
			candYear, runtimeMin, cast := seriesCandidateMeta(ctx, sess.TMDB, match.ID, season, episode, match.ReleaseDate, sig)
			rank := SignalsCorroborate(sig, candYear, runtimeMin, cast, cfg.DurationTolerancePct)
			if rank == CorroborationNone {
				continue
			}
			// Season must exist before we treat this as a real pick.
			if _, err := sess.TMDB.SeasonDetails(ctx, match.ID, season); err != nil {
				continue
			}
			if rank == CorroborationStrong {
				return acceptSeries(match, candYear)
			}
			if weak == nil {
				weak = &weakSeries{match: match, candYear: candYear}
			}
		}
	}
	if weak != nil {
		return acceptSeries(weak.match, weak.candYear)
	}

	if fb := tvdbFallbackSeries(ctx, sess, lastTerm, tracked, generalRoot, foundRoot, season, episode, extraEpisodes, sig, cfg, p); fb != nil {
		return *fb
	}

	// Claude 2026-08-05: Series GuessTitle + Brave Phase 2 (parity with Movies)
	// Reason: deep-interview-rename-brave-phase2
	// Troubleshooting: wrong show title with valid SxxExx stayed Unmatched
	// Review if: Series Brave should re-parse SxxExx from grounded title
	guessed := ""
	if sess.MainstreamAI != nil {
		if g, gerr := identify.GuessTitle(ctx, sess.MainstreamAI, name); gerr == nil && g != "" {
			guessed = g
			if got := trySeriesQueries(ctx, sess, sig, cfg, acceptSeries, season, episode, searchterm.SearchQueries(guessed)); got != nil {
				return *got
			}
		}
	}
	if got := bravePhase2Series(ctx, sess, sig, cfg, acceptSeries, season, episode, guessed, name); got != nil {
		return *got
	}

	if lastLimit == 0 {
		p.Status = proposals.Unmatched
		p.Reason = fmt.Sprintf("no TMDB match for queries derived from %q", name)
		return p
	}
	p.Status = proposals.Unmatched
	p.Reason = fmt.Sprintf("no TMDB candidate in top %d passed corroboration (last query %q)", lastLimit, lastTerm)
	return p
}

func trySeriesQueries(
	ctx context.Context, sess *mode.Session, sig FileSignals, cfg MatchConfig,
	acceptSeries func(tmdb.Item, int) proposals.Proposal, season, episode int, queries []string,
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
	acceptSeries func(tmdb.Item, int) proposals.Proposal, season, episode int, guessed, entryName string,
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
	term string, tracked map[episodeKey]bool,
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
		tmdbID, err := sess.TMDB.FindTVByTVDBID(ctx, best.TVDBID)
		if err != nil || tmdbID == 0 {
			continue
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
			if tracked[episodeKey{tmdbID: tmdbID, season: season, episode: episode}] {
				p := base
				p.Status = proposals.Unmatched
				p.Reason = fmt.Sprintf("TVDB match %q appears to already be in the library as S%02dE%02d — leaving in place for manual review", bestName, season, episode)
				return &p
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
		if tracked[episodeKey{tmdbID: weak.tmdbID, season: season, episode: episode}] {
			p := base
			p.Status = proposals.Unmatched
			p.Reason = fmt.Sprintf("TVDB match %q appears to already be in the library as S%02dE%02d — leaving in place for manual review", weak.bestName, season, episode)
			return &p
		}
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
		return &p
	}
	return nil
}

func RelocateEpisode(sourcePath, destRoot, seriesTitle string, seriesYear, tmdbID, seasonNumber, episodeNumber int, preset naming.Preset) (string, error) {
	return RelocateEpisodeRange(sourcePath, destRoot, seriesTitle, seriesYear, tmdbID, seasonNumber, []int{episodeNumber}, preset)
}

// RelocateEpisodeRange is RelocateEpisode's logical-episode-split sibling:
// identical relocation mechanics, but names the destination file via
// naming.EpisodeRangeFileName's episodeNumbers list (e.g. "S03E05-E06")
// instead of a single episode number. RelocateEpisode is now a thin
// wrapper over this function with a 1-element slice, so the ordinary
// single-episode path's behavior (including its exact destination name) is
// unchanged — EpisodeRangeFileName falls straight through to
// EpisodeFileName's own rendering for fewer than 2 numbers.
func RelocateEpisodeRange(sourcePath, destRoot, seriesTitle string, seriesYear, tmdbID, seasonNumber int, episodeNumbers []int, preset naming.Preset) (string, error) {
	seriesFolder := naming.SeriesFolderName(preset, seriesTitle, seriesYear, tmdbID)
	seasonDir := filepath.Join(destRoot, seriesFolder, naming.SeasonDirName(seasonNumber))
	dest := filepath.Join(seasonDir, naming.EpisodeRangeFileName(preset, seriesTitle, seasonNumber, episodeNumbers, "", filepath.Ext(sourcePath)))
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
func ApplyLibrarySeries(ctx context.Context, libStore *library.Store, p proposals.Proposal, preset naming.Preset, tier string) (episodeID int64, changes []mode.PathChange, err error) {
	if p.Status != proposals.Pending {
		return 0, nil, fmt.Errorf("proposal %d is %q, not pending — nothing to apply", p.ID, p.Status)
	}

	allEpisodeNumbers := append([]int{p.EpisodeNumber}, p.ExtraEpisodeNumbers...)
	moved, err := RelocateEpisodeRange(p.SourcePath, p.RootFolderPath, p.Title, p.Year, p.TMDBID, p.SeasonNumber, allEpisodeNumbers, preset)
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

	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: p.TMDBID, Title: p.Title, Year: p.Year, RootFolderPath: p.RootFolderPath,
		Genres: p.Genres, Cast: p.Cast,
	})
	if err != nil {
		return 0, changes, fmt.Errorf("recording series %q: %w", p.Title, err)
	}

	// Logical episode-splitting: the file was relocated exactly ONCE above
	// (allEpisodeNumbers), so every bundled number (primary plus every
	// extra) gets its own Episode row pointing at that SAME moved path —
	// never a second relocate. Each row's existing title/air-date is looked
	// up and preserved BEFORE the write (a prior TMDB-seeded value must
	// never be silently blanked just because this Apply call only supplied
	// a file path). The writes themselves go through UpsertEpisodes in ONE
	// transaction: without that, a failure partway through (e.g. episode 2's
	// write failing after episode 1's already committed) would leave the
	// relocated file "known" — masked from ever being reported as an orphan
	// again by a later Scan — with episode 2's row still missing and
	// unrecoverable. Atomic writes mean a partial failure commits nothing,
	// so a re-Scan can still discover and correctly resolve the file.
	// One physical file, so one stat: every Episode row a bundled multi-episode
	// file produces carries that file's FULL size, not a share of it. The
	// storage aggregation de-duplicates by file_path rather than dividing, so
	// splitting the bytes here would under-report the file.
	movedSize := library.FileSize(moved)
	toUpsert := make([]library.Episode, 0, 1+len(p.ExtraEpisodeNumbers))
	for _, episodeNumber := range allEpisodeNumbers {
		epTitle, epAirDate := "", ""
		if existing, err := libStore.GetEpisode(ctx, series.ID, p.SeasonNumber, episodeNumber); err == nil {
			epTitle, epAirDate = existing.Title, existing.AirDate
		} else if !errors.Is(err, library.ErrNotFound) {
			return 0, changes, fmt.Errorf("checking existing metadata for episode %d: %w", episodeNumber, err)
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
	return upserted[0].ID, changes, nil
}
