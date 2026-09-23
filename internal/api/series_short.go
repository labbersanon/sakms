package api

import (
	"context"
	"net/url"
	"strings"

	"github.com/labbersanon/sakms/internal/tmdb"
)

// seriesPosterCatalog is the TMDB record a library series should use for
// its poster and overview. FromMovie is set when the id is a short film:
// the same number is a different TV show, and the library year matches the
// movie.
type seriesPosterCatalog struct {
	PosterPath string
	Overview   string
	Title      string
	Year       int
	FromMovie  bool
}

// preferMovieForSeriesYear reports whether a series row should read the
// movie record for this TMDB id.
// Claude 2026-09-23: shorts are filed as series; TMDB files them as movies.
// Reason: movie 48903 is One Good Turn (1931) and TV 48903 is a 2012 show.
// Troubleshooting: a short's poster is the other show, or a TMDB gallery page.
// Review if: shorts move to the Movies library.
func preferMovieForSeriesYear(tvOK bool, tvYear int, movieOK bool, movieYear int, libraryYear int) bool {
	if !movieOK {
		return false
	}
	if !tvOK {
		return true
	}
	if libraryYear <= 0 {
		return false
	}
	return movieYear == libraryYear && tvYear != libraryYear
}

func tvPremiereYear(d tmdb.TVDetails) int {
	best := 0
	for _, s := range d.Seasons {
		y := parseYearPrefix(s.AirDate)
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

func loadSeriesPosterCatalog(ctx context.Context, client *tmdb.Client, tmdbID, libraryYear int) seriesPosterCatalog {
	if client == nil || tmdbID <= 0 {
		return seriesPosterCatalog{}
	}
	tv, tvErr := client.TVDetails(ctx, tmdbID)
	tvOK := tvErr == nil && strings.TrimSpace(tv.Title) != ""
	tvYear := tvPremiereYear(tv)
	if tvOK && tv.PosterPath != "" && (libraryYear <= 0 || tvYear == libraryYear) {
		return seriesPosterCatalog{
			PosterPath: tv.PosterPath,
			Overview:   tv.Overview,
			Title:      tv.Title,
			Year:       tvYear,
		}
	}
	movie, movieErr := client.MovieDetails(ctx, tmdbID)
	movieOK := movieErr == nil && strings.TrimSpace(movie.Title) != ""
	if preferMovieForSeriesYear(tvOK, tvYear, movieOK, parseYearPrefix(movie.ReleaseDate), libraryYear) {
		return seriesPosterCatalog{
			PosterPath: movie.PosterPath,
			Overview:   movie.Overview,
			Title:      movie.Title,
			Year:       parseYearPrefix(movie.ReleaseDate),
			FromMovie:  true,
		}
	}
	if tvOK {
		return seriesPosterCatalog{
			PosterPath: tv.PosterPath,
			Overview:   tv.Overview,
			Title:      tv.Title,
			Year:       tvPremiereYear(tv),
		}
	}
	return seriesPosterCatalog{}
}

// posterURLIsImage is true when the stored poster URL is a file the card
// can render. Gallery pages and wiki file pages are not.
func posterURLIsImage(raw string) bool {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "/api/posters/") {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "image.tmdb.org", "artworks.thetvdb.com":
		return true
	case "commons.wikimedia.org", "www.themoviedb.org", "themoviedb.org", "www.theposterdb.com", "theposterdb.com":
		return false
	}
	path := strings.ToLower(u.Path)
	return strings.HasSuffix(path, ".jpg") || strings.HasSuffix(path, ".jpeg") || strings.HasSuffix(path, ".png") || strings.HasSuffix(path, ".webp")
}
