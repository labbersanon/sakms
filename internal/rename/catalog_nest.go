package rename

import (
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// Claude 2026-09-23: S00E00 is the dummy slot Apply writes for a movie filed
//
//	as its own series (One Good Turn (1931) [tmdbid-48903]/Season 00/…).
//
// Reason: that parse is "successful" so anthology never sees the file, and
//
//	catalog upserts a standalone show whose TMDB movie id collides with a
//	different TV series (48903 = 1931 short vs 2012 I Dream of Jodie).
//
// Troubleshooting: Laurel & Hardy shorts appear as their own Library cards.
// Review if: Organize stops writing Season 00 / S00E00 for movie-as-series.
func dummyMovieEpisodeParse(season int, eps []int) bool {
	return season == 0 && len(eps) == 1 && eps[0] == 0
}

var (
	showFolderYearRe  = regexp.MustCompile(`\((\d{4})\)`)
	showFolderTMDBRe  = regexp.MustCompile(`(?i)\[tmdbid-?-?\d+\]`)
	showFolderYearPar = regexp.MustCompile(`\s*\(\d{4}\)\s*`)
)

func yearFromShowFolder(name string) int {
	m := showFolderYearRe.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	y, _ := strconv.Atoi(m[1])
	return y
}

func titleFromShowFolder(name string) string {
	s := showFolderTMDBRe.ReplaceAllString(name, "")
	s = showFolderYearPar.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func episodeTitleKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// movieTMDBNotSeries is true when this TMDB id is a short film, not the TV
// show that shares the number. Copied from api.preferMovieForSeriesYear so
// rename does not import api.
func movieTMDBNotSeries(ctx context.Context, sess *mode.Session, tmdbID, folderYear int) bool {
	if sess == nil || sess.TMDB == nil || tmdbID <= 0 {
		return false
	}
	tv, tvErr := sess.TMDB.TVDetails(ctx, tmdbID)
	tvOK := tvErr == nil && strings.TrimSpace(tv.Title) != ""
	tvYear := tvPremiereYear(tv)
	movie, movieErr := sess.TMDB.MovieDetails(ctx, tmdbID)
	movieOK := movieErr == nil && strings.TrimSpace(movie.Title) != ""
	if !movieOK {
		return false
	}
	if !tvOK {
		return true
	}
	if folderYear <= 0 {
		return false
	}
	movieYear := 0
	if len(movie.ReleaseDate) >= 4 {
		movieYear, _ = strconv.Atoi(movie.ReleaseDate[:4])
	}
	return movieYear == folderYear && tvYear != folderYear
}

func tvPremiereYear(d tmdb.TVDetails) int {
	best := 0
	for _, s := range d.Seasons {
		if len(s.AirDate) < 4 {
			continue
		}
		y, _ := strconv.Atoi(s.AirDate[:4])
		if y == 0 {
			continue
		}
		if s.SeasonNumber == 1 {
			return y
		}
		if best == 0 || y < best {
			best = y
		}
	}
	return best
}

// findEpisodeNest returns a unique on-disk episode whose title key matches
// the short's folder/file title. Multiple series or slots → no nest.
func findEpisodeNest(ctx context.Context, libStore *library.Store, title string) (library.Series, int, int, bool) {
	key := episodeTitleKey(title)
	if libStore == nil || key == "" {
		return library.Series{}, 0, 0, false
	}
	hits, err := libStore.FindEpisodesByTitleKey(ctx, key)
	if err != nil || len(hits) == 0 {
		return library.Series{}, 0, 0, false
	}
	first := hits[0]
	for _, h := range hits[1:] {
		if h.Series.ID != first.Series.ID || h.SeasonNumber != first.SeasonNumber || h.EpisodeNumber != first.EpisodeNumber {
			return library.Series{}, 0, 0, false
		}
	}
	return first.Series, first.SeasonNumber, first.EpisodeNumber, true
}

func nestTitleHint(hintTitle, showFolder, videoPath string) string {
	if t := strings.TrimSpace(hintTitle); t != "" {
		return t
	}
	if t := titleFromShowFolder(showFolder); t != "" {
		return t
	}
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	return strings.TrimSpace(base)
}
