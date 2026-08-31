// This file is the monitor-on series backfill: enabling season monitoring kicks
// an immediate one-shot search for already-aired missing episodes of those
// seasons, instead of waiting on the shared air-date cycle. That cycle is
// capped at the configured cycle slots and ordered oldest-air-date-first across
// the whole library, so a multi-thousand-episode classic can exhaust every
// cycle's slots and a newly monitored modern series never cues.
//
// It is a separate file because airdatemonitor.go is deliberately free of
// goroutine launches (TestAirDateMonitorHasNoSchedulerOfItsOwn) — this file
// owns the fire-and-forget goroutine the monitor-enable UX needs.
//
// The background pass still handles NEW episodes after they air; this kick only
// covers the already-aired gap at the moment monitoring turns on.
package api

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
	"github.com/labbersanon/sakms/internal/webhooks"
)

const (
	// maxSeriesBackfillGrabsPerKick is the unconfigured per-kick bound. runOnce
	// spends the same budget airDateDispatchCaps gives a background cycle, so a
	// single monitor-on click cannot flood Prowlarr harder than one cycle.
	maxSeriesBackfillGrabsPerKick = defaultAutoGrabSlotsPerCycle

	// seriesBackfillTimeout is the wall-clock bound on one kick's sync+dispatch.
	// Detached from the HTTP request (see kick); without this a hung TMDB or
	// indexer would leave a goroutine alive indefinitely.
	seriesBackfillTimeout = 5 * time.Minute
)

// seriesBackfill owns the monitor-on immediate search. kick no-ops on a nil
// receiver, so a seasonCatalog with backfill unset stays inert.
type seriesBackfill struct {
	deps     AutoGrabDeps
	build    sessionBuilderFunc
	libStore *library.Store

	mu      sync.Mutex
	running map[int64]bool
	pending map[int64]map[int]bool

	// idleNotify is test-only: fired when a series' run exits after draining
	// pending. Nil in production.
	idleNotify func()
}

// newSeriesBackfill builds the backfill wired into seasonCatalog from NewMux.
// Returns nil when a required store is missing so every nil-store NewMux call
// site gets an inert catalog.backfill rather than a panic on first use.
func newSeriesBackfill(httpClient *http.Client, connStore *connections.Store,
	scStore *serviceconn.Store, settingsStore *settings.Store, grabsStore *grabs.Store,
	whStore *webhooks.Store, nzb *usenet.Manager, releaseStore *adultnewest.ReleaseStore,
	dl *downloader.Manager, libStore *library.Store) *seriesBackfill {
	if libStore == nil || grabsStore == nil || settingsStore == nil || connStore == nil {
		return nil
	}
	return &seriesBackfill{
		deps: AutoGrabDeps{
			SettingsStore: settingsStore, NZB: nzb,
			GrabsStore: grabsStore, Webhooks: whStore, ReleaseStore: releaseStore,
		},
		build: func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
			// dl must be passed: a series air-date search can win a torrent, and
			// dispatchToDownloadClient rejects a torrent on a nil Downloader.
			// seasonCatalog's TMDB-only Build calls pass nil on purpose — this
			// path dispatches and must not copy that.
			return mode.Build(ctx, connStore, scStore, settingsStore, httpClient, dl, m)
		},
		libStore: libStore,
		running:  map[int64]bool{},
		pending:  map[int64]map[int]bool{},
	}
}

// kick schedules a detached backfill for seriesID's given seasons. It never
// blocks and takes no context — r.Context() dies when the handler returns, so
// accepting one would make the mistake unrepresentable only by documentation.
//
// Concurrent kicks for the same series coalesce into pending rather than
// dropping: a bare drop would lose the second click's seasons, because the
// running pass is filtered to the first click's map.
//
// seasons is copied by mergeSeasonSet; callers may retain and mutate their own.
func (b *seriesBackfill) kick(seriesID int64, seasons map[int]bool) {
	if b == nil || seriesID == 0 || len(seasons) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	alreadyRunning := b.running[seriesID]
	mergeSeasonSet(b.pending, seriesID, seasons)
	if alreadyRunning {
		return
	}
	b.running[seriesID] = true
	go b.run(seriesID)
}

// run drains pending batches for one series. Take-or-clear-running happens under
// a single lock so a kick cannot land in an orphan window and never start.
// Panic in runOnce is recovered per batch so a bad season cannot wedge the
// series permanently or discard coalesced pending.
func (b *seriesBackfill) run(seriesID int64) {
	for {
		b.mu.Lock()
		seasons := b.pending[seriesID]
		if len(seasons) == 0 {
			delete(b.pending, seriesID)
			delete(b.running, seriesID)
			notify := b.idleNotify
			b.mu.Unlock()
			if notify != nil {
				notify()
			}
			return
		}
		delete(b.pending, seriesID)
		b.mu.Unlock()

		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("series backfill: series %d panicked: %v", seriesID, rec)
				}
			}()
			b.runOnce(seriesID, seasons)
		}()
	}
}

// runOnce is one sync+dispatch pass for the seasons the operator just enabled.
//
// excluded is deliberately nil: NewMux carries no *excludes.Store (requests live
// on a separate mux), and a nil map makes the excluded[...] check inert. That is
// correct here — monitor-enable is an operator click that supersedes a prior
// worklist exclusion for this immediate search. The background cycle still
// honors excludes on later passes.
//
// Season 0 (Specials) is included when present in seasons, matching the
// background cycle's eligibleEpisodes. syncSeriesCatalog's discovery loop skips
// Season 0 for auto-monitor-on-discovery only; that asymmetry must not be
// "aligned" here.
func (b *seriesBackfill) runOnce(seriesID int64, seasons map[int]bool) {
	ctx, cancel := context.WithTimeout(context.Background(), seriesBackfillTimeout)
	defer cancel()

	// Gate FIRST — before any TMDB call. On a default install auto-grab is off,
	// so burning SeasonDetails per monitor click only to discover Gated on the
	// first candidate is wasted work.
	enabled, err := b.deps.SettingsStore.GetBool(ctx, usenetAutoGrabEnabledKey, false)
	if err != nil {
		log.Printf("series backfill: reading usenet auto-grab toggle: %v — skipping", err)
		return
	}
	if !enabled {
		log.Printf("series backfill: usenet auto-grab is off — skipping series %d", seriesID)
		return
	}

	series, err := b.libStore.GetSeries(ctx, seriesID)
	if err != nil {
		log.Printf("series backfill: loading series %d (%T)", seriesID, rootCause(err))
		return
	}

	sess, err := b.build(ctx, mode.Series)
	if err != nil {
		log.Printf("series backfill: building the series session failed (%T) — skipping series %d", rootCause(err), seriesID)
		return
	}
	if sess.TMDB == nil || sess.Prowlarr == nil {
		log.Printf("series backfill: TMDB or Prowlarr isn't configured — skipping series %d", seriesID)
		return
	}

	// discovery=false: discovery true would SetSeasonMonitored for every
	// zero-row TMDB season and grant dispatch authority the operator never
	// asked for. Do NOT call syncSeriesCatalogForRead — its 6h freshness cache
	// would skip the sync after a Calendar/Library page load, exactly when the
	// operator is most likely to click monitor.
	syncSeriesCatalog(ctx, sess, b.libStore, *series, false)
	limit, _ := airDateDispatchCaps(ctx, b.deps.SettingsStore)
	dispatchAirDateGrabsScoped(ctx, b.deps, sess, b.libStore,
		[]library.Series{*series}, seasons,
		limit, 0, nil, time.Now())
}

func mergeSeasonSet(dst map[int64]map[int]bool, seriesID int64, seasons map[int]bool) {
	cur := dst[seriesID]
	if cur == nil {
		cur = map[int]bool{}
		dst[seriesID] = cur
	}
	for sn, on := range seasons {
		if on {
			cur[sn] = true
		}
	}
}
