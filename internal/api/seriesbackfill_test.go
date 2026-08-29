package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
)

// TestSeriesBackfillOnMonitorEnable cues already-aired missing episodes when
// monitoring turns on — without waiting for runUsenetRetryCycle. This is the
// regression for "I enabled monitoring and nothing queued."
func TestSeriesBackfillOnMonitorEnable(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{3: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -365)},
	}}, qualifyingSeriesRelease(3, 1))
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 3, 1, dayOffset(now, -365))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/modes/series/library/%d/seasons/3/monitored", series.ID),
		strings.NewReader(`{"monitored":true}`))
	req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
	req.SetPathValue("seasonNumber", "3")
	putSeasonMonitoredHandler(env.catalog(), env.grabs)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT monitored=true: status %d, body %q", rec.Code, rec.Body.String())
	}
	env.waitBackfillIdle(t)

	list := env.seriesGrabs(t)
	if len(list) != 1 {
		t.Fatalf("expected 1 grab from monitor-on backfill (no runCycle), got %d: %+v", len(list), list)
	}
	if list[0].SeasonNumber != 3 || list[0].EpisodeNumber != 1 {
		t.Errorf("wrong episode: %+v", list[0])
	}
	if list[0].DownloadURL == "" && list[0].Status == grabs.PendingRetry {
		t.Errorf("parked instead of dispatched: %+v", list[0])
	}
}

// TestSeriesBackfillSkipsWhenAutoGrabOff asserts the gate runs before any TMDB
// SeasonDetails call — default installs must not burn TMDB on every toggle.
func TestSeriesBackfillSkipsWhenAutoGrabOff(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -10)},
	}}, noQualifyingRelease)
	setAutoGrabToggle(t, env.settings, false)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -10))
	before := env.tmdbSeasonHitCount()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/modes/series/library/%d/seasons/1/monitored", series.ID),
		strings.NewReader(`{"monitored":true}`))
	req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
	req.SetPathValue("seasonNumber", "1")
	putSeasonMonitoredHandler(env.catalog(), env.grabs)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	env.waitBackfillIdle(t)

	if env.tmdbSeasonHitCount() != before {
		t.Errorf("TMDB season hits moved from %d to %d with auto-grab off — gate must precede sync",
			before, env.tmdbSeasonHitCount())
	}
	if len(env.seriesGrabs(t)) != 0 {
		t.Errorf("grabs created with auto-grab off: %+v", env.seriesGrabs(t))
	}
}

// TestSeriesBackfillRespectsSeasonFilter: enabling season 3 must not search
// season 2's missing episodes.
func TestSeriesBackfillRespectsSeasonFilter(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		2: {{Number: 1, Name: "S2", AirDate: dayOffset(now, -20)}},
		3: {{Number: 1, Name: "S3", AirDate: dayOffset(now, -10)}},
	}, qualifyingSeriesRelease(3, 1))
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 2, 1, dayOffset(now, -20))
	env.seedMissingEpisode(t, series.ID, 3, 1, dayOffset(now, -10))
	env.monitor(t, series.ID, 2, true) // already monitored; must not be re-searched by S3 kick

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/modes/series/library/%d/seasons/3/monitored", series.ID),
		strings.NewReader(`{"monitored":true}`))
	req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
	req.SetPathValue("seasonNumber", "3")
	putSeasonMonitoredHandler(env.catalog(), env.grabs)(rec, req)
	env.waitBackfillIdle(t)

	list := env.seriesGrabs(t)
	if len(list) != 1 || list[0].SeasonNumber != 3 {
		t.Fatalf("want exactly one season-3 grab (season 2 must not be searched), got %+v", list)
	}
}

// TestSeriesBackfillSyncsCatalogBeforeDispatch: a TMDB-only season (no local
// episode rows) still gets searched after monitor-on — the case that motivates
// calling bare syncSeriesCatalog rather than trusting existing rows.
func TestSeriesBackfillSyncsCatalogBeforeDispatch(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{5: {
		{Number: 1, Name: "New", AirDate: dayOffset(now, -5)},
	}}, qualifyingSeriesRelease(5, 1))
	series := env.trackSeries(t)
	// No seedMissingEpisode — catalog sync must create the fileless row.

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/modes/series/library/%d/seasons/5/monitored", series.ID),
		strings.NewReader(`{"monitored":true}`))
	req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
	req.SetPathValue("seasonNumber", "5")
	putSeasonMonitoredHandler(env.catalog(), env.grabs)(rec, req)
	env.waitBackfillIdle(t)

	if got := env.episodeRow(t, series.ID, 5, 1); got.AirDate != dayOffset(now, -5) {
		t.Errorf("catalog was not synced: %+v", got)
	}
	if len(env.seriesGrabs(t)) != 1 {
		t.Fatalf("expected grab after sync, got %+v", env.seriesGrabs(t))
	}
}

// TestSeriesBackfillBypassesReadPathFreshness: a recent Calendar sync must not
// skip the monitor-on catalog refresh.
func TestSeriesBackfillBypassesReadPathFreshness(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -3)},
		{Number: 2, Name: "Two", AirDate: dayOffset(now, -1)},
	}}, qualifyingSeriesRelease(1, 2))
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -3))
	// Simulate Calendar having just synced — only e01 is local; e02 exists only on TMDB.
	resetSeriesCatalogFreshness()
	markSeriesCatalogSynced(series.ID, now)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/modes/series/library/%d/seasons/1/monitored", series.ID),
		strings.NewReader(`{"monitored":true}`))
	req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
	req.SetPathValue("seasonNumber", "1")
	putSeasonMonitoredHandler(env.catalog(), env.grabs)(rec, req)
	env.waitBackfillIdle(t)

	if got := env.episodeRow(t, series.ID, 1, 2); got.AirDate != dayOffset(now, -1) {
		t.Fatalf("read-path freshness skipped monitor-on sync; e02 missing: looking for air date, got %+v", got)
	}
}

// TestSeriesBackfillCoalescesConcurrentKicks: a second kick while the first is
// blocked on TMDB must merge seasons rather than drop them.
func TestSeriesBackfillCoalescesConcurrentKicks(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		1: {{Number: 1, Name: "S1", AirDate: dayOffset(now, -10)}},
		2: {{Number: 1, Name: "S2", AirDate: dayOffset(now, -9)}},
	}, noQualifyingRelease)
	series := env.trackSeries(t)
	env.seedMissingEpisode(t, series.ID, 1, 1, dayOffset(now, -10))
	env.seedMissingEpisode(t, series.ID, 2, 1, dayOffset(now, -9))
	env.monitor(t, series.ID, 1, true)
	env.monitor(t, series.ID, 2, true)

	gate := make(chan struct{})
	env.setTMDBSeasonGate(gate)

	env.backfill.kick(series.ID, map[int]bool{1: true})
	// Wait until the first run is blocked inside TMDB.
	deadline := time.Now().Add(5 * time.Second)
	for env.tmdbSeasonHitCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if env.tmdbSeasonHitCount() == 0 {
		t.Fatal("first kick never reached TMDB")
	}
	env.backfill.kick(series.ID, map[int]bool{2: true})
	close(gate)
	env.waitBackfillIdle(t)

	seen := map[int]bool{}
	for _, g := range env.seriesGrabs(t) {
		seen[g.SeasonNumber] = true
	}
	if !seen[1] || !seen[2] {
		t.Fatalf("coalesced kicks must search both seasons, got seasons %v from %+v", seen, env.seriesGrabs(t))
	}
}

// TestSeriesBackfillNilIsInert: seasonCatalog without a backfill still 204s.
func TestSeriesBackfillNilIsInert(t *testing.T) {
	now := time.Now()
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, -1)},
	}}, noQualifyingRelease)
	series := env.trackSeries(t)
	cat := seasonCatalog{
		httpClient: testHTTPClient(), connStore: env.conns, scStore: env.scConns,
		settings: env.settings, lib: env.lib, backfill: nil,
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/modes/series/library/%d/seasons/1/monitored", series.ID),
		strings.NewReader(`{"monitored":true}`))
	req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
	req.SetPathValue("seasonNumber", "1")
	putSeasonMonitoredHandler(cat, env.grabs)(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	if len(env.seriesGrabs(t)) != 0 {
		t.Errorf("nil backfill somehow created grabs: %+v", env.seriesGrabs(t))
	}
}

func TestSeriesBackfillRespectsPerKickCap(t *testing.T) {
	now := time.Now()
	eps := make([]fakeTMDBEpisode, 0, 30)
	for i := 1; i <= 30; i++ {
		eps = append(eps, fakeTMDBEpisode{Number: i, Name: fmt.Sprintf("E%d", i), AirDate: dayOffset(now, -i)})
	}
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{1: eps}, noQualifyingRelease)
	series := env.trackSeries(t)
	for i := 1; i <= 30; i++ {
		env.seedMissingEpisode(t, series.ID, 1, i, dayOffset(now, -i))
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		fmt.Sprintf("/api/modes/series/library/%d/seasons/1/monitored", series.ID),
		strings.NewReader(`{"monitored":true}`))
	req.SetPathValue("seriesID", strconv.FormatInt(series.ID, 10))
	req.SetPathValue("seasonNumber", "1")
	putSeasonMonitoredHandler(env.catalog(), env.grabs)(rec, req)
	env.waitBackfillIdle(t)

	if got := len(env.seriesGrabs(t)); got != maxSeriesBackfillGrabsPerKick {
		t.Fatalf("grab count = %d, want per-kick cap %d", got, maxSeriesBackfillGrabsPerKick)
	}
}

// TestAirDatePerSeriesCycleCapFairness: a classic backlog cannot consume every
// global slot; a second series with few missing episodes still cues.
func TestAirDatePerSeriesCycleCapFairness(t *testing.T) {
	now := time.Now()
	classicEps := make([]fakeTMDBEpisode, 0, 30)
	for i := 1; i <= 30; i++ {
		classicEps = append(classicEps, fakeTMDBEpisode{
			Number: i, Name: fmt.Sprintf("Old%d", i), AirDate: dayOffset(now, -(1000 + i)),
		})
	}
	env := newAirDateEnv(t, map[int][]fakeTMDBEpisode{
		1: classicEps,
		2: {
			{Number: 1, Name: "New1", AirDate: dayOffset(now, -3)},
			{Number: 2, Name: "New2", AirDate: dayOffset(now, -2)},
			{Number: 3, Name: "New3", AirDate: dayOffset(now, -1)},
		},
	}, noQualifyingRelease)

	classic, err := env.lib.UpsertSeries(env.ctx, library.Series{
		TMDBID: airDateTMDBID, Title: "Classic", RootFolderPath: "/series",
	})
	if err != nil {
		t.Fatalf("classic: %v", err)
	}
	modern, err := env.lib.UpsertSeries(env.ctx, library.Series{
		TMDBID: airDateTMDBID + 1, Title: "Modern", RootFolderPath: "/series",
	})
	if err != nil {
		t.Fatalf("modern: %v", err)
	}
	for i := 1; i <= 30; i++ {
		env.seedMissingEpisode(t, classic.ID, 1, i, dayOffset(now, -(1000+i)))
	}
	for i := 1; i <= 3; i++ {
		env.seedMissingEpisode(t, modern.ID, 2, i, dayOffset(now, -i))
	}
	env.monitor(t, classic.ID, 1, true)
	env.monitor(t, modern.ID, 2, true)

	// fakeTMDBSeries is keyed to airDateTMDBID only; sync for modern's season 2
	// will soft-fail on TMDB. Rows are already seeded, so dispatch still works.
	env.runCycle(t, now)

	bySeries := map[int64]int{}
	for _, g := range env.seriesGrabs(t) {
		if g.TMDBID == classic.TMDBID {
			bySeries[classic.ID]++
		}
		if g.TMDBID == modern.TMDBID {
			bySeries[modern.ID]++
		}
	}
	if bySeries[classic.ID] > maxAirDateGrabsPerSeriesPerCycle {
		t.Errorf("classic took %d slots, per-series cap is %d", bySeries[classic.ID], maxAirDateGrabsPerSeriesPerCycle)
	}
	if bySeries[modern.ID] != 3 {
		t.Errorf("modern got %d grabs, want 3 (fairness vs classic backlog)", bySeries[modern.ID])
	}
}
