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
//
// Claude 2026-10-02: identify honors Movies / Series / Adult.
// Reason: Discs mixed TMDB movies+TV and had no Adult path.
// Troubleshooting: wrong catalog — Mode omitted defaults to movies.
// Review if: empty Mode searches both TMDB catalogs again.
//
// Claude 2026-10-02: each named disc title gets its own library identity.
// Reason: one volume TMDBID imported every short into a compilation folder.
//   Movies searches each wiki name; Series matches TMDB+TVDB episode titles.
// Troubleshooting: MKVs stacked as alternate.N under Vol. N — item TMDBID
//   empty and shared job id was applied.
// Review if: IFO/ffmpeg starts exposing per-title names (wiki skipped).

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
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/tvdb"
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
		catalog := parseDiscMode(req.Mode)
		if catalog == mode.Adult && denyIfAdultLocked(w, r) {
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
		hits := identifyDiscHits(r, httpClient, connStore, scStore, settingsStore, libStore, catalog, queries, works, disc.VolumeNumber(m.Volume, src))
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

func parseDiscMode(raw string) mode.Mode {
	switch mode.Mode(strings.TrimSpace(raw)) {
	case mode.Series:
		return mode.Series
	case mode.Adult:
		return mode.Adult
	default:
		return mode.Movies
	}
}

func identifyDiscHits(
	r *http.Request,
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
	catalog mode.Mode,
	queries []string,
	works []apidto.OrganizeDiscWork,
	volume int,
) []apidto.OrganizeDiscHit {
	if len(queries) == 0 || connStore == nil {
		return nil
	}
	if catalog == mode.Adult {
		return identifyAdultDiscHits(r, httpClient, connStore, scStore, settingsStore, libStore, queries)
	}
	// Claude 2026-10-02: build the operator's catalog session (not always Movies).
	// Reason: Series identify needs TVDB official episodes for year-season shorts.
	// Troubleshooting: Golden shorts unmatched — session was Movies-only TMDB seasons.
	// Review if: Adult identify shares this TMDB path again.
	sess, err := mode.Build(r.Context(), connStore, scStore, settingsStore, httpClient, nil, catalog)
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
			hit.Suggestions = suggestDiscSlots(r.Context(), sess.TMDB, sess.TVDB, hit.TMDBID, works)
		}
		hits = append(hits, hit)
	}
	// Claude 2026-10-02: search only the operator's library chip.
	// Reason: Discs mixed Movie and Series hits; the chip is the mode.
	// Troubleshooting: Series selected, movies still listed — catalog was ignored.
	// Review if: empty Mode goes back to searching both catalogs.
	for _, q := range queries {
		if catalog == mode.Movies {
			movies, err := sess.TMDB.SearchMovies(r.Context(), q)
			if err == nil {
				for i, it := range movies {
					if i >= 5 {
						break
					}
					add(string(mode.Movies), it)
				}
			}
		}
		if catalog == mode.Series {
			shows, err := sess.TMDB.SearchTV(r.Context(), q)
			if err == nil {
				for i, it := range shows {
					if i >= 5 {
						break
					}
					add(string(mode.Series), it)
				}
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
	// Claude 2026-10-02: multi-title movie discs classify each named work.
	// Reason: applying the volume compilation TMDBID stacked shorts as
	//   alternate.N under one disc folder. Each wiki name is its own movie.
	// Troubleshooting: works lack tmdbId — named count was ≤1 or no unique
	//   SearchMovies title hit.
	// Review if: single-feature DVDs start shipping with extra unnamed titles.
	if catalog == mode.Movies {
		attachDiscWorkMovies(r.Context(), sess.TMDB, libStore, works)
	}
	return hits
}

type movieSearcher interface {
	SearchMovies(ctx context.Context, query string) ([]tmdb.Item, error)
}

func attachDiscWorkMovies(ctx context.Context, searcher movieSearcher, libStore *library.Store, works []apidto.OrganizeDiscWork) {
	if searcher == nil || len(works) <= 1 {
		return
	}
	for i := range works {
		title := strings.TrimSpace(works[i].EpisodeTitle)
		if title == "" {
			continue
		}
		items, err := searcher.SearchMovies(ctx, title)
		if err != nil || len(items) == 0 {
			continue
		}
		hit, ok := uniqueMovieByTitle(title, items)
		if !ok {
			continue
		}
		works[i].TMDBID = hit.ID
		works[i].CatalogTitle = hit.Title
		works[i].Year = yearFromDate(hit.ReleaseDate)
		if libStore == nil {
			continue
		}
		item, gerr := libStore.GetByTMDBID(ctx, mode.Movies, hit.ID)
		if gerr != nil || item == nil || item.FilePath == "" {
			continue
		}
		works[i].ExistingPath = item.FilePath
		works[i].ExistingTitle = item.Title
	}
}

func uniqueMovieByTitle(want string, items []tmdb.Item) (tmdb.Item, bool) {
	titles := make([]string, len(items))
	for i, it := range items {
		titles[i] = it.Title
	}
	idx, ok := disc.UniqueNamedHit(want, titles)
	if !ok {
		return tmdb.Item{}, false
	}
	return items[idx], true
}

func identifyAdultDiscHits(
	r *http.Request,
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
	queries []string,
) []apidto.OrganizeDiscHit {
	sess, err := mode.Build(r.Context(), connStore, scStore, settingsStore, httpClient, nil, mode.Adult)
	if err != nil || sess == nil || sess.Identify == nil || sess.Identify.Boxes == nil {
		return nil
	}
	seen := map[string]bool{}
	var hits []apidto.OrganizeDiscHit
	add := func(c identify.SceneCandidate) {
		if strings.TrimSpace(c.Box) == "" || strings.TrimSpace(c.SceneID) == "" {
			return
		}
		key := c.Box + ":" + c.SceneID
		if seen[key] {
			return
		}
		seen[key] = true
		hit := apidto.OrganizeDiscHit{
			Mode:    string(mode.Adult),
			Title:   c.Title,
			Year:    yearFromDate(c.Date),
			Box:     c.Box,
			SceneID: c.SceneID,
			Studio:  c.Studio,
			Date:    c.Date,
		}
		if libStore != nil {
			fillDiscExisting(r.Context(), libStore, &hit)
		}
		hits = append(hits, hit)
	}
	for _, q := range queries {
		items, _ := sess.Identify.Boxes.ListSceneCandidates(r.Context(), q, sess.Identify.StashBoxes)
		for _, it := range items {
			add(it)
			if len(hits) >= 12 {
				return hits
			}
		}
		if movie, merr := sess.Identify.Boxes.SearchTPDBMovies(r.Context(), q); merr == nil && movie != nil {
			add(identify.SceneCandidate{
				Box: movie.Box, SceneID: movie.SceneID, Title: movie.Title,
				Studio: movie.Studio, Date: movie.Date,
			})
			if len(hits) >= 12 {
				return hits
			}
		}
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
	case string(mode.Adult):
		if strings.TrimSpace(hit.Box) == "" || strings.TrimSpace(hit.SceneID) == "" {
			return
		}
		sc, err := libStore.GetScene(ctx, hit.Box, hit.SceneID)
		if err != nil || sc == nil || sc.FilePath == "" {
			return
		}
		hit.ExistingPath = sc.FilePath
		hit.ExistingTitle = sc.Title
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
		// Claude 2026-10-02: replace the file for THIS work's identity.
		// Reason: hit.ExistingPath is the volume compilation; replacing it
		//   would delete the wrong movie when a short has its own TMDBID.
		// Troubleshooting: Replace old removed Golden Collection — item
		//   TMDBID was ignored.
		// Review if: Adult per-work scenes land and need the same lookup.
		if hit.Mode == string(mode.Movies) && it.TMDBID > 0 && libStore != nil {
			if item, err := libStore.GetByTMDBID(ctx, mode.Movies, it.TMDBID); err == nil && item != nil {
				path = item.FilePath
			}
		}
		if path == "" && (hit.Mode == string(mode.Movies) || hit.Mode == string(mode.Adult)) {
			if it.TMDBID <= 0 || it.TMDBID == hit.TMDBID {
				path = hit.ExistingPath
			}
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

func suggestDiscSlots(ctx context.Context, client *tmdb.Client, tvdbClient *tvdb.Client, tmdbID int, works []apidto.OrganizeDiscWork) []apidto.OrganizeDiscSuggestion {
	if tmdbID <= 0 || len(works) == 0 {
		return nil
	}
	var catalog []disc.CatalogEpisode
	if client != nil {
		if details, err := client.TVDetails(ctx, tmdbID); err == nil {
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
		}
	}
	// Claude 2026-10-02: merge TVDB official episodes into disc slot match.
	// Reason: Looney Tunes shorts live in year-seasons on TVDB (S1938E36),
	//   not TMDB's airdate seasons. Unique title match must see both.
	// Troubleshooting: wiki name present, no S/E — ExternalIDs or SeriesEpisodes
	//   failed, or the title was not unique after merge.
	// Review if: TMDB year-seasons match TVDB official for this show.
	if tvdbClient != nil && client != nil {
		if tvdbID, err := client.ExternalIDs(ctx, tmdbID); err == nil && tvdbID > 0 {
			if eps, err := tvdbClient.SeriesEpisodes(ctx, tvdbID, tvdb.SeasonTypeOfficial); err == nil {
				var extra []disc.CatalogEpisode
				for _, ep := range eps {
					extra = append(extra, disc.CatalogEpisode{
						Season: ep.SeasonNumber, Episode: ep.Number,
						Title: ep.Name, RuntimeMin: ep.Runtime,
					})
				}
				catalog = disc.MergeCatalogByTitle(catalog, extra)
			}
		}
	}
	if len(catalog) == 0 {
		return nil
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

func discHasCatalog(req apidto.OrganizeDiscUnpackRequest) bool {
	m := parseDiscMode(req.Mode)
	if m == mode.Adult {
		return strings.TrimSpace(req.Box) != "" && strings.TrimSpace(req.SceneID) != ""
	}
	if req.TMDBID > 0 {
		return true
	}
	for _, it := range req.Items {
		if it.TMDBID > 0 {
			return true
		}
	}
	return false
}

func discMovieIdentity(req apidto.OrganizeDiscUnpackRequest, it apidto.OrganizeDiscUnpackItem) (tmdbID int, title string, year int) {
	if it.TMDBID > 0 {
		title = strings.TrimSpace(it.Title)
		if title == "" {
			title = strings.TrimSpace(it.EpisodeTitle)
		}
		return it.TMDBID, title, it.Year
	}
	return req.TMDBID, req.Title, req.Year
}

func shouldSkipSharedMovieImport(req apidto.OrganizeDiscUnpackRequest, it apidto.OrganizeDiscUnpackItem) bool {
	if it.TMDBID > 0 {
		return false
	}
	// Job-level compilation TMDBID is only safe for a single-feature extract.
	return len(req.Items) > 1 || req.TMDBID <= 0
}

func importDiscOutputs(ctx context.Context, deps discUnpackDeps, src string, req apidto.OrganizeDiscUnpackRequest, outputs []string) ([]string, error) {
	if deps.settingsStore == nil || !discHasCatalog(req) {
		return outputs, nil
	}
	m := parseDiscMode(req.Mode)
	destRoot, err := discDestRoot(ctx, deps, m, req, src)
	if err != nil {
		return nil, err
	}
	preset, err := resolveNamingPreset(ctx, deps.settingsStore, m)
	if err != nil {
		return nil, err
	}
	tier := string(autoGrabTier(ctx, deps.settingsStore, m))
	var tmdbClient *tmdb.Client
	var sess *mode.Session
	if deps.connStore != nil {
		if built, berr := mode.Build(ctx, deps.connStore, deps.scStore, deps.settingsStore, deps.httpClient, nil, m); berr == nil && built != nil {
			sess = built
			tmdbClient = built.TMDB
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
		next, ierr := importOneDiscOutput(ctx, deps.libStore, sess, tmdbClient, m, destRoot, preset, tier, req, it, path)
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
	sess *mode.Session,
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
	title, tmdbID, year := req.Title, req.TMDBID, req.Year
	// Claude 2026-10-02: Movies import uses the work's TMDBID, not the volume.
	// Reason: ApplyLibrary folds same TMDBID as alternate.N — that is the
	//   disc-folder bug. Per-title identity rides on the unpack item.
	// Troubleshooting: shorts landed under Golden Collection — item TMDBID
	//   was 0 and shouldSkipSharedMovieImport did not run.
	// Review if: Adult disc titles get per-work scene assignment.
	if m == mode.Movies {
		if shouldSkipSharedMovieImport(req, it) {
			return src, nil
		}
		tmdbID, title, year = discMovieIdentity(req, it)
		if tmdbID <= 0 {
			return src, nil
		}
	}
	p := proposals.Proposal{
		Mode:            m,
		Workflow:        proposals.Rename,
		Status:          proposals.Pending,
		SourcePath:      src,
		SourceName:      filepath.Base(src),
		RootFolderPath:  destRoot,
		Title:           title,
		TMDBID:          tmdbID,
		Year:            year,
		SeasonNumber:    it.SeasonNumber,
		EpisodeNumber:   it.EpisodeNumber,
		EpisodeTitle:    it.EpisodeTitle,
		Studio:          req.Studio,
		Date:            req.Date,
		GiveBackBox:     req.Box,
		GiveBackSceneID: req.SceneID,
	}
	if m == mode.Movies {
		if existing, gerr := libStore.GetByTMDBID(ctx, mode.Movies, tmdbID); gerr == nil && existing != nil {
			if strings.TrimSpace(existing.RootFolderPath) != "" {
				p.RootFolderPath = existing.RootFolderPath
			}
		}
	}
	if m == mode.Adult {
		_, _, _, err := rename.ApplyLibraryAdult(ctx, sess, libStore, p, tier, nil)
		if err != nil {
			return src, err
		}
		if sc, gerr := libStore.GetScene(ctx, req.Box, req.SceneID); gerr == nil && sc != nil && sc.FilePath != "" {
			return sc.FilePath, nil
		}
		return src, nil
	}
	if m == mode.Movies {
		_, _, err := rename.ApplyLibrary(ctx, libStore, p, preset, tier, nil)
		if err != nil {
			return src, err
		}
		if item, gerr := libStore.GetByTMDBID(ctx, mode.Movies, tmdbID); gerr == nil && item != nil && item.FilePath != "" {
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

func discDestRoot(ctx context.Context, deps discUnpackDeps, m mode.Mode, req apidto.OrganizeDiscUnpackRequest, isoPath string) (string, error) {
	if deps.libStore != nil {
		switch m {
		case mode.Series:
			if req.TMDBID > 0 {
				if s, err := deps.libStore.GetSeriesByTMDBID(ctx, req.TMDBID); err == nil && s != nil && strings.TrimSpace(s.RootFolderPath) != "" {
					return s.RootFolderPath, nil
				}
			}
		case mode.Movies:
			ids := make([]int, 0, 1+len(req.Items))
			if req.TMDBID > 0 {
				ids = append(ids, req.TMDBID)
			}
			for _, item := range req.Items {
				if item.TMDBID > 0 {
					ids = append(ids, item.TMDBID)
				}
			}
			for _, id := range ids {
				if it, err := deps.libStore.GetByTMDBID(ctx, mode.Movies, id); err == nil && it != nil && strings.TrimSpace(it.RootFolderPath) != "" {
					return it.RootFolderPath, nil
				}
			}
		case mode.Adult:
			if strings.TrimSpace(req.Box) != "" && strings.TrimSpace(req.SceneID) != "" {
				if sc, err := deps.libStore.GetScene(ctx, req.Box, req.SceneID); err == nil && sc != nil && strings.TrimSpace(sc.RootFolderPath) != "" {
					return sc.RootFolderPath, nil
				}
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
