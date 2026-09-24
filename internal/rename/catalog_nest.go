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
	"github.com/labbersanon/sakms/internal/proposals"
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

// Claude 2026-09-24: only a series that premiered before 1970 can parent a short.
// Reason: Night Owls (2023) and Tribute (1992) share a title token with a
//   theatrical short; they must never absorb other files.
// Troubleshooting: a 2023 card steals 1930 shorts after Save.
// Review if: the parent-year cutoff moves off 1970.
const nestParentPremiereBefore = 1970

func parentPremiereOK(year int) bool {
	return year > 0 && year < nestParentPremiereBefore
}

// genericEpisodeTitleKeys must not auto-create a parent. "Pilot" is an episode
// of hundreds of series; a unique TVDB hit is still a guess.
var genericEpisodeTitleKeys = map[string]struct{}{
	"pilot":     {},
	"special":   {},
	"episode":   {},
	"episode1":  {},
	"episode01": {},
	"unaired":   {},
	"tba":       {},
	"untitled":  {},
	"bonus":     {},
}

func genericEpisodeTitleKey(key string) bool {
	_, ok := genericEpisodeTitleKeys[key]
	return ok
}

func findEpisodeNest(ctx context.Context, libStore *library.Store, title string) (library.Series, int, int, bool) {
	key := episodeTitleKey(title)
	if libStore == nil || key == "" || genericEpisodeTitleKey(key) {
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
		if !parentPremiereOK(h.Series.Year) {
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
type nestHit struct {
	series library.Series
	season int
	ep     int
	name   string
	aired  string
}

func episodeMatchesKeyYear(ep tvdb.Episode, key string, folderYear int) bool {
	if episodeTitleKey(ep.Name) != key {
		return false
	}
	if folderYear > 0 && len(ep.Aired) >= 4 {
		if y, _ := strconv.Atoi(ep.Aired[:4]); y > 0 && y != folderYear {
			return false
		}
	}
	return true
}

func uniqueCatalogEpisode(catalog []tvdb.Episode, key string, folderYear int) *tvdb.Episode {
	var local *tvdb.Episode
	for i := range catalog {
		if !episodeMatchesKeyYear(catalog[i], key, folderYear) {
			continue
		}
		if local != nil {
			return nil
		}
		local = &catalog[i]
	}
	return local
}

func seriesByTVDBID(all []library.Series, tvdbID int) *library.Series {
	for i := range all {
		if all[i].TVDBID == tvdbID {
			return &all[i]
		}
	}
	return nil
}

func ensureTVDBParent(ctx context.Context, libStore *library.Store, all []library.Series, hit tvdb.Result, foundRoot string) (library.Series, bool) {
	if libStore == nil || hit.TVDBID <= 0 || !parentPremiereOK(hit.Year) {
		return library.Series{}, false
	}
	if existing := seriesByTVDBID(all, hit.TVDBID); existing != nil {
		return *existing, true
	}
	synth := anthologyTMDBID(hit.TVDBID)
	if existing, err := libStore.GetSeriesByTMDBID(ctx, synth); err == nil && existing != nil {
		return *existing, true
	}
	created, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID:         synth,
		TVDBID:         hit.TVDBID,
		Title:          hit.Name,
		Year:           hit.Year,
		RootFolderPath: foundRoot,
	})
	if err != nil {
		return library.Series{}, false
	}
	return created, true
}

// Claude 2026-09-24: B2 — tracked pre-1970 first, then SearchSeries.
// Reason: SearchEpisodes seeds series named like the query, so "Night Owls"
//   never finds Laurel & Hardy. Tracked anthologies are the first parent
//   source; an untracked parent is created only from a unique pre-1970
//   SearchSeries hit whose catalog has exactly one exact-key episode.
// Troubleshooting: shorts stay their own cards when the anthology is untracked.
// Review if: TVDB adds a global episode-title search.
func findTVDBEpisodeNest(ctx context.Context, sess *mode.Session, libStore *library.Store, title string, folderYear int, foundRoot string) (library.Series, int, int, string, string, bool) {
	key := episodeTitleKey(title)
	if sess == nil || sess.TVDB == nil || libStore == nil || key == "" || genericEpisodeTitleKey(key) {
		return library.Series{}, 0, 0, "", "", false
	}
	all, err := libStore.ListSeries(ctx)
	if err != nil {
		return library.Series{}, 0, 0, "", "", false
	}
	var found nestHit
	have := false
	for _, ser := range all {
		if ser.TVDBID <= 0 || !parentPremiereOK(ser.Year) {
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
		local := uniqueCatalogEpisode(catalog, key, folderYear)
		if local == nil {
			continue
		}
		if have {
			return library.Series{}, 0, 0, "", "", false
		}
		found = nestHit{
			series: ser, season: local.SeasonNumber, ep: local.Number,
			name: local.Name, aired: local.Aired,
		}
		have = true
	}
	if have {
		return found.series, found.season, found.ep, found.name, found.aired, true
	}
	return searchCreateEpisodeParent(ctx, sess, libStore, all, title, key, folderYear, foundRoot)
}

func searchCreateEpisodeParent(ctx context.Context, sess *mode.Session, libStore *library.Store, all []library.Series, query, key string, folderYear int, foundRoot string) (library.Series, int, int, string, string, bool) {
	results, err := sess.TVDB.SearchSeries(ctx, query)
	if err != nil || len(results) == 0 {
		return library.Series{}, 0, 0, "", "", false
	}
	var found nestHit
	have := false
	for _, r := range results {
		if r.TVDBID <= 0 || !parentPremiereOK(r.Year) {
			continue
		}
		catalog, catErr := sess.TVDB.SeriesEpisodes(ctx, r.TVDBID, tvdb.SeasonTypeOfficial)
		if catErr != nil {
			continue
		}
		local := uniqueCatalogEpisode(catalog, key, folderYear)
		if local == nil {
			continue
		}
		parent, ok := ensureTVDBParent(ctx, libStore, all, r, foundRoot)
		if !ok {
			continue
		}
		if have && found.series.ID != parent.ID {
			return library.Series{}, 0, 0, "", "", false
		}
		found = nestHit{
			series: parent, season: local.SeasonNumber, ep: local.Number,
			name: local.Name, aired: local.Aired,
		}
		have = true
	}
	if !have {
		return library.Series{}, 0, 0, "", "", false
	}
	return found.series, found.season, found.ep, found.name, found.aired, true
}

func findParentByShowFolder(ctx context.Context, sess *mode.Session, libStore *library.Store, showFolder, foundRoot string) (library.Series, bool) {
	title := titleFromShowFolder(showFolder)
	key := episodeTitleKey(title)
	if libStore == nil || key == "" || genericEpisodeTitleKey(key) {
		return library.Series{}, false
	}
	all, err := libStore.ListSeries(ctx)
	if err != nil {
		return library.Series{}, false
	}
	var tracked []library.Series
	for _, ser := range all {
		if !parentPremiereOK(ser.Year) {
			continue
		}
		if episodeTitleKey(ser.Title) != key {
			continue
		}
		tracked = append(tracked, ser)
	}
	if len(tracked) == 1 {
		return tracked[0], true
	}
	if len(tracked) > 1 || sess == nil || sess.TVDB == nil {
		return library.Series{}, false
	}
	results, err := sess.TVDB.SearchSeries(ctx, title)
	if err != nil {
		return library.Series{}, false
	}
	var hits []tvdb.Result
	for _, r := range results {
		if !parentPremiereOK(r.Year) || episodeTitleKey(r.Name) != key {
			continue
		}
		hits = append(hits, r)
	}
	if len(hits) != 1 {
		return library.Series{}, false
	}
	return ensureTVDBParent(ctx, libStore, all, hits[0], foundRoot)
}

func proposeNestedShortMove(ctx context.Context, libStore *library.Store, videoPath, foundRoot string) (proposals.Proposal, bool) {
	if libStore == nil || videoPath == "" {
		return proposals.Proposal{}, false
	}
	ep, ser, err := libStore.EpisodeOwningFile(ctx, videoPath)
	if err != nil || ep == nil || ser == nil {
		return proposals.Proposal{}, false
	}
	root := ser.RootFolderPath
	if root == "" {
		root = foundRoot
	}
	return proposals.Proposal{
		Mode:           mode.Series,
		Workflow:       proposals.Rename,
		Status:         proposals.Pending,
		SourceName:     filepath.Base(videoPath),
		SourcePath:     videoPath,
		RootFolderPath: root,
		Title:          ser.Title,
		Year:           ser.Year,
		TMDBID:         ser.TMDBID,
		TVDBID:         ser.TVDBID,
		SeasonNumber:   ep.SeasonNumber,
		EpisodeNumber:  ep.EpisodeNumber,
		Reason:         "nested short — propose move into anthology folder",
	}, true
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
