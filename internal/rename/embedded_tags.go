package rename

import (
	"context"
	"fmt"
	"strings"

	"github.com/labbersanon/sakms/internal/classify"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/searchterm"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// Claude 2026-09-23: embedded container tags as first Movies identity authority.
// Reason: operator C/C/A — tags → NFO → filename; title+year even without ids.
// Troubleshooting: unmatched orphans / tmdb_id≤0 library rows when ffprobe has title.
// Review if: Series Rename adopts the same tag precedence.
// Related files: internal/mediainfo, internal/api/poster_backfill.go

// ResolveMovieTMDBFromTags maps container tags to a TMDB movie id.
// Order: explicit TMDB id → IMDb id → title(+year) search. Returns 0 when
// tags are empty or no catalog hit is found (caller falls through).
func ResolveMovieTMDBFromTags(ctx context.Context, client *tmdb.Client, tags mediainfo.Tags) (tmdbID int, title string, year int, err error) {
	if client == nil || !tags.HasIdentity() {
		return 0, "", 0, nil
	}
	if tags.TMDBID > 0 {
		details, err := client.MovieDetails(ctx, tags.TMDBID)
		if err != nil {
			return 0, "", 0, err
		}
		return details.ID, details.Title, yearFromReleaseDate(details.ReleaseDate), nil
	}
	if tags.IMDBID != "" {
		id, err := client.FindMovieByIMDBID(ctx, tags.IMDBID)
		if err != nil {
			return 0, "", 0, err
		}
		if id > 0 {
			details, err := client.MovieDetails(ctx, id)
			if err != nil {
				return 0, "", 0, err
			}
			y := yearFromReleaseDate(details.ReleaseDate)
			if tags.Year > 0 {
				y = tags.Year
			}
			return details.ID, details.Title, y, nil
		}
	}
	title = strings.TrimSpace(tags.Title)
	if title == "" {
		return 0, "", 0, nil
	}
	// Prefer clean title without trailing (YYYY) for search.
	searchTitle := title
	if tags.Year > 0 {
		searchTitle = strings.TrimSpace(strings.ReplaceAll(title, fmt.Sprintf("(%d)", tags.Year), ""))
	}
	items, err := client.SearchMovies(ctx, searchTitle)
	if err != nil {
		return 0, "", 0, err
	}
	if len(items) == 0 && searchTitle != title {
		items, err = client.SearchMovies(ctx, title)
		if err != nil {
			return 0, "", 0, err
		}
	}
	if len(items) == 0 {
		return 0, "", 0, nil
	}
	pick := items[0]
	pickYear := yearFromReleaseDate(pick.ReleaseDate)
	if tags.Year > 0 {
		for _, it := range items {
			y := yearFromReleaseDate(it.ReleaseDate)
			if y == tags.Year {
				pick = it
				pickYear = y
				break
			}
		}
	}
	return pick.ID, pick.Title, pickYear, nil
}

// tryEmbeddedTagsMovie attempts identity from ffprobe format.tags.
// Returns nil when tags are absent or do not resolve — caller continues to NFO/filename.
func tryEmbeddedTagsMovie(
	ctx context.Context,
	sess *mode.Session,
	byTMDB map[int]bool,
	generalRoot, foundRoot string,
	tags mediainfo.Tags,
	durationSec float64,
	cfg MatchConfig,
	p proposals.Proposal,
) *proposals.Proposal {
	if sess == nil || sess.TMDB == nil || !tags.HasIdentity() {
		return nil
	}
	cfg = cfg.Normalize()

	// Id path (TMDB / IMDb) — authoritative like NFO.
	if tags.TMDBID > 0 || tags.IMDBID != "" {
		id, title, year, err := ResolveMovieTMDBFromTags(ctx, sess.TMDB, mediainfo.Tags{
			TMDBID: tags.TMDBID, IMDBID: tags.IMDBID,
		})
		if err != nil || id <= 0 {
			// Fall through to title search from the same tags, then NFO/filename.
		} else {
			return acceptEmbeddedMovie(ctx, sess, byTMDB, generalRoot, foundRoot, id, title, year, p)
		}
	}

	if strings.TrimSpace(tags.Title) == "" {
		return nil
	}

	sig := FileSignals{Year: tags.Year, DurationSec: durationSec}
	acceptMovie := func(match tmdb.Item, candYear int) proposals.Proposal {
		out := acceptEmbeddedMovie(ctx, sess, byTMDB, generalRoot, foundRoot, match.ID, match.Title, candYear, p)
		return *out
	}
	queries := searchterm.SearchQueries(tags.Title)
	if tags.Year > 0 {
		// Prefer "Title Year" style queries first.
		queries = append([]string{fmt.Sprintf("%s %d", strings.TrimSpace(tags.Title), tags.Year)}, queries...)
	}
	if got := tryMovieQueries(ctx, sess, sig, cfg, acceptMovie, queries); got != nil {
		return got
	}
	return nil
}

func acceptEmbeddedMovie(
	ctx context.Context,
	sess *mode.Session,
	byTMDB map[int]bool,
	generalRoot, foundRoot string,
	tmdbID int,
	title string,
	year int,
	p proposals.Proposal,
) *proposals.Proposal {
	targetRoot := generalRoot
	switch {
	case foundRoot == sess.KidsRootPath:
		targetRoot = sess.KidsRootPath
	case sess.KidsRootPath != "" && sess.MainstreamAI != nil:
		overview := ""
		if det, err := sess.TMDB.MovieDetails(ctx, tmdbID); err == nil {
			overview = det.Overview
			if title == "" {
				title = det.Title
			}
			if year == 0 {
				year = yearFromReleaseDate(det.ReleaseDate)
			}
		}
		if result, err := classify.WithAI(ctx, sess.MainstreamAI, title, overview); err == nil && result.IsKids {
			targetRoot = sess.KidsRootPath
		}
	}
	if byTMDB[tmdbID] {
		acceptDuplicatePending(&p, title, tmdbID, year, targetRoot)
	} else {
		p.Status = proposals.Pending
		p.Title = title
		p.TMDBID = tmdbID
		p.Year = year
		p.RootFolderPath = targetRoot
	}
	if det, err := sess.TMDB.MovieDetails(ctx, tmdbID); err == nil {
		p.Genres = det.Genres
		if p.Year == 0 {
			p.Year = yearFromReleaseDate(det.ReleaseDate)
		}
		if p.Title == "" {
			p.Title = det.Title
		}
	}
	if names, err := sess.TMDB.MovieCredits(ctx, tmdbID); err == nil {
		p.Cast = names
	}
	return &p
}

// applyGuessTitle merges an AI GuessTitle result into file signals and builds
// TMDB search queries (title+year first when year is known).
func applyGuessTitle(sig FileSignals, g identify.TitleGrounding) (FileSignals, string, []string) {
	if strings.TrimSpace(g.Title) == "" {
		return sig, "", nil
	}
	if g.Year > 0 && sig.Year == 0 {
		sig.Year = g.Year
	}
	queries := searchterm.SearchQueries(g.Title)
	if g.Year > 0 {
		queries = append([]string{fmt.Sprintf("%s %d", strings.TrimSpace(g.Title), g.Year)}, queries...)
	}
	return sig, g.Title, queries
}
