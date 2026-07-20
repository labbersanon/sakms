// Package availability answers one narrow question: "does any release exist
// for this picked title right now?" — the lightweight indexer probe that
// turns Discover's browse-by-id flow into a badge, replacing the old
// "hand the title string to free-text Search" wiring. It is NOT a grab and
// stages nothing: it fetches the structured ids for a TMDB id (via the
// tmdb.Client's details-by-id calls) and runs one id-scoped prowlarr.SearchByID,
// reporting only whether anything came back.
//
// Adult is the one asymmetric case: an Adult scene has no tmdb/imdb/tvdb id, so
// CheckAdultScene skips TMDB entirely and probes prowlarr with a free-text
// studio+title query over the XXX category (see its doc).
//
// It deliberately does not import internal/api (that would be a cycle — api
// imports this), so the Newznab category values below are local constants that
// mirror internal/api's categoriesForSearch (Movies=2000, Series=5000), kept in
// sync by convention, not a shared symbol — with the Adult category (6000) the
// one deliberate exception, see adultCategory.
package availability

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// Newznab category codes, mirroring internal/api's categoriesForSearch — the
// 2000-range for Movies, the 5000-range for TV (both cover single files and
// packs; Newznab doesn't split those). Duplicated here rather than imported to
// avoid an internal/api import cycle (see package doc).
const (
	moviesCategory = 2000
	seriesCategory = 5000
	// adultCategory is the XXX Newznab range (6000). This deliberately does
	// NOT mirror categoriesForSearch, which still gives Adult 2000 — a
	// pre-existing imprecision Stage 6 left untouched; CheckAdultScene is new
	// code with no existing behavior to preserve, so it uses the correct range.
	adultCategory = 6000
)

// Result is one availability probe's outcome — an existence check, never a
// grab. CheckedAt is a UTC RFC3339Nano timestamp, matching the Go-side
// time-formatting convention this project already uses for in-memory times
// (e.g. rename.ApplyLibraryAdult's PHashFileMTime, dedup's fileIdentity).
type Result struct {
	Available    bool   `json:"available"`
	ReleaseCount int    `json:"releaseCount"`
	CheckedAt    string `json:"checkedAt"`
}

// newResult builds a Result from a probe's release list, stamping CheckedAt.
func newResult(releases []prowlarr.Release) Result {
	return Result{
		Available:    len(releases) > 0,
		ReleaseCount: len(releases),
		CheckedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// CheckMovie probes whether any release exists for the Movies-catalog TMDB id.
// It fetches the movie's details for its IMDB id — which /movie/{id} carries
// natively at the top level (no separate external_ids round-trip, unlike TV) —
// then runs one id-scoped Prowlarr search over the Movies category. IMDBID is
// passed straight through: SearchByID strips the "tt" prefix itself. The same
// details call also supplies Query (the title) — see SearchByIDParams' own
// doc for why id params alone aren't a reliable filter for every indexer;
// this was a real, confirmed bug (id-only requests returning a broad,
// unrelated "recent releases" dump instead of nothing), not a defensive
// guess.
//
// A nil tmdbClient or prowlarrClient yields a clear "not configured" error
// rather than a panic, matching the tolerant-nil convention every mode.Session
// client already follows — so a caller reaching this directly (e.g. a future
// recheck job) gets an honest error, not a crash.
func CheckMovie(ctx context.Context, tmdbClient *tmdb.Client, prowlarrClient *prowlarr.Client, tmdbID int) (Result, error) {
	if tmdbClient == nil {
		return Result{}, fmt.Errorf("tmdb isn't configured — add it in Settings first")
	}
	if prowlarrClient == nil {
		return Result{}, fmt.Errorf("prowlarr isn't configured — add it in Settings first")
	}

	details, err := tmdbClient.MovieDetails(ctx, tmdbID)
	if err != nil {
		return Result{}, fmt.Errorf("fetching movie details for tmdb id %d: %w", tmdbID, err)
	}

	releases, err := prowlarrClient.SearchByID(ctx, prowlarr.SearchByIDParams{
		Query:      details.Title,
		TMDBID:     tmdbID,
		IMDBID:     details.IMDBID,
		Categories: []int{moviesCategory},
	})
	if err != nil {
		return Result{}, fmt.Errorf("probing prowlarr for tmdb id %d: %w", tmdbID, err)
	}
	return newResult(releases), nil
}

// CheckSeries probes whether any release exists for a Series-catalog TMDB id,
// optionally scoped to a season and/or episode (0 for either means "not
// scoped" — a whole-show probe, which is what Discover fires per card since it
// has no season/episode context there).
//
// The load-bearing id here is the TVDB id: TMDB's /tv/{id} has NO top-level
// imdb_id (see tmdb.TVDetails' doc), and /tv/{id}/external_ids yields only a
// tvdb_id — so ExternalIDs is the one call that produces a usable structured id
// for the tvsearch. TVDetails IS also fetched now, purely for its Title —
// SearchByIDParams' Query field needs it (see that field's doc for why: id
// params alone weren't a reliable filter for every indexer, a real confirmed
// bug). A TVDetails failure is NOT fatal to the probe — Query is a
// compatibility improvement, not a hard requirement the way the tvdb id is —
// it just degrades to an empty Query rather than erroring the whole check
// over a supplementary title lookup.
//
// If ExternalIDs returns 0 (TMDB has no TVDB id on file for this show) and no
// season/episode was given, the probe would degenerate into an id-less search;
// short-circuit that to "unavailable" instead of issuing a meaningless query.
// A season/episode-scoped probe is still meaningful even without the tvdb id,
// so it's allowed through.
func CheckSeries(ctx context.Context, tmdbClient *tmdb.Client, prowlarrClient *prowlarr.Client, tmdbID, season, episode int) (Result, error) {
	if tmdbClient == nil {
		return Result{}, fmt.Errorf("tmdb isn't configured — add it in Settings first")
	}
	if prowlarrClient == nil {
		return Result{}, fmt.Errorf("prowlarr isn't configured — add it in Settings first")
	}

	tvdbID, err := tmdbClient.ExternalIDs(ctx, tmdbID)
	if err != nil {
		return Result{}, fmt.Errorf("resolving tvdb id for tmdb id %d: %w", tmdbID, err)
	}
	if tvdbID == 0 && season == 0 && episode == 0 {
		// No usable id and no season/episode scope — an id-less tvsearch would
		// be a noise query, so report unavailable without hitting Prowlarr.
		return newResult(nil), nil
	}

	var query string
	if details, err := tmdbClient.TVDetails(ctx, tmdbID); err == nil {
		query = details.Title
	}

	releases, err := prowlarrClient.SearchByID(ctx, prowlarr.SearchByIDParams{
		Query:      query,
		TVDBID:     tvdbID,
		Season:     season,
		Episode:    episode,
		Categories: []int{seriesCategory},
	})
	if err != nil {
		return Result{}, fmt.Errorf("probing prowlarr for tmdb id %d: %w", tmdbID, err)
	}
	return newResult(releases), nil
}

// CheckAdultScene probes whether any release exists for an Adult scene. Unlike
// CheckMovie/CheckSeries, an Adult scene has no tmdb/imdb/tvdb id to search by —
// its identity is a stash-box/TPDB scene (see identify.MatchResult's Box/SceneID)
// and Adult releases aren't id-indexed on the trackers — so this is deliberately
// the one asymmetric case that uses the free-text prowlarr.Search path (a
// studio+title query) rather than the id-scoped SearchByID every other mode
// uses. It probes the XXX (6000-range) Newznab category (see adultCategory).
//
// There is no tmdbClient parameter: Adult never touches TMDB. A nil
// prowlarrClient yields a clear "not configured" error rather than a panic,
// matching CheckMovie/CheckSeries' tolerant-nil convention — so a caller
// reaching this directly (e.g. a future recheck job) gets an honest error.
func CheckAdultScene(ctx context.Context, prowlarrClient *prowlarr.Client, studio, title string) (Result, error) {
	if prowlarrClient == nil {
		return Result{}, fmt.Errorf("prowlarr isn't configured — add it in Settings first")
	}

	query := strings.TrimSpace(strings.TrimSpace(studio) + " " + strings.TrimSpace(title))
	releases, err := prowlarrClient.Search(ctx, query, []int{adultCategory})
	if err != nil {
		return Result{}, fmt.Errorf("probing prowlarr for adult scene %q: %w", query, err)
	}
	return newResult(releases), nil
}
