// Package adultmergecache is a DELIBERATE, on-by-default background scheduler —
// a considered exception to this project's "manual by default, no background
// pollers" convention (see CLAUDE.md), of the same scan/propose/passive-flag
// class the 2026-07-23 carve-out permits: it only POPULATES a read cache, never
// mutates the library, never grabs. It precomputes the leading pages
// (PrecomputePerformerPages / PrecomputeStudioPages) of Adult Discover's merged
// Performers and merged Studios rows (the PRE-availability-filter merged card
// lists) and stores them in the additive adult_merged_row_cache table
// (migration 0045), so the two read handlers can serve those pages instantly
// instead of running the synchronous O(n×m) fuzzy merge live per request.
//
// It is intentionally self-contained and independently deletable: to remove it
// entirely, delete this package, its single go adultmergecache.Run(...) line and
// store construction in cmd/sakms/main.go, its NewMux param, and migration 0045.
// It shares no state or machinery with any workflow package. It builds its own
// tolerant TPDB/StashDB clients per cycle straight from connStore (see
// precompute.go) and owns its own bounded HTTP client via main — it depends on
// nothing a request handler sets up, and it touches TPDB/StashDB ONLY, never
// Prowlarr (Discover's non-negotiable rule).
//
// Config plumbing mirrors internal/recheck (settings-key interval, tolerant
// LoadInterval, single go X.Run(...) line); the default VALUE and the immediate
// boot poll mirror internal/adultnewest (on-by-default at 24h, boot poll to
// survive this deployment's frequent-redeploy cadence). Unlike recheck/
// adultnewest, Run takes a pre-resolved interval (not settingsStore), so it does
// NOT live-retune per tick — the interval is fixed at boot and changing it needs
// a restart. There is deliberately no settings endpoint for this cadence yet
// (plan OQ3, deferred); without one, there is nothing to retune at runtime.
package adultmergecache

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/imageproxy"
	"github.com/labbersanon/sakms/internal/settings"
)

// IntervalSettingKey is the settings key holding the precompute cadence, in
// whole seconds. Genuinely unset (a fresh install) defaults ON at
// defaultIntervalHours; an explicit stored "0"/negative means off — same
// unset-vs-explicit-0 distinction as adultnewest.IntervalSettingKey (NOT
// recheck's off-by-default shape). No internal/api handler reads or writes this
// key today (no settings endpoint yet — plan OQ3), keeping the package
// independently deletable.
const IntervalSettingKey = "adult_merged_cache_interval_seconds"

// defaultIntervalHours is the precompute cadence when IntervalSettingKey has
// never been set — 24 hours (plan OQ1), the same class as adultnewest.
const defaultIntervalHours = 24

// PrecomputePerformerPages and PrecomputeStudioPages are the per-entity leading
// page counts the scheduler precomputes AND the read handlers serve from cache.
// They are exported so package api's read handlers gate on the exact same K the
// precompute loop uses — a page beyond the matching K must never be served from a
// stale cache row (plan R-h), so each handler's `page <= Precompute{Performer,
// Studio}Pages` gate single-sources its bound here.
//
// The two differ deliberately (plan §2.5): performers reach 25 pages (→ 500 items
// at cachePerPage) because BrowsePerformers orders by relevance, not availability,
// so a grabbable performer can sit deep; studios cap at 5 pages (→ ~100 items),
// the STRUCTURAL MAX under tpdbrest.maxSitePagesPerRequest's 15 raw-page cap —
// going deeper returns empty pages, so maxSitePagesPerRequest is deliberately NOT
// raised (a rejected alternative in the plan's ADR).
const (
	PrecomputePerformerPages = 25
	PrecomputeStudioPages    = 5
)

// ImageCacheMaxBytesSettingKey holds the durable image cache's hard disk cap, in
// bytes. Unset (a fresh install) or malformed falls back to defaultImageCacheMaxBytes
// — the cap is a safety circuit breaker, so a bad value degrades to the default
// bound, never to "no cap" (mirrors LoadInterval's tolerant-parse shape, but with
// a safe default rather than off). No internal/api handler reads or writes this
// key today (no settings endpoint yet), keeping the package independently deletable.
const ImageCacheMaxBytesSettingKey = "adult_image_cache_max_bytes"

// defaultImageCacheMaxBytes is the disk cap when ImageCacheMaxBytesSettingKey is
// unset or malformed — 512 MB (plan §2.5). At ~150 KB/poster over 500 performers +
// 100 studios (~90 MB, or ~300 MB pessimistic) this never engages in the designed
// scope; it exists as a hard bound against an unbounded-growth cliff.
const defaultImageCacheMaxBytes int64 = 512 * 1024 * 1024

// cachePerPage is the page size every precomputed page is stored at. It MUST
// stay equal to internal/api's mergedBrowsePerPage (20): the read handler only
// serves from cache when the request's perPage matches this, else it falls back
// to live. Both are 20 today; if one changes, change the other or the cache
// simply never hits (safe, just slower).
const cachePerPage = 20

// LoadInterval reads IntervalSettingKey and returns it as a Duration. Genuinely
// unset (settings.ErrNotFound — a fresh install, or an install from before this
// default existed) returns defaultIntervalHours, so the cache populates out of
// the box; an explicitly stored "0"/negative means off; a real store error or a
// blank/non-integer value degrades to off (not a guessed default). This mirrors
// adultnewest.LoadInterval's shape exactly. main passes the result straight into
// Run.
func LoadInterval(ctx context.Context, settingsStore *settings.Store) time.Duration {
	v, err := settingsStore.Get(ctx, IntervalSettingKey)
	if errors.Is(err, settings.ErrNotFound) {
		return defaultIntervalHours * time.Hour
	}
	if err != nil {
		return 0 // a real store error degrades to off, not a guessed default
	}
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// LoadImageCacheMaxBytes reads ImageCacheMaxBytesSettingKey and returns the
// durable image cache's hard disk cap in bytes. Genuinely unset, a store error, a
// blank/non-integer value, or a non-positive value all degrade to
// defaultImageCacheMaxBytes (512 MB): unlike LoadInterval (where a bad value means
// "off"), the cap is a safety bound, so a malformed value must NEVER become "no
// cap" — it falls back to the default cap instead. Only a real positive integer
// overrides. main passes the result straight into Run. Never crashes on any input.
func LoadImageCacheMaxBytes(ctx context.Context, settingsStore *settings.Store) int64 {
	v, err := settingsStore.Get(ctx, ImageCacheMaxBytesSettingKey)
	if err != nil {
		return defaultImageCacheMaxBytes // unset or a store error → safe default cap
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		return defaultImageCacheMaxBytes // malformed/non-positive → safe default cap
	}
	return n
}

// Run drives the background precompute loop until ctx is cancelled. interval is
// the boot-time cadence (from LoadInterval); if it is <= 0 — which only happens
// when an operator has explicitly saved "0" — Run returns immediately and starts
// nothing, so main can call it unconditionally.
//
// When enabled it fires one IMMEDIATE boot poll before the ticker loop (mirrors
// adultnewest.Run): this deployment redeploys several times a day, so a 24h
// ticker with no initial tick would effectively never fire between redeploys —
// the boot poll is what actually populates the cache after each deploy. It does
// NOT live-retune per tick (the interval is fixed at boot; see the package doc),
// since no settings endpoint can change it at runtime yet. Each tick delegates to
// runCycle, kept separate and directly callable so its logic is testable without
// waiting on a wall clock (recheck/adultnewest convention).
// proxy is the shared durable-backed image proxy (US-6 wiring; nil-tolerant — a
// nil proxy runs metadata-only precompute with no image warming and no durable
// maintenance). capBytes is the pre-resolved durable-image-cache disk cap (from
// LoadImageCacheMaxBytes), threaded to runCycle where the every-cycle Reconcile +
// PruneToCap runs before warming.
func Run(ctx context.Context, interval time.Duration, capBytes int64, httpClient *http.Client, connStore *connections.Store, store *Store, proxy *imageproxy.Proxy) {
	if interval <= 0 {
		return // opt-in gate: off only when explicitly disabled
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("adultmergecache: background merged-row precompute enabled (every %s) — a deliberate on-by-default exception to manual-by-default, immediate boot poll", interval)
	runCycle(ctx, httpClient, connStore, store, proxy, capBytes)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCycle(ctx, httpClient, connStore, store, proxy, capBytes)
		}
	}
}

// runCycle performs exactly one precompute pass and returns — extracted from
// Run's ticker loop so tests exercise it directly rather than waiting on a wall
// clock (recheck/adultnewest convention). It precomputes Performers then Studios;
// a whole-row-type failure (e.g. a connStore read error) is logged and never
// aborts the other row type. Per-page fault isolation lives inside each
// precompute function.
func runCycle(ctx context.Context, httpClient *http.Client, connStore *connections.Store, store *Store, proxy *imageproxy.Proxy, capBytes int64) {
	// Durable maintenance runs at the START of EVERY cycle (boot poll AND every
	// tick), before any image warming, so the durable index matches disk and the
	// disk cap is enforced for this cycle (§2.5). A nil proxy has no durable store,
	// so there is nothing to maintain — the metadata-only path skips this entirely.
	warmProxy := proxy
	if proxy != nil {
		res, err := proxy.MaintainDurable(ctx, capBytes)
		if err != nil {
			// A maintenance error must not abort the cycle — metadata precompute is
			// the primary job and still runs; only the durable-image side is degraded.
			log.Printf("adultmergecache: durable maintenance: %v", err)
		}
		log.Printf("adultmergecache: durable reconcile: strayTemps=%d orphanRows=%d orphanFiles=%d usage=%d bytes cap=%d bytes", res.StrayTemps, res.OrphanRows, res.OrphanFiles, res.TotalBytes, capBytes)
		if res.OverCap {
			// Disk cap reached even after pruning: pause NEW image warming for the
			// remainder of this cycle (metadata caching is unaffected — precompute's
			// store.Put still runs). A nil warmProxy yields a nil imageWarmer, so no
			// image bytes are persisted; the next cycle re-evaluates from a fresh walk.
			log.Printf("adultmergecache: durable image cache at/over %d-byte cap after prune — pausing image warming for this cycle (metadata caching continues)", capBytes)
			warmProxy = nil
		}
	}
	if err := precomputePerformers(ctx, httpClient, connStore, store, warmProxy); err != nil {
		log.Printf("adultmergecache: precompute performers: %v", err)
	}
	if err := precomputeStudios(ctx, httpClient, connStore, store, warmProxy); err != nil {
		log.Printf("adultmergecache: precompute studios: %v", err)
	}
}
