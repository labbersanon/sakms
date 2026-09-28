package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/secrets"
)

func TestQualityWatchOriginated(t *testing.T) {
	base := grabs.Grab{
		Mode: mode.Movies, Origin: grabOriginUpgradeWatch, Status: grabs.PendingRetry,
		TMDBID: 42,
	}
	if !qualityWatchOriginated(base, 42) {
		t.Fatal("expected originated for a never-dispatched watch grab")
	}
	dispatched := base
	dispatched.Indexer = "I"
	dispatched.DownloadURL = "magnet:?xt=urn:btih:abc"
	if qualityWatchOriginated(dispatched, 42) {
		t.Fatal("dispatched grab must not be originated")
	}
	wrongTitle := base
	if qualityWatchOriginated(wrongTitle, 99) {
		t.Fatal("other TMDB id must not match")
	}
}

func TestCancelQualityWatchRetries_CancelsNeverDispatchedOnly(t *testing.T) {
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("secret store: %v", err)
	}
	grabsStore := grabs.New(sqlDB, secretStore)
	ctx := context.Background()

	parked, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Watched", TMDBID: 42, RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("create parked: %v", err)
	}
	if err := grabsStore.SetOrigin(ctx, parked.ID, grabOriginUpgradeWatch); err != nil {
		t.Fatalf("origin: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, parked.ID, grabs.PendingRetry); err != nil {
		t.Fatalf("status: %v", err)
	}

	live, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Watched", TMDBID: 42,
		Indexer: "I", DownloadURL: "magnet:?xt=urn:btih:abc", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("create live: %v", err)
	}
	if err := grabsStore.SetOrigin(ctx, live.ID, grabOriginUpgradeWatch); err != nil {
		t.Fatalf("origin live: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, live.ID, grabs.PendingRetry); err != nil {
		t.Fatalf("status live: %v", err)
	}

	other, err := grabsStore.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Other", TMDBID: 99, RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	if err := grabsStore.SetOrigin(ctx, other.ID, grabOriginUpgradeWatch); err != nil {
		t.Fatalf("origin other: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, other.ID, grabs.PendingRetry); err != nil {
		t.Fatalf("status other: %v", err)
	}

	cancelQualityWatchRetries(ctx, grabsStore, 42)

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

	gotOther, err := grabsStore.Get(ctx, other.ID)
	if err != nil {
		t.Fatalf("get other: %v", err)
	}
	if gotOther.Status != grabs.PendingRetry {
		t.Fatalf("other title was cancelled; status=%q", gotOther.Status)
	}
}

func TestPutMovieUpgradeWatch_RoundTripAndQualityPrefsCarryFlag(t *testing.T) {
	lib, mux := newTrackedMux(t)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx := context.Background()

	item, err := lib.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie",
		FilePath: "/movies/Some.Movie.480p.WEB.DL.x264-GRP.mkv", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	body, _ := json.Marshal(apidto.MovieUpgradeWatchRequest{UpgradeWatch: true})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/library/tmdb/42/upgrade-watch", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT = %d (%s), want 200", resp.StatusCode, b)
	}

	got, err := lib.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.UpgradeWatch {
		t.Fatal("expected upgrade watch on")
	}

	prefsResp, err := http.Get(srv.URL + "/api/modes/movies/library/tmdb/42/quality-prefs")
	if err != nil {
		t.Fatalf("GET prefs: %v", err)
	}
	defer prefsResp.Body.Close()
	var prefs apidto.TitleQualityPrefsResponse
	if err := json.NewDecoder(prefsResp.Body).Decode(&prefs); err != nil {
		t.Fatalf("decode prefs: %v", err)
	}
	if !prefs.UpgradeWatchAvailable || !prefs.UpgradeWatch {
		t.Fatalf("prefs flag = %+v, want available+on", prefs)
	}
}

func TestPutMovieUpgradeWatch_MissingMovie404(t *testing.T) {
	_, mux := newTrackedMux(t)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(apidto.MovieUpgradeWatchRequest{UpgradeWatch: true})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/library/tmdb/404/upgrade-watch", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("PUT missing = %d, want 404", resp.StatusCode)
	}
}

func TestMonitorMovieUpgradeWatch_NilStoreSkips(t *testing.T) {
	monitorMovieUpgradeWatch(context.Background(), AutoGrabDeps{}, nil, nil, nil)
}

func TestMonitorMovieUpgradeWatch_SkipsWhenFileMeetsPrefs(t *testing.T) {
	ctx := context.Background()
	env := newMovieUpgradeWatchEnv(t, "Some.Movie.2023.1080p.BluRay.REMUX.mkv", "lossless")
	monitorMovieUpgradeWatch(ctx, env.deps, env.build, env.lib, nil)
	if n := atomic.LoadInt32(env.prowlarrCount); n != 0 {
		t.Fatalf("Prowlarr calls = %d, want 0 when the file already meets prefs", n)
	}
	list, err := env.grabs.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("grabs = %+v, want none", list)
	}
}

func TestMonitorMovieUpgradeWatch_HuntsWhenFileBelowPrefs(t *testing.T) {
	ctx := context.Background()
	env := newMovieUpgradeWatchEnv(t, "Some.Movie.2023.480p.WEB.DL.x264-GRP.mkv", "medium")
	monitorMovieUpgradeWatch(ctx, env.deps, env.build, env.lib, nil)
	if n := atomic.LoadInt32(env.prowlarrCount); n == 0 {
		t.Fatal("expected a Prowlarr search for a below-floor file")
	}
	list, err := env.grabs.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("expected a parked or dispatched grab")
	}
	if list[0].Origin != grabOriginUpgradeWatch {
		t.Fatalf("origin = %q, want %q", list[0].Origin, grabOriginUpgradeWatch)
	}
}

func TestMonitorMovieUpgradeWatch_SkipsActiveGrab(t *testing.T) {
	ctx := context.Background()
	env := newMovieUpgradeWatchEnv(t, "Some.Movie.2023.480p.WEB.DL.x264-GRP.mkv", "medium")
	if _, err := env.grabs.Create(ctx, grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		Status: grabs.PendingRetry, RootFolderPath: "/movies",
	}); err != nil {
		t.Fatalf("create active: %v", err)
	}
	monitorMovieUpgradeWatch(ctx, env.deps, env.build, env.lib, nil)
	if n := atomic.LoadInt32(env.prowlarrCount); n != 0 {
		t.Fatalf("Prowlarr calls = %d, want 0 while a grab is active", n)
	}
}

func TestMonitorMovieUpgradeWatch_SkipsEmptyFile(t *testing.T) {
	ctx := context.Background()
	env := newMovieUpgradeWatchEnv(t, "", "")
	monitorMovieUpgradeWatch(ctx, env.deps, env.build, env.lib, nil)
	if n := atomic.LoadInt32(env.prowlarrCount); n != 0 {
		t.Fatalf("Prowlarr calls = %d, want 0 with no file on disk", n)
	}
}

func TestListTracked_Movies_UpgradeWatchLightsMonitored(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	item, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 7, Title: "Watched", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := libStore.SetItemUpgradeWatch(ctx, item.ID, true); err != nil {
		t.Fatalf("set watch: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)

	got := getTrackedItems(t, srv, "movies")
	if len(got) != 1 || !got[0].Monitored {
		t.Fatalf("expected monitored from upgrade watch, got %+v", got)
	}
}

type movieUpgradeWatchEnv struct {
	deps          AutoGrabDeps
	build         sessionBuilderFunc
	lib           *library.Store
	grabs         *grabs.Store
	prowlarrCount *int32
}

func newMovieUpgradeWatchEnv(t *testing.T, filePath, stampedTier string) movieUpgradeWatchEnv {
	t.Helper()
	ctx := context.Background()
	connStore, _, settingsStore, grabsStore, libStore, _, _, _, _, _, scStore := testStoresWithRegistry(t)
	tmdbSrv := fakeTMDBMovieRuntime(t, 100)
	prowlarrSrv, count := fakeProwlarrCounting(t, `[]`)
	overrideFixedURL(t, "tmdb", tmdbSrv.URL)
	for _, c := range []struct{ service, url string }{{"tmdb", tmdbSrv.URL}, {"prowlarr", prowlarrSrv.URL}} {
		if err := connStore.Upsert(ctx, c.service, c.url, "key"); err != nil {
			t.Fatalf("upserting %s: %v", c.service, err)
		}
	}
	if err := settingsStore.Set(ctx, qualityTierKey(mode.Movies), string(quality.High)); err != nil {
		t.Fatalf("quality: %v", err)
	}
	if err := settingsStore.Set(ctx, moviesLibraryRootFolderKey, "/movies"); err != nil {
		t.Fatalf("root: %v", err)
	}
	setAutoGrabToggle(t, settingsStore, true)

	item, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie",
		FilePath: filePath, RootFolderPath: "/movies", QualityTier: stampedTier,
	})
	if err != nil {
		t.Fatalf("upsert movie: %v", err)
	}
	if err := libStore.SetItemUpgradeWatch(ctx, item.ID, true); err != nil {
		t.Fatalf("set watch: %v", err)
	}

	dl := newTestDownloader("gid-watch", t.TempDir())
	build := func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
		return mode.Build(ctx, connStore, scStore, settingsStore, testHTTPClient(), dl, m)
	}
	return movieUpgradeWatchEnv{
		deps:          AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore, LibStore: libStore},
		build:         build,
		lib:           libStore,
		grabs:         grabsStore,
		prowlarrCount: count,
	}
}
