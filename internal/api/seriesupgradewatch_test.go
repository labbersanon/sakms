package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
)

func TestPutSeriesUpgradeWatch_RoundTripAndQualityPrefsCarryFlag(t *testing.T) {
	lib, mux := newTrackedMux(t)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx := context.Background()

	series, err := lib.UpsertSeries(ctx, library.Series{
		TMDBID: 42, Title: "Some Show", RootFolderPath: "/series",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	body, _ := json.Marshal(apidto.MovieUpgradeWatchRequest{UpgradeWatch: true})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/series/library/tmdb/42/upgrade-watch", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT tmdb: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT tmdb = %d (%s), want 200", resp.StatusCode, b)
	}

	got, err := lib.GetSeries(ctx, series.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.UpgradeWatch {
		t.Fatal("expected upgrade watch on")
	}

	prefsResp, err := http.Get(srv.URL + "/api/modes/series/library/tmdb/42/quality-prefs")
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

	idBody, _ := json.Marshal(apidto.MovieUpgradeWatchRequest{UpgradeWatch: false})
	idReq, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/series/library/"+strconv.FormatInt(series.ID, 10)+"/upgrade-watch", bytes.NewReader(idBody))
	if err != nil {
		t.Fatalf("id request: %v", err)
	}
	idResp, err := http.DefaultClient.Do(idReq)
	if err != nil {
		t.Fatalf("PUT id: %v", err)
	}
	defer idResp.Body.Close()
	if idResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(idResp.Body)
		t.Fatalf("PUT id = %d (%s), want 200", idResp.StatusCode, b)
	}
	got, err = lib.GetSeries(ctx, series.ID)
	if err != nil {
		t.Fatalf("get after id put: %v", err)
	}
	if got.UpgradeWatch {
		t.Fatal("expected upgrade watch off after seriesID PUT")
	}
}

func TestPutSeriesUpgradeWatch_MissingSeries404(t *testing.T) {
	_, mux := newTrackedMux(t)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(apidto.MovieUpgradeWatchRequest{UpgradeWatch: true})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/series/library/tmdb/404/upgrade-watch", bytes.NewReader(body))
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

func TestMonitorSeriesUpgradeWatch_NilStoreSkips(t *testing.T) {
	monitorSeriesUpgradeWatch(context.Background(), AutoGrabDeps{}, nil, nil, nil)
}

func TestMonitorSeriesUpgradeWatch_SkipsWhenFileMeetsPrefs(t *testing.T) {
	env := newSeriesUpgradeWatchEnv(t, "/series/Some.Show.S01E01.1080p.BluRay.REMUX.mkv", "lossless")
	monitorSeriesUpgradeWatch(env.ctx, env.deps, env.build, env.lib, nil)
	if n := len(env.seriesGrabs(t)); n != 0 {
		t.Fatalf("grabs = %d, want 0 when the file already meets prefs", n)
	}
}

func TestMonitorSeriesUpgradeWatch_HuntsWhenFileBelowPrefs(t *testing.T) {
	env := newSeriesUpgradeWatchEnv(t, "/series/Some.Show.S01E01.480p.WEB.DL.x264-GRP.mkv", "medium")
	monitorSeriesUpgradeWatch(env.ctx, env.deps, env.build, env.lib, nil)
	list := env.seriesGrabs(t)
	if len(list) == 0 {
		t.Fatal("expected a parked or dispatched grab")
	}
	if list[0].Origin != grabOriginUpgradeWatch {
		t.Fatalf("origin = %q, want %q", list[0].Origin, grabOriginUpgradeWatch)
	}
	if list[0].SeasonNumber != 1 || list[0].EpisodeNumber != 1 || !list[0].SeasonSpecified {
		t.Fatalf("season/episode = S%dE%d specified=%v", list[0].SeasonNumber, list[0].EpisodeNumber, list[0].SeasonSpecified)
	}
}

func TestMonitorSeriesUpgradeWatch_SkipsActiveGrab(t *testing.T) {
	env := newSeriesUpgradeWatchEnv(t, "/series/Some.Show.S01E01.480p.WEB.DL.x264-GRP.mkv", "medium")
	if _, err := env.grabs.Create(env.ctx, grabs.Grab{
		Mode: mode.Series, Title: airDateTitle, TMDBID: airDateTMDBID,
		SeasonNumber: 1, EpisodeNumber: 1, SeasonSpecified: true,
		Status: grabs.PendingRetry, RootFolderPath: "/series",
	}); err != nil {
		t.Fatalf("create active: %v", err)
	}
	monitorSeriesUpgradeWatch(env.ctx, env.deps, env.build, env.lib, nil)
	list := env.seriesGrabs(t)
	if len(list) != 1 {
		t.Fatalf("grabs = %d, want the pre-existing active row only", len(list))
	}
	if list[0].Origin == grabOriginUpgradeWatch {
		t.Fatal("expected the existing grab to stay untagged")
	}
}

func TestMonitorSeriesUpgradeWatch_SkipsEmptyFile(t *testing.T) {
	env := newSeriesUpgradeWatchEnv(t, "", "")
	monitorSeriesUpgradeWatch(env.ctx, env.deps, env.build, env.lib, nil)
	if n := len(env.seriesGrabs(t)); n != 0 {
		t.Fatalf("grabs = %d, want 0 with no file on disk", n)
	}
}

func TestMonitorSeriesUpgradeWatch_ZeroBudgetSkips(t *testing.T) {
	env := newSeriesUpgradeWatchEnv(t, "/series/Some.Show.S01E01.480p.WEB.DL.x264-GRP.mkv", "medium")
	used := runSeriesUpgradeWatch(env.ctx, env.deps, env.build, env.lib, nil, 0)
	if used != 0 {
		t.Fatalf("used = %d, want 0", used)
	}
	if n := len(env.seriesGrabs(t)); n != 0 {
		t.Fatalf("grabs = %d, want 0 at zero budget", n)
	}
}

func TestListTracked_Series_UpgradeWatchLightsMonitored(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 7, Title: "Watched Show", RootFolderPath: "/series",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := libStore.SetSeriesUpgradeWatch(ctx, series.ID, true); err != nil {
		t.Fatalf("set watch: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)

	got := getTrackedItems(t, srv, "series")
	if len(got) != 1 || !got[0].Monitored {
		t.Fatalf("expected monitored from upgrade watch, got %+v", got)
	}
}

func newSeriesUpgradeWatchEnv(t *testing.T, filePath, stampedTier string) *airDateEnv {
	t.Helper()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		1: {{Number: 1, Name: "Pilot", AirDate: "2020-01-01"}},
	}, noQualifyingRelease)
	if err := env.settings.Set(env.ctx, qualityTierKey(mode.Series), string(quality.High)); err != nil {
		t.Fatalf("quality: %v", err)
	}
	series := env.trackSeries(t)
	if _, err := env.lib.UpsertEpisode(env.ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1,
		Title: "Pilot", AirDate: "2020-01-01",
		FilePath: filePath, QualityTier: stampedTier,
	}); err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	if err := env.lib.SetSeriesUpgradeWatch(env.ctx, series.ID, true); err != nil {
		t.Fatalf("set watch: %v", err)
	}
	env.deps.LibStore = env.lib
	return env
}
