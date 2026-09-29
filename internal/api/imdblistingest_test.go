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

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
)

const imdbListRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
  <item>
    <title>Some Movie</title>
    <link>https://www.imdb.com/title/tt0111161/</link>
    <guid>https://www.imdb.com/title/tt0111161/</guid>
  </item>
  <item>
    <title>Some Show</title>
    <link>https://www.imdb.com/title/tt0944947/</link>
  </item>
</channel></rss>`

func TestPutIMDbListIngest_DefaultOffRoundTripAndCancel(t *testing.T) {
	_, _, settingsStore, grabsStore, _, _, _, _, _, _ := testStores(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/imdb/list-ingest", getIMDbListIngestHandler(settingsStore))
	mux.HandleFunc("PUT /api/imdb/list-ingest", putIMDbListIngestHandler(settingsStore, grabsStore))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/imdb/list-ingest")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var got imdbListIngestResponse
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
	if err := grabsStore.SetOrigin(context.Background(), parked.ID, grabOriginIMDbList); err != nil {
		t.Fatalf("origin: %v", err)
	}
	if err := grabsStore.UpdateStatus(context.Background(), parked.ID, grabs.PendingRetry); err != nil {
		t.Fatalf("status: %v", err)
	}

	body, _ := json.Marshal(imdbListIngestRequest{Enabled: true, ListIDs: "ls123456789"})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/imdb/list-ingest", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT on: %v", err)
	}
	putResp.Body.Close()

	off, _ := json.Marshal(imdbListIngestRequest{Enabled: false, ListIDs: "ls123456789"})
	req, err = http.NewRequest(http.MethodPut, srv.URL+"/api/imdb/list-ingest", bytes.NewReader(off))
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

func TestIngestIMDbLists_OffDoesNotFetch(t *testing.T) {
	env := newIMDbListIngestEnv(t, "2020-01-01T00:00:00.000Z")
	if err := env.settings.SetBool(context.Background(), imdbListIngestEnabledKey, false); err != nil {
		t.Fatalf("ingest off: %v", err)
	}
	ingestIMDbLists(context.Background(), env.deps, env.build, env.lib, nil, nil)
	if n := atomic.LoadInt32(env.rssCount); n != 0 {
		t.Fatalf("RSS fetches = %d, want 0 when ingest is off", n)
	}
}

func TestIngestIMDbLists_ReleasedMovieParksRequest(t *testing.T) {
	env := newIMDbListIngestEnv(t, "2020-01-01T00:00:00.000Z")
	ingestIMDbLists(context.Background(), env.deps, env.build, env.lib, nil, nil)
	if n := atomic.LoadInt32(env.rssCount); n == 0 {
		t.Fatal("expected a live IMDb RSS fetch")
	}
	if n := atomic.LoadInt32(env.findCount); n == 0 {
		t.Fatal("expected TMDB /find for IMDb ids")
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
	if list[0].Origin != grabOriginIMDbList {
		t.Fatalf("origin = %q, want %q", list[0].Origin, grabOriginIMDbList)
	}
}

func TestIngestIMDbLists_AddsNewSeriesAndSkipsExisting(t *testing.T) {
	env := newIMDbListIngestEnv(t, "2020-01-01T00:00:00.000Z")
	ctx := context.Background()
	ingestIMDbLists(ctx, env.deps, env.build, env.lib, nil, nil)

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
	ingestIMDbLists(ctx, env.deps, env.build, env.lib, nil, nil)
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

type imdbListIngestEnv struct {
	deps          AutoGrabDeps
	build         sessionBuilderFunc
	lib           *library.Store
	grabs         *grabs.Store
	settings      *settings.Store
	rssCount      *int32
	findCount     *int32
	prowlarrCount *int32
}

func newIMDbListIngestEnv(t *testing.T, movieReleaseDate string) imdbListIngestEnv {
	t.Helper()
	ctx := context.Background()
	connStore, _, settingsStore, grabsStore, libStore, _, _, _, _, _, scStore := testStoresWithRegistry(t)

	var rssHits, findHits int32
	rssSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&rssHits, 1)
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(imdbListRSS))
	}))
	t.Cleanup(rssSrv.Close)
	prevRSS := imdbRSSBaseURL
	imdbRSSBaseURL = rssSrv.URL
	t.Cleanup(func() { imdbRSSBaseURL = prevRSS })

	tmdbSrv := fakeIMDbIngestTMDB(t, movieReleaseDate, &findHits)
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
	if err := settingsStore.SetBool(ctx, imdbListIngestEnabledKey, true); err != nil {
		t.Fatalf("ingest on: %v", err)
	}
	if err := settingsStore.Set(ctx, imdbListIDsKey, "ls123456789"); err != nil {
		t.Fatalf("list ids: %v", err)
	}

	dl := newTestDownloader("gid-imdb-ingest", t.TempDir())
	build := func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
		return mode.Build(ctx, connStore, scStore, settingsStore, testHTTPClient(), dl, m)
	}
	return imdbListIngestEnv{
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
		rssCount:      &rssHits,
		findCount:     &findHits,
		prowlarrCount: prowlarrCount,
	}
}

func fakeIMDbIngestTMDB(t *testing.T, movieReleaseDate string, findHits *int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/find/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(findHits, 1)
		w.Header().Set("Content-Type", "application/json")
		id := strings.TrimPrefix(r.URL.Path, "/find/")
		switch id {
		case "tt0944947":
			json.NewEncoder(w).Encode(map[string]any{
				"movie_results": []any{},
				"tv_results":    []map[string]any{{"id": 200}},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{
				"movie_results": []map[string]any{{"id": 100}},
				"tv_results":    []any{},
			})
		}
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
