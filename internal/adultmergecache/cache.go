// Package adultmergecache is a DELIBERATE, on-by-default background scheduler —
// a considered exception to this project's "manual by default, no background
// pollers" convention (see CLAUDE.md), of the same scan/propose/passive-flag
// class the 2026-07-23 carve-out permits: it only POPULATES a read cache, never
// mutates the library, never grabs. It precomputes the first
// PrecomputePages pages of Adult Discover's merged Performers and merged Studios
// rows (the PRE-availability-filter merged card lists) and stores them in the
// additive adult_merged_row_cache table (migration 0045), so the two read
// handlers can serve those pages instantly instead of running the synchronous
// O(n×m) fuzzy merge live per request.
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

// PrecomputePages is how many leading pages (per row type) the scheduler
// precomputes AND the read handlers serve from cache. Exported so package api's
// read handlers gate on the exact same K the precompute loop uses — a page
// beyond K must never be served from a stale cache row (plan R-h), so the
// handler's `page <= PrecomputePages` gate needs this value single-sourced here.
const PrecomputePages = 3

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
func Run(ctx context.Context, interval time.Duration, httpClient *http.Client, connStore *connections.Store, store *Store) {
	if interval <= 0 {
		return // opt-in gate: off only when explicitly disabled
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("adultmergecache: background merged-row precompute enabled (every %s) — a deliberate on-by-default exception to manual-by-default, immediate boot poll", interval)
	runCycle(ctx, httpClient, connStore, store)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCycle(ctx, httpClient, connStore, store)
		}
	}
}

// runCycle performs exactly one precompute pass and returns — extracted from
// Run's ticker loop so tests exercise it directly rather than waiting on a wall
// clock (recheck/adultnewest convention). It precomputes Performers then Studios;
// a whole-row-type failure (e.g. a connStore read error) is logged and never
// aborts the other row type. Per-page fault isolation lives inside each
// precompute function.
func runCycle(ctx context.Context, httpClient *http.Client, connStore *connections.Store, store *Store) {
	if err := precomputePerformers(ctx, httpClient, connStore, store); err != nil {
		log.Printf("adultmergecache: precompute performers: %v", err)
	}
	if err := precomputeStudios(ctx, httpClient, connStore, store); err != nil {
		log.Printf("adultmergecache: precompute studios: %v", err)
	}
}
