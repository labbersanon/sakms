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
	"github.com/labbersanon/sakms/internal/tvdb"
)

// Claude 2026-09-23: S00E00 is the dummy slot Apply writes for a movie filed
// as its own series (One Good Turn (1931) [tmdbid-48903]/Season 00/…).
// Reason: that parse is "successful" so anthology never sees the file, and
// catalog upserts a standalone show whose TMDB movie id collides with a
// different TV series (48903 = 1931 short vs 2012 I Dream of Jodie).
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

func findEpisodeNest(ctx context.Context, libStore *library.Store, title string) (library.Series, int, int, bool) {
	key := episodeTitleKey(title)
	if libStore == nil || key == "" {
		return library.Series{}, 0, 0, false
	}
	hits, err := libStore.FindEpisodesByTitleKey(ctx, key)
	if err != nil || len(hits) == 0 {
		return library.Series{}, 0, 0, false
	}
	var kept []library.EpisodeTitleHit
	for _, h := range hits {
		if dummyMovieEpisodeParse(h.SeasonNumber, []int{h.EpisodeNumber}) && h.Series.TMDBID > 0 {
			continue
		}
		kept = append(kept, h)
	}
	if len(kept) == 0 {
		return library.Series{}, 0, 0, false
	}
	first := kept[0]
	for _, h := range kept[1:] {
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

// Exact title key only — token overlap would nest "laughing" under Leave 'Em Laughing.
//
// Claude 2026-09-23: local FindEpisodesByTitleKey only hits episodes already
// on disk (One Good Turn). Night Owls / Leave 'Em Laughing / Early to Bed
// are not in the 87 Laurel & Hardy rows, so they stayed as movie-id cards.
// Reason: anthology groups by the SHORT's folder name, not Laurel & Hardy,
// and a tracked movie-id folder is skipped as already pinned.
// Troubleshooting: stray L&H shorts remain their own Library cards after Scan.
// Review if: those shorts exist as titled library_episodes on the anthology.
func findTVDBEpisodeNest(ctx context.Context, sess *mode.Session, libStore *library.Store, title string, folderYear int) (library.Series, int, int, string, string, bool) {
	key := episodeTitleKey(title)
	if sess == nil || sess.TVDB == nil || libStore == nil || key == "" {
		return library.Series{}, 0, 0, "", "", false
	}
	all, err := libStore.ListSeries(ctx)
	if err != nil {
		return library.Series{}, 0, 0, "", "", false
	}
	type hit struct {
		series library.Series
		season int
		ep     int
		name   string
		aired  string
	}
	var found hit
	have := false
	for _, ser := range all {
		if ser.TVDBID <= 0 {
			continue
		}
		has, hasErr := libStore.SeriesHasOnDiskFile(ctx, ser.ID)
		if hasErr != nil || !has {
			continue
		}
		catalog, catErr := sess.TVDB.SeriesEpisodes(ctx, ser.TVDBID, tvdb.SeasonTypeOfficial)
		if catErr != nil {
			continue
		}
		var local *tvdb.Episode
		for i := range catalog {
			ep := catalog[i]
			if episodeTitleKey(ep.Name) != key {
				continue
			}
			if folderYear > 0 && len(ep.Aired) >= 4 {
				if y, _ := strconv.Atoi(ep.Aired[:4]); y > 0 && y != folderYear {
					continue
				}
			}
			if local != nil {
				local = nil
				break
			}
			local = &catalog[i]
		}
		if local == nil {
			continue
		}
		if have {
			return library.Series{}, 0, 0, "", "", false
		}
		found = hit{
			series: ser, season: local.SeasonNumber, ep: local.Number,
			name: local.Name, aired: local.Aired,
		}
		have = true
	}
	if !have {
		return library.Series{}, 0, 0, "", "", false
	}
	return found.series, found.season, found.ep, found.name, found.aired, true
}

// Night Owls (2023) S01 files keep the 2023 card; only the dummy S00E00 row for videoPath is dropped.
func retireStrayMovieSeries(ctx context.Context, libStore *library.Store, movieTMDB int, videoPath string, parentTMDB int) {
	if libStore == nil || movieTMDB <= 0 || movieTMDB == parentTMDB || videoPath == "" {
		return
	}
	stray, err := libStore.GetSeriesByTMDBID(ctx, movieTMDB)
	if err != nil || stray == nil {
		return
	}
	eps, err := libStore.ListEpisodes(ctx, stray.ID)
	if err != nil {
		return
	}
	for _, ep := range eps {
		if !dummyMovieEpisodeParse(ep.SeasonNumber, []int{ep.EpisodeNumber}) {
			continue
		}
		owns := ep.FilePath == videoPath
		if !owns {
			files, listErr := libStore.ListEpisodeFiles(ctx, ep.ID)
			if listErr != nil {
				continue
			}
			for _, f := range files {
				if f.FilePath == videoPath {
					owns = true
					break
				}
			}
		}
		if owns {
			_ = libStore.DeleteEpisode(ctx, ep.ID)
		}
	}
	if has, hasErr := libStore.SeriesHasOnDiskFile(ctx, stray.ID); hasErr == nil && !has {
		_ = libStore.DeleteSeries(ctx, stray.ID)
	}
}
