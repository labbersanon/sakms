package api

// These cover the READ-PATH WRAPPER only — syncSeriesCatalogForRead's three
// bounds, plus the two guards a browsing caller must have and the auto-grab
// pass must not gain.
//
// syncSeriesCatalog itself is NOT re-tested here. Its coverage lives in
// airdatemonitor_test.go, driven end to end through runUsenetRetryCycle
// (TestAirDateCatalogSyncCreatesFilelessRows, TestAirDateDiscovery*,
// TestCatalogSyncSurvivesATVDetailsFailure, TestMonitoringAZeroRowSeasonSyncsItsEpisodes,
// TestUnMonitoredZeroRowSeasonIsNotSynced). Those are monitorAirDates
// integration tests: they are the evidence that moving the function changed
// nothing on the auto-grab path, so relocating them to this file would destroy
// the very thing they prove.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// readSyncEnv wires an airDateEnv, a Series session, and a clean freshness
// cache. The reset is mandatory, not tidiness: every test builds its own temp
// database, so the first tracked series is id 1 in ALL of them, and a
// package-level cache surviving between tests would let one test's series look
// fresh to the next — a silent, vacuous pass.
func readSyncEnv(t *testing.T, seasons map[int][]fakeTMDBEpisode) (*airDateEnv, *mode.Session) {
	t.Helper()
	resetSeriesCatalogFreshness()
	t.Cleanup(resetSeriesCatalogFreshness)

	env := newAirDateEnv(t, seasons, noQualifyingRelease)
	sess, err := env.build(env.ctx, mode.Series)
	if err != nil {
		t.Fatalf("building the series session: %v", err)
	}
	return env, sess
}

// trackMonitoredSeries records one tracked series with season 1 monitored and no
// episode rows. Monitoring is what puts season 1 into the sync set with
// discovery off, so the sync has something to do and its effect is observable.
func trackMonitoredSeries(t *testing.T, env *airDateEnv, tmdbID int, title string) library.Series {
	t.Helper()
	series, err := env.lib.UpsertSeries(env.ctx, library.Series{
		TMDBID: tmdbID, TVDBID: 4321, Title: title, RootFolderPath: "/series",
	})
	if err != nil {
		t.Fatalf("tracking %s: %v", title, err)
	}
	env.monitor(t, series.ID, 1, true)
	return series
}

func (e *airDateEnv) allSeries(t *testing.T) []library.Series {
	t.Helper()
	list, err := e.lib.ListSeries(e.ctx)
	if err != nil {
		t.Fatalf("listing series: %v", err)
	}
	return list
}

// seriesWithEpisodeRows counts how many tracked series the sync has actually
// populated. Counting rather than naming names keeps the assertion independent
// of ListSeries' ORDER BY title.
func (e *airDateEnv) seriesWithEpisodeRows(t *testing.T) int {
	t.Helper()
	n := 0
	for _, series := range e.allSeries(t) {
		rows, err := e.lib.ListEpisodes(e.ctx, series.ID)
		if err != nil {
			t.Fatalf("listing episodes for series %d: %v", series.ID, err)
		}
		if len(rows) > 0 {
			n++
		}
	}
	return n
}

func (e *airDateEnv) hasEpisodeRow(t *testing.T, seriesID int64, season, episode int) bool {
	t.Helper()
	rows, err := e.lib.ListEpisodes(e.ctx, seriesID)
	if err != nil {
		t.Fatalf("listing episodes: %v", err)
	}
	for _, ep := range rows {
		if ep.SeasonNumber == season && ep.EpisodeNumber == episode {
			return true
		}
	}
	return false
}

// TestSeriesCatalogReadPathSkipsAFreshSeries is the freshness cache: a series
// synced inside the TTL costs nothing on the next page load, and one synced
// outside it is refreshed.
//
// The fixture is mutated between calls and internal/tmdb's response cache is
// cleared before each one, so the only thing that can explain the middle call
// seeing nothing is this file's own TTL — not a TMDB body served from that
// other, unrelated cache.
func TestSeriesCatalogReadPathSkipsAFreshSeries(t *testing.T) {
	now := time.Now()
	seasons := map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, 3)},
	}}
	env, sess := readSyncEnv(t, seasons)
	series := trackMonitoredSeries(t, env, airDateTMDBID, airDateTitle)
	list := env.allSeries(t)

	syncSeriesCatalogForRead(env.ctx, sess, env.lib, list, now)
	if !env.hasEpisodeRow(t, series.ID, 1, 1) {
		t.Fatalf("the first read did not sync s01e01 — the read path never ran")
	}

	// TMDB now knows about a second episode.
	seasons[1] = append(seasons[1], fakeTMDBEpisode{Number: 2, Name: "Two", AirDate: dayOffset(now, 10)})

	tmdb.ResetDefaultCache()
	syncSeriesCatalogForRead(env.ctx, sess, env.lib, list, now.Add(seriesCatalogFreshFor-time.Minute))
	if env.hasEpisodeRow(t, series.ID, 1, 2) {
		t.Fatalf("a read inside the TTL re-synced the series — the freshness cache is not skipping, so every Calendar load costs a TMDB call per tracked series")
	}

	tmdb.ResetDefaultCache()
	syncSeriesCatalogForRead(env.ctx, sess, env.lib, list, now.Add(seriesCatalogFreshFor+time.Minute))
	if !env.hasEpisodeRow(t, series.ID, 1, 2) {
		t.Fatalf("a read past the TTL did not re-sync — the cache never expires, so the catalog would go permanently stale")
	}
}

// TestSeriesCatalogReadPathCapsSyncsPerRequest is the hard per-request bound,
// and the degradation it is supposed to produce: the first read populates
// exactly maxSeriesCatalogSyncsPerRead series and returns, and the NEXT read
// picks up the remainder rather than re-doing the same 20 forever. A view that
// is partially populated for one load is the intended outcome; a stall is not.
func TestSeriesCatalogReadPathCapsSyncsPerRequest(t *testing.T) {
	now := time.Now()
	env, sess := readSyncEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, 3)},
	}})

	total := maxSeriesCatalogSyncsPerRead + 5
	for i := 0; i < total; i++ {
		trackMonitoredSeries(t, env, airDateTMDBID+i, fmt.Sprintf("Show %02d", i))
	}
	list := env.allSeries(t)
	if len(list) != total {
		t.Fatalf("seeded %d series, want %d", len(list), total)
	}

	syncSeriesCatalogForRead(env.ctx, sess, env.lib, list, now)
	if got := env.seriesWithEpisodeRows(t); got != maxSeriesCatalogSyncsPerRead {
		t.Fatalf("one read synced %d series, want exactly %d — the per-request cap is not bounding the TMDB fan-out", got, maxSeriesCatalogSyncsPerRead)
	}

	// Same instant: the first 20 are still fresh, so their slots are free for
	// the ones the cap skipped.
	syncSeriesCatalogForRead(env.ctx, sess, env.lib, list, now)
	if got := env.seriesWithEpisodeRows(t); got != total {
		t.Fatalf("after a second read %d of %d series are populated — capped series must be picked up by a later load, not stranded", got, total)
	}
}

// TestSeriesCatalogReadPathHonoursItsDeadline is the fourth bound (2026-08-02).
// The per-series cap bounds the wrong unit for a synchronous request: one
// series costs one SeasonDetails call PER KNOWN SEASON, so twenty long-running
// shows is 100+ sequential TMDB round trips inside one page load with nothing
// bounding the total. seriesCatalogReadBudget bounds the wall clock instead.
//
// Driven by an ALREADY-CANCELLED context rather than a slow fake server: the
// loop's exit condition is ctx.Err() != nil either way, and a deadline that has
// already passed reaches it deterministically with no sleep and no flake.
//
// The second assertion is the one that catches the subtle regression. Moving
// the ctx.Err() check to AFTER markSeriesCatalogSynced (or folding it into the
// loop condition alongside the mark) would still stop the loop, so a
// zero-episode-rows assertion alone keeps passing — while every unattempted
// series is silently marked fresh for six hours and starved of its catalog. The
// follow-up call with a live context is what proves the mark never happened.
func TestSeriesCatalogReadPathHonoursItsDeadline(t *testing.T) {
	now := time.Now()
	env, sess := readSyncEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, 3)},
	}})
	for i := 0; i < 3; i++ {
		trackMonitoredSeries(t, env, airDateTMDBID+i, fmt.Sprintf("Show %02d", i))
	}
	list := env.allSeries(t)

	spent, cancel := context.WithCancel(env.ctx)
	cancel()
	syncSeriesCatalogForRead(spent, sess, env.lib, list, now)

	if got := env.seriesWithEpisodeRows(t); got != 0 {
		t.Fatalf("%d series were synced after the budget was spent — the read path has no wall-clock bound, so a slow TMDB blocks the whole page load", got)
	}

	// Nothing was attempted, so nothing may have been marked fresh: a live
	// call at the same instant must still sync all three.
	syncSeriesCatalogForRead(env.ctx, sess, env.lib, list, now)
	if got := env.seriesWithEpisodeRows(t); got != len(list) {
		t.Fatalf("%d of %d series synced on the next read — a series the deadline skipped was marked fresh anyway, so it is starved of its catalog for the whole TTL", got, len(list))
	}
}

// TestSeriesCatalogReadPathNeverDiscoversOrMonitors is the constraint that
// matters most for safety: the monitored flag is what makes a season's aired
// episodes auto-searchable, so a browsing view that set it would turn opening a
// tab into granting dispatch authority. discovery is hardcoded false in the
// wrapper — the operator's new-season discovery toggle is deliberately not even
// read here, which is why it is switched ON for this test.
func TestSeriesCatalogReadPathNeverDiscoversOrMonitors(t *testing.T) {
	now := time.Now()
	env, sess := readSyncEnv(t, map[int][]fakeTMDBEpisode{
		1: {{Number: 1, Name: "One", AirDate: dayOffset(now, -30)}},
		2: {{Number: 1, Name: "Two-One", AirDate: dayOffset(now, 7)}},
	})
	env.setDiscovery(t, true)
	series := trackMonitoredSeries(t, env, airDateTMDBID, airDateTitle)

	syncSeriesCatalogForRead(env.ctx, sess, env.lib, env.allSeries(t), now)

	if !env.hasEpisodeRow(t, series.ID, 1, 1) {
		t.Fatalf("the monitored season was not synced — the read path did nothing")
	}
	if env.hasEpisodeRow(t, series.ID, 2, 1) {
		t.Fatalf("season 2 gained episode rows — the read path ran discovery, which it must never do")
	}
	monitored, err := env.lib.MonitoredSeasons(env.ctx, series.ID)
	if err != nil {
		t.Fatalf("reading monitored seasons: %v", err)
	}
	if monitored[2] {
		t.Fatalf("season 2 was monitored by a READ — that grants it auto-search authority, which only an operator may do")
	}
}

// TestSeriesCatalogReadPathNeedsNoProwlarr proves the read path inherits none of
// monitorAirDates' dispatch dependencies. That guard is
// `sess.TMDB == nil || sess.Prowlarr == nil`, and it lives in monitorAirDates,
// the CALLER — a Prowlarr-less install can still browse upcoming episodes.
func TestSeriesCatalogReadPathNeedsNoProwlarr(t *testing.T) {
	now := time.Now()
	env, sess := readSyncEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, 3)},
	}})
	series := trackMonitoredSeries(t, env, airDateTMDBID, airDateTitle)

	sess.Prowlarr = nil
	syncSeriesCatalogForRead(env.ctx, sess, env.lib, env.allSeries(t), now)

	if !env.hasEpisodeRow(t, series.ID, 1, 1) {
		t.Fatalf("no episode rows were written without Prowlarr — the read path picked up a dispatch-only dependency it has no use for")
	}
}

// TestSeriesCatalogReadPathWithoutTMDBIsInert is the nil guard a handler needs.
// With discovery hardcoded off the wrapper never reaches TVDetails, but it does
// reach SeasonDetails, so an unconfigured TMDB would be a nil dereference inside
// a page load. Degrading to "serve whatever the library already has" is the
// contract; panicking or erroring is not.
func TestSeriesCatalogReadPathWithoutTMDBIsInert(t *testing.T) {
	now := time.Now()
	env, sess := readSyncEnv(t, map[int][]fakeTMDBEpisode{1: {
		{Number: 1, Name: "One", AirDate: dayOffset(now, 3)},
	}})
	series := trackMonitoredSeries(t, env, airDateTMDBID, airDateTitle)

	sess.TMDB = nil
	syncSeriesCatalogForRead(env.ctx, sess, env.lib, env.allSeries(t), now)
	syncSeriesCatalogForRead(env.ctx, nil, env.lib, env.allSeries(t), now)

	if env.hasEpisodeRow(t, series.ID, 1, 1) {
		t.Fatalf("episode rows appeared with no TMDB client configured — impossible, so the fixture is lying")
	}
}
