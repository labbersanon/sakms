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

	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
)

func TestPutTMDBListIngest_DefaultOffRoundTripAndCancel(t *testing.T) {
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tmdb/list-ingest", getTMDBListIngestHandler(settingsStore))
	mux.HandleFunc("PUT /api/tmdb/list-ingest", putTMDBListIngestHandler(settingsStore, grabsStore))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/tmdb/list-ingest")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var got tmdbListIngestResponse
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
	if err := grabsStore.SetOrigin(context.Background(), parked.ID, grabOriginTMDBList); err != nil {
		t.Fatalf("origin: %v", err)
	}
	if err := grabsStore.UpdateStatus(context.Background(), parked.ID, grabs.PendingRetry); err != nil {
		t.Fatalf("status: %v", err)
	}

	body, _ := json.Marshal(tmdbListIngestRequest{Enabled: true, ListIDs: "123"})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/tmdb/list-ingest", bytes.NewReader(body))
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

	off, _ := json.Marshal(tmdbListIngestRequest{Enabled: false, ListIDs: "123"})
	req, err = http.NewRequest(http.MethodPut, srv.URL+"/api/tmdb/list-ingest", bytes.NewReader(off))
	if err != nil {
		t.Fatalf("request off: %v", err)
	}
	putResp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT off: %v", err)
	}
	io.Copy(io.Discard, putResp.Body)
	putResp.Body.Close()

	gotParked, err := grabsStore.Get(context.Background(), parked.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if gotParked.Status != grabs.Failed {
		t.Fatalf("turning ingest off must cancel never-dispatched parks, status=%q", gotParked.Status)
	}
}

func TestIngestTMDBLists_OffDoesNotFetch(t *testing.T) {
	env := newTMDBListIngestEnv(t, "2020-01-01T00:00:00.000Z", false)
	if err := env.settings.SetBool(context.Background(), tmdbListIngestEnabledKey, false); err != nil {
		t.Fatalf("ingest off: %v", err)
	}
	ingestTMDBLists(context.Background(), env.deps, env.build, env.lib, nil, nil)
	if n := atomic.LoadInt32(env.listCount); n != 0 {
		t.Fatalf("list fetches = %d, want 0 when ingest is off", n)
	}
}

func TestIngestTMDBLists_ReleasedMovieParksRequest(t *testing.T) {
	env := newTMDBListIngestEnv(t, "2020-01-01T00:00:00.000Z", false)
	ingestTMDBLists(context.Background(), env.deps, env.build, env.lib, nil, nil)
	if n := atomic.LoadInt32(env.listCount); n == 0 {
		t.Fatal("expected a live TMDB list fetch")
	}
	if n := atomic.LoadInt32(env.prowlarrCount); n == 0 {
		t.Fatal("expected a Prowlarr search for a released list movie")
	}
	list, err := env.grabs.List(context.Background(), mode.Movies)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("expected a movie grab from ingest")
	}
	if list[0].Origin != grabOriginTMDBList {
		t.Fatalf("origin = %q, want %q", list[0].Origin, grabOriginTMDBList)
	}
}

func TestIngestTMDBLists_UnreleasedMovieHolds(t *testing.T) {
	env := newTMDBListIngestEnv(t, "2099-06-01T00:00:00.000Z", false)
	ingestTMDBLists(context.Background(), env.deps, env.build, env.lib, nil, nil)
	if n := atomic.LoadInt32(env.prowlarrCount); n != 0 {
		t.Fatalf("Prowlarr calls = %d, want 0 for an unreleased movie", n)
	}
	list, err := env.grabs.List(context.Background(), mode.Movies)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].HoldUntil == "" || list[0].Origin != grabOriginTMDBList {
		t.Fatalf("held grab = %+v", list)
	}
}

func TestIngestTMDBLists_AccountWatchlistAndSkipOwned(t *testing.T) {
	env := newTMDBListIngestEnv(t, "2020-01-01T00:00:00.000Z", true)
	ctx := context.Background()
	if err := env.settings.Set(ctx, tmdbListIDsKey, ""); err != nil {
		t.Fatalf("clear lists: %v", err)
	}
	ingestTMDBLists(ctx, env.deps, env.build, env.lib, nil, nil)
	if n := atomic.LoadInt32(env.accountCount); n == 0 {
		t.Fatal("expected account watchlist fetch")
	}
	if n := atomic.LoadInt32(env.prowlarrCount); n == 0 {
		t.Fatal("expected a Prowlarr search from the account watchlist")
	}

	env2 := newTMDBListIngestEnv(t, "2020-01-01T00:00:00.000Z", false)
	if _, err := env2.lib.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 100, Title: "Some Movie",
		FilePath: "/movies/Some.Movie.mkv", RootFolderPath: "/movies",
	}); err != nil {
		t.Fatalf("upsert owned: %v", err)
	}
	ingestTMDBLists(ctx, env2.deps, env2.build, env2.lib, nil, nil)
	if n := atomic.LoadInt32(env2.prowlarrCount); n != 0 {
		t.Fatalf("Prowlarr calls = %d, want 0 for an owned movie", n)
	}

	env3 := newTMDBListIngestEnv(t, "2020-01-01T00:00:00.000Z", false)
	excluded := map[string]bool{excludes.Key(string(mode.Movies), 100, "Some Movie"): true}
	ingestTMDBLists(ctx, env3.deps, env3.build, env3.lib, excluded, nil)
	if n := atomic.LoadInt32(env3.prowlarrCount); n != 0 {
		t.Fatalf("Prowlarr calls = %d, want 0 for an excluded movie", n)
	}
}

func TestIngestTMDBLists_AddsNewSeriesAndSkipsExisting(t *testing.T) {
	env := newTMDBListIngestEnv(t, "2020-01-01T00:00:00.000Z", false)
	ctx := context.Background()
	ingestTMDBLists(ctx, env.deps, env.build, env.lib, nil, nil)

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
	if err := env.lib.SetSeasonMonitored(ctx, series.ID, states[0].SeasonNumber, false); err != nil {
		t.Fatalf("unmonitor: %v", err)
	}
	ingestTMDBLists(ctx, env.deps, env.build, env.lib, nil, nil)
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

type tmdbListIngestEnv struct {
	deps          AutoGrabDeps
	build         sessionBuilderFunc
	lib           *library.Store
	grabs         *grabs.Store
	settings      *settings.Store
	listCount     *int32
	accountCount  *int32
	prowlarrCount *int32
}

func newTMDBListIngestEnv(t *testing.T, movieReleaseDate string, withSession bool) tmdbListIngestEnv {
	t.Helper()
	ctx := context.Background()
	connStore, _, settingsStore, grabsStore, libStore, _, _, _, _, _, scStore := testStoresWithRegistry(t)

	var listHits, accountHits int32
	tmdbSrv := fakeListIngestTMDB(t, movieReleaseDate, &listHits, &accountHits)
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
	if err := settingsStore.SetBool(ctx, tmdbListIngestEnabledKey, true); err != nil {
		t.Fatalf("ingest on: %v", err)
	}
	if err := settingsStore.Set(ctx, tmdbListIDsKey, "123"); err != nil {
		t.Fatalf("list ids: %v", err)
	}
	if withSession {
		if err := settingsStore.Set(ctx, mode.TMDBSessionIDKey, "sess"); err != nil {
			t.Fatalf("session: %v", err)
		}
	}

	dl := newTestDownloader("gid-tmdb-ingest", t.TempDir())
	build := func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
		return mode.Build(ctx, connStore, scStore, settingsStore, testHTTPClient(), dl, m)
	}
	return tmdbListIngestEnv{
		deps: AutoGrabDeps{
			SettingsStore: settingsStore,
			GrabsStore:    grabsStore,
			LibStore:      libStore,
			TraktIngest: &traktWatchlistIngest{
				HTTPClient: testHTTPClient(),
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
		listCount:     &listHits,
		accountCount:  &accountHits,
		prowlarrCount: prowlarrCount,
	}
}

func fakeListIngestTMDB(t *testing.T, movieReleaseDate string, listHits, accountHits *int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/account", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(accountHits, 1)
		json.NewEncoder(w).Encode(map[string]any{"id": 9})
	})
	mux.HandleFunc("/account/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(accountHits, 1)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/watchlist/tv") {
			json.NewEncoder(w).Encode(map[string]any{
				"page": 1, "total_pages": 1,
				"results": []map[string]any{{"id": 200, "name": "Some Show", "first_air_date": "2021-01-01"}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"page": 1, "total_pages": 1,
			"results": []map[string]any{{"id": 100, "title": "Some Movie", "release_date": "2020-01-01"}},
		})
	})
	mux.HandleFunc("/list/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(listHits, 1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": 100, "title": "Some Movie", "media_type": "movie"},
				{"id": 200, "name": "Some Show", "media_type": "tv"},
			},
		})
	})
	mux.HandleFunc("/movie/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/release_dates") {
			json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{"iso_3166_1": "US", "release_dates": []map[string]any{
						{"type": 4, "release_date": movieReleaseDate},
					}},
				},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 100, "title": "Some Movie", "runtime": 100})
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
