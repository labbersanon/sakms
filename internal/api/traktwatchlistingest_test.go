package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
)

const ingestWatchlistJSON = `[
  {"type":"movie","movie":{"title":"Some Movie","year":2023,"ids":{"tmdb":100}}},
  {"type":"show","show":{"title":"Some Show","year":2021,"ids":{"tmdb":200}}}
]`

func TestTraktWatchlistOriginated(t *testing.T) {
	base := grabs.Grab{
		Mode: mode.Movies, Origin: grabOriginTraktWatchlist, Status: grabs.PendingRetry,
		TMDBID: 100,
	}
	if !traktWatchlistOriginated(base) {
		t.Fatal("expected originated for a never-dispatched ingest grab")
	}
	dispatched := base
	dispatched.Indexer = "I"
	dispatched.DownloadURL = "magnet:?xt=urn:btih:abc"
	if traktWatchlistOriginated(dispatched) {
		t.Fatal("dispatched grab must not be originated")
	}
}

func TestCancelTraktWatchlistRetries_CancelsNeverDispatchedOnly(t *testing.T) {
	_, _, _, grabsStore, _, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	parked, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 100, RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("create parked: %v", err)
	}
	if err := grabsStore.SetOrigin(ctx, parked.ID, grabOriginTraktWatchlist); err != nil {
		t.Fatalf("origin: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, parked.ID, grabs.PendingRetry); err != nil {
		t.Fatalf("status: %v", err)
	}

	live, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 100,
		Indexer: "I", DownloadURL: "magnet:?xt=urn:btih:abc", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("create live: %v", err)
	}
	if err := grabsStore.SetOrigin(ctx, live.ID, grabOriginTraktWatchlist); err != nil {
		t.Fatalf("origin live: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, live.ID, grabs.PendingRetry); err != nil {
		t.Fatalf("status live: %v", err)
	}

	cancelTraktWatchlistRetries(ctx, grabsStore)

	gotParked, err := grabsStore.Get(ctx, parked.ID)
	if err != nil {
		t.Fatalf("get parked: %v", err)
	}
	if gotParked.Status != grabs.Failed {
		t.Fatalf("parked status = %q, want failed", gotParked.Status)
	}

	gotLive, err := grabsStore.Get(ctx, live.ID)
	if err != nil {
		t.Fatalf("get live: %v", err)
	}
	if gotLive.Status != grabs.PendingRetry {
		t.Fatalf("dispatched row was cancelled; status=%q", gotLive.Status)
	}
}

func TestPutTraktWatchlistIngest_DefaultOffRoundTripAndCancel(t *testing.T) {
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/trakt/watchlist-ingest", getTraktWatchlistIngestHandler(settingsStore))
	mux.HandleFunc("PUT /api/trakt/watchlist-ingest", putTraktWatchlistIngestHandler(settingsStore, grabsStore))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/trakt/watchlist-ingest")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var got traktWatchlistIngestResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Enabled {
		t.Fatal("ingest must default off")
	}

	parked, err := grabsStore.Create(context.Background(), grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 100, RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := grabsStore.SetOrigin(context.Background(), parked.ID, grabOriginTraktWatchlist); err != nil {
		t.Fatalf("origin: %v", err)
	}
	if err := grabsStore.UpdateStatus(context.Background(), parked.ID, grabs.PendingRetry); err != nil {
		t.Fatalf("status: %v", err)
	}

	body, _ := json.Marshal(traktWatchlistIngestRequest{Enabled: true})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/trakt/watchlist-ingest", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT on: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT on = %d, want 204", putResp.StatusCode)
	}

	off, _ := json.Marshal(traktWatchlistIngestRequest{Enabled: false})
	req, err = http.NewRequest(http.MethodPut, srv.URL+"/api/trakt/watchlist-ingest", bytes.NewReader(off))
	if err != nil {
		t.Fatalf("request off: %v", err)
	}
	putResp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT off: %v", err)
	}
	io.Copy(io.Discard, putResp.Body)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT off = %d, want 204", putResp.StatusCode)
	}

	gotParked, err := grabsStore.Get(context.Background(), parked.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if gotParked.Status != grabs.Failed {
		t.Fatalf("turning ingest off must cancel never-dispatched parks, status=%q", gotParked.Status)
	}
}

func TestMonitorTraktWatchlist_NilDepsSkips(t *testing.T) {
	monitorTraktWatchlist(context.Background(), AutoGrabDeps{}, nil, nil, nil)
}

func TestMonitorTraktWatchlist_OffDoesNotFetch(t *testing.T) {
	env := newTraktWatchlistIngestEnv(t, ingestWatchlistJSON, "2020-01-01T00:00:00.000Z")
	if err := env.settings.SetBool(context.Background(), traktWatchlistIngestEnabledKey, false); err != nil {
		t.Fatalf("ingest off: %v", err)
	}
	monitorTraktWatchlist(context.Background(), env.deps, env.build, env.lib, nil)
	if n := atomic.LoadInt32(env.traktCount); n != 0 {
		t.Fatalf("Trakt calls = %d, want 0 when ingest is off", n)
	}
}

func TestMonitorTraktWatchlist_AutoGrabOffDoesNotFetch(t *testing.T) {
	env := newTraktWatchlistIngestEnv(t, ingestWatchlistJSON, "2020-01-01T00:00:00.000Z")
	setAutoGrabToggle(t, env.settings, false)
	monitorTraktWatchlist(context.Background(), env.deps, env.build, env.lib, nil)
	if n := atomic.LoadInt32(env.traktCount); n != 0 {
		t.Fatalf("Trakt calls = %d, want 0 when auto-grab is off", n)
	}
}

func TestMonitorTraktWatchlist_ReleasedMovieParksRequest(t *testing.T) {
	env := newTraktWatchlistIngestEnv(t, ingestWatchlistJSON, "2020-01-01T00:00:00.000Z")
	monitorTraktWatchlist(context.Background(), env.deps, env.build, env.lib, nil)
	if n := atomic.LoadInt32(env.traktCount); n == 0 {
		t.Fatal("expected a live Trakt watchlist fetch")
	}
	if n := atomic.LoadInt32(env.prowlarrCount); n == 0 {
		t.Fatal("expected a Prowlarr search for a released watchlist movie")
	}
	list, err := env.grabs.List(context.Background(), mode.Movies)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("expected a movie grab from ingest")
	}
	if list[0].Origin != grabOriginTraktWatchlist {
		t.Fatalf("origin = %q, want %q", list[0].Origin, grabOriginTraktWatchlist)
	}
	if list[0].TMDBID != 100 {
		t.Fatalf("tmdb = %d, want 100", list[0].TMDBID)
	}
}

func TestMonitorTraktWatchlist_UnreleasedMovieHolds(t *testing.T) {
	env := newTraktWatchlistIngestEnv(t, ingestWatchlistJSON, "2099-06-01T00:00:00.000Z")
	monitorTraktWatchlist(context.Background(), env.deps, env.build, env.lib, nil)
	if n := atomic.LoadInt32(env.prowlarrCount); n != 0 {
		t.Fatalf("Prowlarr calls = %d, want 0 for an unreleased movie", n)
	}
	list, err := env.grabs.List(context.Background(), mode.Movies)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("grabs = %d, want 1 held row", len(list))
	}
	if list[0].HoldUntil == "" {
		t.Fatal("expected hold_until on the unreleased ingest row")
	}
	if list[0].Origin != grabOriginTraktWatchlist {
		t.Fatalf("origin = %q, want %q", list[0].Origin, grabOriginTraktWatchlist)
	}
	if list[0].RetryAfter != "" {
		t.Fatalf("held row must have empty retry_after, got %q", list[0].RetryAfter)
	}
}

func TestMonitorTraktWatchlist_SkipsOwnedAndExcludedAndActive(t *testing.T) {
	env := newTraktWatchlistIngestEnv(t, ingestWatchlistJSON, "2020-01-01T00:00:00.000Z")
	ctx := context.Background()
	if _, err := env.lib.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 100, Title: "Some Movie",
		FilePath: "/movies/Some.Movie.mkv", RootFolderPath: "/movies",
	}); err != nil {
		t.Fatalf("upsert owned: %v", err)
	}
	monitorTraktWatchlist(ctx, env.deps, env.build, env.lib, nil)
	if n := atomic.LoadInt32(env.prowlarrCount); n != 0 {
		t.Fatalf("Prowlarr calls = %d, want 0 for an owned movie", n)
	}

	env2 := newTraktWatchlistIngestEnv(t, ingestWatchlistJSON, "2020-01-01T00:00:00.000Z")
	excluded := map[string]bool{excludes.Key(string(mode.Movies), 100, "Some Movie"): true}
	monitorTraktWatchlist(ctx, env2.deps, env2.build, env2.lib, excluded)
	if n := atomic.LoadInt32(env2.prowlarrCount); n != 0 {
		t.Fatalf("Prowlarr calls = %d, want 0 for an excluded movie", n)
	}

	env3 := newTraktWatchlistIngestEnv(t, ingestWatchlistJSON, "2020-01-01T00:00:00.000Z")
	if _, err := env3.grabs.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 100,
		Status: grabs.PendingRetry, RootFolderPath: "/movies",
	}); err != nil {
		t.Fatalf("create active: %v", err)
	}
	monitorTraktWatchlist(ctx, env3.deps, env3.build, env3.lib, nil)
	if n := atomic.LoadInt32(env3.prowlarrCount); n != 0 {
		t.Fatalf("Prowlarr calls = %d, want 0 while a grab is active", n)
	}
}

func TestMonitorTraktWatchlist_AddsNewSeriesAndSkipsExisting(t *testing.T) {
	env := newTraktWatchlistIngestEnv(t, ingestWatchlistJSON, "2020-01-01T00:00:00.000Z")
	ctx := context.Background()
	monitorTraktWatchlist(ctx, env.deps, env.build, env.lib, nil)

	series, err := env.lib.GetSeriesByTMDBID(ctx, 200)
	if err != nil {
		t.Fatalf("expected ingest to add the show: %v", err)
	}
	states, err := env.lib.ListSeasonStates(ctx, series.ID)
	if err != nil {
		t.Fatalf("seasons: %v", err)
	}
	if len(states) == 0 {
		t.Fatal("expected monitored seasons on the new show")
	}
	for _, st := range states {
		if !st.Monitored {
			t.Fatalf("season %d not monitored after ingest", st.SeasonNumber)
		}
	}

	if err := env.lib.SetSeasonMonitored(ctx, series.ID, states[0].SeasonNumber, false); err != nil {
		t.Fatalf("unmonitor: %v", err)
	}
	monitorTraktWatchlist(ctx, env.deps, env.build, env.lib, nil)
	after, err := env.lib.ListSeasonStates(ctx, series.ID)
	if err != nil {
		t.Fatalf("seasons after: %v", err)
	}
	for _, st := range after {
		if st.SeasonNumber == states[0].SeasonNumber && st.Monitored {
			t.Fatal("ingest must not re-monitor a show already in the library")
		}
	}
}

type traktWatchlistIngestEnv struct {
	deps     AutoGrabDeps
	build    sessionBuilderFunc
	lib      *library.Store
	grabs         *grabs.Store
	settings      *settings.Store
	traktCount    *int32
	prowlarrCount *int32
}

func newTraktWatchlistIngestEnv(t *testing.T, watchlistJSON, movieReleaseDate string) traktWatchlistIngestEnv {
	t.Helper()
	ctx := context.Background()
	connStore, _, settingsStore, grabsStore, libStore, _, traktStore, _, _, _, scStore := testStoresWithRegistry(t)

	var traktHits int32
	traktSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sync/watchlist" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&traktHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(watchlistJSON))
	}))
	t.Cleanup(traktSrv.Close)

	tmdbSrv := fakeIngestTMDB(t, movieReleaseDate)
	prowlarrSrv, prowlarrCount := fakeProwlarrCounting(t, `[]`)
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	for _, c := range []struct{ service, url string }{{"tmdb", tmdbSrv.URL}, {"prowlarr", prowlarrSrv.URL}} {
		if err := connStore.Upsert(ctx, c.service, c.url, "key"); err != nil {
			t.Fatalf("upserting %s: %v", c.service, err)
		}
	}
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, "/movies"); err != nil {
		t.Fatalf("movies root: %v", err)
	}
	if err := settingsStore.Set(ctx, seriesLibraryRootFolderKey, "/series"); err != nil {
		t.Fatalf("series root: %v", err)
	}
	setAutoGrabToggle(t, settingsStore, true)
	if err := settingsStore.SetBool(ctx, traktWatchlistIngestEnabledKey, true); err != nil {
		t.Fatalf("ingest on: %v", err)
	}
	secret := "secret"
	if err := traktStore.SaveCredentials(ctx, "client-id", &secret); err != nil {
		t.Fatalf("trakt credentials: %v", err)
	}
	if err := traktStore.SaveTokens(ctx, "access-token", "refresh-token", time.Now().Add(24*time.Hour)); err != nil {
		t.Fatalf("trakt tokens: %v", err)
	}

	dl := newTestDownloader("gid-ingest", t.TempDir())
	build := func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
		return mode.Build(ctx, connStore, scStore, settingsStore, testHTTPClient(), dl, m)
	}
	return traktWatchlistIngestEnv{
		deps: AutoGrabDeps{
			SettingsStore: settingsStore,
			GrabsStore:    grabsStore,
			LibStore:      libStore,
			TraktIngest: &traktWatchlistIngest{
				Store:      traktStore,
				HTTPClient: testHTTPClient(),
				BaseURL:    traktSrv.URL,
				Catalog: seasonCatalog{
					httpClient: testHTTPClient(), connStore: connStore, scStore: scStore,
					settings: settingsStore, lib: libStore,
				},
			},
		},
		build:         build,
		lib:           libStore,
		grabs:         grabsStore,
		settings:      settingsStore,
		traktCount:    &traktHits,
		prowlarrCount: prowlarrCount,
	}
}

func fakeIngestTMDB(t *testing.T, movieReleaseDate string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/movie/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/release_dates") {
			json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{
						"iso_3166_1": "US",
						"release_dates": []map[string]any{
							{"type": 4, "release_date": movieReleaseDate},
						},
					},
				},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": 100, "title": "Some Movie", "runtime": 100,
		})
	})
	mux.HandleFunc("/tv/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		if strings.Contains(path, "/external_ids") {
			json.NewEncoder(w).Encode(map[string]any{"tvdb_id": 4321})
			return
		}
		if strings.Contains(path, "/season/") {
			json.NewEncoder(w).Encode(map[string]any{
				"episodes": []map[string]any{
					{"episode_number": 1, "name": "Pilot", "air_date": "2020-01-01", "runtime": 45},
				},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": 200, "name": "Some Show",
			"seasons": []map[string]any{
				{"season_number": 1, "episode_count": 1},
				{"season_number": 2, "episode_count": 1},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
