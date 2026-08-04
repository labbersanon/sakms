package discoverrefresh

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// IntervalSettingKey is the settings key holding the global refresh cadence
// (§3.5), in whole seconds. One shared interval governs all three cached
// sources together — deliberately NOT adultnewest's split browse/feed
// dual-interval model (spec Round 4: "one global interval, simpler").
//
// Unlike recheck (off by default, opt-in), this scheduler defaults ON at
// defaultIntervalHours when the key has never been set: eliminating
// Discover's per-render external calls is the entire point of this feature,
// so an opt-in default would undermine it (spec Round 1). An explicit "0"
// still means off — see LoadInterval's four-case table.
const IntervalSettingKey = "discover_refresh_interval_seconds"

// defaultIntervalHours is the refresh cadence used when IntervalSettingKey
// has never been set — 24 hours, matching internal/adultnewest's precedent
// (spec Round 2) rather than something shorter tuned to TMDB/Trakt's cadence.
const defaultIntervalHours = 24

// LoadInterval mirrors internal/adultnewest.LoadInterval EXACTLY
// (internal/adultnewest/scan.go:141-154) — same four-case table, same
// unset-vs-explicit-0 distinction. A key that was NEVER saved (a fresh
// install, or an install from before this feature existed) is treated as ON
// at the default; a key explicitly saved as "0" means an operator turned it
// off, which must stay off:
//
//	settings.ErrNotFound (never saved)          -> defaultIntervalHours, ON
//	explicit "0"                                -> 0, off
//	blank / non-integer / negative               -> 0, off (unreachable via
//	                                                the Settings UI, which
//	                                                only ever sends an
//	                                                integer — the safer read
//	                                                of a value that
//	                                                shouldn't exist)
//	a real store error                          -> 0, off (not a guessed
//	                                                default)
func LoadInterval(ctx context.Context, settingsStore *settings.Store) time.Duration {
	v, err := settingsStore.Get(ctx, IntervalSettingKey)
	if errors.Is(err, settings.ErrNotFound) {
		return defaultIntervalHours * time.Hour
	}
	if err != nil {
		return 0
	}
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// loadIntervalSafely is LoadInterval with a nil-SettingsStore guard.
// settings.Store's Get dereferences its db field unconditionally, so a nil
// *settings.Store would panic settingsStore.Get rather than return
// settings.ErrNotFound — RefreshAll's "never panics" contract (§3.7) needs
// this indirection wherever Deps might arrive with a zero-value
// SettingsStore (a misconfigured caller, or a test's minimal Deps). Treated
// the same as genuinely-unset: on, at the default.
func loadIntervalSafely(ctx context.Context, d Deps) time.Duration {
	if d.SettingsStore == nil {
		return defaultIntervalHours * time.Hour
	}
	return LoadInterval(ctx, d.SettingsStore)
}

// dueForRefresh implements the force=false due-check (§3.5): a key is due if
// its row is absent, its refreshed_at is blank, or refreshed_at is older
// than now-interval. Every other Get outcome — including an error decoding a
// corrupt row — is also treated as due, since the safe reading of "cannot
// tell whether this is fresh" is "refresh it," not "assume it's fine."
func dueForRefresh(ctx context.Context, cache *Store, interval time.Duration, source, key string) bool {
	entry, err := cache.Get(ctx, source, key)
	if err != nil {
		return true // ErrNotFound (never cached), or any other Get failure
	}
	if entry.RefreshedAt == "" {
		return true
	}
	refreshedAt, err := time.Parse(timeLayout, entry.RefreshedAt)
	if err != nil {
		return true // an unparsable stamp cannot be trusted to be fresh
	}
	return time.Since(refreshedAt) >= interval
}

// inFlightKeys marks each (source, key) currently being refreshed by
// RefreshAll's per-key loops below — one entry per key actually in progress,
// taken and released around every refreshOneTMDBTarget/refreshOneSlider/
// RefreshTrakt call a cycle makes.
//
// It exists for §5.1's SECOND single-flight guard: a future single-key
// populate (RefreshSlider from the slider-create hook, RefreshTrakt from the
// Trakt-link hook) can call KeyInFlight before writing, and skip when the
// cycle currently in progress is about to write that same key anyway — the
// Put race Critic finding H3 identified.
//
// BE-8 SCOPE NOTE: this cycle already marks/clears every key it touches, so
// KeyInFlight reports true for the right duration. But RefreshSlider and
// RefreshTrakt (internal/discoverrefresh/sliders.go, trakt.go) do not yet
// CALL KeyInFlight themselves — wiring that consultation in, and its test
// T-12b, is explicitly out of BE-8's scope (a later task wires the
// lifecycle-hook side of §5.1). cycleRunning below is the only guard BE-8
// wires end-to-end.
var inFlightKeys sync.Map

// KeyInFlight reports whether (source, key) is currently being refreshed by
// an in-progress RefreshAll cycle. See inFlightKeys' doc comment for who is
// meant to call this and why no caller does yet.
func KeyInFlight(source, key string) bool {
	_, ok := inFlightKeys.Load(source + "\x00" + key)
	return ok
}

func markKeyInFlight(source, key string)  { inFlightKeys.Store(source+"\x00"+key, struct{}{}) }
func clearKeyInFlight(source, key string) { inFlightKeys.Delete(source + "\x00" + key) }

// refreshTMDBCategoriesDue is refreshTMDBCategories with the force=false
// per-key due-check spliced in: each of the six fixed targets is refreshed
// only when force is true or dueForRefresh says so, via
// refreshOneTMDBTarget (tmdbrows.go) — the same accumulate-and-Put body
// refreshTMDBCategories itself calls, so there is exactly one implementation
// of "how a TMDB target gets refreshed," not two that could drift.
func refreshTMDBCategoriesDue(ctx context.Context, d Deps, client *tmdb.Client, force bool, interval time.Duration) {
	for _, t := range tmdbTargets {
		key := t.cacheKey()
		if !force && !dueForRefresh(ctx, d.Cache, interval, "tmdb", key) {
			continue
		}
		markKeyInFlight("tmdb", key)
		refreshOneTMDBTarget(ctx, d, client, t)
		clearKeyInFlight("tmdb", key)
	}
}

// refreshSlidersDue is refreshSliders with the force=false per-key due-check
// spliced in, reusing refreshOneSlider (sliders.go) — already a complete,
// self-contained per-slider refresh — for each enabled slider that is due.
// The orphan sweep runs unconditionally afterward, exactly as refreshSliders
// runs it: it never calls upstream, so due-checking it buys nothing and
// skipping it would let orphaned rows survive a whole cycle longer.
func refreshSlidersDue(ctx context.Context, d Deps, client *tmdb.Client, force bool, interval time.Duration) error {
	if d.SlidersStore == nil {
		return nil
	}
	sliders, err := d.SlidersStore.List(ctx)
	if err != nil {
		return fmt.Errorf("listing discover sliders: %w", err)
	}
	keep := make([]int, 0, len(sliders))
	for _, sl := range sliders {
		if !sl.Enabled {
			continue
		}
		keep = append(keep, sl.ID)
		key := strconv.Itoa(sl.ID)
		if !force && !dueForRefresh(ctx, d.Cache, interval, sliderSource, key) {
			continue
		}
		markKeyInFlight(sliderSource, key)
		if err := refreshOneSlider(ctx, d, client, sl); err != nil {
			log.Printf("discoverrefresh: refreshing slider %d (%s): %v", sl.ID, sl.Title, err)
		}
		clearKeyInFlight(sliderSource, key)
	}
	if err := d.Cache.DeleteOrphanSliders(ctx, keep); err != nil {
		return err
	}
	return nil
}

// refreshTraktDue is RefreshTrakt with the force=false due-check spliced in.
// Trakt has exactly one key (traktCacheKey, the empty string — see trakt.go),
// so the due-check is a single dueForRefresh call rather than a loop.
//
// Claude 2026-08-03: calls the unexported refreshTrakt directly, not the
// exported RefreshTrakt (BE-16, plan §5.1).
// Reason: RefreshTrakt now consults KeyInFlight itself (the populate hook's
// guard, §5.1's second single-flight rule) — calling it here, AFTER this
// function has already marked the very same key in flight, would make it a
// permanent no-op and silently break the scheduled/boot-poll refresh path
// entirely (caught by TestRefreshAll_DueCheck). refreshTMDBCategoriesDue and
// refreshSlidersDue already call their per-key workers directly for the
// same reason — only the exported single-key entry points populate hooks
// use need the guard.
func refreshTraktDue(ctx context.Context, d Deps, force bool, interval time.Duration) {
	if d.TraktStore == nil || d.Cache == nil {
		return
	}
	if !force && !dueForRefresh(ctx, d.Cache, interval, sourceTrakt, traktCacheKey) {
		return
	}
	markKeyInFlight(sourceTrakt, traktCacheKey)
	if err := refreshTrakt(ctx, d); err != nil {
		log.Printf("discoverrefresh: trakt watchlist refresh failed: %v", err)
	}
	clearKeyInFlight(sourceTrakt, traktCacheKey)
}

// RefreshAll runs one full refresh cycle across exactly THREE sources — tmdb
// (refreshTMDBCategoriesDue), slider (refreshSlidersDue), and trakt
// (refreshTraktDue). It never returns an error and never panics the caller's
// goroutine (a recovered panic is logged, not propagated), matching
// adultnewest.runCycle/recheck.runCycle's posture: RefreshAll is called from
// Run's ticker loop, which must keep ticking no matter what one bad cycle
// does.
//
// # Stash-box is NOT a fourth source
//
// An earlier draft of this feature's plan (§3.1/§3.3, "stashboxrows.go")
// scoped a fourth source, refreshStashBox, for the StashDB/FansDB catalog
// rows. Commit e25274f deleted every stash-box browse route
// (internal/api/adultdiscover_stashbox.go and its five constructors) before
// this task landed, so there is nothing left for a fourth source to cache —
// refreshStashBox and stashboxrows.go were never written, and RefreshAll
// must not call a function that doesn't exist. If stash-box browse ever
// returns, restoring the fourth row in this doc comment's table and in
// RefreshAll's body is the reintegration point.
//
// # force
//
//   - false (the scheduled tick, and Run's due-checked boot poll, §3.5): a
//     key is refreshed only if its row is absent, its refreshed_at is blank,
//     or refreshed_at is older than now-interval (dueForRefresh). Interval
//     is loaded once per cycle via loadIntervalSafely, not per key — the
//     cadence is one global setting, not per-source.
//   - true (TriggerOnce's manual "refresh now", §5.1): every key, every
//     source, regardless of how recently it last succeeded. This is what
//     makes the manual trigger meaningful as a troubleshooting tool — a
//     stuck slider a moment after its own scheduled refresh must still be
//     repopulate-able on demand.
//
// # Fault isolation (§3.7), three levels
//
//  1. Per key — a failed fetch calls MarkFailure and the loop moves to the
//     next key (refreshOneTMDBTarget, refreshOneSlider, refreshTrakt each
//     do this on their own key).
//  2. Per source — buildBypassedTMDBClient failing (a connections.Store
//     error, not merely "not configured") skips BOTH tmdb-dependent sources
//     (tmdb rows AND sliders, since sliders resolve through the same TMDB
//     client) rather than aborting the whole cycle; an unconfigured source
//     (no TMDB connection, no Trakt credentials) is skipped silently with no
//     failure mark at all — not configured is not a failure.
//  3. Per cycle — this function's own top-level recover() and its "no
//     error return" signature.
//
// # Call budget (§3.6), priced for a force=true cycle, sequential throughout
//
//	tmdb list pages      6 categories x up to 6 raw pages   = <=36 (typ. 18)
//	tmdb HasUSRelease    2 categories x <=6 pages x 20 items = <=240 (errgroup(5), fail-open)
//	sliders              N enabled x up to 6 raw pages x (1 or 2) = <=12N (typ. 3N)
//	trakt                1
//
// Worst case with 5 sliders: ~300 external calls, once per defaultIntervalHours
// (24h) by default — a reduction versus today's live-on-every-render cost,
// where a single Discover page load already fires 6 TMDB list calls plus
// ~40 HasUSRelease calls, for every visit.
func RefreshAll(ctx context.Context, d Deps, force bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("discoverrefresh: RefreshAll cycle panicked and recovered: %v", r)
		}
	}()
	if d.Cache == nil {
		return // nothing to write to — a misconfigured Deps, not worth crashing over
	}

	var interval time.Duration
	if !force {
		interval = loadIntervalSafely(ctx, d)
	}

	client, err := buildBypassedTMDBClient(ctx, d)
	if err != nil {
		log.Printf("discoverrefresh: building tmdb client: %v", err)
		client = nil
	}
	if client != nil {
		refreshTMDBCategoriesDue(ctx, d, client, force, interval)
		if err := refreshSlidersDue(ctx, d, client, force, interval); err != nil {
			log.Printf("discoverrefresh: refreshing sliders: %v", err)
		}
	}
	refreshTraktDue(ctx, d, force, interval)
}

// cycleRunning is the CYCLE-scoped single-flight guard from §5.1: true for
// the duration of ANY RefreshAll, whether started by TriggerOnce, Run's boot
// poll, or a scheduled tick. The eventual triggerDiscoverRefreshHandler maps
// a failed CAS to 409 Conflict so a manual "refresh now" during an in-flight
// cycle is rejected rather than doubled.
//
// Claude 2026-08-03: held by Run as well as TriggerOnce (plan §5.1).
// Reason: an earlier BE-8 draft only gated TriggerOnce-vs-TriggerOnce, which
// left a scheduled tick free to overlap a manual trigger and double the
// upstream fan-out — the exact race §5.1 forbids.
// Troubleshooting: if a 409 fires with no operator click, a scheduled cycle
// is in flight; that is correct, not a stuck flag.
// Review if: RefreshAll is split into per-source entry points that need a
// finer guard — inFlightKeys already covers the per-key populate case.
var cycleRunning atomic.Bool

// withCycleRunning runs fn while holding cycleRunning. Returns false if a
// cycle is already in flight (fn is not called); true after fn completes.
func withCycleRunning(fn func()) bool {
	if !cycleRunning.CompareAndSwap(false, true) {
		return false
	}
	defer cycleRunning.Store(false)
	fn()
	return true
}

// TriggerOnce attempts to run one full, forced refresh cycle — §5.1's manual
// "refresh now." It is single-flight at the CYCLE scope: if ANY RefreshAll is
// already running (another TriggerOnce, a boot poll, or a scheduled tick),
// this call returns false immediately without starting a second one (the
// eventual triggerDiscoverRefreshHandler maps that to 409 Conflict); otherwise
// it holds cycleRunning for the cycle's whole duration, runs
// RefreshAll(ctx, d, force=true), clears the flag, and returns true.
//
// Two overlapping cycles would double every upstream's load for no benefit
// and race on Store.Put for any key both cycles happen to touch — this
// guard exists to make that structurally impossible across every RefreshAll
// entry point. Per-key populates (RefreshSlider / RefreshTrakt) use
// inFlightKeys instead and are deliberately NOT gated by cycleRunning, so
// AC5/AC6 still hold during a cycle.
func TriggerOnce(ctx context.Context, d Deps) bool {
	return withCycleRunning(func() { RefreshAll(ctx, d, true) })
}

// Claude 2026-08-03: added TriggerOnce's non-blocking sibling for BE-15
// (discover-scheduled-refresh plan §5.1).
// Reason: TriggerOnce is intentionally synchronous end-to-end (T-12 wraps it
// in its own `go func()` specifically to test that) — calling it directly
// from an HTTP handler would hold the request open for the whole forced
// cycle (up to ~300 external calls, §3.6), not the "check synchronously,
// 409 or 202 immediately" contract §5.1 specifies. TriggerAsync claims the
// SAME cycleRunning flag with the same CompareAndSwap so a manual trigger,
// a scheduled tick and a boot poll remain mutually exclusive by
// construction, but runs the forced RefreshAll in its own goroutine rather
// than inline, returning the CAS outcome immediately either way.
// Troubleshooting: if a manual "refresh now" click hangs for minutes before
// the response arrives, something is calling TriggerOnce (or RefreshAll
// directly) from the handler instead of this function.
// Review if: TriggerOnce's own contract (T-12) changes to be non-blocking,
// which would make this wrapper redundant.

// TriggerAsync is TriggerOnce's non-blocking sibling — the shape
// triggerDiscoverRefreshHandler needs (§5.1): claim cycleRunning
// synchronously and report the outcome immediately, launching the forced
// cycle in the background rather than awaiting it. Returns false without
// starting anything if a cycle (from TriggerOnce, Run's boot poll/ticks, or
// another TriggerAsync) is already in flight.
func TriggerAsync(ctx context.Context, d Deps) bool {
	if !cycleRunning.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer cycleRunning.Store(false)
		RefreshAll(ctx, d, true)
	}()
	return true
}

// Run drives the background refresh loop until ctx is cancelled — mirrors
// recheck.Run/adultnewest.Run's shape (ticker, re-read the interval each
// tick, live retune via ticker.Reset, ctx cancellation returns) with one
// deliberate improvement over adultnewest: the boot poll is DUE-CHECKED
// (RefreshAll(force=false)), not unconditional. adultnewest.Run boot-polls
// unconditionally because its own doc records why it had to
// (scan.go:181-189: a 24h ticker in a deployment that redeploys several
// times a day "effectively never fired"). This feature needs that same
// mandatory boot poll for the same reason — on-by-default plus 24h has
// exactly that failure mode — but an UNCONDITIONAL one means a full
// tmdb+slider+trakt fan-out on every redeploy
// (sakms-auto-update.service runs on every push). Due-checking the boot poll
// resolves that tension: a redeploy minutes after a successful cycle makes
// zero external calls; a redeploy after a real gap catches up immediately.
//
// # Interval <= 0 purges rather than freezes (Critic finding H4, §3.5)
//
// Nothing on the read path consults the interval — a cache hit is served
// regardless of whether the schedule that would refresh it is even running.
// So an operator who sets the interval to 0 expecting "off" would otherwise
// get every cached row serving indefinitely-stale content forever, with no
// refresh path and no staleness indicator to make that visible (§6.5 ships
// none). Run therefore purges the WHOLE cache (Store.DeleteAll) whenever it
// OBSERVES a non-positive interval, in both places it can observe one:
//
//   - at boot, before starting a ticker — without this, restarting with the
//     interval already at 0 would never purge, and the rows would survive
//     indefinitely across restarts;
//   - on a live retune to 0, in the same branch that stops the ticker and
//     returns.
//
// TriggerOnce still works with the interval at 0 (it calls
// RefreshAll(force=true) independently of the ticker) — an operator can
// repopulate on demand while leaving the schedule off.
func Run(ctx context.Context, interval time.Duration, d Deps) {
	if interval <= 0 {
		purgeCache(ctx, d, "boot")
		return
	}

	log.Printf("discoverrefresh: background discover-row refresh enabled (every %s) — on by default, due-checked boot poll", interval)
	// Boot poll and ticks hold cycleRunning so a concurrent TriggerOnce
	// returns false (409) rather than overlapping this fan-out (§5.1).
	_ = withCycleRunning(func() { RefreshAll(ctx, d, false) })

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := loadIntervalSafely(ctx, d)
			if cur <= 0 {
				purgeCache(ctx, d, "live retune to 0")
				return
			}
			if cur != interval {
				interval = cur
				ticker.Reset(cur)
			}
			_ = withCycleRunning(func() { RefreshAll(ctx, d, false) })
		}
	}
}

// purgeCache is Run's interval<=0 purge (see Run's doc comment), factored
// out only because it fires from two call sites with the same nil-guard and
// log line.
func purgeCache(ctx context.Context, d Deps, when string) {
	if d.Cache == nil {
		return
	}
	if err := d.Cache.DeleteAll(ctx); err != nil {
		log.Printf("discoverrefresh: purging discover cache at %s: %v", when, err)
		return
	}
	log.Printf("discoverrefresh: interval <= 0 at %s — purged the discover cache, Discover now fetches live until the interval is raised above 0", when)
}
