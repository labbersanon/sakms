// This file is series air-date monitoring — the FOURTH trigger source for the
// shared auto-grab mechanism, implementing the TriggerAirDate const reserved in
// autograb_shared.go. It is Pattern B ("new trigger": no prior grabs row, so
// RunAutoGrab takes its Create path) per the torrent-behavior plan's §5, with
// that section's per-cycle precondition gap closed here — see
// activeSeriesGrabKeys.
//
// NO GOROUTINE, NO TICKER, NO INTERVAL KEY lives in this file, deliberately.
// The pass is a plain function called as the third and LAST step of
// runUsenetRetryCycle. The sibling plan wanted its own scheduler here; the
// spec's AC5 forbids one ("no new goroutine, no new interval setting is added
// anywhere in the codebase for this feature"), and the spec wins. What the
// sibling plan actually objected to — that runUsenetRetryCycle's passes are
// usenet-specific by name and doc, and air-date monitoring is not a usenet
// concern — is answered in usenetretry.go's file doc, not by adding a
// scheduler.
//
// It lives in internal/api rather than its own package for the same reason
// usenetretry.go does: everything it drives is package-private here
// (RunAutoGrab's deps, isActiveGrab, the retry reason strings). It stays
// independently deletable, though the cost is slightly more than this file plus
// its call sites: delete this file, its one call in runUsenetRetryCycle, and its
// five route registrations in handler.go — then unwind the `libStore` parameter
// that exists only to feed it, from both runUsenetRetryCycle's and
// RunUsenetRetry's signatures and from RunUsenetRetry's call site in
// cmd/sakms/main.go.
//
// What lives here is DETECTION + DISPATCH + the air-date retry BACKOFF.
// Detection is a pure filter over an unmodified MissingEpisodes; dispatch goes
// through RunAutoGrab like every other trigger; the backoff is what keeps an
// air-date row that never finds a release from costing one live Prowlarr search
// every 24 hours forever (see airDateRetryBackoff).
//
// The EPISODE CATALOG SYNC that feeds all of it used to live here too, as the
// second half of this file. It now lives in seriescatalog.go, moved verbatim so
// a read-only browsing surface can populate episode air dates without the
// unattended auto-grab toggle being on (see that file's doc for the full
// reasoning, including why a new file rather than an export from this one).
// monitorAirDates calls it exactly as it always did, and the sync's behaviour on
// this path is unchanged.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

const (
	// seriesNewSeasonDiscoveryKey gates NEW-SEASON discovery only — whether a
	// season TMDB reports that has ZERO episode rows is synced and
	// auto-monitored. Seasons that already have episode rows are always synced
	// regardless: monitoring gates SEARCHING, never METADATA, and gating the
	// sync too would make an unmonitored season permanently invisible to the
	// operator who wants to monitor it.
	//
	// Global rather than per-series: every other automation toggle in this
	// codebase is global, a per-series flag needs a second schema addition, and
	// there is no proven demand for one.
	seriesNewSeasonDiscoveryKey = "series_new_season_discovery_enabled"

	// maxAirDateGrabsPerCycle bounds how many NEW dispatch attempts one cycle
	// may start, mirroring the ≤20 cap the Discover bulk-grab path already
	// established. Each attempt costs one Prowlarr SearchByID + two TMDB calls,
	// sequential — never an errgroup fan-out, which would recreate the exact
	// concurrent-indexer-query pattern that got the per-card availability badge
	// permanently banned.
	//
	// It bounds DISPATCH ONLY. The catalog sync completes for every tracked
	// series every cycle regardless, or MissingCount and the season UI would lag
	// reality by however long the backlog takes to drain. It is also not a
	// Prowlarr cap in any full-system sense: every attempt that finds nothing
	// leaves a pending_retry row whose re-searches belong to retryDueGrabs.
	// airDateRetryBackoff is what bounds that accumulated stock.
	maxAirDateGrabsPerCycle = 20

	// airDateRetryReason is written by the backoff sweep. It is BOTH the
	// operator-facing explanation on the Requests screen AND the
	// "already-backed-off this cycle" flag: a row carrying it is skipped by the
	// sweep until something actually searches for it again and rewrites the
	// reason to noQualifyingCandidateReason. That is what keeps retry_count
	// advancing +1 per REAL search rather than +1 per cycle.
	airDateRetryReason = "no release has surfaced yet — retrying on a widening schedule"

	// airDateUnmonitoredReason explains a row this feature terminated because
	// its season stopped being monitored.
	//
	// Writing it is NOT cosmetic. grabs.Failed means something specific
	// everywhere else in this app — "this release was taken down" (a 451/DMCA
	// classification) — so an operator who un-monitors a season and then opens
	// Requests would otherwise see rows flip to Failed with nothing whatsoever
	// telling them it actually means "you un-monitored this". retry_reason is
	// the field that already exists to explain a row's state, and UpdateStatus
	// cannot carry one (it takes only id and status), which is why the reason is
	// a separate, EARLIER call.
	airDateUnmonitoredReason = "the season was un-monitored, so this retry was cancelled"
)

// monitorAirDates is the whole pass: catalog sync for every tracked series,
// then detection over the freshly-synced rows, then dispatch, then the backoff
// sweep. It runs as the third and LAST step of runUsenetRetryCycle.
//
// The ORDER inside this function is a requirement, not an implementation
// detail. The sync runs for EVERY tracked series FIRST so a season discovered
// this cycle is eligible in the SAME cycle rather than the next one, and the
// backoff sweep runs LAST so it has the final word on retry_after.
//
// It takes `build sessionBuilderFunc`, not a *mode.Session: runUsenetRetryCycle
// holds no session and never has (both of its other passes are session-free by
// design), so a session parameter could only be satisfied by building a Series
// session on every cycle even when this pass is inert.
func monitorAirDates(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc,
	libStore *library.Store, excluded map[string]bool, now time.Time) {

	if libStore == nil {
		return // the library store isn't wired — nothing to monitor
	}

	sess, err := build(ctx, mode.Series)
	if err != nil {
		// The innermost error's TYPE, never its message: a *url.Error's Error()
		// renders the whole URL, api_key query parameter included. Same contract
		// reparkFailedRetry follows. A session-build failure is pass-wide rather
		// than row-scoped, so there is nothing to park — returning is the whole
		// handling.
		log.Printf("air-date monitor: building the series session failed (%T) — skipping this cycle", rootCause(err))
		return
	}
	// autoGrabSearch's own doc requires callers to have confirmed TMDB is
	// present for Series, and without Prowlarr nothing could dispatch anyway —
	// the catalog sync alone would burn a TMDB call per season per cycle for a
	// feature that cannot act on them.
	if sess.TMDB == nil || sess.Prowlarr == nil {
		log.Printf("air-date monitor: TMDB or Prowlarr isn't configured — skipping this cycle")
		return
	}

	seriesList, err := libStore.ListSeries(ctx)
	if err != nil {
		log.Printf("air-date monitor: listing tracked series: %v", err)
		return
	}

	discovery, err := deps.SettingsStore.GetBool(ctx, seriesNewSeasonDiscoveryKey, false)
	if err != nil {
		log.Printf("air-date monitor: reading the new-season discovery toggle: %v — treating it as off", err)
		discovery = false
	}

	for _, series := range seriesList {
		syncSeriesCatalog(ctx, sess, libStore, series, discovery)
	}

	dispatchAirDateGrabs(ctx, deps, sess, libStore, seriesList, excluded, now)
	airDateBackoffSweep(ctx, deps, libStore, now)
}

// eligibleEpisodes filters MissingEpisodes' output to the episodes this cycle
// may search for. MissingEpisodes itself is CALLED, NEVER MODIFIED — Requests'
// MissingCount is the other consumer and must keep seeing the unfiltered
// result.
//
// Three predicates, in this order:
//
//  1. The season is monitored. An absent season is absent from the map, which
//     is false, which is skipped. This sits at the TOP of the filter, where it
//     cannot be accidentally short-circuited past: an unmonitored season's
//     episodes are never auto-searched, no matter how long ago they aired.
//  2. The air date is KNOWN. An unknown air date is NOT "<= today". TMDB leaves
//     air_date empty for unannounced episodes, and an empty string sorts before
//     every real date — so a bare `AirDate <= today` would treat every
//     unannounced episode as already aired and search for it immediately. This
//     guard is mandatory, not defensive.
//  3. The air date has arrived. Plain string comparison: TMDB's air_date is a
//     fixed YYYY-MM-DD, for which lexicographic order IS chronological order.
//     Do not "fix" this into a time.Parse that then has to invent a policy for
//     malformed input.
//
// Timezone honesty, documented rather than hidden: TMDB's air_date is the
// show's origin-country broadcast date, so comparing it against a UTC "today"
// can be off by up to a day either direction. The requirement is same-day with
// no grace period, so no fudge factor is added.
func eligibleEpisodes(missing []library.Episode, monitored map[int]bool, today string) []library.Episode {
	out := []library.Episode{}
	for _, ep := range missing {
		if !monitored[ep.SeasonNumber] {
			continue
		}
		if ep.AirDate == "" {
			continue
		}
		if ep.AirDate > today {
			continue
		}
		out = append(out, ep)
	}
	return out
}

// seriesSeasonKey / seriesEpisodeKey are the two masking keys the active-grab
// pre-filter builds. Both are needed: the per-episode map alone has a
// season-pack hole, and the season-level map alone would mask single-episode
// grabs of OTHER episodes in the same season.
type seriesSeasonKey struct{ tmdbID, season int }

type seriesEpisodeKey struct{ tmdbID, season, episode int }

// activeSeriesGrabKeys is the per-cycle precondition Pattern B does not get for
// free. A downloading episode still has file_path = "", so MissingEpisodes
// returns it again next cycle; without this set, cycle 2 would Create a SECOND
// row and dispatch a SECOND download of the same episode — activeGrabForGID
// cannot catch that, because AddNZB mints a fresh GID per call.
//
// Stated as a general rule, because the sibling plan's decision table lacked
// it: Pattern A gets its precondition for free (DueForRetry selects only rows
// in the right state); a PERIODIC Pattern B caller must re-establish "no prior
// row" itself, every cycle, before calling RunAutoGrab.
//
// This is the FIRST of the cycle's two grabsStore.List calls, taken BEFORE
// dispatch because it is a pre-filter. airDateBackoffSweep takes a SECOND one
// AFTER dispatch because it must see the rows this cycle's own dispatches just
// created. Do not "optimize" them into one: reusing this snapshot there would
// silently skip every row created this cycle, and the backoff would appear to
// work while never applying to a first park.
func activeSeriesGrabKeys(ctx context.Context, deps AutoGrabDeps) (map[seriesEpisodeKey]bool, map[seriesSeasonKey]bool) {
	episodes := map[seriesEpisodeKey]bool{}
	seasons := map[seriesSeasonKey]bool{}

	list, err := deps.GrabsStore.List(ctx, mode.Series)
	if err != nil {
		log.Printf("air-date monitor: listing series grabs for the active-grab pre-filter: %v", err)
		return episodes, seasons
	}
	for _, g := range list {
		// isActiveGrab is CALLED, not duplicated: same package, no edit to
		// requests.go, and the two definitions of "still outstanding" cannot
		// drift. It counts pending_retry as active, which is what makes a
		// no-match row mask its own episode until something retires it.
		if !isActiveGrab(g.Status) || !g.SeasonSpecified {
			// SeasonSpecified is required and is not redundant: a bare
			// SeasonNumber == 0 means "no season was picked", not Specials, so
			// without it every season-less Series grab would mask Specials
			// episode 0 of every series.
			continue
		}
		if g.EpisodeNumber == 0 {
			// A SEASON PACK (season>0, episode=0 per grabs.Grab's type doc).
			// Keyed per-episode it would key as (x, 3, 0) and mask NOTHING,
			// leaving every episode of a season that is already downloading as a
			// pack eligible for its own individual grab.
			seasons[seriesSeasonKey{g.TMDBID, g.SeasonNumber}] = true
			continue
		}
		episodes[seriesEpisodeKey{g.TMDBID, g.SeasonNumber, g.EpisodeNumber}] = true
	}
	return episodes, seasons
}

// airDateCandidate is one eligible episode paired with the series it belongs
// to, so the dispatch loop can order the whole library's candidates together
// rather than per series.
type airDateCandidate struct {
	series  library.Series
	episode library.Episode
}

// dispatchAirDateGrabs runs detection over the freshly-synced catalog and
// dispatches up to maxAirDateGrabsPerCycle of the result, oldest air date
// first.
//
// The ordering (air date, then series title, season, episode) is what makes a
// backlog drain predictably instead of the same 20 rows being retried every
// cycle, and it is what makes the cap deterministic across runs.
func dispatchAirDateGrabs(ctx context.Context, deps AutoGrabDeps, sess *mode.Session,
	libStore *library.Store, seriesList []library.Series, excluded map[string]bool, now time.Time) {

	today := now.UTC().Format("2006-01-02")

	candidates := []airDateCandidate{}
	for _, series := range seriesList {
		monitored, err := libStore.MonitoredSeasons(ctx, series.ID)
		if err != nil {
			log.Printf("air-date monitor: reading monitored seasons for series %d: %v", series.ID, err)
			continue
		}
		if len(monitored) == 0 {
			continue
		}
		missing, err := libStore.MissingEpisodes(ctx, series.ID)
		if err != nil {
			log.Printf("air-date monitor: listing missing episodes for series %d: %v", series.ID, err)
			continue
		}
		for _, ep := range eligibleEpisodes(missing, monitored, today) {
			candidates = append(candidates, airDateCandidate{series: series, episode: ep})
		}
	}
	if len(candidates) == 0 {
		return
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.episode.AirDate != b.episode.AirDate {
			return a.episode.AirDate < b.episode.AirDate
		}
		if a.series.Title != b.series.Title {
			return a.series.Title < b.series.Title
		}
		if a.episode.SeasonNumber != b.episode.SeasonNumber {
			return a.episode.SeasonNumber < b.episode.SeasonNumber
		}
		return a.episode.EpisodeNumber < b.episode.EpisodeNumber
	})

	activeEpisodes, activeSeasons := activeSeriesGrabKeys(ctx, deps)
	// Re-read cache for the still-monitored guard below: PER SERIES, never per
	// dispatch. MonitoredSeasons returns a whole series' map by design precisely
	// so the cycle does not do an N+1, and a per-dispatch re-read would
	// reintroduce exactly that, up to 20× for one series.
	rechecked := map[int64]map[int]bool{}

	attempts := 0
	for _, c := range candidates {
		if attempts >= maxAirDateGrabsPerCycle {
			return
		}
		// An operator who excluded this title from the Requests worklist has
		// said "stop working on this" — identical treatment retryDueGrabs gives
		// it. There is deliberately NO Failed exclusion: a Failed episode (a
		// 451/DMCA takedown, or this feature's own un-monitor cleanup) is
		// genuinely eligible again, which is what makes re-monitoring a season
		// free of special cases.
		if excluded[excludes.Key(string(mode.Series), c.series.TMDBID, c.series.Title)] {
			continue
		}
		if activeSeasons[seriesSeasonKey{c.series.TMDBID, c.episode.SeasonNumber}] {
			continue
		}
		if activeEpisodes[seriesEpisodeKey{c.series.TMDBID, c.episode.SeasonNumber, c.episode.EpisodeNumber}] {
			continue
		}

		// Defense in depth against an operator un-monitoring a season DURING
		// this cycle: the map above was read before the catalog sync, which can
		// take minutes over a large library. Re-reading here narrows the race to
		// the milliseconds between this read and this series' own dispatches.
		monitored, ok := rechecked[c.series.ID]
		if !ok {
			var err error
			monitored, err = libStore.MonitoredSeasons(ctx, c.series.ID)
			if err != nil {
				log.Printf("air-date monitor: re-checking monitored seasons for series %d: %v", c.series.ID, err)
				continue
			}
			rechecked[c.series.ID] = monitored
		}
		if !monitored[c.episode.SeasonNumber] {
			continue
		}

		attempts++
		out, err := RunAutoGrab(ctx, deps, sess, AutoGrabRequest{
			Mode: mode.Series, Title: c.series.Title,
			TMDBID: c.series.TMDBID, TVDBID: c.series.TVDBID,
			Season: c.episode.SeasonNumber, Episode: c.episode.EpisodeNumber,
			// Always true: a real Season 0 must stay distinguishable from "no
			// season was picked".
			SeasonSpecified: true,
			RootFolderPath:  c.series.RootFolderPath,
			Trigger:         TriggerAirDate,
			// Pattern B: RunAutoGrab takes its Create path. Releases nil means
			// "search for me" — a tracked series row carries the TMDB id
			// autoGrabSearch needs, unlike the Search hook.
			ExistingGrabID: 0,
		})
		switch {
		case err != nil:
			log.Printf("air-date monitor: %s s%02de%02d — auto-grab failed (%T)", c.series.Title, c.episode.SeasonNumber, c.episode.EpisodeNumber, rootCause(err))
		case out.Gated:
			// The toggle went off between the cycle's interval read and the
			// gate. Every remaining candidate would be gated too, so stop
			// dispatching rather than log once per episode. The backoff sweep
			// still runs: an un-monitored season's rows must be reaped whether
			// or not anything dispatched.
			log.Printf("air-date monitor: usenet auto-grab is switched off — abandoning this cycle's dispatches")
			return
		case out.AlreadyGrabbing:
			log.Printf("air-date monitor: %s s%02de%02d is already being downloaded", c.series.Title, c.episode.SeasonNumber, c.episode.EpisodeNumber)
		case out.Grabbed:
			log.Printf("air-date monitor: %s s%02de%02d dispatched", c.series.Title, c.episode.SeasonNumber, c.episode.EpisodeNumber)
		default:
			log.Printf("air-date monitor: %s s%02de%02d has no qualifying candidate yet — parked for re-search", c.series.Title, c.episode.SeasonNumber, c.episode.EpisodeNumber)
		}
	}
}

// airDateRetryBackoff maps an air-date-originated pending_retry row's current
// retry_count to how far ahead its NEXT attempt is parked.
//
// Why it exists: with the global removal of the retry-attempt cap (2026-08-01),
// an air-date row that never finds a qualifying release is re-searched by
// retryDueGrabs every 24h FOREVER, and the set of such rows only ever grows —
// an unbounded, permanently increasing live-indexer query volume, which is the
// pattern CLAUDE.md's per-card-availability-badge rule bans outright.
//
// The resolution is a BACKOFF, not a re-introduced attempt cap: a
// permanently-stuck episode must keep trying (a release genuinely can surface
// years later) but must cost less and less to keep trying. The schedule
// therefore never returns 0 and never stops — it plateaus.
//
//	retry_count   next attempt in     rationale
//	-----------   ---------------     ---------
//	0, 1, 2       24h                 a just-aired episode is usually indexed
//	                                  within a few days; keep the daily cadence
//	                                  for the window where it actually pays off.
//	3, 4          72h    (3 days)
//	5, 6          168h   (7 days)     weekly
//	7, 8          336h   (14 days)    fortnightly
//	>= 9          720h   (30 days)    the CEILING. Never longer, never zero.
//
// Wall clock for a permanently-stuck episode: the first dispatch is day 0, then
// attempts land on days 1, 2, 3, 6, 9, 16, 23, 37, 51, then every 30 days. ~9
// searches in the first two months, ~12 per year thereafter — against 365 per
// year, forever, under the flat interval.
//
// That schedule is EXACT only because the sweep calls SetRetryAfter, which does
// NOT increment retry_count. retry_count therefore advances by exactly +1 per
// REAL search (parkPendingRetry's increment), and the row read back carries the
// same count the interval above was computed from. An executor who traces a
// double-increment and concludes this table is wrong has it backwards: the
// table is right and the store call would be wrong.
//
// A negative retryCount (impossible from the schema, which defaults it to 0) is
// treated as 0 rather than panicking or returning a zero Duration; a zero
// Duration would make the row due immediately and reintroduce a tight loop.
func airDateRetryBackoff(retryCount int) time.Duration {
	switch {
	case retryCount <= 2:
		return 24 * time.Hour
	case retryCount <= 4:
		return 72 * time.Hour
	case retryCount <= 6:
		return 168 * time.Hour
	case retryCount <= 8:
		return 336 * time.Hour
	default:
		return 720 * time.Hour
	}
}

// airDateShaped reports whether a pending_retry grab row has the STRUCTURAL
// shape only the air-date pass and a manual single-episode Series grab can
// produce. It is the WIDER of this file's two origin predicates, and it is what
// the backoff sweep uses — including the sweep's un-monitored reap branch.
//
// The grabs table has no trigger column, and adding one would modify the shared
// mechanism this feature is not allowed to touch, so origin is derived
// structurally:
//
//   - Mode Series + status pending_retry: the pass only ever runs Series.
//   - TMDBID > 0 rules out EVERY toggle-ON Search hook row (searchGrabIdentity's
//     own doc: "TMDBID is 0 at this hook in EVERY mode").
//   - SeasonSpecified && EpisodeNumber > 0 rules out season packs (season>0,
//     episode=0) and season-less Series rows. SeasonSpecified is required and is
//     not redundant: a bare SeasonNumber == 0 means "no season was picked", not
//     Specials.
//
// retry_reason is deliberately NOT the origin key, and this is the non-obvious
// part. A reason-string marker cannot survive one cycle: retryDueGrabs →
// RunAutoGrab → parkPendingRetry unconditionally overwrites it with
// noQualifyingCandidateReason. The obvious reason-string approach works in a
// single-cycle test and silently degrades to "no backoff at all" in production.
func airDateShaped(g grabs.Grab) bool {
	return g.Mode == mode.Series &&
		g.Status == grabs.PendingRetry &&
		g.TMDBID > 0 &&
		g.SeasonSpecified &&
		g.EpisodeNumber > 0
}

// airDateOriginated is airDateShaped PLUS the never-dispatched clause. It is
// the NARROWER predicate and is used ONLY by the un-monitor cleanup, the one
// consumer whose action is destructive.
//
// Indexer == "" && DownloadURL == "" rules out every row that was ever
// DISPATCHED and later parked: the GID sweep's rows and the stale-torrent
// requeue rows both carry the indexer and download URL of the release they
// actually grabbed, while parkPendingRetry's Create path supplies neither.
//
// WHY THE SPLIT — and do not "simplify" it back to one predicate in either
// direction. A Discover one-click Grab (TriggerOperator, real TMDB id,
// SeasonSpecified, Episode > 0) that QUALIFIES, dispatches, and then eats an
// asynchronous 430 is re-parked by sweepUsenetFailures into a row that is
// byte-for-byte the same shape as a dispatched-then-reparked air-date row, and
// no field-level discriminator exists to tell them apart. So the discriminator
// is WHICH CONSUMER IS ASKING:
//
//   - The backoff sweep uses the WIDE predicate. Mis-classifying an operator's
//     row there only slows that row's re-searches toward the 30-day ceiling —
//     the download is already dead (SetPendingRetry cleared download_gid), so
//     nothing in flight is harmed, the error is in the safe direction (fewer
//     indexer queries, never more), and it is self-limiting at the ceiling.
//   - The un-monitor cleanup uses THIS, the NARROW one. Mis-classifying there
//     flips an operator's own hand-made Discover request to Failed because of a
//     season flag flip — a worse bug than the one the cleanup exists to fix.
//     Destructive plus ambiguous means take the conservative branch.
//
// The residual that leaves — a dispatched-then-reparked air-date row surviving
// the toggle — is closed by the sweep's reap branch on the first cycle in which
// the row comes due, at the cost of at most one accepted re-search (retryDueGrabs
// runs before this pass).
func airDateOriginated(g grabs.Grab) bool {
	return airDateShaped(g) && g.Indexer == "" && g.DownloadURL == ""
}

// airDateBackoffSweep is where the backoff interval is actually applied, and
// where an un-monitored season's rows are reaped. It runs as the LAST step of
// monitorAirDates, using its OWN second grabsStore.List — not the pre-filter's
// snapshot, which predates this cycle's own Creates.
//
// Running last is deliberate, for two reasons. The pre-filter must see rows
// retryDueGrabs just re-armed, and — the load-bearing one — this sweep must
// have the LAST WORD on retry_after: retryDueGrabs re-parks every row it
// re-searches at the flat interval via parkPendingRetry, and this overrides that
// with the backoff. Run before retryDueGrabs, the backoff would be silently
// discarded every cycle while every unit test of airDateRetryBackoff still
// passed.
//
// A row this cycle's own dispatch just parked is handled by the SAME code path,
// with no special case: parkPendingRetry's Create writes retry_count 0 with
// noQualifyingCandidateReason, so it satisfies both conditions below and is
// picked up in the same cycle.
//
// Known, accepted gap, stated rather than hidden: a row re-parked by
// reparkFailedRetry or by retryDueGrabs' AlreadyGrabbing branch carries a
// different reason, fails condition 2, and stays on the flat interval for that
// ONE cycle. It rejoins the backoff on the next cycle that produces a genuine
// no-match. Bounded single-cycle slip, not a leak.
func airDateBackoffSweep(ctx context.Context, deps AutoGrabDeps, libStore *library.Store, now time.Time) {
	list, err := deps.GrabsStore.List(ctx, mode.Series)
	if err != nil {
		log.Printf("air-date monitor: listing series grabs for the backoff sweep: %v", err)
		return
	}

	monitored := newMonitoredSeasonCache(libStore)
	for _, g := range list {
		if !airDateShaped(g) {
			continue
		}

		// The monitored-state branches are checked FIRST, before the
		// this-cycle-touched condition below: a season whose flag was flipped
		// must be handled whether or not anything searched for it this cycle.
		// This is also the ONLY place a dispatched-then-reparked air-date row for
		// an EXPLICITLY un-monitored season is caught, since the toggle handler's
		// narrow predicate deliberately does not touch it.
		flags, err := monitored.lookup(ctx, g.TMDBID)
		if err != nil {
			// A real DB failure keeps the general soft-fail treatment: log the
			// type and leave the row alone for this cycle. The ErrNotFound
			// exception below is scoped to ErrNotFound alone and must not become
			// a blanket "any lookup error kills the row".
			log.Printf("air-date monitor: resolving series for tmdb id %d (%T) — leaving grab %d alone this cycle", g.TMDBID, rootCause(err), g.ID)
			continue
		}

		// THREE-WAY, not two-way, and the third branch is the whole point.
		// airDateShaped is WIDE — an operator's own Discover one-click grab that
		// dispatched and then ate an asynchronous 430 matches it byte for byte —
		// so "this season is not in the monitored set" is not evidence of an
		// un-monitor. An absent row is the state of EVERY season on EVERY install
		// by default, which would make this destructive branch fire on rows that
		// have nothing to do with this feature.
		if !flags.tracked {
			// The series was deleted or untracked, so nothing will ever reap
			// these rows. Unchanged, and pinned by T-C1.4.
			cancelAirDateRetry(ctx, deps.GrabsStore, g, now)
			continue
		}
		isMonitored, touched := flags.monitored[g.SeasonNumber]
		if !touched {
			// NEVER TOGGLED. Not this pass's concern at all — leave the row
			// completely alone, which means skipping the backoff branch below
			// too, not just the reap. That is safe rather than a leak: a
			// genuinely air-date-originated row can only exist for a season
			// something called SetSeasonMonitored(true) on, so it always has a
			// row here and always reaches the backoff.
			continue
		}
		if !isMonitored {
			// An EXPLICIT prior un-monitor. Reap, same as before.
			cancelAirDateRetry(ctx, deps.GrabsStore, g, now)
			continue
		}

		// The this-cycle-touched flag. Only parkPendingRetry writes
		// noQualifyingCandidateReason, and it writes it exactly when a re-search
		// just ran and found nothing. A row this sweep re-parked last cycle
		// carries airDateRetryReason instead, so it is skipped until something
		// actually searches for it again — which is what keeps retry_count
		// advancing +1 per REAL search rather than +1 per cycle.
		if g.RetryReason != noQualifyingCandidateReason {
			continue
		}

		// SetRetryAfter, NOT SetPendingRetry. SetPendingRetry increments
		// retry_count unconditionally, and parkPendingRetry has ALREADY
		// incremented it earlier in this same cycle (that is precisely what the
		// condition above detects). Using it here would advance the count by 2
		// per searched cycle and halve the documented schedule.
		if err := deps.GrabsStore.SetRetryAfter(ctx, g.ID, now.Add(airDateRetryBackoff(g.RetryCount)), airDateRetryReason); err != nil {
			log.Printf("air-date monitor: backing off grab %d: %v", g.ID, err)
		}
	}
}

// seasonFlags is one series' answer to the sweep's question, and it is
// deliberately NOT a bare map: the sweep must distinguish three states, and a
// map alone can only carry two.
//
//   - tracked = false: no library_series row for this TMDB id at all (deleted or
//     untracked). The reap-unconditionally case.
//   - tracked = true, no key for a season: that season has NEVER been toggled.
//     Leave its rows alone — this is the default state of every season.
//   - tracked = true, key present: the operator's explicit choice, true or false.
type seasonFlags struct {
	tracked   bool
	monitored map[int]bool
}

// monitoredSeasonCache resolves a grab row's TMDBID to its series' season
// monitor flags, once per series per sweep. The sweep walks a whole List and
// many rows share one series, so a naive per-row lookup would be the N+1
// SeasonMonitorFlags' whole-map shape exists to prevent.
type monitoredSeasonCache struct {
	libStore *library.Store
	flags    map[int]seasonFlags
}

func newMonitoredSeasonCache(libStore *library.Store) *monitoredSeasonCache {
	return &monitoredSeasonCache{libStore: libStore, flags: map[int]seasonFlags{}}
}

// lookup returns tmdbID's season monitor flags, or an error for a genuine DB
// failure.
//
// SeasonMonitorFlags, NOT MonitoredSeasons, and the difference is the whole
// reason this cache exists in its current shape: MonitoredSeasons drops
// monitored = false rows, which collapses "explicitly un-monitored" and "never
// touched" into one indistinguishable answer. The sweep's reap branch is
// destructive and must tell them apart.
//
// THE EXCEPTION, AND IT IS LOAD-BEARING: library.ErrNotFound is NOT an error
// here — it returns tracked = false, which the sweep reads as "this series is
// gone, reap this row". It is a deliberate exception to this feature's general
// soft-fail convention, and it is scoped to ErrNotFound alone.
//
// The general convention is right for a TRANSIENT failure, where skipping costs
// one cycle and the row is retried correctly next time. ErrNotFound is not
// transient: the series row was deleted or untracked, so it will never come
// back on its own and no season of it can ever again report as monitored.
// Soft-failing it would mean a deleted series' air-date retry rows are
// re-searched every cycle, FOREVER — the exact unbounded-volume failure the
// backoff exists to close, arriving through the un-monitor door. DeleteSeries
// removes the series, its episodes and its monitored flags, but it does not
// touch grabs (nor should it), so nothing else would ever reap them.
//
// Both the found and the not-found result are cached; a transient error from
// EITHER query is not, and must propagate as an error rather than as a
// zero-valued seasonFlags — the zero value has tracked = false, which the sweep
// would reap on.
func (c *monitoredSeasonCache) lookup(ctx context.Context, tmdbID int) (seasonFlags, error) {
	if flags, ok := c.flags[tmdbID]; ok {
		return flags, nil
	}
	series, err := c.libStore.GetSeriesByTMDBID(ctx, tmdbID)
	if err != nil {
		if errors.Is(err, library.ErrNotFound) {
			c.flags[tmdbID] = seasonFlags{tracked: false}
			return c.flags[tmdbID], nil
		}
		return seasonFlags{}, err
	}
	monitored, err := c.libStore.SeasonMonitorFlags(ctx, series.ID)
	if err != nil {
		return seasonFlags{}, err
	}
	c.flags[tmdbID] = seasonFlags{tracked: true, monitored: monitored}
	return c.flags[tmdbID], nil
}

// cancelAirDateRetry terminates one air-date retry row: a reason write FIRST,
// then the status flip. Shared by the un-monitor toggle handler (immediate
// cleanup) and the backoff sweep's reap branch (the cycle-boundary backstop);
// both are required and neither substitutes for the other.
//
// The ORDER is not stylistic — UpdateStatus takes only (id, status) and cannot
// carry a reason, so the reason must be a separate, earlier call, written while
// the row is still pending_retry.
//
// SetRetryAfter rather than SetPendingRetry: either would work (the row is
// terminal a line later, and nothing reads the retry_count of a Failed row), but
// SetPendingRetry would also re-assert status = 'pending_retry' and clear
// download_gid for no reason. `now` is passed as the timestamp because the value
// is irrelevant on a terminal row, and a far-future one would read as if
// something were still scheduled.
//
// Failed rather than deletion or a far-future park, the four options weighed:
// deletion destroys the audit trail (an operator cannot see what was cancelled
// or why); a far-future park leaves the row on the worklist forever, because
// isActiveGrab counts PendingRetry as active; a bare UpdateStatus leaves the row
// reading as a DMCA-class permanent failure with no explanation. Failed removes
// it from /api/requests (isActiveGrab excludes it), from the retry sweep and from
// parkPendingRetry's dedup (both filter status = 'pending_retry') — and that last
// property is what makes RE-monitoring free: the episode still has file_path = "",
// so a Failed row does not mask it, and the next pass simply creates a fresh grab.
// Failed is NOT a permanent exclusion, here or anywhere.
func cancelAirDateRetry(ctx context.Context, grabsStore *grabs.Store, g grabs.Grab, now time.Time) {
	if err := grabsStore.SetRetryAfter(ctx, g.ID, now, airDateUnmonitoredReason); err != nil {
		log.Printf("air-date monitor: recording the un-monitor reason on grab %d: %v", g.ID, err)
		return
	}
	if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Failed); err != nil {
		log.Printf("air-date monitor: cancelling grab %d after its season was un-monitored: %v", g.ID, err)
	}
}

// cancelAirDateRetriesForSeasons terminates every NEVER-DISPATCHED air-date
// retry queued for one series' given seasons — the immediate half of "un-
// monitoring actively cancels in-flight air-date retries, rather than merely
// stopping new ones".
//
// Without it, un-monitoring a season leaves every already-queued air-date retry
// for it being re-searched on its own schedule forever: retryDueGrabs consults
// exactly DueForRetry and the excludes map, and knows nothing at all about
// library_season_monitored.
//
// It uses the NARROW airDateOriginated on purpose — see that predicate's doc for
// why widening it here would destroy an operator's own Discover grab.
func cancelAirDateRetriesForSeasons(ctx context.Context, grabsStore *grabs.Store, tmdbID int, seasons map[int]bool, now time.Time) {
	if tmdbID == 0 || len(seasons) == 0 {
		return
	}
	list, err := grabsStore.List(ctx, mode.Series)
	if err != nil {
		log.Printf("air-date monitor: listing series grabs for the un-monitor cleanup: %v", err)
		return
	}
	for _, g := range list {
		if g.TMDBID != tmdbID || !seasons[g.SeasonNumber] {
			continue
		}
		if !airDateOriginated(g) {
			continue
		}
		cancelAirDateRetry(ctx, grabsStore, g, now)
	}
}

// rootCause unwraps to the innermost error, so a log line can name its TYPE
// without rendering its message — a *url.Error's Error() prints the whole URL,
// api_key query parameter included. Same contract reparkFailedRetry follows.
func rootCause(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

// --- Routes -----------------------------------------------------------------
//
// The literal `series` path segment in these routes is DELIBERATE AND
// PRECEDENTED, not an inconsistency with neighbours like
// /api/modes/{mode}/library/root-folder. handler.go already carries the
// literal-mode-segment convention in three separate blocks for routes that are
// genuinely one-mode-only (every /api/modes/adult/... subtree). Per-season
// monitoring is Series-only by construction — Movies has no seasons and Adult
// has no episode model at all — so a {mode} wildcard would have to reject two of
// its three possible values at runtime. The literal segment makes that a routing
// fact instead of a handler check.

// seasonCatalog is the season list the two operator-facing season routes read:
// ListSeasonStates' local rows, plus every season TMDB reports that has NO
// local row at all.
//
// The merge closes a real dead end. ListSeasonStates unions episode rows with
// library_season_monitored rows, so a season with NEITHER — one TMDB knows
// about that nothing was ever downloaded from — can never appear, and an
// operator therefore has exactly one way to reach it: the GLOBAL new-season
// discovery toggle, which auto-monitors EVERY such season across the WHOLE
// library at once. There was no way to see and monitor one specific
// never-touched season. Now there is.
//
// The TMDB call is the same carve-out shape this codebase already permits
// twice — Discover's detail-popup availability check (CLAUDE.md, 2026-07-14)
// and the Adult Performers/Studios drill-down (2026-07-30): ONE explicit
// operator action (opening a series' detail panel, or clicking its "All
// seasons" switch), ONE entity, ONE extra call. It is NOT the banned
// automatic per-card probe — nothing here fires on page load, per card, or on
// a background cycle — and it is TMDB, not Prowlarr, on a route that is not
// Discover. If a future change makes this fire without an explicit operator
// action, or for more than the one series in the path, that is the rule being
// broken again.
type seasonCatalog struct {
	httpClient *http.Client
	connStore  *connections.Store
	scStore    *serviceconn.Store
	settings   *settings.Store
	lib        *library.Store
}

// states returns seriesID's merged season list, ascending.
//
// EVERY TMDB-side failure degrades to the local list rather than erroring the
// request: a season the operator already has files or monitoring for must stay
// visible (and monitorable) when TMDB is unconfigured, unreachable, or slow.
// Only a local-store failure is fatal, because then there is nothing to show.
//
// It NEVER writes. No monitored row, no episode row — episode rows stay
// syncSeriesCatalog's job, on its own cycle, gated as before. A merged entry is
// display state only.
func (c seasonCatalog) states(ctx context.Context, seriesID int64) ([]library.SeasonState, error) {
	states, err := c.lib.ListSeasonStates(ctx, seriesID)
	if err != nil {
		return nil, err
	}

	series, err := c.lib.GetSeries(ctx, seriesID)
	if err != nil {
		// Deliberately tolerant, unlike lookupSeries: GET .../seasons answers 200
		// with an empty list for an unknown series id, and the PUT's own 404 is
		// raised by its own lookupSeries call, not here.
		if !errors.Is(err, library.ErrNotFound) {
			log.Printf("season catalog: loading series %d: %v — listing local seasons only", seriesID, err)
		}
		return states, nil
	}
	// Same guard, same reason, as syncSeriesCatalog's: library_series.tmdb_id has
	// no positive constraint, and GET /tv/0 is a wasted round trip per panel open.
	if series.TMDBID == 0 {
		return states, nil
	}

	sess, err := mode.Build(ctx, c.connStore, c.scStore, c.settings, c.httpClient, nil, mode.Series)
	if err != nil {
		// The error's TYPE, never its message — a *url.Error renders the whole
		// URL, api_key query parameter included (same contract monitorAirDates
		// follows).
		log.Printf("season catalog: building the series session failed (%T) — listing local seasons only", rootCause(err))
		return states, nil
	}
	if sess.TMDB == nil {
		return states, nil // TMDB isn't configured; the local list is the whole answer
	}
	details, err := sess.TMDB.TVDetails(ctx, series.TMDBID)
	if err != nil {
		log.Printf("season catalog: loading TMDB details for series %d: %v — listing local seasons only", seriesID, err)
		return states, nil
	}

	known := map[int]bool{}
	for _, st := range states {
		known[st.SeasonNumber] = true
	}
	for _, season := range details.Seasons {
		if known[season.SeasonNumber] {
			continue
		}
		known[season.SeasonNumber] = true
		// Season 0 (Specials) is INCLUDED here on purpose. syncSeriesCatalog skips
		// it, but that skip is about auto-MONITORING on discovery, never about
		// display — Specials have always been visible and manually monitorable
		// wherever they have rows. Do not "align" this with that loop.
		//
		// Monitored is false by construction, not by an unchecked assumption:
		// ListSeasonStates unions library_season_monitored UNFILTERED BY VALUE, so
		// a season carrying any monitored row at all is already in `states` and was
		// skipped above. A season reaching here provably has no row.
		states = append(states, library.SeasonState{SeasonNumber: season.SeasonNumber})
	}
	// ListSeasonStates is ORDER BY season_number; TMDB's seasons[] is in whatever
	// order TMDB returns it. Appending breaks the ascending order the UI renders
	// verbatim (it does no sorting of its own), most visibly for season 0.
	sort.Slice(states, func(i, j int) bool { return states[i].SeasonNumber < states[j].SeasonNumber })
	return states, nil
}

// listSeasonStatesHandler backs GET /api/modes/series/library/{seriesID}/seasons
// — one explicit panel open, one series, one TMDB call (see seasonCatalog).
func listSeasonStatesHandler(catalog seasonCatalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		seriesID, ok := seriesIDPathValue(w, r)
		if !ok {
			return
		}
		states, err := catalog.states(r.Context(), seriesID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]apidto.SeasonState, 0, len(states))
		for _, st := range states {
			out = append(out, apidto.SeasonState{
				SeasonNumber: st.SeasonNumber, EpisodeCount: st.EpisodeCount,
				MissingCount: st.MissingCount, Monitored: st.Monitored,
			})
		}
		writeJSON(w, out)
	}
}

// putSeasonMonitoredHandler backs
// PUT /api/modes/series/library/{seriesID}/seasons/{seasonNumber}/monitored.
//
// Un-monitoring is NOT a pure flag write: it also cancels the season's queued
// air-date retries in the same request (see cancelAirDateRetriesForSeasons).
func putSeasonMonitoredHandler(libStore *library.Store, grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		seriesID, ok := seriesIDPathValue(w, r)
		if !ok {
			return
		}
		seasonNumber, err := strconv.Atoi(r.PathValue("seasonNumber"))
		if err != nil {
			http.Error(w, "seasonNumber path parameter must be an integer", http.StatusBadRequest)
			return
		}
		// Season 0 is real (Specials); a NEGATIVE season is not, and Atoi accepts
		// one. Left unbounded it would write a junk library_season_monitored row
		// no season UI can ever surface or clear.
		if seasonNumber < 0 {
			http.Error(w, "seasonNumber path parameter must not be negative", http.StatusBadRequest)
			return
		}
		var req apidto.SetSeasonMonitoredRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		series, ok := lookupSeries(ctx, w, libStore, seriesID)
		if !ok {
			return
		}
		if err := libStore.SetSeasonMonitored(ctx, seriesID, seasonNumber, req.Monitored); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !req.Monitored {
			cancelAirDateRetriesForSeasons(ctx, grabsStore, series.TMDBID, map[int]bool{seasonNumber: true}, time.Now())
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// putAllSeasonsMonitoredHandler backs
// PUT /api/modes/series/library/{seriesID}/seasons/monitored — every season of
// one series at once.
//
// It exists because an absent row means unmonitored, so an operator adopting
// this feature on a large library would otherwise click a toggle per season. It
// is one round trip, not a queue-wide action, and it still only writes a
// monitored flag — it grabs nothing.
//
// The season set comes from seasonCatalog.states rather than from episode rows
// alone, and NOT from bare ListSeasonStates either. It must be the exact same
// set the GET renders or this switch is visibly dead: the panel would list a
// TMDB-only season, the switch would read "off" because that season is
// unmonitored, clicking it would write every season EXCEPT that one, and the
// re-read would flip the switch straight back off — forever, no matter how many
// times the operator clicks. Sharing the merge is what keeps the control honest.
func putAllSeasonsMonitoredHandler(catalog seasonCatalog, grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		libStore := catalog.lib
		seriesID, ok := seriesIDPathValue(w, r)
		if !ok {
			return
		}
		var req apidto.SetSeasonMonitoredRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		series, ok := lookupSeries(ctx, w, libStore, seriesID)
		if !ok {
			return
		}
		states, err := catalog.states(ctx, seriesID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// The loop is not transactional, so a mid-loop failure leaves the seasons
		// BEFORE it genuinely written. The cleanup must therefore run for what
		// actually landed even on the error path — returning 500 straight from
		// inside the loop would leave those seasons un-monitored but still
		// retrying, the exact orphan state cancelAirDateRetriesForSeasons exists
		// to prevent. A DB transaction would not help: the cancellation is
		// grabs-store work, outside any library-store transaction.
		touched := map[int]bool{}
		var writeErr error
		for _, st := range states {
			if err := libStore.SetSeasonMonitored(ctx, seriesID, st.SeasonNumber, req.Monitored); err != nil {
				writeErr = err
				break
			}
			touched[st.SeasonNumber] = true
		}
		if !req.Monitored {
			cancelAirDateRetriesForSeasons(ctx, grabsStore, series.TMDBID, touched, time.Now())
		}
		if writeErr != nil {
			http.Error(w, writeErr.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getSeriesNewSeasonDiscoveryHandler reports the new-season discovery toggle,
// false by default.
func getSeriesNewSeasonDiscoveryHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled, err := settingsStore.GetBool(r.Context(), seriesNewSeasonDiscoveryKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, apidto.SeriesNewSeasonDiscoveryResponse{Enabled: enabled})
	}
}

// putSeriesNewSeasonDiscoveryHandler sets the new-season discovery toggle.
//
// Deliberately WITHOUT the coupled interval side effect
// putUsenetAutoGrabEnabledHandler carries: this toggle has no scheduler of its
// own to gate — that is the whole point of running inside the existing cycle.
func putSeriesNewSeasonDiscoveryHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.SeriesNewSeasonDiscoveryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := settingsStore.SetBool(r.Context(), seriesNewSeasonDiscoveryKey, req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// seriesIDPathValue parses the {seriesID} path segment shared by the three
// season routes.
func seriesIDPathValue(w http.ResponseWriter, r *http.Request) (int64, bool) {
	seriesID, err := strconv.ParseInt(r.PathValue("seriesID"), 10, 64)
	if err != nil {
		http.Error(w, "seriesID path parameter must be an integer", http.StatusBadRequest)
		return 0, false
	}
	return seriesID, true
}

// lookupSeries resolves {seriesID} to its tracked series, whose TMDBID is what
// correlates it with grab rows (a grabs row carries a TMDB id and no library
// series id). GetSeries is used rather than ListSeries plus a linear scan: this
// is a primary-key lookup on a route an operator can click repeatedly.
func lookupSeries(ctx context.Context, w http.ResponseWriter, libStore *library.Store, seriesID int64) (*library.Series, bool) {
	series, err := libStore.GetSeries(ctx, seriesID)
	if err != nil {
		if errors.Is(err, library.ErrNotFound) {
			http.Error(w, "no tracked series with that id", http.StatusNotFound)
			return nil, false
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, false
	}
	return series, true
}
