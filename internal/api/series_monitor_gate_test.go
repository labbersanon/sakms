package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/mode"
)

func TestRefuseUnmonitoredSeriesGrab(t *testing.T) {
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.monitor(t, series.ID, 13, true)

	if err := refuseUnmonitoredSeriesGrab(env.ctx, env.lib, mode.Movies, airDateTMDBID, 0, false); err != nil {
		t.Fatalf("movies: %v", err)
	}
	if err := refuseUnmonitoredSeriesGrab(env.ctx, nil, mode.Series, airDateTMDBID, 0, false); err != nil {
		t.Fatalf("nil libStore: %v", err)
	}
	if err := refuseUnmonitoredSeriesGrab(env.ctx, env.lib, mode.Series, 0, 0, false); err != nil {
		t.Fatalf("tmdbID 0: %v", err)
	}
	if err := refuseUnmonitoredSeriesGrab(env.ctx, env.lib, mode.Series, 404, 1, true); err != nil {
		t.Fatalf("untracked series: %v", err)
	}
	if err := refuseUnmonitoredSeriesGrab(env.ctx, env.lib, mode.Series, airDateTMDBID, 0, false); !errors.Is(err, errSeriesGrabNeedsSeason) {
		t.Fatalf("season-less tracked grab: %v", err)
	}
	if err := refuseUnmonitoredSeriesGrab(env.ctx, env.lib, mode.Series, airDateTMDBID, 1, true); !errors.Is(err, errSeriesSeasonUnmonitored) {
		t.Fatalf("unmonitored season: %v", err)
	}
	if err := refuseUnmonitoredSeriesGrab(env.ctx, env.lib, mode.Series, airDateTMDBID, 13, true); err != nil {
		t.Fatalf("monitored season: %v", err)
	}
}

func TestMonitoredMissingEpisodes_FiltersUnmonitoredSeasons(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -30))
	env.seedMissingEpisode(t, series.ID, 13, 4, dayOffset(now, -1))
	env.monitor(t, series.ID, 13, true)

	got, err := monitoredMissingEpisodes(env.ctx, env.lib, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SeasonNumber != 13 || got[0].EpisodeNumber != 4 {
		t.Fatalf("got %+v, want only S13E04", got)
	}
}

func TestMissingEpisodesByTMDB_FiltersUnmonitoredSeasons(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -30))
	env.seedMissingEpisode(t, series.ID, 13, 4, dayOffset(now, -1))
	env.monitor(t, series.ID, 13, true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/modes/series/library/tmdb/%d/missing-episodes", airDateTMDBID), nil)
	req.SetPathValue("tmdbId", strconv.Itoa(airDateTMDBID))
	missingEpisodesByTMDBHandler(env.lib)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
	var out apidto.MissingEpisodesResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Episodes) != 1 || out.Episodes[0].SeasonNumber != 13 || out.Episodes[0].EpisodeNumber != 4 {
		t.Fatalf("got %+v, want only S13E04", out.Episodes)
	}
}

func TestRequestsOmitsSeriesWithOnlyUnmonitoredMissing(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -30))
	env.monitor(t, series.ID, 13, true)

	srv := httptest.NewServer(NewRequestsMux(env.grabs, env.lib, env.excludes, nil, nil))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/requests")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out apidto.RequestStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	for _, item := range out.Items {
		if item.TMDBID == airDateTMDBID {
			t.Fatalf("unmonitored-only series appeared on Requests: %+v", item)
		}
	}
}
