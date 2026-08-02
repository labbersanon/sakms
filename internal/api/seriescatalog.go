// This file is the series episode-catalog sync, extracted from airdatemonitor.go
// so it can serve TWO callers instead of one. The function itself moved
// VERBATIM — this is a relocation, not a rewrite.
//
// WHY IT MOVED. Nothing in this codebase ever wrote a library_episodes row with
// an empty file_path after internal/sonarrimport was deleted, and nothing ever
// seeded air_date at all, so syncSeriesCatalog is what makes MissingEpisodes
// have any data. Its only caller was monitorAirDates, which runs inside the
// usenet retry cycle, which does not start at all while unattended auto-grab is
// off — the default on every install. Any read-only surface that wants episode
// air dates (Calendar's Upcoming/Series view) would therefore render
// permanently empty. The decision (Wade, 2026-08-02) was that METADATA becomes
// always-available while DISPATCH stays gated exactly as before; this file is
// the metadata half.
//
// WHY A NEW FILE RATHER THAN AN EXPORT FROM airdatemonitor.go.
// airdatemonitor_static_test.go asserts that airdatemonitor.go declares no
// goroutine launch, no stdlib timer/ticker, no interval-setting access, and no
// const whose NAME CONTAINS "Interval", and that cmd/sakms/main.go references
// nothing declared in it. The bounding this read path requires — a TTL and a
// per-request cap — would trip that test from inside airdatemonitor.go: a const
// named anything like calendarSyncInterval fails it by SUBSTRING MATCH ON THE
// NAME ALONE, whatever it is actually used for. That test covers exactly one
// file by name, so a new file trips none of it. Keep it that way: the TTL and
// the cap live HERE, and no "Interval"-named const or settings-backed TTL is
// ever declared in airdatemonitor.go.
//
// THE TWO CALLERS, and the two things the read-path caller must NOT inherit:
//
//  1. monitorAirDates (airdatemonitor.go) — unchanged, still passes the
//     operator's new-season discovery toggle through, still guarded by its own
//     `sess.TMDB == nil || sess.Prowlarr == nil` early return. That guard lives
//     in the CALLER and stays there.
//  2. A read path, through syncSeriesCatalogForRead below, which hardcodes
//     discovery=false and requires TMDB only. Prowlarr is a DISPATCH
//     dependency; a browsing view that cannot dispatch must not inherit it, or
//     the view is empty on a Prowlarr-less install for no reason.
package api

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

const (
	// seriesCatalogFreshFor is how long one series' catalog is considered fresh
	// enough to skip on a read path. A plain time.Duration const, deliberately
	// NOT a settings key: a settings key is an operator-facing knob nobody asked
	// for, and reading one through loadIntervalSeconds is the exact pattern the
	// static test forbids anywhere near this code.
	//
	// Six hours because TMDB episode data for an already-tracked series changes
	// on the order of days, and the worst case of being stale is that one
	// upcoming episode shows a day late in a browsing view — not a missed grab,
	// which is monitorAirDates' job and is not affected by this cache at all.
	seriesCatalogFreshFor = 6 * time.Hour

	// maxSeriesCatalogSyncsPerRead bounds how many series ONE read may sync,
	// mirroring maxAirDateGrabsPerCycle's reasoning (airdatemonitor.go:76-89):
	// each sync costs at least one TMDB SeasonDetails call per season,
	// SEQUENTIALLY — never an errgroup fan-out, which would recreate the exact
	// concurrent-query pattern that got the per-card availability badge
	// permanently banned. A library with hundreds of tracked series must not
	// turn one page load into hundreds of round trips.
	//
	// Series beyond the cap are simply synced by a later request: skipped-as-
	// fresh series do not consume a slot, so successive loads advance through
	// the list. The view degrades to partially-populated data, never to a stall
	// and never to an error.
	maxSeriesCatalogSyncsPerRead = 20

	// seriesCatalogReadBudget is the WALL-CLOCK bound on one read's whole sync
	// loop, and it exists because the cap above bounds the wrong unit for a
	// synchronous request.
	//
	// maxSeriesCatalogSyncsPerRead bounds SERIES, but one series costs one
	// SeasonDetails call PER KNOWN SEASON — so twenty long-running shows is
	// comfortably 100+ sequential TMDB round trips inside a single page-load
	// request, with nothing bounding the total. A caller's r.Context() carries
	// no deadline of its own, so without this the Calendar page load blocks for
	// as long as TMDB takes to answer all of them.
	//
	// A wall-clock deadline rather than a threaded call counter, deliberately:
	// syncSeriesCatalog soft-fails internally and reports nothing back, and
	// giving it a return value to carry a budget would break the verbatim
	// relocation this file's header documents. A deadline needs no change to it
	// at all, and bounds the thing that actually matters to a page load.
	//
	// Eight seconds is chosen as a REQUEST-scoped bound, not by analogy to any
	// background cycle: maxAirDateGrabsPerCycle's precedent is a 24h loop where
	// latency is free, and copying its generosity here would be the mistake.
	// The degradation is the same as the cap's — fewer series synced, picked up
	// by a later request — never a stall and never an error.
	seriesCatalogReadBudget = 8 * time.Second
)

// seriesCatalogFreshness records when each series was last synced by a read
// path. Package-level for the same reason internal/tmdb's response cache is:
// every handler builds its own mode.Session per HTTP request, so state hung off
// a session or a store would live exactly as long as one request and would
// never produce a single hit.
//
// It is advisory only. Losing it (a restart, a test reset) costs one extra sync
// per series, never correctness.
var seriesCatalogFreshness = struct {
	mu     sync.Mutex
	synced map[int64]time.Time
}{synced: map[int64]time.Time{}}

// resetSeriesCatalogFreshness clears the cache. Test-only, and mandatory in any
// test that exercises the TTL or the cap: tests build a fresh temp DB each, so
// the FIRST tracked series is id 1 in every one of them, and a cache surviving
// between tests would make one test's series look fresh to the next — a
// vacuous pass, the same isolation hazard internal/tmdb's ResetDefaultCache
// exists for.
func resetSeriesCatalogFreshness() {
	seriesCatalogFreshness.mu.Lock()
	defer seriesCatalogFreshness.mu.Unlock()
	seriesCatalogFreshness.synced = map[int64]time.Time{}
}

// syncSeriesCatalogForRead is the bounded, read-path entry point: it syncs the
// catalogs of as many of the given series as its bounds allow and returns,
// having never failed. A read path's contract is that the view renders — a TMDB
// outage degrades to "whatever the library already has," never to an error page
// and never to a blank one.
//
// Four bounds, all required, none optional:
//
//  1. FRESHNESS. A series synced within seriesCatalogFreshFor is skipped and
//     does not consume a cap slot.
//  2. THE CAP. At most maxSeriesCatalogSyncsPerRead series are synced per call.
//  3. SEQUENTIAL. One series at a time, in order. No goroutines, no errgroup.
//  4. THE DEADLINE. The whole loop runs under seriesCatalogReadBudget, so a
//     slow or unresponsive TMDB bounds the page load rather than the other way
//     around. The cap alone cannot do this: it bounds SERIES, while the cost is
//     per SEASON. Added 2026-08-02 — see seriesCatalogReadBudget's own doc.
//
// discovery is hardcoded false and is deliberately NOT a parameter. With
// discovery true, syncSeriesCatalog calls SetSeasonMonitored(..., true) for
// every TMDB season with no episode rows, and the monitored flag is the
// top-of-filter gate that makes a season's aired episodes auto-searchable
// (eligibleEpisodes). Passing it through would mean OPENING A BROWSING TAB
// GRANTS SEASONS DISPATCH AUTHORITY. Discovery is a monitoring decision, not
// metadata; a read path must never call SetSeasonMonitored. Making it
// unreachable here is worth more than a comment telling a caller not to.
//
// It requires TMDB and nothing else. Prowlarr is monitorAirDates' requirement,
// enforced in monitorAirDates, because that pass exists to dispatch; a browsing
// view that cannot dispatch must not inherit a dependency it has no use for.
func syncSeriesCatalogForRead(ctx context.Context, sess *mode.Session, libStore *library.Store,
	seriesList []library.Series, now time.Time) {

	if sess == nil || sess.TMDB == nil || libStore == nil {
		return
	}

	// The deadline wraps the whole loop, never one series: the point is to bound
	// the REQUEST, and a per-series timeout would let twenty series each take
	// the full budget.
	ctx, cancel := context.WithTimeout(ctx, seriesCatalogReadBudget)
	defer cancel()

	synced := 0
	for _, series := range seriesList {
		if synced >= maxSeriesCatalogSyncsPerRead {
			return
		}
		// Checked BEFORE markSeriesCatalogSynced below, which is the whole
		// reason this is a statement rather than a loop condition: marking a
		// series this call never attempted would make it look fresh for the next
		// six hours, so a budget exhausted on a slow TMDB would silently starve
		// every remaining series of its catalog rather than deferring it.
		if ctx.Err() != nil {
			return
		}
		if seriesCatalogIsFresh(series.ID, now) {
			continue
		}
		// Marked BEFORE the call and unconditionally: syncSeriesCatalog soft-
		// fails internally and reports nothing back (giving it a return value
		// would break the verbatim relocation), so a series whose TMDB calls
		// fail every time would otherwise consume a cap slot on every single
		// request forever and starve every series after it in the list.
		//
		// It is also what stops two concurrent page loads from syncing the same
		// series twice: the mark is taken under the mutex BEFORE the multi-second
		// TMDB work, so the second load sees it as fresh. "Tidying" this into a
		// mark-on-success would silently reintroduce a duplicate concurrent
		// fan-out on a read path.
		markSeriesCatalogSynced(series.ID, now)
		synced++
		syncSeriesCatalog(ctx, sess, libStore, series, false)
	}
}

// seriesCatalogIsFresh reports whether this series was synced recently enough to
// skip. A zero/absent entry is never fresh.
func seriesCatalogIsFresh(seriesID int64, now time.Time) bool {
	seriesCatalogFreshness.mu.Lock()
	defer seriesCatalogFreshness.mu.Unlock()
	last, ok := seriesCatalogFreshness.synced[seriesID]
	if !ok {
		return false
	}
	return now.Sub(last) < seriesCatalogFreshFor
}

// markSeriesCatalogSynced records a sync attempt for this series.
func markSeriesCatalogSynced(seriesID int64, now time.Time) {
	seriesCatalogFreshness.mu.Lock()
	defer seriesCatalogFreshness.mu.Unlock()
	seriesCatalogFreshness.synced[seriesID] = now
}

// syncSeriesCatalog records TMDB's episode catalog for one tracked series, so
// the episodes TMDB knows about but that are not on disk exist as fileless rows
// with a real air_date. It is what makes MissingEpisodes and this feature's
// air-date predicate have any data at all.
//
// Scoped to TRACKED SERIES, not to MONITORED SEASONS, and that is load-bearing
// rather than sloppy: gating the sync on the monitored flag creates a dead
// chicken-and-egg — an unmonitored season would never gain episode rows, so
// ListSeasonStates would never show it, so the operator could never find it to
// monitor it. Monitoring gates SEARCHING, never METADATA. This is the first
// thing a reviewer will want to "tighten"; don't.
//
// Soft-fail per series: one series' TMDB error logs and the cycle moves to the
// next, matching retryDueGrabs' fault isolation. One bad series never kills a
// cycle.
func syncSeriesCatalog(ctx context.Context, sess *mode.Session, libStore *library.Store,
	series library.Series, discovery bool) {

	// library_series.tmdb_id is INTEGER NOT NULL with no positive constraint,
	// and every TMDB-facing path in this package already treats 0 as "no id".
	// GET /tv/0 and GET /tv/0/season/1 are wasted round trips that return
	// nothing usable, once per cycle, per such series, forever.
	if series.TMDBID == 0 {
		return
	}

	rows, err := libStore.ListEpisodes(ctx, series.ID)
	if err != nil {
		log.Printf("air-date monitor: listing episodes for series %d: %v", series.ID, err)
		return
	}
	known := map[int]bool{}
	syncSeasons := []int{}
	for _, ep := range rows {
		if known[ep.SeasonNumber] {
			continue
		}
		known[ep.SeasonNumber] = true
		syncSeasons = append(syncSeasons, ep.SeasonNumber)
	}

	// An EXPLICITLY MONITORED season with zero episode rows is synced too,
	// independently of the discovery toggle. This is the same "monitoring gates
	// SEARCHING, never METADATA" rule this function's doc already states, applied
	// to the case that became reachable when seasonCatalog started surfacing
	// TMDB-only seasons in the season panel: an operator can now monitor a season
	// nothing was ever downloaded from, and with discovery off — the default —
	// that season would otherwise NEVER enter syncSeasons, never gain episode
	// rows, and therefore never be searched for. Monitoring it would be a
	// permanent no-op, which is exactly the dead chicken-and-egg the doc above
	// warns about, arrived at from the other direction.
	//
	// MonitoredSeasons, not SeasonMonitorFlags: an explicitly UN-monitored
	// episode-less season must not burn a SeasonDetails call every cycle. This
	// costs one extra call per monitored-but-episode-less season per cycle, and
	// only until that season gains rows — after the first successful sync it
	// arrives through ListEpisodes like any other.
	if monitoredSeasons, err := libStore.MonitoredSeasons(ctx, series.ID); err != nil {
		log.Printf("air-date monitor: reading monitored seasons for series %d: %v — syncing episode-backed seasons only this cycle", series.ID, err)
	} else {
		for season := range monitoredSeasons {
			if known[season] {
				continue
			}
			known[season] = true
			syncSeasons = append(syncSeasons, season)
		}
	}

	if discovery {
		// A TVDetails failure skips DISCOVERY ONLY and falls through to the sync
		// loop below — it must not return. Discovery is the only half that needs
		// TMDB's season list at all; the known-season sync is always-on and needs
		// nothing from it, so abandoning the whole function on a transient
		// discovery failure would stop this series' episode catalog (and
		// therefore MissingCount and the season UI) from updating for reasons
		// that have nothing to do with it.
		// TVDetails returns a zero-valued TVDetails on error, whose Seasons is
		// nil, so the loop below is a no-op on the failure path — but the nil is
		// stated here rather than relied on implicitly.
		details, err := sess.TMDB.TVDetails(ctx, series.TMDBID)
		if err != nil {
			log.Printf("air-date monitor: loading TMDB details for series %d: %v — syncing known seasons only this cycle", series.ID, err)
			details.Seasons = nil
		}
		for _, season := range details.Seasons {
			if known[season.SeasonNumber] {
				continue
			}
			// Season 0 (Specials) is excluded from auto-monitor-on-discovery
			// only — it is still synced when it already has episode rows, and
			// still manually monitorable. Auto-monitoring Specials on renewal
			// would surprise an operator who asked for "Season 5 onward";
			// Sonarr's own default is the same.
			if season.SeasonNumber == 0 {
				continue
			}
			// Monitored BEFORE its episodes are synced, so the same cycle's
			// detection pass already sees it as monitored. This is the operator's
			// explicit override of the conservative default — do not "restore"
			// a manual opt-in here.
			if err := libStore.SetSeasonMonitored(ctx, series.ID, season.SeasonNumber, true); err != nil {
				log.Printf("air-date monitor: monitoring discovered season %d of series %d: %v", season.SeasonNumber, series.ID, err)
				continue
			}
			known[season.SeasonNumber] = true
			syncSeasons = append(syncSeasons, season.SeasonNumber)
		}
	}

	sort.Ints(syncSeasons)
	for _, season := range syncSeasons {
		episodes, err := sess.TMDB.SeasonDetails(ctx, series.TMDBID, season)
		if err != nil {
			log.Printf("air-date monitor: loading TMDB season %d of series %d: %v", season, series.ID, err)
			continue
		}
		for _, e := range episodes {
			// UpsertEpisodeCatalog, never UpsertEpisode: the latter overwrites
			// EVERY column on conflict, so a zero-valued Episode would blank the
			// file_path of every already-tracked episode this sync touches —
			// turning downloaded episodes into missing ones and then auto-grabbing
			// duplicates of them next cycle.
			if err := libStore.UpsertEpisodeCatalog(ctx, series.ID, season, e.EpisodeNumber, e.Name, e.AirDate); err != nil {
				log.Printf("air-date monitor: recording s%02de%02d of series %d: %v", season, e.EpisodeNumber, series.ID, err)
			}
		}
	}
}
