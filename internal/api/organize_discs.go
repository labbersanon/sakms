package api

// Claude 2026-10-02: Organize Discs identify + existing-library fill.
// Reason: ISOs are manual-import-only; TMDB runs after the operator picks
//   one ISO. Existing movie FilePath / episode FilePath start unchecked.
// Troubleshooting: empty hits — TMDB search used SearchQueries(volume, src).
// Review if: Usenet finalize calls the same import path.
//
// Claude 2026-10-02: Wikipedia TOC names when IFO has none.
// Reason: unique duration fails for ~7m Golden shorts; wiki Disc N tables
//   are in disc order. SearXNG only finds the wiki URL.
// Troubleshooting: names omitted when TOC length ≠ feature count.
// Review if: OVID or ffprobe starts returning per-title names.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/disc"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/websearch"
)

// lookupDiscTOCFn is Wikipedia (+ SearXNG URLs) when the image has no names.
var lookupDiscTOCFn = disc.LookupTOC

// inspectDiscFn is swappable in tests.
var inspectDiscFn = disc.Inspect

func organizeDiscIdentifyHandler(
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.OrganizeDiscIdentifyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		src, err := resolveBrowsablePath(req.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !disc.IsDiscImage(src) {
			http.Error(w, "path must be an .iso or .img file", http.StatusBadRequest)
			return
		}
		m, err := inspectDiscFn(r.Context(), src)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if len(disc.TOCForWorks(m)) == 0 {
			works := discWorksFromMap(m)
			extra := discWikiURLs(r.Context(), httpClient, connStore, scStore, settingsStore, m.Volume, src)
			names := lookupDiscTOCFn(r.Context(), httpClient, m.Volume, src, len(works), extra)
			_ = disc.AttachTOCNames(m, names)
		}
		works := discWorksFromMap(m)
		queries := disc.SearchQueries(m.Volume, src)
		hits := identifyDiscHits(r, httpClient, connStore, scStore, settingsStore, libStore, queries, works, disc.VolumeNumber(m.Volume, src))
		writeJSON(w, apidto.OrganizeDiscIdentifyResponse{
			Path: src, Volume: m.Volume, Queries: queries, Works: works, Hits: hits,
		})
	}
}

func discWorksFromMap(m *disc.Map) []apidto.OrganizeDiscWork {
	if m == nil {
		return nil
	}
	titles := disc.AssignRoles(m.Titles)
	byN := map[int]disc.Title{}
	for _, t := range titles {
		byN[t.N] = t
	}
	toc := disc.TOCForWorks(m)
	var out []apidto.OrganizeDiscWork
	for i, w := range disc.PlanWorks(titles) {
		dur := byN[w.Title].DurationS
		if w.ChapterStart > 0 && byN[w.Title].Chapters > 0 {
			dur = byN[w.Title].DurationS / float64(byN[w.Title].Chapters)
		}
		epTitle := ""
		if i < len(toc) {
			epTitle = toc[i]
		}
		out = append(out, apidto.OrganizeDiscWork{
			Name: w.Name, Title: w.Title, Chapter: w.ChapterStart,
			DurationS: dur, Role: w.Role, EpisodeTitle: epTitle,
		})
	}
	return out
}

func discWikiURLs(
	ctx context.Context,
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	volume, src string,
) []string {
	if connStore == nil || httpClient == nil {
		return nil
	}
	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Movies)
	if err != nil || sess == nil || sess.WebSearch == nil {
		return nil
	}
	q := disc.WikiQuery(volume, src)
	if q == "" {
		return nil
	}
	res, err := sess.WebSearch.Search(ctx, q+" wikipedia", 8)
	if err != nil {
		return nil
	}
	return wikiURLsFromSearch(res)
}

func wikiURLsFromSearch(res []websearch.Result) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range res {
		u := strings.TrimSpace(r.URL)
		if u == "" || seen[u] {
			continue
		}
		low := strings.ToLower(u)
		if !strings.Contains(low, "wikipedia.org/wiki/") {
			continue
		}
		if strings.Contains(low, "/wiki/file:") || strings.Contains(low, "/wiki/special:") ||
			strings.Contains(low, "/wiki/template:") || strings.Contains(low, "/wiki/category:") {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

func identifyDiscHits(
	r *http.Request,
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
	queries []string,
	works []apidto.OrganizeDiscWork,
	volume int,
) []apidto.OrganizeDiscHit {
	if len(queries) == 0 || connStore == nil {
		return nil
	}
	sess, err := mode.Build(r.Context(), connStore, scStore, settingsStore, httpClient, nil, mode.Movies)
	if err != nil || sess == nil || sess.TMDB == nil {
		return nil
	}
	seen := map[string]bool{}
	var hits []apidto.OrganizeDiscHit
	add := func(modeName string, it tmdb.Item) {
		key := modeName + ":" + strconv.Itoa(it.ID)
		if it.ID == 0 || seen[key] {
			return
		}
		seen[key] = true
		hit := apidto.OrganizeDiscHit{
			Mode: modeName, TMDBID: it.ID, Title: it.Title, Year: yearFromDate(it.ReleaseDate),
		}
		if libStore != nil {
			fillDiscExisting(r.Context(), libStore, &hit)
		}
		if hit.Mode == string(mode.Series) && sess.TMDB != nil {
			hit.Suggestions = suggestDiscSlots(r.Context(), sess.TMDB, hit.TMDBID, works)
		}
		hits = append(hits, hit)
	}
	for _, q := range queries {
		movies, err := sess.TMDB.SearchMovies(r.Context(), q)
		if err == nil {
			for i, it := range movies {
				if i >= 5 {
					break
				}
				add(string(mode.Movies), it)
			}
		}
		shows, err := sess.TMDB.SearchTV(r.Context(), q)
		if err == nil {
			for i, it := range shows {
				if i >= 5 {
					break
				}
				add(string(mode.Series), it)
			}
		}
		if len(hits) >= 12 {
			break
		}
	}
	// Claude 2026-10-02: Volume N titles first when filename has vNdM.
	// Reason: TMDB "Golden" still returns Vol. 1–6; v5d1 means Volume 5.
	// Troubleshooting: Vol. 1 selected — VolumeTitleScore missed "Vol. 5".
	// Review if: TMDB search with "Volume N" is unique enough to skip sort.
	if volume > 0 {
		sort.SliceStable(hits, func(i, j int) bool {
			return disc.VolumeTitleScore(hits[i].Title, volume) > disc.VolumeTitleScore(hits[j].Title, volume)
		})
	}
	return hits
}

func fillDiscExisting(ctx context.Context, libStore *library.Store, hit *apidto.OrganizeDiscHit) {
	switch hit.Mode {
	case string(mode.Movies):
		item, err := libStore.GetByTMDBID(ctx, mode.Movies, hit.TMDBID)
		if err != nil || item.FilePath == "" {
			return
		}
		hit.ExistingPath = item.FilePath
		hit.ExistingTitle = item.Title
	case string(mode.Series):
		series, err := libStore.GetSeriesByTMDBID(ctx, hit.TMDBID)
		if err != nil {
			return
		}
		eps, err := libStore.ListEpisodes(ctx, series.ID)
		if err != nil {
			return
		}
		for _, ep := range eps {
			if strings.TrimSpace(ep.FilePath) == "" {
				continue
			}
			hit.Episodes = append(hit.Episodes, apidto.OrganizeDiscExistingEpisode{
				Season: ep.SeasonNumber, Episode: ep.EpisodeNumber,
				Title: ep.Title, Path: ep.FilePath,
			})
		}
	}
}

func discOnlyNames(items []apidto.OrganizeDiscUnpackItem) []string {
	var names []string
	for _, it := range items {
		if strings.TrimSpace(it.Name) != "" {
			names = append(names, it.Name)
		}
	}
	return names
}

func applyDiscConflicts(ctx context.Context, libStore *library.Store, items []apidto.OrganizeDiscUnpackItem, hit *apidto.OrganizeDiscHit) error {
	if libStore == nil || hit == nil {
		return nil
	}
	for _, it := range items {
		if it.Conflict != "replace" {
			continue
		}
		path := ""
		if hit.Mode == string(mode.Movies) {
			path = hit.ExistingPath
		}
		if hit.Mode == string(mode.Series) && it.SeasonNumber >= 0 && it.EpisodeNumber >= 1 {
			for _, ep := range hit.Episodes {
				if ep.Season == it.SeasonNumber && ep.Episode == it.EpisodeNumber {
					path = ep.Path
					break
				}
			}
		}
		if path == "" {
			continue
		}
		if err := removeExistingLibraryFile(ctx, libStore, path); err != nil {
			return err
		}
	}
	return nil
}

func removeExistingLibraryFile(ctx context.Context, libStore *library.Store, path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, err := libStore.ForgetPath(ctx, path)
	return err
}

func suggestDiscSlots(ctx context.Context, client *tmdb.Client, tmdbID int, works []apidto.OrganizeDiscWork) []apidto.OrganizeDiscSuggestion {
	if client == nil || tmdbID <= 0 || len(works) == 0 {
		return nil
	}
	details, err := client.TVDetails(ctx, tmdbID)
	if err != nil {
		return nil
	}
	var catalog []disc.CatalogEpisode
	for _, s := range details.Seasons {
		eps, err := client.SeasonDetails(ctx, tmdbID, s.SeasonNumber)
		if err != nil {
			continue
		}
		for _, ep := range eps {
			catalog = append(catalog, disc.CatalogEpisode{
				Season: s.SeasonNumber, Episode: ep.EpisodeNumber,
				Title: ep.Name, RuntimeMin: ep.Runtime,
			})
		}
	}
	durs := map[string]float64{}
	titles := map[string]string{}
	for _, w := range works {
		durs[w.Name] = w.DurationS
		if strings.TrimSpace(w.EpisodeTitle) != "" {
			titles[w.Name] = w.EpisodeTitle
		}
	}
	slots := disc.MatchUniqueSlots(durs, catalog)
	for name, ep := range disc.MatchUniqueTitles(titles, catalog) {
		if slots == nil {
			slots = map[string]disc.CatalogEpisode{}
		}
		slots[name] = ep
	}
	if len(slots) == 0 {
		return nil
	}
	var out []apidto.OrganizeDiscSuggestion
	for _, w := range works {
		ep, ok := slots[w.Name]
		if !ok {
			continue
		}
		out = append(out, apidto.OrganizeDiscSuggestion{
			Name: w.Name, Season: ep.Season, Episode: ep.Episode, Title: ep.Title,
		})
	}
	return out
}

// importDiscOutputsFn is swappable in tests.
var importDiscOutputsFn = importDiscOutputs

func importDiscOutputs(ctx context.Context, deps discUnpackDeps, src string, req apidto.OrganizeDiscUnpackRequest, outputs []string) ([]string, error) {
	if deps.settingsStore == nil || req.TMDBID <= 0 {
		return outputs, nil
	}
	m := mode.Mode(strings.TrimSpace(req.Mode))
	if m != mode.Movies && m != mode.Series {
		return outputs, nil
	}
	destRoot, err := discDestRoot(ctx, deps, m, req.TMDBID, src)
	if err != nil {
		return nil, err
	}
	preset, err := resolveNamingPreset(ctx, deps.settingsStore, m)
	if err != nil {
		return nil, err
	}
	tier := string(autoGrabTier(ctx, deps.settingsStore, m))
	var tmdbClient *tmdb.Client
	if deps.connStore != nil {
		if sess, berr := mode.Build(ctx, deps.connStore, deps.scStore, deps.settingsStore, deps.httpClient, nil, m); berr == nil && sess != nil {
			tmdbClient = sess.TMDB
		}
	}
	itemByName := map[string]apidto.OrganizeDiscUnpackItem{}
	for _, it := range req.Items {
		itemByName[it.Name] = it
	}
	out := make([]string, 0, len(outputs))
	var errs []string
	for _, path := range outputs {
		name := discWorkNameFromOutput(path)
		it := itemByName[name]
		next, ierr := importOneDiscOutput(ctx, deps.libStore, tmdbClient, m, destRoot, preset, tier, req, it, path)
		if ierr != nil {
			errs = append(errs, filepath.Base(path)+": "+ierr.Error())
			out = append(out, path)
			continue
		}
		out = append(out, next)
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return out, nil
}

func discWorkNameFromOutput(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if i := strings.LastIndex(base, " - "); i >= 0 {
		return base[i+3:]
	}
	return base
}

func importOneDiscOutput(
	ctx context.Context,
	libStore *library.Store,
	tmdbClient *tmdb.Client,
	m mode.Mode,
	destRoot string,
	preset naming.Preset,
	tier string,
	req apidto.OrganizeDiscUnpackRequest,
	it apidto.OrganizeDiscUnpackItem,
	src string,
) (string, error) {
	if libStore == nil {
		return src, nil
	}
	if m == mode.Series && it.EpisodeNumber < 1 {
		return src, nil
	}
	p := proposals.Proposal{
		Mode:           m,
		Workflow:       proposals.Rename,
		Status:         proposals.Pending,
		SourcePath:     src,
		SourceName:     filepath.Base(src),
		RootFolderPath: destRoot,
		Title:          req.Title,
		TMDBID:         req.TMDBID,
		Year:           req.Year,
		SeasonNumber:   it.SeasonNumber,
		EpisodeNumber:  it.EpisodeNumber,
		EpisodeTitle:   it.EpisodeTitle,
	}
	if m == mode.Movies {
		_, _, err := rename.ApplyLibrary(ctx, libStore, p, preset, tier, nil)
		if err != nil {
			return src, err
		}
		if item, gerr := libStore.GetByTMDBID(ctx, mode.Movies, req.TMDBID); gerr == nil && item != nil && item.FilePath != "" {
			return item.FilePath, nil
		}
		return src, nil
	}
	_, _, err := rename.ApplyLibrarySeries(ctx, libStore, tmdbClient, nil, p, preset, tier, nil)
	if err != nil {
		return src, err
	}
	series, err := libStore.GetSeriesByTMDBID(ctx, req.TMDBID)
	if err != nil {
		return src, nil
	}
	ep, err := libStore.GetEpisode(ctx, series.ID, it.SeasonNumber, it.EpisodeNumber)
	if err != nil || ep == nil || ep.FilePath == "" {
		return src, nil
	}
	return ep.FilePath, nil
}

func discDestRoot(ctx context.Context, deps discUnpackDeps, m mode.Mode, tmdbID int, isoPath string) (string, error) {
	if deps.libStore != nil && tmdbID > 0 {
		switch m {
		case mode.Series:
			if s, err := deps.libStore.GetSeriesByTMDBID(ctx, tmdbID); err == nil && s != nil && strings.TrimSpace(s.RootFolderPath) != "" {
				return s.RootFolderPath, nil
			}
		case mode.Movies:
			if it, err := deps.libStore.GetByTMDBID(ctx, mode.Movies, tmdbID); err == nil && it != nil && strings.TrimSpace(it.RootFolderPath) != "" {
				return it.RootFolderPath, nil
			}
		}
	}
	if deps.settingsStore == nil {
		return "", fmt.Errorf("no library root folder configured yet")
	}
	if key, ok := m.KidsRootPathKey(); ok {
		kids, err := deps.settingsStore.Get(ctx, key)
		if err == nil {
			kids = strings.TrimSpace(kids)
			if kids != "" && (isoPath == kids || strings.HasPrefix(isoPath, strings.TrimRight(kids, "/")+"/")) {
				return kids, nil
			}
		}
	}
	key, ok := libraryRootFolderKey(m)
	if !ok {
		return "", fmt.Errorf("no library root folder key for this mode")
	}
	path, err := deps.settingsStore.Get(ctx, key)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return "", err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("no %s library root folder configured yet — add one in Settings first", m)
	}
	return path, nil
}
