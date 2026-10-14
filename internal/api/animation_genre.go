package api

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// Claude 2026-10-13: process-lifetime Animation-genre cache keyed by mode+ids.
// Reason: drain grades per episode; TVDetails/SeriesGenres per episode stampede.
// Troubleshooting: wrong flag after a manual genre edit → restart sakms.
// Review if: a TTL or settings-driven invalidation is added.
var animationGenreCache sync.Map

func genresIncludeAnimation(genres []string) bool {
	for _, g := range genres {
		if strings.EqualFold(strings.TrimSpace(g), "Animation") {
			return true
		}
	}
	return false
}

func animationGenreCacheKey(m mode.Mode, tmdbID, tvdbID int) string {
	return string(m) + ":" + strconv.Itoa(tmdbID) + ":" + strconv.Itoa(tvdbID)
}

func storedTitleGenres(ctx context.Context, lib *library.Store, m mode.Mode, tmdbID, tvdbID int) []string {
	if lib == nil {
		return nil
	}
	switch m {
	case mode.Series:
		if tmdbID != 0 {
			s, err := lib.GetSeriesByTMDBID(ctx, tmdbID)
			if err == nil && s != nil && len(s.Genres) > 0 {
				return s.Genres
			}
		}
		if tvdbID > 0 {
			s, err := lib.GetSeriesByTVDBID(ctx, tvdbID)
			if err == nil && s != nil {
				return s.Genres
			}
		}
		return nil
	case mode.Movies:
		if tmdbID <= 0 {
			return nil
		}
		item, err := lib.GetByTMDBID(ctx, mode.Movies, tmdbID)
		if err != nil || item == nil {
			return nil
		}
		return item.Genres
	default:
		return nil
	}
}

// tmdbTitleGenres reports ok=false when there is no TMDB client or the lookup
// fails, so the caller can skip caching a transient miss.
func tmdbTitleGenres(ctx context.Context, sess *mode.Session, m mode.Mode, tmdbID int) ([]string, bool) {
	if sess == nil || sess.TMDB == nil || tmdbID <= 0 {
		return nil, false
	}
	if m == mode.Series {
		d, err := sess.TMDB.TVDetails(ctx, tmdbID)
		return d.Genres, err == nil
	}
	d, err := sess.TMDB.MovieDetails(ctx, tmdbID)
	return d.Genres, err == nil
}

func tvdbSeriesGenres(ctx context.Context, sess *mode.Session, tvdbID int) ([]string, bool) {
	if sess == nil || sess.TVDB == nil || tvdbID <= 0 {
		return nil, false
	}
	g, err := sess.TVDB.SeriesGenres(ctx, tvdbID)
	if err != nil {
		return nil, false
	}
	return g, true
}

// resolveSeriesTVDBIDForAnimation prefers an explicit id, then the library row,
// then TMDB ExternalIDs. Anthology rows have tmdb_id < 0 and a real tvdb_id.
func resolveSeriesTVDBIDForAnimation(ctx context.Context, lib *library.Store, sess *mode.Session, tmdbID, tvdbID int) int {
	if tvdbID > 0 {
		return tvdbID
	}
	if lib != nil && tmdbID != 0 {
		if s, err := lib.GetSeriesByTMDBID(ctx, tmdbID); err == nil && s != nil && s.TVDBID > 0 {
			return s.TVDBID
		}
	}
	if tmdbID > 0 && sess != nil && sess.TMDB != nil {
		if id, err := sess.TMDB.ExternalIDs(ctx, tmdbID); err == nil && id > 0 {
			return id
		}
	}
	return 0
}

// titleIsAnimation is true when stored, TMDB, or TVDB (series) genres list
// Animation. Missing/failed lookup is false — live-action floors, fail closed.
// Claude 2026-10-13: TVDB fallback; allow AnthologyTMDBID (negative tmdb).
// Reason: Looney Tunes year-seasons do not map cleanly on TMDB.
// Troubleshooting: Honey-Mousers still Medium@480 — tmdb<=0 skipped TVDB.
// Review if: movies also use TVDB movie genres.
func titleIsAnimation(ctx context.Context, lib *library.Store, sess *mode.Session, m mode.Mode, tmdbID, tvdbID int) bool {
	if m != mode.Movies && m != mode.Series {
		return false
	}
	if tmdbID == 0 && tvdbID <= 0 {
		return false
	}
	key := animationGenreCacheKey(m, tmdbID, tvdbID)
	if v, ok := animationGenreCache.Load(key); ok {
		return v.(bool)
	}
	genres := storedTitleGenres(ctx, lib, m, tmdbID, tvdbID)
	if genresIncludeAnimation(genres) {
		animationGenreCache.Store(key, true)
		return true
	}
	tmdbOK := false
	if tmdbID > 0 {
		var g []string
		if g, tmdbOK = tmdbTitleGenres(ctx, sess, m, tmdbID); tmdbOK && genresIncludeAnimation(g) {
			animationGenreCache.Store(key, true)
			return true
		}
	}
	if m == mode.Series {
		id := resolveSeriesTVDBIDForAnimation(ctx, lib, sess, tmdbID, tvdbID)
		if g, ok := tvdbSeriesGenres(ctx, sess, id); ok {
			anim := genresIncludeAnimation(g)
			animationGenreCache.Store(key, anim)
			return anim
		}
	}
	if len(genres) > 0 || tmdbOK {
		animationGenreCache.Store(key, false)
	}
	return false
}

func applyTitleAnimation(ctx context.Context, lib *library.Store, sess *mode.Session, m mode.Mode, tmdbID, tvdbID int, cands []autograb.Candidate) {
	if !titleIsAnimation(ctx, lib, sess, m, tmdbID, tvdbID) {
		return
	}
	for i := range cands {
		cands[i].Animation = true
	}
}
