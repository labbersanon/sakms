package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

const (
	airDateTMDBID = 900
	airDateTitle  = "Some Show"
	// sqliteTimeFormat mirrors grabs.FormatTime's layout, which is unexported
	// there. Only used to step a test's injected clock past a row's retry_after.
	sqliteTimeFormat = "2006-01-02T15:04:05.000Z"
	// noQualifyingRelease is an empty indexer result: autograb.Select reaches
	// Selection.Fallback, so a gated trigger parks a pending_retry row instead of
	// dispatching. This is the ordinary outcome for an episode that aired today.
	noQualifyingRelease = `[]`
)

// qualifyingSeriesRelease builds a single-release Prowlarr fixture whose
// title carries the given season/episode as an SxxEyy marker, clearing
// every 1080p tier floor against the 45-minute per-episode runtime the fake
// TMDB season serves: 8 GB / 2700 s ≈ 23 Mbps implied, roughly doubled again
// by the x265 equivalence. Its title's single-episode SxxEyy marker (not a
// season pack) means isSeasonPackTitle cannot neutralize it.
//
// Claude 2026-08-03: was a fixed `S01E01` const string; now built per-call
// from the actual season/episode under test.
// Reason: RunAutoGrab now runs FilterSeasonScope (see releasematch.go)
// before scoring, which drops a release whose title's season/episode
// conflicts with the request. A fixed S01E01 fixture silently worked for
// EVERY season/episode combination before that filter existed — several
// call sites in this file exercise season 3 or episode 4, and a fixed
// S01E01 body would now get filtered out as a wrong-season/wrong-episode
// release, parking a pending_retry row instead of the dispatched row these
// tests assert on.
// Troubleshooting: if a test using this helper unexpectedly parks instead
// of dispatching, confirm the season/episode passed here matches the
// season/episode actually being monitored/searched in that test.
func qualifyingSeriesRelease(season, episode int) string {
	title := fmt.Sprintf("Some.Show.S%02dE%02d.1080p.WEB-DL.x265-GROUP", season, episode)
	return fmt.Sprintf(`[{"guid":"1","title":%q,"indexer":"I","protocol":"torrent","size":8000000000,"seeders":50,"downloadUrl":"magnet:?xt=urn:btih:ABCDEF1234567890abcdef1234567890abcdef12"}]`, title)
}

// fakeTMDBEpisode is one episode of the fake TMDB season catalog.
type fakeTMDBEpisode struct {
	Number  int
	Name    string
	AirDate string
}

// fakeTMDBSeries serves the three /tv endpoints this pass touches: /tv/{id}
// (whose seasons[] drives new-season discovery), /tv/{id}/season/{n} (the
// catalog sync's data source, also read for the per-episode runtime), and
// /tv/{id}/external_ids (autoGrabSearch's Series branch).
//
// Keys of `seasons` are the seasons TMDB reports, so a season present here but
// absent from library_episodes is exactly a discoverable new season.
//
// It takes the env so /tv/{id} can be failed mid-test via setTVDetailsFail,
// exactly the way prowlarrBody is already swappable: a transient discovery
// failure must not stop the always-on known-season sync, and only a fixture
// that can fail ONE of the three endpoints can prove that.
func fakeTMDBSeries(t *testing.T, env *airDateEnv, seasons map[int][]fakeTMDBEpisode) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tv/{id}/external_ids", func(w http.ResponseWriter, r *http.Request) {
		writeFakeTMDBJSON(w, map[string]any{"tvdb_id": 4321})
	})
	mux.HandleFunc("GET /tv/{id}/season/{season}", func(w http.ResponseWriter, r *http.Request) {
		season, err := strconv.Atoi(r.PathValue("season"))
		if err != nil {
			http.Error(w, "bad season", http.StatusBadRequest)
			return
		}
		episodes := []map[string]any{}
		for _, e := range seasons[season] {
			episodes = append(episodes, map[string]any{
				"episode_number": e.Number, "name": e.Name,
				"air_date": e.AirDate, "runtime": 45,
			})
		}
		writeFakeTMDBJSON(w, map[string]any{"episodes": episodes})
	})
	mux.HandleFunc("GET /tv/{id}", func(w http.ResponseWriter, r *http.Request) {
		if env.tvDetailsFailing() {
			http.Error(w, "TMDB is having a bad day", http.StatusInternalServerError)
			return
		}
		numbers := make([]int, 0, len(seasons))
		for n := range seasons {
			numbers = append(numbers, n)
		}
		sort.Ints(numbers)
		list := []map[string]any{}
		for _, n := range numbers {
			list = append(list, map[string]any{"season_number": n, "episode_count": len(seasons[n])})
		}
		writeFakeTMDBJSON(w, map[string]any{
			"id": airDateTMDBID, "name": airDateTitle,
			"episode_run_time": []int{45}, "seasons": list,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeFakeTMDBJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}

// airDateEnv is one fully-wired air-date monitoring environment: real stores on
// one temp database, a fake TMDB and a fake Prowlarr, and the session builder
// runUsenetRetryCycle takes. It builds its own database rather than reusing
// testStores so it can also hand out excludes.Store (GET /api/requests needs
// one) and the raw *sql.DB.
type airDateEnv struct {
	ctx      context.Context
	settings *settings.Store
	grabs    *grabs.Store
	lib      *library.Store
	excludes *excludes.Store
	dl       *downloader.Manager
	build    sessionBuilderFunc
	deps     AutoGrabDeps
	excluded map[string]bool

	// conns/scConns back seasonCatalog, which builds its own Series session
	// per request rather than taking the sessionBuilderFunc the background pass
	// uses — the same connection-store wiring discoverAvailabilityHandler takes.
	// Held here (rather than staying locals of newAirDateEnv) so the real-mux
	// test can pass them instead of the nils it used to, which would now panic
	// inside mode.Build the moment the season GET is hit.
	conns   *connections.Store
	scConns *serviceconn.Store

	// prowlarrBody is swappable mid-test: several cases need a release to
	// QUALIFY once (so a row genuinely dispatches) and then stop qualifying (so
	// the retry that follows parks instead of re-dispatching), which is exactly
	// how a real indexer behaves when the release it served is gone.
	prowlarrMu   sync.Mutex
	prowlarrBody string

	// tvDetailsFail makes the fake TMDB's /tv/{id} return 500 while the other
	// two endpoints keep working — a transient discovery-half failure.
	tmdbMu        sync.Mutex
	tvDetailsFail bool
}

func (e *airDateEnv) setProwlarrBody(body string) {
	e.prowlarrMu.Lock()
	defer e.prowlarrMu.Unlock()
	e.prowlarrBody = body
}

func (e *airDateEnv) setTVDetailsFail(fail bool) {
	e.tmdbMu.Lock()
	defer e.tmdbMu.Unlock()
	e.tvDetailsFail = fail
}

func (e *airDateEnv) tvDetailsFailing() bool {
	e.tmdbMu.Lock()
	defer e.tmdbMu.Unlock()
	return e.tvDetailsFail
}

func newAirDateEnv(t *testing.T, seasons map[int][]fakeTMDBEpisode, prowlarrBody string) *airDateEnv {
	t.Helper()
	ctx := context.Background()

	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	connStore := connections.New(sqlDB, secretStore)
	scStore := serviceconn.NewStore(sqlDB, secretStore)
	settingsStore := settings.New(sqlDB)
	grabsStore := grabs.New(sqlDB, secretStore)
	libStore := library.New(sqlDB)

	env := &airDateEnv{prowlarrBody: prowlarrBody}

	dl := newTestDownloader("gid-airdate", t.TempDir())
	tmdbSrv := fakeTMDBSeries(t, env, seasons)
	prowlarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.prowlarrMu.Lock()
		body := env.prowlarrBody
		env.prowlarrMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(prowlarrSrv.Close)
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	for _, c := range []struct{ service, url string }{{"tmdb", tmdbSrv.URL}, {"prowlarr", prowlarrSrv.URL}} {
		if err := connStore.Upsert(ctx, c.service, c.url, "key"); err != nil {
			t.Fatalf("upserting %s: %v", c.service, err)
		}
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Series), string(quality.Low)); err != nil {
		t.Fatalf("setting the series quality tier: %v", err)
	}
	setAutoGrabToggle(t, settingsStore, true)

	env.ctx, env.settings, env.grabs, env.lib = ctx, settingsStore, grabsStore, libStore
	env.excludes, env.dl = excludes.New(sqlDB), dl
	env.conns, env.scConns = connStore, scStore
	env.build = func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
		return mode.Build(ctx, connStore, scStore, settingsStore, testHTTPClient(), dl, m)
	}
	env.deps = AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}
	return env
}

// catalog is the seasonCatalog the two operator-facing season routes read
// through — the merged local + TMDB season list.
func (e *airDateEnv) catalog() seasonCatalog {
	return seasonCatalog{
		httpClient: testHTTPClient(), connStore: e.conns, scStore: e.scConns,
		settings: e.settings, lib: e.lib,
	}
}

// runCycle drives one full runUsenetRetryCycle, which is how every integration
// test here reaches monitorAirDates: RunUsenetRetry itself returns immediately
// on a default install (interval <= 0), so a test that went through it would
// silently execute nothing and pass.
func (e *airDateEnv) runCycle(t *testing.T, now time.Time) {
	t.Helper()
	runUsenetRetryCycle(e.ctx, e.deps, e.build, nil, e.lib, e.excluded, now)
}

func (e *airDateEnv) trackSeries(t *testing.T) library.Series {
	t.Helper()
	series, err := e.lib.UpsertSeries(e.ctx, library.Series{
		TMDBID: airDateTMDBID, TVDBID: 4321, Title: airDateTitle, RootFolderPath: "/series",
	})
	if err != nil {
		t.Fatalf("tracking series: %v", err)
	}
	return series
}

// seedMissingEpisode records a fileless episode row — "TMDB says this exists,
// we don't have it" — which is exactly what MissingEpisodes returns.
func (e *airDateEnv) seedMissingEpisode(t *testing.T, seriesID int64, season, episode int, airDate string) {
	t.Helper()
	if err := e.lib.UpsertEpisodeCatalog(e.ctx, seriesID, season, episode, fmt.Sprintf("Ep %d", episode), airDate); err != nil {
		t.Fatalf("seeding missing episode s%02de%02d: %v", season, episode, err)
	}
}

func (e *airDateEnv) seedOnDiskEpisode(t *testing.T, seriesID int64, season, episode int, airDate, path string) {
	t.Helper()
	if _, err := e.lib.UpsertEpisode(e.ctx, library.Episode{
		SeriesID: seriesID, SeasonNumber: season, EpisodeNumber: episode,
		Title: fmt.Sprintf("Ep %d", episode), AirDate: airDate, FilePath: path,
	}); err != nil {
		t.Fatalf("seeding on-disk episode s%02de%02d: %v", season, episode, err)
	}
}

func (e *airDateEnv) monitor(t *testing.T, seriesID int64, season int, monitored bool) {
	t.Helper()
	if err := e.lib.SetSeasonMonitored(e.ctx, seriesID, season, monitored); err != nil {
		t.Fatalf("setting season %d monitored: %v", season, err)
	}
}

func (e *airDateEnv) setDiscovery(t *testing.T, enabled bool) {
	t.Helper()
	if err := e.settings.SetBool(e.ctx, seriesNewSeasonDiscoveryKey, enabled); err != nil {
		t.Fatalf("setting the discovery toggle: %v", err)
	}
}

func (e *airDateEnv) seriesGrabs(t *testing.T) []grabs.Grab {
	t.Helper()
	list, err := e.grabs.List(e.ctx, mode.Series)
	if err != nil {
		t.Fatalf("listing series grabs: %v", err)
	}
	return list
}

func (e *airDateEnv) episodeRow(t *testing.T, seriesID int64, season, episode int) library.Episode {
	t.Helper()
	rows, err := e.lib.ListEpisodes(e.ctx, seriesID)
	if err != nil {
		t.Fatalf("listing episodes: %v", err)
	}
	for _, ep := range rows {
		if ep.SeasonNumber == season && ep.EpisodeNumber == episode {
			return ep
		}
	}
	t.Fatalf("no episode row for s%02de%02d (have %+v)", season, episode, rows)
	return library.Episode{}
}

func dayOffset(now time.Time, days int) string {
	return now.UTC().AddDate(0, 0, days).Format("2006-01-02")
}

// --- T-2.*: the episode catalog sync ---------------------------------------

// TestAirDateCatalogSyncCreatesFilelessRows (T-2.1) is the sync's whole reason
// for existing. Nothing in this codebase wrote a library_episodes row with an
// empty file_path after the Sonarr importer was deleted, and nothing ever
// seeded air_date at all — so MissingEpisodes returned the empty set on every
// install and the air-date filter had no data to filter on.
//
// It also asserts the corruption guard end to end: an episode that IS on disk
// keeps its file_path after the sync touches its row.
func TestAirDateCatalogSyncCreatesFilelessRows(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -30)},
		{Number: 2, Name: "Two", AirDate: dayOffset(now, -23)},
		{Number: 3, Name: "Three", AirDate: dayOffset(now, -16)},
	}}, noQualifyingRelease)
	series := env.trackSeries(t)
	// One known episode makes season 1 a KNOWN season, which is what the
	// always-on half of the sync operates over. The season stays unmonitored, so
	// nothing may dispatch — monitoring gates searching, never metadata.
	env.seedOnDiskEpisode(t, series.ID, 1, 1, "", "/series/Some Show/S01E01.mkv")

	env.runCycle(t, now)

	for _, tc := range []struct {
		episode int
		airDate string
	}{{2, dayOffset(now, -23)}, {3, dayOffset(now, -16)}} {
		got := env.episodeRow(t, series.ID, 1, tc.episode)
		if got.FilePath != "" {
			t.Errorf("s01e%02d was synced with a file path %q — a catalog row must be fileless, or it never counts as missing", tc.episode, got.FilePath)
		}
		if got.AirDate != tc.airDate {
			t.Errorf("s01e%02d air date = %q, want %q", tc.episode, got.AirDate, tc.airDate)
		}
	}
	onDisk := env.episodeRow(t, series.ID, 1, 1)
	if onDisk.FilePath != "/series/Some Show/S01E01.mkv" {
		t.Fatalf("the sync blanked a tracked episode's file path (%q) — that is the library-corruption failure UpsertEpisodeCatalog exists to make structurally impossible", onDisk.FilePath)
	}
	if onDisk.AirDate != dayOffset(now, -30) {
		t.Errorf("the sync did not enrich a tracked episode's air date: %q", onDisk.AirDate)
	}
	if len(env.seriesGrabs(t)) != 0 {
		t.Error("an UNMONITORED season's episodes were dispatched — the sync must populate metadata without ever implying a search")
	}
}

// TestAirDateDiscoveryOffLeavesNewSeasonsAlone (T-2.2): with the toggle off, a
// season TMDB reports that has zero episode rows is neither synced nor
// monitored. Seasons that already have rows are still synced regardless — the
// toggle governs discovery only.
func TestAirDateDiscoveryOffLeavesNewSeasonsAlone(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		1: {{Number: 1, Name: "One", AirDate: dayOffset(now, -30)}, {Number: 2, Name: "Two", AirDate: dayOffset(now, -23)}},
		2: {{Number: 1, Name: "New One", AirDate: dayOffset(now, -2)}},
	}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedOnDiskEpisode(t, series.ID, 1, 1, "", "/series/Some Show/S01E01.mkv")
	env.setDiscovery(t, false)

	env.runCycle(t, now)

	rows, err := env.lib.ListEpisodes(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("listing episodes: %v", err)
	}
	for _, ep := range rows {
		if ep.SeasonNumber == 2 {
			t.Fatalf("season 2 was synced with discovery off: %+v", ep)
		}
	}
	if len(rows) != 2 {
		t.Errorf("known season 1 was not fully synced (rows %+v) — discovery gates only ZERO-ROW seasons", rows)
	}
	monitored, err := env.lib.MonitoredSeasons(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("reading monitored seasons: %v", err)
	}
	if monitored[2] {
		t.Error("season 2 was auto-monitored with discovery off")
	}
}

// TestAirDateDiscoveryMonitorsAndSyncsInTheSameCycle (T-2.3) is AC4, and the
// "same cycle" half is the load-bearing one: the sync runs for every tracked
// series BEFORE detection, so a season discovered this cycle is eligible NOW
// rather than 24 hours later.
func TestAirDateDiscoveryMonitorsAndSyncsInTheSameCycle(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		1: {{Number: 1, Name: "One", AirDate: dayOffset(now, -300)}},
		2: {{Number: 1, Name: "New One", AirDate: dayOffset(now, -2)}},
	}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedOnDiskEpisode(t, series.ID, 1, 1, "", "/series/Some Show/S01E01.mkv")
	env.setDiscovery(t, true)

	env.runCycle(t, now)

	monitored, err := env.lib.MonitoredSeasons(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("reading monitored seasons: %v", err)
	}
	if !monitored[2] {
		t.Fatal("a discovered season was not auto-monitored — the operator override is that it needs no separate opt-in")
	}
	if monitored[1] {
		t.Error("an ALREADY-KNOWN season was auto-monitored; discovery governs zero-row seasons only")
	}
	if got := env.episodeRow(t, series.ID, 2, 1); got.AirDate != dayOffset(now, -2) {
		t.Errorf("discovered season 2 episode air date = %q, want %q", got.AirDate, dayOffset(now, -2))
	}
	list := env.seriesGrabs(t)
	if len(list) != 1 {
		t.Fatalf("expected the discovered season's aired episode to be searched in the SAME cycle, got %d grabs: %+v", len(list), list)
	}
	if list[0].SeasonNumber != 2 || list[0].EpisodeNumber != 1 {
		t.Errorf("wrong episode searched: %+v", list[0])
	}
}

// TestAirDateDiscoverySkipsSeasonZero (T-2.4, plan §5.4): auto-monitoring
// Specials on a renewal would surprise an operator who asked for "season 5
// onward". Season 0 stays synced when it is already known, and stays manually
// monitorable — it is only excluded from auto-monitor-on-discovery.
func TestAirDateDiscoverySkipsSeasonZero(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		0: {{Number: 1, Name: "Special", AirDate: dayOffset(now, -5)}},
		1: {{Number: 1, Name: "One", AirDate: dayOffset(now, -300)}},
	}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedOnDiskEpisode(t, series.ID, 1, 1, "", "/series/Some Show/S01E01.mkv")
	env.setDiscovery(t, true)

	env.runCycle(t, now)

	monitored, err := env.lib.MonitoredSeasons(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("reading monitored seasons: %v", err)
	}
	if monitored[0] {
		t.Fatal("season 0 (Specials) was auto-monitored by discovery")
	}
	if len(env.seriesGrabs(t)) != 0 {
		t.Error("a Specials episode was searched off the back of discovery")
	}
}

// --- T-3.*: the detection filter -------------------------------------------

// TestEligibleEpisodesPredicates (T-3.2/T-3.3) pins the three predicates,
// including the two that are easy to get subtly wrong: an EMPTY air date is not
// "<= today" (an empty string sorts before every real date, so a naive
// comparison would search every unannounced episode immediately), and the
// comparison is lexicographic on TMDB's fixed YYYY-MM-DD, for which
// lexicographic order IS chronological order.
func TestEligibleEpisodesPredicates(t *testing.T) {
	const today = "2026-08-01"
	monitored := map[int]bool{1: true}

	for _, tc := range []struct {
		name    string
		episode library.Episode
		want    bool
	}{
		{"aired today is eligible", library.Episode{SeasonNumber: 1, EpisodeNumber: 1, AirDate: today}, true},
		{"aired yesterday is eligible", library.Episode{SeasonNumber: 1, EpisodeNumber: 2, AirDate: "2026-07-31"}, true},
		{"airs tomorrow is not", library.Episode{SeasonNumber: 1, EpisodeNumber: 3, AirDate: "2026-08-02"}, false},
		{"unannounced (empty air date) is not", library.Episode{SeasonNumber: 1, EpisodeNumber: 4, AirDate: ""}, false},
		{"long-aired but unmonitored season is not", library.Episode{SeasonNumber: 2, EpisodeNumber: 1, AirDate: "2020-01-01"}, false},
		{"year boundary sorts chronologically", library.Episode{SeasonNumber: 1, EpisodeNumber: 5, AirDate: "2025-12-31"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := eligibleEpisodes([]library.Episode{tc.episode}, monitored, today)
			if (len(got) == 1) != tc.want {
				t.Fatalf("eligible = %v, want %v (episode %+v)", len(got) == 1, tc.want, tc.episode)
			}
		})
	}
}

// TestAirDateNeverSearchesAnUnmonitoredSeason (T-3.1) is AC2 end to end: an
// episode of an unmonitored season that aired a year ago is never searched, no
// matter how long ago it aired.
func TestAirDateNeverSearchesAnUnmonitoredSeason(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -400)},
		{Number: 2, Name: "Two", AirDate: dayOffset(now, -365)},
	}}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -400))
	env.seedMissingEpisode(t, series.ID, 1, 2, dayOffset(now, -365))

	env.runCycle(t, now)

	if list := env.seriesGrabs(t); len(list) != 0 {
		t.Fatalf("an unmonitored season's long-aired episodes were searched: %+v", list)
	}
}

// TestAirDateIgnoresEpisodesAlreadyOnDisk (T-3.4). Inherited from
// MissingEpisodes' own file_path = "" filter, asserted anyway so a future
// change to how candidates are gathered cannot quietly re-grab the library.
func TestAirDateIgnoresEpisodesAlreadyOnDisk(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -30)},
	}}, qualifyingSeriesRelease(1, 1))
	series := env.trackSeries(t)
	env.seedOnDiskEpisode(t, series.ID, 1, 1, dayOffset(now, -30), "/series/Some Show/S01E01.mkv")
	env.monitor(t, series.ID, 1, true)

	env.runCycle(t, now)

	if list := env.seriesGrabs(t); len(list) != 0 {
		t.Fatalf("an episode already on disk was grabbed again: %+v", list)
	}
}

// --- T-4.*: Pattern B, the pre-filter, the cap ------------------------------

// TestAirDateDoesNotDispatchTwiceForOneEpisode (T-4.1) is the duplicate-download
// regression guard, and neither half is optional.
//
// (a) A dispatched episode still has file_path = "", so MissingEpisodes returns
// it again next cycle. Without the per-cycle active-grab pre-filter, cycle 2
// would Create a SECOND row and dispatch a SECOND download — activeGrabForGID
// cannot catch it, because AddNZB mints a fresh GID per call. Running the cycle
// once proves nothing; this runs it TWICE.
//
// (b) An in-flight SEASON PACK must mask every episode of that season. Keyed
// per-episode a pack keys as (tmdb, 3, 0) and masks NOTHING, so every episode of
// a season already downloading as a pack would get its own individual grab.
func TestAirDateDoesNotDispatchTwiceForOneEpisode(t *testing.T) {
	t.Run("a dispatched episode is not re-dispatched next cycle", func(t *testing.T) {
		now := time.Now()
		env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
			{Number: 1, Name: "One", AirDate: dayOffset(now, -3)},
		}}, qualifyingSeriesRelease(1, 1))
		series := env.trackSeries(t)
		env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -3))
		env.monitor(t, series.ID, 1, true)

		env.runCycle(t, now)
		first := env.seriesGrabs(t)
		if len(first) != 1 {
			t.Fatalf("cycle 1 produced %d grabs, want 1: %+v", len(first), first)
		}

		env.runCycle(t, now.Add(24*time.Hour))
		second := env.seriesGrabs(t)
		if len(second) != 1 {
			t.Fatalf("cycle 2 produced a DUPLICATE grab (%d rows) for an episode still missing on disk: %+v", len(second), second)
		}
		if second[0].ID != first[0].ID {
			t.Errorf("the surviving row is a new one (id %d, want %d)", second[0].ID, first[0].ID)
		}
		if got := len(env.dl.List()); got != 1 {
			t.Errorf("expected exactly one download-client add across two cycles, got %d", got)
		}
	})

	t.Run("an in-flight season pack masks every episode of its season", func(t *testing.T) {
		now := time.Now()
		env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{3: {
			{Number: 1, Name: "One", AirDate: dayOffset(now, -9)},
			{Number: 2, Name: "Two", AirDate: dayOffset(now, -2)},
		}}, qualifyingSeriesRelease(3, 1))
		series := env.trackSeries(t)
		env.seedMissingEpisode(t, series.ID, 3, 1, dayOffset(now, -9))
		env.seedMissingEpisode(t, series.ID, 3, 2, dayOffset(now, -2))
		env.monitor(t, series.ID, 3, true)

		pack, err := env.grabs.Create(env.ctx, grabs.Grab{
			Mode: mode.Series, Title: airDateTitle, TMDBID: airDateTMDBID,
			SeasonNumber: 3, EpisodeNumber: 0, SeasonSpecified: true,
			Indexer: "I", Protocol: "torrent", RootFolderPath: "/series",
			DownloadURL: "magnet:?xt=urn:btih:0000000000000000000000000000000000000000",
		})
		if err != nil {
			t.Fatalf("creating the in-flight season pack: %v", err)
		}

		env.runCycle(t, now)

		list := env.seriesGrabs(t)
		if len(list) != 1 || list[0].ID != pack.ID {
			t.Fatalf("individual episodes were dispatched behind an in-flight season pack: %+v", list)
		}
	})
}

// TestAirDateRespectsTheAutoGrabGate (T-4.2) proves the trigger does not route
// around the one gate. RunAutoGrab enforces usenet_autograb_enabled for every
// trigger that is not TriggerOperator, so a gated outcome here is also what pins
// this pass's trigger as a gated one.
func TestAirDateRespectsTheAutoGrabGate(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -3)},
	}}, qualifyingSeriesRelease(1, 1))
	setAutoGrabToggle(t, env.settings, false)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -3))
	env.monitor(t, series.ID, 1, true)

	env.runCycle(t, now)

	if list := env.seriesGrabs(t); len(list) != 0 {
		t.Fatalf("auto-grab is off and rows were still recorded: %+v", list)
	}
	if got := len(env.dl.List()); got != 0 {
		t.Fatalf("auto-grab is off and %d downloads were dispatched", got)
	}
}

// TestAirDateTakesThePatternBCreatePath (T-4.3). Two halves, because no single
// observable proves both:
//
//   - The qualifying case proves the Create path: a brand-new row with the GID
//     the dispatch returned, SeasonSpecified true (so a genuine Season 0 stays
//     distinguishable from "no season was picked"), and no prior row to re-arm.
//   - The no-match case proves the trigger is NOT TriggerOperator: RunAutoGrab's
//     fallback branch returns BEFORE parking for TriggerOperator ("a manual pick
//     list, not a retry row"), so a parked pending_retry row can only come from a
//     gated trigger.
func TestAirDateTakesThePatternBCreatePath(t *testing.T) {
	t.Run("a qualifying release creates a fresh dispatched row", func(t *testing.T) {
		now := time.Now()
		env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
			{Number: 4, Name: "Four", AirDate: dayOffset(now, -1)},
		}}, qualifyingSeriesRelease(1, 4))
		series := env.trackSeries(t)
		env.seedMissingEpisode(t, series.ID, 1, 4, dayOffset(now, -1))
		env.monitor(t, series.ID, 1, true)

		env.runCycle(t, now)

		list := env.seriesGrabs(t)
		if len(list) != 1 {
			t.Fatalf("expected exactly one created grab, got %+v", list)
		}
		got := list[0]
		if got.Status != grabs.Queued || got.DownloadGID != "gid-airdate" {
			t.Errorf("row did not join the normal download lifecycle: %+v", got)
		}
		if !got.SeasonSpecified || got.SeasonNumber != 1 || got.EpisodeNumber != 4 {
			t.Errorf("season/episode identity is wrong: %+v", got)
		}
		if got.TMDBID != airDateTMDBID {
			t.Errorf("tmdb id = %d, want %d", got.TMDBID, airDateTMDBID)
		}
	})

	t.Run("no qualifying release parks a retry row, which TriggerOperator never would", func(t *testing.T) {
		now := time.Now()
		env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
			{Number: 4, Name: "Four", AirDate: dayOffset(now, -1)},
		}}, noQualifyingRelease)
		series := env.trackSeries(t)
		env.seedMissingEpisode(t, series.ID, 1, 4, dayOffset(now, -1))
		env.monitor(t, series.ID, 1, true)

		env.runCycle(t, now)

		list := env.seriesGrabs(t)
		if len(list) != 1 {
			t.Fatalf("expected exactly one parked grab, got %+v", list)
		}
		if list[0].Status != grabs.PendingRetry {
			t.Fatalf("status = %q, want %q", list[0].Status, grabs.PendingRetry)
		}
		if list[0].RetryCount != 0 {
			t.Errorf("a first park must land at retry_count 0, got %d", list[0].RetryCount)
		}
	})
}

// TestAirDateSkipsExcludedSeries (T-4.4): excluding a title from the Requests
// worklist is an explicit "stop working on this", and it gets the same treatment
// retryDueGrabs gives it.
func TestAirDateSkipsExcludedSeries(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -3)},
	}}, qualifyingSeriesRelease(1, 1))
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -3))
	env.monitor(t, series.ID, 1, true)
	env.excluded = map[string]bool{excludes.Key(string(mode.Series), airDateTMDBID, airDateTitle): true}

	env.runCycle(t, now)

	if list := env.seriesGrabs(t); len(list) != 0 {
		t.Fatalf("an excluded series was searched anyway: %+v", list)
	}
}

// TestAirDateCapsDispatchesAtTwentyOldestFirst (T-4.5). The cap bounds NEW
// dispatch attempts only, and the ordering is what makes a backlog drain
// predictably instead of the same 20 rows being retried every cycle.
func TestAirDateCapsDispatchesAtTwentyOldestFirst(t *testing.T) {
	now := time.Now()
	episodes := []fakeTMDBEpisode{}
	for i := 1; i <= 25; i++ {
		episodes = append(episodes, fakeTMDBEpisode{Number: i, Name: fmt.Sprintf("Ep %d", i), AirDate: dayOffset(now, -(30 - i))})
	}
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: episodes}, noQualifyingRelease)
	series := env.trackSeries(t)
	for _, e := range episodes {
		env.seedMissingEpisode(t, series.ID, 1, e.Number, e.AirDate)
	}
	env.monitor(t, series.ID, 1, true)

	env.runCycle(t, now)

	list := env.seriesGrabs(t)
	if len(list) != maxAirDateGrabsPerCycle {
		t.Fatalf("dispatched %d episodes, want exactly %d", len(list), maxAirDateGrabsPerCycle)
	}
	dispatched := map[int]bool{}
	for _, g := range list {
		dispatched[g.EpisodeNumber] = true
	}
	// Episode N airs on day -(30-N), so lower numbers are OLDER: the cap must
	// take 1..20 and leave 21..25 for the next cycle.
	for i := 1; i <= maxAirDateGrabsPerCycle; i++ {
		if !dispatched[i] {
			t.Errorf("episode %d (older) was skipped while newer episodes were dispatched", i)
		}
	}
	for i := maxAirDateGrabsPerCycle + 1; i <= 25; i++ {
		if dispatched[i] {
			t.Errorf("episode %d (newer) was dispatched ahead of an older one", i)
		}
	}
}

// TestAirDatePreFilterExcludesAPendingRetryEpisode (T-4.6): pending_retry is an
// active status for isActiveGrab, so a parked episode masks itself and never
// gets a duplicate row. There is deliberately NO Failed special case — the retry
// cap was removed globally, so Failed is no longer reachable via retry
// exhaustion here or anywhere else.
func TestAirDatePreFilterExcludesAPendingRetryEpisode(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -3)},
	}}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -3))
	env.monitor(t, series.ID, 1, true)

	parked, err := env.grabs.Create(env.ctx, grabs.Grab{
		Mode: mode.Series, Title: airDateTitle, TMDBID: airDateTMDBID,
		SeasonNumber: 1, EpisodeNumber: 1, SeasonSpecified: true,
		RootFolderPath: "/series", Status: grabs.PendingRetry,
		// Far enough ahead that retryDueGrabs does not pick it up this cycle,
		// so the only thing under test is the pre-filter.
		RetryAfter:  grabs.FormatTime(now.Add(240 * time.Hour)),
		RetryReason: airDateRetryReason,
	})
	if err != nil {
		t.Fatalf("creating the parked row: %v", err)
	}

	env.runCycle(t, now)

	list := env.seriesGrabs(t)
	if len(list) != 1 || list[0].ID != parked.ID {
		t.Fatalf("a pending_retry episode was re-triggered into a duplicate grab: %+v", list)
	}
}

// --- T-C2.*: the air-date retry backoff -------------------------------------

// TestAirDateRetryBackoffSchedule (T-C2.1) pins the exact documented schedule.
// The table is correct as written: an implementation that produces a different
// wall clock has a defect in its STORE CALL (SetPendingRetry's unconditional
// retry_count increment), not in this table.
func TestAirDateRetryBackoffSchedule(t *testing.T) {
	want := map[int]time.Duration{
		-1: 24 * time.Hour, 0: 24 * time.Hour, 1: 24 * time.Hour, 2: 24 * time.Hour,
		3: 72 * time.Hour, 4: 72 * time.Hour,
		5: 168 * time.Hour, 6: 168 * time.Hour,
		7: 336 * time.Hour, 8: 336 * time.Hour,
		9: 720 * time.Hour, 10: 720 * time.Hour, 50: 720 * time.Hour, 1000: 720 * time.Hour,
	}
	counts := []int{-1, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 50, 1000}
	previous := time.Duration(0)
	for _, c := range counts {
		got := airDateRetryBackoff(c)
		if got != want[c] {
			t.Errorf("airDateRetryBackoff(%d) = %s, want %s", c, got, want[c])
		}
		if got <= 0 {
			t.Errorf("airDateRetryBackoff(%d) = %s — a zero interval makes the row due immediately, reintroducing the tight loop", c, got)
		}
		if got < previous {
			t.Errorf("airDateRetryBackoff(%d) = %s is SHORTER than the previous step (%s); the schedule must be monotonic non-decreasing", c, got, previous)
		}
		previous = got
	}
	if airDateRetryBackoff(1000) != 720*time.Hour {
		t.Error("the 30-day ceiling is not a ceiling")
	}
}

// TestAirDateBackoffAppliesAcrossCycles (T-C2.2) is the integration assertion,
// and it is not optional: without it airDateRetryBackoff can be perfectly
// correct and never called (e.g. if monitorAirDates were ordered BEFORE
// retryDueGrabs, whose parkPendingRetry would then have the last word on
// retry_after), and T-C2.1 would still pass green.
//
// It is also the regression guard for the store call. If the sweep used
// SetPendingRetry instead of SetRetryAfter, the row read back would carry
// retryCount + 1 relative to the value the interval was computed from, so this
// assertion fails at EVERY step boundary while passing inside a flat run. Do not
// "fix" that by loosening it to a tolerance or by reading back retryCount - 1 —
// it is reporting a real defect.
func TestAirDateBackoffAppliesAcrossCycles(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(base, -3)},
	}}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(base, -3))
	env.monitor(t, series.ID, 1, true)

	now := base
	lastCount := -1
	// Six cycles crosses both the 2->3 and the 4->5 step boundaries, which are
	// the cheapest transitions that catch a double increment.
	for cycle := 1; cycle <= 6; cycle++ {
		env.runCycle(t, now)

		list := env.seriesGrabs(t)
		if len(list) != 1 {
			t.Fatalf("cycle %d: expected exactly one row, got %+v", cycle, list)
		}
		got := list[0]
		if got.Status != grabs.PendingRetry {
			t.Fatalf("cycle %d: status = %q, want %q", cycle, got.Status, grabs.PendingRetry)
		}
		if want := cycle - 1; got.RetryCount != want {
			t.Fatalf("cycle %d: retry_count = %d, want %d — it must advance by exactly +1 per REAL search, which is what SetRetryAfter (not SetPendingRetry) preserves", cycle, got.RetryCount, want)
		}
		lastCount = got.RetryCount
		wantAfter := grabs.FormatTime(now.Add(airDateRetryBackoff(got.RetryCount)))
		if got.RetryAfter != wantAfter {
			t.Fatalf("cycle %d: retry_after = %q, want %q (= now + airDateRetryBackoff(%d)); the flat interval would have been %q",
				cycle, got.RetryAfter, wantAfter, got.RetryCount, grabs.FormatTime(now.Add(24*time.Hour)))
		}
		if got.RetryReason != airDateRetryReason {
			t.Fatalf("cycle %d: retry_reason = %q, want %q — the sweep's own reason is also the already-backed-off flag", cycle, got.RetryReason, airDateRetryReason)
		}

		due, err := time.Parse(sqliteTimeFormat, got.RetryAfter)
		if err != nil {
			t.Fatalf("cycle %d: parsing retry_after %q: %v", cycle, got.RetryAfter, err)
		}
		now = due.Add(time.Minute)
	}
	if lastCount != 5 {
		t.Fatalf("after six cycles retry_count = %d, want 5", lastCount)
	}
	if airDateRetryBackoff(lastCount) != 168*time.Hour {
		t.Fatalf("the run never crossed a step boundary, so it proves nothing about the double-increment defect")
	}
}

// TestAirDateBackoffLeavesOtherTriggersAlone (T-C2.3) is the scoping regression
// guard. The backoff must change the cadence of air-date-shaped rows and NOTHING
// else — usenet no-match rows, Movies rows and the sibling stale-torrent path
// all keep their flat, uncapped interval, byte for byte.
//
// Each row is seeded NOT yet due, so retryDueGrabs skips it and the only thing
// that could touch it is the sweep. The seeded retry_count is high on purpose:
// at that count the backoff would be a visibly different interval, so "unchanged"
// is a real assertion rather than a coincidence of two 24-hour values agreeing.
func TestAirDateBackoffLeavesOtherTriggersAlone(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -3)},
	}}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedOnDiskEpisode(t, series.ID, 1, 1, dayOffset(now, -3), "/series/Some Show/S01E01.mkv")
	env.monitor(t, series.ID, 1, true)

	notYetDue := grabs.FormatTime(now.Add(24 * time.Hour))
	seed := func(t *testing.T, g grabs.Grab) grabs.Grab {
		t.Helper()
		g.Status = grabs.PendingRetry
		g.RetryAfter, g.RetryCount, g.RetryReason = notYetDue, 7, noQualifyingCandidateReason
		created, err := env.grabs.Create(env.ctx, g)
		if err != nil {
			t.Fatalf("seeding grab: %v", err)
		}
		return created
	}

	searchHook := seed(t, grabs.Grab{Mode: mode.Series, Title: "Search Hook Row", TMDBID: 0, RootFolderPath: "/series"})
	seasonPack := seed(t, grabs.Grab{
		Mode: mode.Series, Title: airDateTitle, TMDBID: airDateTMDBID,
		SeasonNumber: 3, EpisodeNumber: 0, SeasonSpecified: true, RootFolderPath: "/series",
	})
	movie := seed(t, grabs.Grab{Mode: mode.Movies, Title: "Some Movie", TMDBID: 42, RootFolderPath: "/movies"})
	// Positive control: same seeded shape, but air-date-shaped, so the sweep MUST
	// re-park it. Without this the test would pass against a sweep that does
	// nothing at all.
	inScope := seed(t, grabs.Grab{
		Mode: mode.Series, Title: airDateTitle, TMDBID: airDateTMDBID,
		SeasonNumber: 1, EpisodeNumber: 1, SeasonSpecified: true, RootFolderPath: "/series",
	})

	env.runCycle(t, now)

	for _, tc := range []struct {
		name string
		seed grabs.Grab
	}{
		{"a toggle-ON Search hook row (TMDBID 0)", searchHook},
		{"a season-pack row (episode 0)", seasonPack},
		{"a Movies-mode row", movie},
	} {
		got, err := env.grabs.Get(env.ctx, tc.seed.ID)
		if err != nil {
			t.Fatalf("%s: reloading: %v", tc.name, err)
		}
		if got.RetryAfter != notYetDue || got.RetryReason != noQualifyingCandidateReason || got.RetryCount != 7 {
			t.Errorf("%s had its retry cadence changed by the air-date backoff: after=%q reason=%q count=%d",
				tc.name, got.RetryAfter, got.RetryReason, got.RetryCount)
		}
	}

	got, err := env.grabs.Get(env.ctx, inScope.ID)
	if err != nil {
		t.Fatalf("reloading the in-scope row: %v", err)
	}
	if want := grabs.FormatTime(now.Add(airDateRetryBackoff(7))); got.RetryAfter != want {
		t.Fatalf("the in-scope control row was NOT backed off: after=%q, want %q", got.RetryAfter, want)
	}
	if got.RetryReason != airDateRetryReason {
		t.Errorf("the in-scope control row's reason = %q, want %q", got.RetryReason, airDateRetryReason)
	}
}

// --- T-C1.*: un-monitoring cancels in-flight air-date retries ---------------

// dispatchedThenReparkedAirDateGrab builds the row category a single origin
// predicate would have missed: an air-date grab that QUALIFIED, dispatched, and
// was later re-parked by an asynchronous retrieval failure. It carries a real
// Indexer/DownloadURL and a cleared GID, which makes it structurally
// indistinguishable from an operator's own Discover grab that met the same fate.
//
// Built through the production path (a real dispatch, then parkGrabForRetry —
// the exact call sweepUsenetFailures makes) rather than hand-written, because a
// hand-written fixture would encode this test's own guess about the shape.
func dispatchedThenReparkedAirDateGrab(t *testing.T, env *airDateEnv, now time.Time) grabs.Grab {
	t.Helper()
	env.runCycle(t, now)
	list := env.seriesGrabs(t)
	if len(list) != 1 {
		t.Fatalf("expected exactly one dispatched grab to re-park, got %+v", list)
	}
	if list[0].Indexer == "" || list[0].DownloadURL == "" {
		t.Fatalf("the dispatched grab carries no indexer/download URL, so it cannot stand in for a reparked one: %+v", list[0])
	}
	if err := parkGrabForRetry(env.ctx, env.deps, list[0].ID, articlesUnavailableReason); err != nil {
		t.Fatalf("parking the dispatched grab: %v", err)
	}
	got, err := env.grabs.Get(env.ctx, list[0].ID)
	if err != nil {
		t.Fatalf("reloading the reparked grab: %v", err)
	}
	return *got
}

// TestUnMonitoringCancelsQueuedAirDateRetries (T-C1.1 + MJ-4). Setting the flag
// alone would leave every already-queued air-date retry for that season being
// re-searched forever: retryDueGrabs consults exactly DueForRetry and the
// excludes map and knows nothing about monitored state.
//
// The reason assertion is as load-bearing as the status one. A test that checks
// only status == Failed passes against the exact confusing-signal bug the reason
// write exists to fix — Failed means "this release was taken down" everywhere
// else in this app.
//
// The negative case is the most important line here: a genuinely dispatched grab
// awaiting retry must survive the flag flip. Destroying an operator-initiated
// download because a season flag changed is a worse bug than the one this fixes.
func TestUnMonitoringCancelsQueuedAirDateRetries(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{3: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -3)},
	}}, qualifyingSeriesRelease(3, 1))
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 3, 1, dayOffset(now, -3))
	env.monitor(t, series.ID, 3, true)

	// The negative case, built through a real dispatch: episode 1 of season 3.
	dispatched := dispatchedThenReparkedAirDateGrab(t, env, now)
	// The positive case: a never-dispatched air-date retry for episode 2.
	parked, err := env.grabs.Create(env.ctx, grabs.Grab{
		Mode: mode.Series, Title: airDateTitle, TMDBID: airDateTMDBID,
		SeasonNumber: 3, EpisodeNumber: 2, SeasonSpecified: true,
		RootFolderPath: "/series", Status: grabs.PendingRetry,
		RetryAfter: grabs.FormatTime(now.Add(24 * time.Hour)), RetryReason: noQualifyingCandidateReason,
	})
	if err != nil {
		t.Fatalf("creating the parked air-date row: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/modes/series/library/%d/seasons/3/monitored", series.ID),
		strings.NewReader(`{"monitored":false}`))
	req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
	req.SetPathValue("seasonNumber", "3")
	putSeasonMonitoredHandler(env.lib, env.grabs)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT monitored=false: status %d, body %q", rec.Code, rec.Body.String())
	}

	got, err := env.grabs.Get(env.ctx, parked.ID)
	if err != nil {
		t.Fatalf("reloading the parked row: %v", err)
	}
	if got.Status != grabs.Failed {
		t.Fatalf("the queued air-date retry is %q right after the toggle returned, want %q — cancellation must be IMMEDIATE, not eventual", got.Status, grabs.Failed)
	}
	if got.RetryReason != airDateUnmonitoredReason {
		t.Errorf("retry_reason = %q, want %q — without it an operator cannot tell this apart from a 451/DMCA failure", got.RetryReason, airDateUnmonitoredReason)
	}
	due, err := env.grabs.DueForRetry(env.ctx, now.Add(365*24*time.Hour))
	if err != nil {
		t.Fatalf("listing due grabs: %v", err)
	}
	for _, d := range due {
		if d.ID == parked.ID {
			t.Fatal("a cancelled air-date retry is still due for re-search")
		}
	}

	srv := httptest.NewServer(NewRequestsMux(env.grabs, env.lib, env.excludes))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/requests")
	if err != nil {
		t.Fatalf("GET /api/requests: %v", err)
	}
	defer resp.Body.Close()
	var requests apidto.RequestStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&requests); err != nil {
		t.Fatalf("decoding /api/requests: %v", err)
	}
	for _, item := range requests.Items {
		if item.GrabID == parked.ID {
			t.Errorf("a cancelled air-date retry is still on the worklist: %+v", item)
		}
	}

	survivor, err := env.grabs.Get(env.ctx, dispatched.ID)
	if err != nil {
		t.Fatalf("reloading the dispatched row: %v", err)
	}
	if survivor.Status != grabs.PendingRetry || survivor.RetryReason != articlesUnavailableReason {
		t.Fatalf("un-monitoring destroyed a genuinely DISPATCHED grab awaiting retry (%+v) — the narrow airDateOriginated predicate exists precisely to prevent this", survivor)
	}
}

// TestUnMonitoredSeasonIsReapedByTheSweep (T-C1.2) constructs the race the
// toggle handler cannot cover: the flag flipped without the cleanup running (a
// crash mid-request, or a cycle that overlapped the flip). The sweep is the
// backstop, and it must REAP the row rather than re-park it.
func TestUnMonitoredSeasonIsReapedByTheSweep(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{3: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -3)},
	}}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 3, 1, dayOffset(now, -3))
	// Flipped straight to false without going through the handler.
	env.monitor(t, series.ID, 3, false)

	parked, err := env.grabs.Create(env.ctx, grabs.Grab{
		Mode: mode.Series, Title: airDateTitle, TMDBID: airDateTMDBID,
		SeasonNumber: 3, EpisodeNumber: 1, SeasonSpecified: true,
		RootFolderPath: "/series", Status: grabs.PendingRetry,
		RetryAfter: grabs.FormatTime(now.Add(-time.Hour)), RetryReason: noQualifyingCandidateReason,
	})
	if err != nil {
		t.Fatalf("creating the due air-date row: %v", err)
	}

	env.runCycle(t, now)

	got, err := env.grabs.Get(env.ctx, parked.ID)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if got.Status != grabs.Failed {
		t.Fatalf("status = %q, want %q — an un-monitored season's retry row must be reaped, not re-parked", got.Status, grabs.Failed)
	}
	if got.RetryReason != airDateUnmonitoredReason {
		t.Errorf("retry_reason = %q, want %q", got.RetryReason, airDateUnmonitoredReason)
	}
	if n := len(env.dl.List()); n != 0 {
		t.Errorf("%d downloads were dispatched for an un-monitored season", n)
	}
	if list := env.seriesGrabs(t); len(list) != 1 {
		t.Errorf("a new grab was created for an un-monitored season's episode: %+v", list)
	}
}

// TestDispatchedThenReparkedAirDateRowTwoTierBound (T-C1.3, MJ-2) covers the row
// category a single shared origin predicate missed entirely, and asserts BOTH
// consumers behave per the split.
//
// Note carefully what is NOT asserted: zero searches on the reaping cycle.
// runUsenetRetryCycle runs retryDueGrabs BEFORE monitorAirDates, so the cycle in
// which the row finally comes due re-searches it once before the sweep reaps it.
// That single residual search is the design's accepted cost of keeping the
// destructive cleanup narrow; an assertion of zero would fail against a correct
// implementation and push an executor to widen the cleanup predicate and destroy
// operator grabs.
func TestDispatchedThenReparkedAirDateRowTwoTierBound(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{3: {
		{Number: 1, Name: "One", AirDate: dayOffset(base, -3)},
	}}, qualifyingSeriesRelease(3, 1))
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 3, 1, dayOffset(base, -3))
	env.monitor(t, series.ID, 3, true)

	reparked := dispatchedThenReparkedAirDateGrab(t, env, base)
	if !airDateShaped(reparked) {
		t.Fatalf("the wide predicate does not match a dispatched-then-reparked air-date row: %+v", reparked)
	}
	if airDateOriginated(reparked) {
		t.Fatalf("the NARROW predicate matches a dispatched row; its whole job is to exclude one: %+v", reparked)
	}

	// (a) The backoff sweep treats it as in scope. The reason is the
	// discriminator that survives at any retry_count: only the sweep writes
	// airDateRetryReason.
	//
	// The release stops qualifying first, so the re-search parks the row again
	// (the state the sweep acts on) instead of re-dispatching it.
	env.setProwlarrBody(noQualifyingRelease)
	parkedAt, err := time.Parse(sqliteTimeFormat, reparked.RetryAfter)
	if err != nil {
		t.Fatalf("parsing retry_after %q: %v", reparked.RetryAfter, err)
	}
	searched := parkedAt.Add(time.Minute)
	env.runCycle(t, searched)
	got, err := env.grabs.Get(env.ctx, reparked.ID)
	if err != nil {
		t.Fatalf("reloading after the searched cycle: %v", err)
	}
	if got.RetryReason != airDateRetryReason {
		t.Fatalf("retry_reason = %q, want %q — the backoff sweep must own this row, or it retries forever on the flat interval", got.RetryReason, airDateRetryReason)
	}
	if want := grabs.FormatTime(searched.Add(airDateRetryBackoff(got.RetryCount))); got.RetryAfter != want {
		t.Fatalf("retry_after = %q, want the BACKOFF %q rather than the flat interval", got.RetryAfter, want)
	}

	// (b) Un-monitoring leaves it alone in the handler, and the sweep reaps it on
	// the first cycle in which it comes due.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/modes/series/library/%d/seasons/3/monitored", series.ID),
		strings.NewReader(`{"monitored":false}`))
	req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
	req.SetPathValue("seasonNumber", "3")
	putSeasonMonitoredHandler(env.lib, env.grabs)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT monitored=false: status %d, body %q", rec.Code, rec.Body.String())
	}
	afterToggle, err := env.grabs.Get(env.ctx, reparked.ID)
	if err != nil {
		t.Fatalf("reloading after the toggle: %v", err)
	}
	if afterToggle.Status != grabs.PendingRetry {
		t.Fatalf("the toggle handler killed a DISPATCHED row (status %q); that is what the narrow predicate exists to prevent", afterToggle.Status)
	}

	due, err := time.Parse(sqliteTimeFormat, afterToggle.RetryAfter)
	if err != nil {
		t.Fatalf("parsing retry_after %q: %v", afterToggle.RetryAfter, err)
	}
	reaping := due.Add(time.Minute)
	env.runCycle(t, reaping)

	reaped, err := env.grabs.Get(env.ctx, reparked.ID)
	if err != nil {
		t.Fatalf("reloading after the reaping cycle: %v", err)
	}
	if reaped.Status != grabs.Failed {
		t.Fatalf("status = %q by the END of the reaping cycle, want %q", reaped.Status, grabs.Failed)
	}
	stillDue, err := env.grabs.DueForRetry(env.ctx, reaping.Add(365*24*time.Hour))
	if err != nil {
		t.Fatalf("listing due grabs: %v", err)
	}
	for _, d := range stillDue {
		if d.ID == reparked.ID {
			t.Fatal("the reaped row is still due for re-search")
		}
	}
	// A further cycle must neither search nor re-park it.
	env.runCycle(t, reaping.Add(48*time.Hour))
	after, err := env.grabs.Get(env.ctx, reparked.ID)
	if err != nil {
		t.Fatalf("reloading after a further cycle: %v", err)
	}
	if after.Status != grabs.Failed || after.RetryCount != reaped.RetryCount {
		t.Fatalf("a later cycle resurrected the reaped row: %+v", after)
	}
}

// TestDeletedSeriesAirDateRowsAreReaped (T-C1.4, MJ-3) covers the one explicit
// exception to this feature's soft-fail convention. ErrNotFound from the
// TMDBID->series lookup means REAP, not skip: the series row is gone, so
// MonitoredSeasons can never again report that season as monitored, and skipping
// would mean a deleted series' air-date rows are re-searched every cycle forever
// — the exact unbounded-volume failure the backoff exists to close, arriving
// through a different door. DeleteSeries does not touch grabs (nor should it), so
// nothing else would ever reap them.
func TestDeletedSeriesAirDateRowsAreReaped(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -3)},
	}}, noQualifyingRelease)
	series := env.trackSeries(t)
	parked, err := env.grabs.Create(env.ctx, grabs.Grab{
		Mode: mode.Series, Title: airDateTitle, TMDBID: airDateTMDBID,
		SeasonNumber: 1, EpisodeNumber: 1, SeasonSpecified: true,
		RootFolderPath: "/series", Status: grabs.PendingRetry,
		RetryAfter: grabs.FormatTime(now.Add(240 * time.Hour)), RetryReason: noQualifyingCandidateReason,
	})
	if err != nil {
		t.Fatalf("creating the air-date row: %v", err)
	}
	if err := env.lib.DeleteSeries(env.ctx, series.ID); err != nil {
		t.Fatalf("deleting the series: %v", err)
	}

	env.runCycle(t, now)

	got, err := env.grabs.Get(env.ctx, parked.ID)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if got.Status != grabs.Failed {
		t.Fatalf("status = %q, want %q — a deleted series' air-date retries must be reaped, not left pending_retry forever", got.Status, grabs.Failed)
	}
	// A real reap, not an accidental one-off.
	env.runCycle(t, now.Add(48*time.Hour))
	again, err := env.grabs.Get(env.ctx, parked.ID)
	if err != nil {
		t.Fatalf("reloading after a second cycle: %v", err)
	}
	if again.Status != grabs.Failed {
		t.Fatalf("the row came back to %q on a second cycle", again.Status)
	}

	t.Run("a genuine DB failure leaves the row untouched", func(t *testing.T) {
		// The exception is scoped to ErrNotFound alone; it must not have become a
		// blanket "any lookup error kills the row". A closed database makes
		// GetSeriesByTMDBID fail with a driver error rather than ErrNotFound.
		env2 := newAirDateEnv(t, map[int][]fakeTMDBEpisode{}, noQualifyingRelease)
		row, err := env2.grabs.Create(env2.ctx, grabs.Grab{
			Mode: mode.Series, Title: airDateTitle, TMDBID: airDateTMDBID,
			SeasonNumber: 1, EpisodeNumber: 1, SeasonSpecified: true,
			RootFolderPath: "/series", Status: grabs.PendingRetry,
			RetryAfter: grabs.FormatTime(now.Add(240 * time.Hour)), RetryReason: noQualifyingCandidateReason,
		})
		if err != nil {
			t.Fatalf("creating the air-date row: %v", err)
		}
		brokenDB := dbtest.New(t)
		brokenDB.Close()

		airDateBackoffSweep(env2.ctx, env2.deps, library.New(brokenDB), now)

		got, err := env2.grabs.Get(env2.ctx, row.ID)
		if err != nil {
			t.Fatalf("reloading: %v", err)
		}
		if got.Status != grabs.PendingRetry || got.RetryReason != noQualifyingCandidateReason {
			t.Fatalf("a transient DB failure was treated as 'not monitored' and killed the row: %+v", got)
		}
	})
}

// TestNeverMonitoredSeasonRowIsLeftAloneBySweep is the regression guard for the
// bug all three Phase-4 reviewers independently found: the sweep's reap branch
// used the WIDE airDateShaped predicate together with MonitoredSeasons, which
// only returns monitored = true rows. Since an ABSENT row means unmonitored, that
// pair cannot distinguish "explicitly un-monitored" from "never touched by this
// feature at all" — the state of EVERY season on EVERY install by default.
//
// The victim is not hypothetical and has nothing to do with this feature: an
// operator's own Discover one-click grab (TriggerOperator, ungated) for a Series
// episode that dispatches and then eats a retryable asynchronous failure is
// re-parked into a row carrying a real Indexer/DownloadURL, a real TMDB id,
// SeasonSpecified and Episode > 0 — airDateShaped, byte for byte. Pre-fix, the
// very next cycle killed it and labelled it "the season was un-monitored, so
// this retry was cancelled", even though the operator never monitored or
// un-monitored anything.
//
// Built through the production path (a real TriggerOperator dispatch, then the
// exact parkGrabForRetry call sweepUsenetFailures makes) rather than
// hand-written, so it cannot encode this test's own guess about the shape.
// env.monitor is deliberately NEVER called: no library_season_monitored row for
// this season must exist at any point, which is the whole fixture.
func TestNeverMonitoredSeasonRowIsLeftAloneBySweep(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{3: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -3)},
	}}, qualifyingSeriesRelease(3, 1))
	series := env.trackSeries(t)
	// The episode row is what supplies the per-episode runtime the bitrate
	// scorer needs; without it nothing qualifies and nothing dispatches.
	env.seedMissingEpisode(t, series.ID, 3, 1, dayOffset(now, -3))

	sess, err := env.build(env.ctx, mode.Series)
	if err != nil {
		t.Fatalf("building the series session: %v", err)
	}
	out, err := RunAutoGrab(env.ctx, env.deps, sess, AutoGrabRequest{
		Mode: mode.Series, Title: airDateTitle, TMDBID: airDateTMDBID, TVDBID: 4321,
		Season: 3, Episode: 1, SeasonSpecified: true, RootFolderPath: "/series",
		// The operator's own click. Ungated by construction, and nothing to do
		// with season monitoring.
		Trigger: TriggerOperator,
	})
	if err != nil {
		t.Fatalf("the operator's Discover grab failed: %v (%+v)", err, out)
	}
	list := env.seriesGrabs(t)
	if len(list) != 1 {
		t.Fatalf("expected exactly one dispatched operator grab, got %+v", list)
	}
	if list[0].Indexer == "" || list[0].DownloadURL == "" {
		t.Fatalf("the operator grab carries no indexer/download URL, so it cannot stand in for a reparked one: %+v", list[0])
	}
	// The asynchronous retrieval failure (a 430 from every subscription).
	if err := parkGrabForRetry(env.ctx, env.deps, list[0].ID, articlesUnavailableReason); err != nil {
		t.Fatalf("parking the operator grab: %v", err)
	}
	parked, err := env.grabs.Get(env.ctx, list[0].ID)
	if err != nil {
		t.Fatalf("reloading the parked operator grab: %v", err)
	}
	if !airDateShaped(*parked) {
		t.Fatalf("the fixture is not airDateShaped, so it cannot exercise the reap branch at all: %+v", parked)
	}

	// The release stops qualifying, so nothing can re-dispatch and change the row
	// out from under the assertions below.
	env.setProwlarrBody(noQualifyingRelease)
	env.runCycle(t, now)

	got, err := env.grabs.Get(env.ctx, parked.ID)
	if err != nil {
		t.Fatalf("reloading after the cycle: %v", err)
	}
	if got.Status != grabs.PendingRetry {
		t.Fatalf("status = %q, want %q — the sweep killed an operator's own Discover retry for a season nobody ever monitored or un-monitored", got.Status, grabs.PendingRetry)
	}
	// The reason and cadence assertions pin the OTHER half of the three-way
	// branch: a never-toggled season's row must be left COMPLETELY alone, not
	// re-parked by the backoff branch either. Status alone would pass against a
	// sweep that merely re-parked it.
	if got.RetryReason != articlesUnavailableReason {
		t.Errorf("retry_reason = %q, want %q — the sweep rewrote a row that is none of its business", got.RetryReason, articlesUnavailableReason)
	}
	if got.RetryAfter != parked.RetryAfter || got.RetryCount != parked.RetryCount {
		t.Errorf("the sweep changed the retry cadence of a never-monitored season's row: after=%q count=%d, want after=%q count=%d",
			got.RetryAfter, got.RetryCount, parked.RetryAfter, parked.RetryCount)
	}
}

// TestPutSeasonMonitoredRejectsANegativeSeason: Atoi accepts "-1", and season 0
// is real (Specials) so the bound cannot simply be > 0. An unbounded negative
// would write a junk library_season_monitored row no season UI can surface or
// clear.
func TestPutSeasonMonitoredRejectsANegativeSeason(t *testing.T) {
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{}, noQualifyingRelease)
	series := env.trackSeries(t)

	put := func(t *testing.T, season string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut,
			fmt.Sprintf("/api/modes/series/library/%d/seasons/%s/monitored", series.ID, season),
			strings.NewReader(`{"monitored":true}`))
		req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
		req.SetPathValue("seasonNumber", season)
		putSeasonMonitoredHandler(env.lib, env.grabs)(rec, req)
		return rec
	}

	if rec := put(t, "-1"); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT season -1: status %d, want %d", rec.Code, http.StatusBadRequest)
	}
	flags, err := env.lib.SeasonMonitorFlags(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("listing season monitor flags: %v", err)
	}
	if len(flags) != 0 {
		t.Errorf("a rejected season number still wrote a row: %v", flags)
	}

	// Season 0 (Specials) is real and must still be accepted.
	if rec := put(t, "0"); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT season 0: status %d, body %q — Specials must stay monitorable", rec.Code, rec.Body.String())
	}
}

// TestCatalogSyncSurvivesATVDetailsFailure: a transient /tv/{id} failure must
// skip the DISCOVERY half only. The known-season sync needs nothing from
// TVDetails, so abandoning the whole series on that error would freeze its
// episode catalog — and therefore MissingCount and the season UI — for a reason
// that has nothing to do with the known seasons.
func TestCatalogSyncSurvivesATVDetailsFailure(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		1: {{Number: 1, Name: "One", AirDate: dayOffset(now, -3)}, {Number: 2, Name: "Two", AirDate: dayOffset(now, -2)}},
		2: {{Number: 1, Name: "S2 One", AirDate: dayOffset(now, -1)}},
	}, noQualifyingRelease)
	series := env.trackSeries(t)
	// Season 1 is KNOWN (it has an episode row), so it syncs regardless. Season 2
	// is discoverable only via TVDetails.
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -3))
	env.setDiscovery(t, true)
	env.setTVDetailsFail(true)

	env.runCycle(t, now)

	// The known season still gained its second episode from SeasonDetails.
	ep := env.episodeRow(t, series.ID, 1, 2)
	if ep.AirDate != dayOffset(now, -2) {
		t.Errorf("s01e02 air date = %q, want %q", ep.AirDate, dayOffset(now, -2))
	}
	// The discovery half is the only casualty.
	states, err := env.lib.ListSeasonStates(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("listing season states: %v", err)
	}
	for _, st := range states {
		if st.SeasonNumber == 2 {
			t.Errorf("season 2 was discovered despite TVDetails failing: %+v", st)
		}
	}

	// And discovery recovers on its own next cycle, once TMDB does.
	env.setTVDetailsFail(false)
	env.runCycle(t, now)
	states, err = env.lib.ListSeasonStates(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("re-listing season states: %v", err)
	}
	found := false
	for _, st := range states {
		if st.SeasonNumber == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("season 2 was never discovered after TVDetails recovered: %+v", states)
	}
}

// --- Season routes and the un-filtered MissingCount -------------------------

// TestSeasonMonitoringRoutes covers the read and the all-seasons write,
// including the union ListSeasonStates reports: a season known only from a
// monitored row (discovered from TMDB before its episodes synced) must still
// appear, or an operator could never un-monitor it again.
func TestSeasonMonitoringRoutes(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedOnDiskEpisode(t, series.ID, 1, 1, dayOffset(now, -30), "/series/Some Show/S01E01.mkv")
	env.seedMissingEpisode(t, series.ID, 1, 2, dayOffset(now, -23))
	env.monitor(t, series.ID, 2, false) // an episode-less, discovered season

	list := func(t *testing.T) []apidto.SeasonState {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/modes/series/library/%d/seasons", series.ID), nil)
		req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
		listSeasonStatesHandler(env.catalog())(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET seasons: status %d, body %q", rec.Code, rec.Body.String())
		}
		var out []apidto.SeasonState
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decoding seasons: %v", err)
		}
		return out
	}

	states := list(t)
	if len(states) != 2 {
		t.Fatalf("expected seasons 1 and 2 (the union of episode rows and monitored rows), got %+v", states)
	}
	if states[0].SeasonNumber != 1 || states[0].EpisodeCount != 2 || states[0].MissingCount != 1 {
		t.Errorf("season 1 counts are wrong: %+v", states[0])
	}
	if states[1].SeasonNumber != 2 || states[1].EpisodeCount != 0 {
		t.Errorf("an episode-less monitored season must still be listed: %+v", states[1])
	}
	for _, st := range states {
		if st.Monitored {
			t.Errorf("season %d reads as monitored before anything set it", st.SeasonNumber)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/modes/series/library/%d/seasons/monitored", series.ID),
		strings.NewReader(`{"monitored":true}`))
	req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
	putAllSeasonsMonitoredHandler(env.catalog(), env.grabs)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT all seasons: status %d, body %q", rec.Code, rec.Body.String())
	}
	for _, st := range list(t) {
		if !st.Monitored {
			t.Errorf("season %d was not monitored by the all-seasons write", st.SeasonNumber)
		}
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/modes/series/library/999999/seasons", nil)
	req.SetPathValue("seriesID", "999999")
	listSeasonStatesHandler(env.catalog())(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("an unknown series id should list no seasons, got status %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/modes/series/library/999999/seasons/monitored", strings.NewReader(`{"monitored":true}`))
	req.SetPathValue("seriesID", "999999")
	putAllSeasonsMonitoredHandler(env.catalog(), env.grabs)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("writing seasons for an unknown series: status %d, want 404", rec.Code)
	}
}

// getSeasons drives the GET handler and decodes it — the merged list an
// operator's panel actually renders, as opposed to lib.ListSeasonStates' raw
// local rows.
func (e *airDateEnv) getSeasons(t *testing.T, seriesID int64) []apidto.SeasonState {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/modes/series/library/%d/seasons", seriesID), nil)
	req.SetPathValue("seriesID", strconv.FormatInt(seriesID, 10))
	listSeasonStatesHandler(e.catalog())(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET seasons: status %d, body %q", rec.Code, rec.Body.String())
	}
	var out []apidto.SeasonState
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decoding seasons: %v", err)
	}
	return out
}

func findSeason(states []apidto.SeasonState, season int) (apidto.SeasonState, bool) {
	for _, st := range states {
		if st.SeasonNumber == season {
			return st, true
		}
	}
	return apidto.SeasonState{}, false
}

// TestZeroRowSeasonAppearsFromTMDB is the whole point of the merge: a season
// TMDB knows about that has NO episode rows and NO monitored row cannot appear
// in ListSeasonStates' union at all, so before this the only way to reach it was
// the GLOBAL discovery toggle, which auto-monitors every such season across the
// entire library at once. There was no way to see and monitor one.
//
// It also pins the read-only half. A GET that quietly wrote a monitored row (or
// an episode row) would make the merge indistinguishable from discovery, so the
// assertions go to the stores directly rather than trusting the response.
func TestZeroRowSeasonAppearsFromTMDB(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		0: {{Number: 1, Name: "Special", AirDate: dayOffset(now, -400)}},
		1: {{Number: 1, Name: "One", AirDate: dayOffset(now, -30)}},
		4: {{Number: 1, Name: "S4 One", AirDate: dayOffset(now, -2)}},
	}, noQualifyingRelease)
	series := env.trackSeries(t)
	// Season 1 is the only one with any local row at all. Seasons 0 and 4 exist
	// solely in TMDB's seasons[].
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -30))

	states := env.getSeasons(t, series.ID)
	if len(states) != 3 {
		t.Fatalf("expected seasons 0, 1 and 4 after the merge, got %+v", states)
	}
	// Ascending, including season 0 — the UI renders this order verbatim.
	for i, want := range []int{0, 1, 4} {
		if states[i].SeasonNumber != want {
			t.Errorf("season at index %d = %d, want %d (merged list must stay ascending): %+v", i, states[i].SeasonNumber, want, states)
		}
	}
	s4, ok := findSeason(states, 4)
	if !ok {
		t.Fatalf("a TMDB-known season with zero local rows never appeared: %+v", states)
	}
	if s4.EpisodeCount != 0 || s4.MissingCount != 0 || s4.Monitored {
		t.Errorf("a merged season must be zero-valued and unmonitored, got %+v", s4)
	}
	// Season 0 (Specials) is included on purpose — syncSeriesCatalog's season-0
	// skip is about auto-monitoring, never display.
	if _, ok := findSeason(states, 0); !ok {
		t.Errorf("season 0 (Specials) must be visible when TMDB reports it: %+v", states)
	}
	// The one season that does have a row keeps its real counts.
	if s1, _ := findSeason(states, 1); s1.EpisodeCount != 1 || s1.MissingCount != 1 {
		t.Errorf("season 1's local counts were clobbered by the merge: %+v", s1)
	}

	// Nothing was written. All three reads must agree that the merge is display
	// state only.
	flags, err := env.lib.SeasonMonitorFlags(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("reading season monitor flags: %v", err)
	}
	if len(flags) != 0 {
		t.Errorf("the GET wrote library_season_monitored rows: %+v", flags)
	}
	episodes, err := env.lib.ListEpisodes(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("listing episodes: %v", err)
	}
	for _, ep := range episodes {
		if ep.SeasonNumber != 1 {
			t.Errorf("the GET created an episode row for season %d: %+v", ep.SeasonNumber, ep)
		}
	}
	local, err := env.lib.ListSeasonStates(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("listing local season states: %v", err)
	}
	if len(local) != 1 || local[0].SeasonNumber != 1 {
		t.Errorf("ListSeasonStates itself must be untouched by the merge, got %+v", local)
	}
}

// TestSeasonMergeDegradesWhenTMDBFails: a season the operator already has files
// or monitoring for must stay visible — and monitorable — when TMDB is
// unreachable. Erroring the whole request would take the working half down with
// the enrichment.
func TestSeasonMergeDegradesWhenTMDBFails(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		1: {{Number: 1, Name: "One", AirDate: dayOffset(now, -30)}},
		4: {{Number: 1, Name: "S4 One", AirDate: dayOffset(now, -2)}},
	}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -30))
	env.setTVDetailsFail(true)

	states := env.getSeasons(t, series.ID) // fatals on any non-200
	if len(states) != 1 || states[0].SeasonNumber != 1 {
		t.Fatalf("a TVDetails failure must degrade to the local seasons, got %+v", states)
	}
	if states[0].EpisodeCount != 1 || states[0].MissingCount != 1 {
		t.Errorf("the local season's counts are wrong on the degraded path: %+v", states[0])
	}

	// And it recovers on its own once TMDB does — no restart, no cached failure.
	env.setTVDetailsFail(false)
	if _, ok := findSeason(env.getSeasons(t, series.ID), 4); !ok {
		t.Errorf("season 4 never appeared after TMDB recovered")
	}
}

// TestSeasonMergeWithoutTMDBConfigured covers the other degradation door: an
// install with no TMDB connection at all. mode.Build succeeds and leaves
// sess.TMDB nil rather than erroring, so this is a distinct branch from the
// failure above, not a duplicate of it.
func TestSeasonMergeWithoutTMDBConfigured(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		1: {{Number: 1, Name: "One", AirDate: dayOffset(now, -30)}},
		4: {{Number: 1, Name: "S4 One", AirDate: dayOffset(now, -2)}},
	}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -30))
	if err := env.conns.Delete(env.ctx, "tmdb"); err != nil {
		t.Fatalf("removing the tmdb connection: %v", err)
	}

	states := env.getSeasons(t, series.ID)
	if len(states) != 1 || states[0].SeasonNumber != 1 {
		t.Fatalf("with no TMDB configured the local seasons are the whole answer, got %+v", states)
	}
}

// TestAllSeasonsMonitorsAMergedSeason is the bug the shared merge exists to
// prevent. If the all-seasons PUT read bare ListSeasonStates while the GET read
// the merged list, the "All seasons" switch would write every season EXCEPT the
// TMDB-only one, the re-read would still see it unmonitored, and the switch
// would flip straight back off — forever, however many times it is clicked.
func TestAllSeasonsMonitorsAMergedSeason(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		1: {{Number: 1, Name: "One", AirDate: dayOffset(now, -30)}},
		4: {{Number: 1, Name: "S4 One", AirDate: dayOffset(now, -2)}},
	}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -30))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/modes/series/library/%d/seasons/monitored", series.ID),
		strings.NewReader(`{"monitored":true}`))
	req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
	putAllSeasonsMonitoredHandler(env.catalog(), env.grabs)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT all seasons: status %d, body %q", rec.Code, rec.Body.String())
	}

	states := env.getSeasons(t, series.ID)
	if len(states) != 2 {
		t.Fatalf("expected seasons 1 and 4, got %+v", states)
	}
	for _, st := range states {
		if !st.Monitored {
			t.Errorf("season %d was skipped by the all-seasons write, so the switch would never latch: %+v", st.SeasonNumber, st)
		}
	}
}

// TestMonitoringAZeroRowSeasonSyncsItsEpisodes closes the loop the merge opened.
// Surfacing a TMDB-only season is only half a feature: an operator can now
// monitor a season nothing was ever downloaded from, and with the GLOBAL
// discovery toggle OFF (the default) that season would never enter syncSeasons,
// never gain episode rows, and therefore never be searched for — monitoring it
// would be a permanent no-op.
//
// Discovery stays off throughout on purpose. With it on, season 4 would sync via
// the discovery branch and this test would pass without proving anything.
func TestMonitoringAZeroRowSeasonSyncsItsEpisodes(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		1: {{Number: 1, Name: "One", AirDate: dayOffset(now, -30)}},
		4: {{Number: 1, Name: "S4 One", AirDate: dayOffset(now, -2)}},
	}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -30))

	// Exactly the path an operator takes: open the panel (the merge surfaces
	// season 4), then flip its switch.
	if _, ok := findSeason(env.getSeasons(t, series.ID), 4); !ok {
		t.Fatalf("season 4 was not surfaced by the merge, so this test proves nothing")
	}
	env.monitor(t, series.ID, 4, true)

	env.runCycle(t, now)

	ep := env.episodeRow(t, series.ID, 4, 1)
	if ep.AirDate != dayOffset(now, -2) {
		t.Errorf("s04e01 air date = %q, want %q", ep.AirDate, dayOffset(now, -2))
	}
	if ep.FilePath != "" {
		t.Errorf("s04e01 must be a fileless catalog row, got file_path %q", ep.FilePath)
	}
	// And the season's counts now come from real rows rather than the merge.
	s4, ok := findSeason(env.getSeasons(t, series.ID), 4)
	if !ok {
		t.Fatalf("season 4 vanished after its episodes synced")
	}
	if s4.EpisodeCount != 1 || s4.MissingCount != 1 || !s4.Monitored {
		t.Errorf("season 4 state after the sync = %+v, want 1 episode / 1 missing / monitored", s4)
	}
}

// TestUnMonitoredZeroRowSeasonIsNotSynced is the other side of the guard above:
// the sync seeds from MonitoredSeasons (monitored = true only), never
// SeasonMonitorFlags, so an explicitly un-monitored episode-less season must not
// burn a SeasonDetails call every cycle forever.
func TestUnMonitoredZeroRowSeasonIsNotSynced(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		1: {{Number: 1, Name: "One", AirDate: dayOffset(now, -30)}},
		4: {{Number: 1, Name: "S4 One", AirDate: dayOffset(now, -2)}},
	}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -30))
	env.monitor(t, series.ID, 4, false)

	env.runCycle(t, now)

	episodes, err := env.lib.ListEpisodes(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("listing episodes: %v", err)
	}
	for _, ep := range episodes {
		if ep.SeasonNumber == 4 {
			t.Errorf("an un-monitored episode-less season was synced anyway: %+v", ep)
		}
	}
}

// TestSeasonRoutesAreRegisteredOnTheRealMux drives all five routes through
// NewMux by literal URL. Every other handler test in this file calls the
// handler directly and supplies path values by hand, which cannot catch the
// failure this one exists for: a missing or misspelled registration compiles
// cleanly, passes go vet, and 404s (or 400s on an unresolved path value)
// silently at runtime.
//
// The all-seasons PUT is exercised alongside the per-season one on purpose:
// .../seasons/monitored and .../seasons/{seasonNumber}/monitored are sibling
// patterns, and only a real mux proves the shorter one is not shadowed.
func TestSeasonRoutesAreRegisteredOnTheRealMux(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 3, 1, dayOffset(now, -3))
	env.monitor(t, series.ID, 3, true)

	// The connection stores are real here, not the nils this test used to pass:
	// the season GET now builds a Series session to reach TMDB, and mode.Build
	// dereferences both stores. Passing them also makes this the one test that
	// drives the TMDB merge all the way through the production mux.
	srv := httptest.NewServer(NewMux(testHTTPClient(), env.conns, env.scConns, nil, nil, testPHasher(t), testVideoHasher(t),
		env.settings, env.grabs, env.lib, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	do := func(t *testing.T, method, path, body string) *http.Response {
		t.Helper()
		var reader *strings.Reader
		if body == "" {
			reader = strings.NewReader("")
		} else {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, srv.URL+path, reader)
		if err != nil {
			t.Fatalf("building %s %s: %v", method, path, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	base := fmt.Sprintf("/api/modes/series/library/%d/seasons", series.ID)

	resp := do(t, http.MethodGet, base, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d — the route is not registered", base, resp.StatusCode)
	}
	var states []apidto.SeasonState
	if err := json.NewDecoder(resp.Body).Decode(&states); err != nil {
		t.Fatalf("decoding seasons: %v", err)
	}
	resp.Body.Close()
	if len(states) != 1 || states[0].SeasonNumber != 3 || !states[0].Monitored {
		t.Fatalf("GET seasons returned %+v, want season 3 monitored", states)
	}

	resp = do(t, http.MethodPut, base+"/3/monitored", `{"monitored":false}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT %s/3/monitored: status %d", base, resp.StatusCode)
	}
	monitored, err := env.lib.MonitoredSeasons(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("reading monitored seasons: %v", err)
	}
	if monitored[3] {
		t.Fatal("the per-season route returned 204 without flipping season 3 — the {seasonNumber} path value did not resolve")
	}

	resp = do(t, http.MethodPut, base+"/monitored", `{"monitored":true}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT %s/monitored (all seasons): status %d — the shorter sibling pattern is shadowed or unregistered", base, resp.StatusCode)
	}
	monitored, err = env.lib.MonitoredSeasons(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("reading monitored seasons: %v", err)
	}
	if !monitored[3] {
		t.Fatal("the all-seasons route returned 204 without monitoring anything")
	}

	resp = do(t, http.MethodPut, "/api/settings/series-new-season-discovery", `{"enabled":true}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT the discovery toggle: status %d", resp.StatusCode)
	}
	resp = do(t, http.MethodGet, "/api/settings/series-new-season-discovery", "")
	var discovery apidto.SeriesNewSeasonDiscoveryResponse
	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		t.Fatalf("decoding the discovery toggle: %v", err)
	}
	resp.Body.Close()
	if !discovery.Enabled {
		t.Error("the discovery toggle did not round-trip through the real mux")
	}
}

// TestSeriesNewSeasonDiscoveryToggle: off by default, round-trips, and —
// deliberately unlike the usenet auto-grab toggle — writes no coupled interval,
// because this feature adds no scheduler of its own.
func TestSeriesNewSeasonDiscoveryToggle(t *testing.T) {
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{}, noQualifyingRelease)

	get := func(t *testing.T) apidto.SeriesNewSeasonDiscoveryResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		getSeriesNewSeasonDiscoveryHandler(env.settings)(rec, httptest.NewRequest(http.MethodGet, "/api/settings/series-new-season-discovery", nil))
		var out apidto.SeriesNewSeasonDiscoveryResponse
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		return out
	}

	if get(t).Enabled {
		t.Fatal("new-season discovery defaults to ON — it must be off until an operator opts in")
	}
	before := LoadUsenetRetryInterval(env.ctx, env.settings)

	rec := httptest.NewRecorder()
	putSeriesNewSeasonDiscoveryHandler(env.settings)(rec, httptest.NewRequest(http.MethodPut, "/api/settings/series-new-season-discovery", strings.NewReader(`{"enabled":true}`)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT: status %d, body %q", rec.Code, rec.Body.String())
	}
	if !get(t).Enabled {
		t.Error("the toggle did not persist")
	}
	if after := LoadUsenetRetryInterval(env.ctx, env.settings); after != before {
		t.Errorf("the discovery toggle changed the retry interval (%s -> %s); it must have no scheduler side effect", before, after)
	}
}

// TestRequestsMissingCountStaysUnfiltered (T-5.1) is AC9. requests.go is
// byte-for-byte unchanged, and MissingCount must keep counting the UNFILTERED
// MissingEpisodes result — unmonitored seasons and future-dated episodes
// included. If the air-date/monitored filter ever leaked into that computation,
// the Requests screen would start under-reporting what is actually missing.
func TestRequestsMissingCountStaysUnfiltered(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -30)) // monitored, aired
	env.seedMissingEpisode(t, series.ID, 1, 2, dayOffset(now, 30))  // monitored, future
	env.seedMissingEpisode(t, series.ID, 2, 1, dayOffset(now, -30)) // UNMONITORED, aired
	env.seedMissingEpisode(t, series.ID, 2, 2, "")                  // UNMONITORED, unannounced
	env.monitor(t, series.ID, 1, true)

	missing, err := env.lib.MissingEpisodes(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("listing missing episodes: %v", err)
	}
	if len(missing) != 4 {
		t.Fatalf("fixture is wrong: %d missing episodes, want 4", len(missing))
	}

	srv := httptest.NewServer(NewRequestsMux(env.grabs, env.lib, env.excludes))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/requests")
	if err != nil {
		t.Fatalf("GET /api/requests: %v", err)
	}
	defer resp.Body.Close()
	var out apidto.RequestStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	for _, item := range out.Items {
		if item.TMDBID != airDateTMDBID {
			continue
		}
		if item.MissingCount != len(missing) {
			t.Fatalf("MissingCount = %d, want %d (the UNFILTERED MissingEpisodes count)", item.MissingCount, len(missing))
		}
		return
	}
	t.Fatal("the tracked series did not appear on the Requests worklist at all")
}
