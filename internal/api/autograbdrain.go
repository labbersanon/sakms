// Package api — autograbdrain.go is the short-interval drain worker that
// dispatches one Series episode at a time, newest-first, while a download slot
// is free. It is the EIGHTH interval-driven scheduler in this codebase and the
// SECOND one with dispatch authority (the first being RunUsenetRetry).
//
// Both distinctions are deliberate and documented here rather than hidden:
//   - Eighth scheduler: CLAUDE.md's scheduler enumeration needs updating when
//     this lands (see Step 8 of the drain plan).
//   - Second dispatcher: same opt-in gate (usenet_autograb_enabled), same
//     per-cycle toggle re-read, an explicit CLAUDE.md amendment — the
//     "manual by default" principle is bent twice, intentionally.
//
// Design decisions recorded rather than inferred:
//
//  1. One item at a time (locked decision #2). After each dispatch the slot
//     census is recomputed before the next candidate, so the count cannot
//     drift between a dispatch and the next free-slot check.
//
//  2. Slot accounting reads grabs table rows (queued+downloading by protocol),
//     never the in-memory usenet engine queue. The engine queues internally and
//     accepts jobs beyond the configured concurrency, making its own queue
//     length useless as a census. The grabs table is the restart-surviving
//     source of truth (Risk 5 in the plan: over-count on a slow import flip is
//     the safe direction — the drain waits rather than double-dispatches).
//
//  3. Usenet-first slot semantics: if Usenet slots are full, the drain WAITS.
//     It does not escalate to torrent merely because the Usenet engine is busy.
//     Protocol choice is about where the release lives, not queue length.
//
//  4. Catalog sync is NOT here. The drain is library-only: MonitoredSeasons +
//     MissingEpisodes + eligibleEpisodes. Zero TMDB calls per candidate beyond
//     what autoGrabSearch already does. The daily cycle keeps catalog sync.
//
//  5. Active-grab pre-filter (activeSeriesGrabKeys) is RECOMPUTED every
//     iteration (after each dispatch), not cached for the whole tick. A dispatch
//     in iteration N adds a row that N+1 must see.
//
//  6. In-process token bucket for search pacing. A first run against a large
//     library issues one Usenet search per missing episode. NZBGeek-class
//     indexers cap daily API hits (~100/day free tier). The bucket resets on
//     restart — it smooths, never accounts.
//
// NO GOROUTINE, NO TICKER, NO INTERVAL KEY lives in airdatemonitor.go —
// airdatemonitor_static_test.go enforces that. The drain worker's goroutine and
// ticker live HERE, and autograbdrain_static_test.go mirrors that enforcement
// in the other direction.
//
// To remove entirely: delete this file, its one call in cmd/sakms/main.go, and
// unwind autoGrabDrainIntervalKey from autograb_shared.go and usenetretry.go.
package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

const (
	// autoGrabDrainSearchesPerHourKey is the in-process hourly budget for
	// Usenet (phase-1) searches. Default 60. 0 = unlimited. Reset on restart.
	autoGrabDrainSearchesPerHourKey = "autograb_drain_searches_per_hour"
	// autoGrabDrainTorrentSearchesPerDayKey is the in-process daily budget for
	// torrent (phase-2) searches. Default 25. 0 = unlimited. Reset on restart.
	autoGrabDrainTorrentSearchesPerDayKey = "autograb_drain_torrent_searches_per_day"
	// autoGrabTorrentFallbackEnabledKey is the fallback-C master switch.
	// true by default: Usenet miss → torrent search allowed.
	autoGrabTorrentFallbackEnabledKey = "autograb_torrent_fallback_enabled"
	// autoGrabTorrentFallbackMaxAgeDaysKey limits torrent escalation to
	// episodes that aired within this many days. Default 30 days.
	autoGrabTorrentFallbackMaxAgeDaysKey = "autograb_torrent_fallback_max_age_days"

	defaultAutoGrabDrainSearchesPerHour       = 60
	defaultAutoGrabDrainTorrentSearchesPerDay = 25
	defaultAutoGrabTorrentFallbackMaxAgeDays  = 30
)

// AutoGrabDrainDeps is the fixed dependency set for the drain worker. It
// mirrors RunUsenetRetry's dependency list so the two can be launched with
// consistent wiring from cmd/sakms/main.go.
type AutoGrabDrainDeps struct {
	AutoGrabDeps                        // SettingsStore, NZB, GrabsStore, Webhooks, ReleaseStore
	HTTPClient  *http.Client
	ConnStore   *connections.Store
	SCStore     *serviceconn.Store
	LibStore    *library.Store
	ExcludeStore *excludes.Store
	DL          *downloader.Manager
	NZBManager  *usenet.Manager
}

// LoadAutoGrabDrainInterval reads the drain cadence, returning 0 ("off") for
// any unset, blank, non-integer, or non-positive value — same tolerant read
// LoadUsenetRetryInterval uses.
//
// Claude 2026-09-16: seed drain interval when auto-grab is already on
// Reason: the toggle couples drain on→60 / off→0, but installs that enabled
//   auto-grab before this feature shipped never flip the toggle again — without
//   a seed the drain stays at 0 forever and newest-first Usenet-first drain never
//   runs. One-shot write when enabled && unset/0; never overrides an explicit
//   positive value or an explicit off while auto-grab is off.
// Troubleshooting: journal missing "autograb drain: background drain enabled"
// Review if: settings UI exposes an independent drain interval control
func LoadAutoGrabDrainInterval(ctx context.Context, settingsStore *settings.Store) time.Duration {
	seconds, err := loadIntervalSeconds(ctx, settingsStore, autoGrabDrainIntervalKey, 0)
	if err != nil {
		return 0
	}
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	enabled, err := settingsStore.GetBool(ctx, usenetAutoGrabEnabledKey, false)
	if err != nil || !enabled {
		return 0
	}
	// Auto-grab on but drain unset/0 — seed the coupled default.
	if _, err := storeIntervalSeconds(ctx, settingsStore, autoGrabDrainIntervalKey, 60, 0); err != nil {
		log.Printf("autograb drain: seeding interval failed: %v", err)
		return 0
	}
	return 60 * time.Second
}

// RunAutoGrabDrain drives the drain loop until ctx is cancelled. interval is
// the boot-time cadence (from LoadAutoGrabDrainInterval); when it is <= 0 the
// function returns immediately, so main can call it unconditionally.
//
// Each tick re-reads the interval so an operator switching auto-grab off stops
// the loop cleanly on the next tick. Re-enabling from 0 needs a restart.
func RunAutoGrabDrain(ctx context.Context, interval time.Duration, deps AutoGrabDrainDeps) {
	if interval <= 0 {
		return // opt-in gate: off by default
	}

	build := func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
		return mode.Build(ctx, deps.ConnStore, deps.SCStore, deps.SettingsStore, deps.HTTPClient, deps.DL, m)
	}

	// In-process token buckets for search pacing. Reset on restart (see doc #6).
	usenetBudget := newSearchBudget(hourlyWindow, time.Hour)
	torrentBudget := newSearchBudget(dailyWindow, 24*time.Hour)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("autograb drain: background drain enabled (every %s) — Usenet-first, one at a time, slot-gated", interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := LoadAutoGrabDrainInterval(ctx, deps.SettingsStore)
			if cur <= 0 {
				log.Printf("autograb drain: interval set to 0 — stopping drain (restart to re-enable)")
				return
			}
			if cur != interval {
				interval = cur
				ticker.Reset(cur)
			}
			// Re-read budgets from settings on each tick.
			hourly := drainSettingInt(ctx, deps.SettingsStore, autoGrabDrainSearchesPerHourKey, defaultAutoGrabDrainSearchesPerHour)
			daily := drainSettingInt(ctx, deps.SettingsStore, autoGrabDrainTorrentSearchesPerDayKey, defaultAutoGrabDrainTorrentSearchesPerDay)
			usenetBudget.setMax(hourly)
			torrentBudget.setMax(daily)

			runAutoGrabDrainCycle(ctx, deps, build, usenetBudget, torrentBudget, time.Now())
		}
	}
}

// runAutoGrabDrainCycle performs exactly one drain tick and returns. Extracted
// from the ticker loop so tests can drive it without a clock.
//
// Gate order: enabled flag → global pause → slot census → candidate loop.
func runAutoGrabDrainCycle(
	ctx context.Context,
	deps AutoGrabDrainDeps,
	build sessionBuilderFunc,
	usenetBudget, torrentBudget *searchBudget,
	now time.Time,
) {
	// Gate 1: auto-grab toggle.
	enabled, err := deps.SettingsStore.GetBool(ctx, usenetAutoGrabEnabledKey, false)
	if err != nil || !enabled {
		return
	}

	// Gate 2: global pause (dispatch would 423 anyway — search.go:410-416).
	paused, err := deps.SettingsStore.GetBool(ctx, downloadsGlobalPausedKey, false)
	if err != nil || paused {
		return
	}

	if deps.LibStore == nil {
		return
	}

	seriesList, err := deps.LibStore.ListSeries(ctx)
	if err != nil {
		log.Printf("autograb drain: listing series: %v", err)
		return
	}

	excluded := excludedRequestKeys(ctx, deps.ExcludeStore)
	today := now.UTC().Format("2006-01-02")

	// Build phase order from protocol preference — first behavioural consumer
	// of {mode}_protocol_preference (see AutoGrabRequest.SearchPhases doc).
	phases := drainPhases(ctx, deps.SettingsStore)

	// Torrent fallback settings.
	torrentEnabled, _ := deps.SettingsStore.GetBool(ctx, autoGrabTorrentFallbackEnabledKey, true)
	torrentMaxAge := drainSettingInt(ctx, deps.SettingsStore, autoGrabTorrentFallbackMaxAgeDaysKey, defaultAutoGrabTorrentFallbackMaxAgeDays)

	// Drain loop: one item at a time. We rebuild the session each outer loop
	// iteration because building it is cheap and sess.Prowlarr must exist.
	// We read it once here and reuse; if a config change invalidates it, the
	// next tick will get a fresh one.
	sess, sessErr := build(ctx, mode.Series)
	if sessErr != nil {
		log.Printf("autograb drain: building series session failed (%T) — skipping tick", rootCause(sessErr))
		return
	}
	if sess.TMDB == nil || sess.Prowlarr == nil {
		return // TMDB or Prowlarr not configured
	}

	// Claude 2026-09-17: resume transport-parked rows before the drain proper.
	// Reason: a transport park keeps download_gid; RelaunchNZB resumes into the
	//   existing staging dir without a new search, consuming no indexer budget.
	//   Running it before the drain loop avoids counting resumed grabs as "slots
	//   already consumed" before they have actually been handed off.
	// Review if: the resume pass gains a dedicated interval shorter than 60s.
	var resumeEng usenetResumeEngine
	if deps.NZBManager != nil {
		resumeEng = deps.NZBManager
	}
	resumeDueTransportRetries(ctx, deps.AutoGrabDeps, resumeEng, excluded, now)

	// Claude 2026-09-16: process escalated pending_retry rows first.
	// Reason: a 430 Usenet failure sets next_search_scope='torrent' + retry_after=now
	// so the row is immediately due. The drain handles these on the next tick (≤60s),
	// faster than the daily retry cycle. Only rows with a non-empty next_search_scope
	// are handled here; all other due rows remain the retry cycle's responsibility.
	// "Whichever runs first" picks up the row — if the drain dispatched it, it
	// leaves pending_retry and retryDueGrabs will not see it as due.
	// Review if: the drain should take over all due-retry handling from retryDueGrabs.
	drainEscalatedDueRetries(ctx, deps, sess, excluded, now)

	// Claude 2026-09-17: process alternate-release retries before the main loop.
	// Reason: a content failure (PAR2/unpack/no-video) parks due-now with
	//   tried_release_keys set. drainAlternateReleaseRetries picks these up
	//   on the next drain tick (≤60s) instead of waiting 24h (daily retry).
	//   Placement: after escalated retries (a 430 is higher urgency), before
	//   the missing-episode loop (a failed download outranks a newly-noticed gap).
	// Review if: the two passes are merged into one due-now dispatch loop.
	drainAlternateReleaseRetries(ctx, deps, build, usenetBudget, excluded, now)

	for {
		// Gate 3: slot census — recomputed before every potential dispatch.
		usenetFree, slotErr := freeUsenetSlots(ctx, deps.GrabsStore, deps.SettingsStore)
		if slotErr != nil {
			log.Printf("autograb drain: counting in-flight slots: %v", slotErr)
			return
		}
		if usenetFree <= 0 {
			// Usenet slots full — wait; do not escalate to torrent (plan Risk 3/doc #3).
			return
		}

		// Gate 4: pacing budget.
		if !usenetBudget.allow() {
			log.Printf("autograb drain: hourly Usenet search budget exhausted — waiting until next window")
			return
		}

		// Recompute active-grab pre-filter per iteration so a dispatch from
		// this iteration is visible to the next (plan doc #5).
		activeEpisodes, activeSeasons := activeSeriesGrabKeys(ctx, deps.AutoGrabDeps)

		// Build candidate list: newest-first, library-only, no TMDB call.
		candidates := drainCandidates(ctx, deps, seriesList, excluded, today, activeEpisodes, activeSeasons)
		if len(candidates) == 0 {
			return // nothing eligible
		}

		// Pick the first candidate (newest air date) and dispatch.
		c := candidates[0]

		// Determine phases for this specific candidate.
		candidatePhases := []prowlarr.Scope{prowlarr.ScopeUsenet} // Usenet only by default
		if torrentEnabled && candidateEscalatesToTorrent(c, now, torrentMaxAge) && torrentBudget.allow() {
			candidatePhases = phases // include both phases
		}

		out, runErr := RunAutoGrab(ctx, deps.AutoGrabDeps, sess, AutoGrabRequest{
			Mode:            mode.Series,
			Title:           c.series.Title,
			TMDBID:          c.series.TMDBID,
			TVDBID:          c.series.TVDBID,
			Season:          c.episode.SeasonNumber,
			Episode:         c.episode.EpisodeNumber,
			SeasonSpecified: true,
			RootFolderPath:  c.series.RootFolderPath,
			Trigger:         TriggerAirDate,
			ExistingGrabID:  0,
			SearchPhases:    candidatePhases,
		})

		switch {
		case runErr != nil:
			log.Printf("autograb drain: %s s%02de%02d — RunAutoGrab error (%T)", c.series.Title, c.episode.SeasonNumber, c.episode.EpisodeNumber, rootCause(runErr))
		case out.Gated:
			// Toggle went off mid-cycle.
			log.Printf("autograb drain: usenet auto-grab is switched off — abandoning this tick")
			return
		case out.MovieBlocked:
			// Should not happen for Series, but handle gracefully.
			log.Printf("autograb drain: %s s%02de%02d — movie-release gate blocked (unexpected for series)", c.series.Title, c.episode.SeasonNumber, c.episode.EpisodeNumber)
		case out.AlreadyGrabbing:
			log.Printf("autograb drain: %s s%02de%02d — already being downloaded", c.series.Title, c.episode.SeasonNumber, c.episode.EpisodeNumber)
		case out.Grabbed:
			log.Printf("autograb drain: %s s%02de%02d dispatched", c.series.Title, c.episode.SeasonNumber, c.episode.EpisodeNumber)
			// A dispatch consumed a slot — stop this tick and wait for next.
			return
		default:
			// NoMatch: parked for re-search. No slot consumed; continue to next candidate.
			log.Printf("autograb drain: %s s%02de%02d — no qualifying candidate, parked", c.series.Title, c.episode.SeasonNumber, c.episode.EpisodeNumber)
		}

		// On a miss (or error), remove this candidate's series from consideration
		// for this tick so the next iteration picks from a different series.
		// activeSeriesGrabKeys is rebuilt at the top of each iteration.
		seriesList = drainWithoutSeries(seriesList, c.series.ID)
		if len(seriesList) == 0 {
			return
		}
	}
}

// drainEscalatedDueRetries handles pending_retry rows whose next_search_scope
// is 'torrent' — Usenet-failure escalations that are due for a torrent search.
// It runs at the TOP of each drain tick, before the main missing-episode loop,
// because an escalated row is a higher-urgency signal (a download already failed)
// than a freshly identified missing episode.
//
// Only series-mode escalated rows are handled here; other modes and non-escalated
// due rows remain the daily retryDueGrabs cycle's responsibility.
func drainEscalatedDueRetries(
	ctx context.Context,
	deps AutoGrabDrainDeps,
	sess *mode.Session,
	excluded map[string]bool,
	now time.Time,
) {
	due, err := deps.GrabsStore.DueForRetry(ctx, now)
	if err != nil {
		log.Printf("autograb drain: listing due retries for escalation check: %v", err)
		return
	}
	for _, g := range due {
		if g.Mode != mode.Series || g.NextSearchScope != "torrent" {
			continue // retryDueGrabs handles non-escalated and non-series rows
		}
		if excluded[excludes.Key(string(g.Mode), g.TMDBID, g.Title)] {
			continue
		}
		// Slot check before each escalated dispatch.
		usenetFree, slotErr := freeUsenetSlots(ctx, deps.GrabsStore, deps.SettingsStore)
		if slotErr != nil || usenetFree <= 0 {
			return // wait for next tick
		}
		// Consume the one-shot marker before calling RunAutoGrab, regardless of outcome.
		if clearErr := deps.GrabsStore.ClearNextSearchScope(ctx, g.ID); clearErr != nil {
			log.Printf("autograb drain: clearing next_search_scope for grab %d: %v (continuing)", g.ID, clearErr)
		}
		log.Printf("autograb drain: retry grab %d (%s s%02de%02d) — torrent escalation", g.ID, g.Title, g.SeasonNumber, g.EpisodeNumber)
		out, runErr := RunAutoGrab(ctx, deps.AutoGrabDeps, sess, AutoGrabRequest{
			Mode:            g.Mode,
			Title:           g.Title,
			TMDBID:          g.TMDBID,
			TVDBID:          g.TVDBID,
			Season:          g.SeasonNumber,
			Episode:         g.EpisodeNumber,
			SeasonSpecified: g.SeasonSpecified,
			RootFolderPath:  g.RootFolderPath,
			Trigger:         TriggerRetry,
			ExistingGrabID:  g.ID,
			SearchPhases:    []prowlarr.Scope{prowlarr.ScopeTorrent},
		})
		switch {
		case runErr != nil:
			log.Printf("autograb drain: retry grab %d (%s) — RunAutoGrab error (%T)", g.ID, g.Title, rootCause(runErr))
		case out.Gated:
			log.Printf("autograb drain: usenet auto-grab switched off — abandoning escalated retry pass")
			return
		case out.Grabbed:
			log.Printf("autograb drain: retry grab %d (%s s%02de%02d) dispatched via torrent", g.ID, g.Title, g.SeasonNumber, g.EpisodeNumber)
			return // slot consumed
		default:
			log.Printf("autograb drain: retry grab %d (%s) torrent escalation miss", g.ID, g.Title)
		}
	}
}

// drainAlternateReleaseRetries picks up pending_retry rows that are due-now
// AND carry non-empty tried_release_keys — content-failure alternate-release
// rows parked by parkUsenetContentFailure. These rows have empty
// next_search_scope (they are NOT the 430-escalated torrent rows handled by
// drainEscalatedDueRetries) and would otherwise wait up to 24 h for the daily
// retryDueGrabs cycle.
//
// Per due row (exit-path rules from plan §5 — every exit must dispatch or park):
//   - worklist-excluded: skip (unchanged)
//   - slots full or budget exhausted: return without parking (row re-tried next tick)
//   - RunAutoGrab error or AlreadyGrabbing: reparkFailedRetry (days ladder, keys cleared)
//   - Grabbed: return (slot consumed)
//   - NoMatch: RunAutoGrab already parked via parkPendingRetry → days ladder, keys cleared
//   - Gated: return
//
// "Stops after one dispatch" matches drainEscalatedDueRetries' semantics: one
// slot was consumed and a fresh slot census would be needed.
//
// Claude 2026-09-17: all modes, Usenet-only phases, slot- and budget-gated.
// Reason: movie and Adult scene failures use the same content-failure path as
//   Series; restricting to Series would leave movie/adult rows for the daily cycle.
// Review if: a separate budget key is needed for alternate-release searches.
func drainAlternateReleaseRetries(
	ctx context.Context,
	deps AutoGrabDrainDeps,
	build sessionBuilderFunc,
	usenetBudget *searchBudget,
	excluded map[string]bool,
	now time.Time,
) {
	due, err := deps.GrabsStore.DueForRetry(ctx, now)
	if err != nil {
		log.Printf("autograb drain: listing due retries for alternate-release check: %v", err)
		return
	}
	for _, g := range due {
		if g.TriedReleaseKeys == "" || g.NextSearchScope != "" {
			continue // not an alternate-release row (drainEscalatedDueRetries handles scope ones)
		}
		if excluded[excludes.Key(string(g.Mode), g.TMDBID, g.Title)] {
			continue
		}
		// Slot gate — honour freeUsenetSlots; do not escalate to torrent.
		usenetFree, slotErr := freeUsenetSlots(ctx, deps.GrabsStore, deps.SettingsStore)
		if slotErr != nil {
			log.Printf("autograb drain: counting slots for alternate retry: %v", slotErr)
			return
		}
		if usenetFree <= 0 {
			return // wait for next tick
		}
		// Budget gate.
		if !usenetBudget.allow() {
			log.Printf("autograb drain: hourly Usenet budget exhausted — skipping alternate retries this tick")
			return
		}

		sess, err := build(ctx, g.Mode)
		if err != nil {
			log.Printf("autograb drain: alternate retry grab %d (%s) — session build error (%T)", g.ID, g.Title, rootCause(err))
			reparkFailedRetry(ctx, deps.AutoGrabDeps, g, err)
			return
		}
		triedKeys := grabs.ParseTriedReleaseKeys(g.TriedReleaseKeys)
		log.Printf("autograb drain: alternate retry grab %d (%s) — searching Usenet-only, %d keys excluded",
			g.ID, g.Title, grabs.AlternateAttempts(triedKeys))
		out, runErr := RunAutoGrab(ctx, deps.AutoGrabDeps, sess, AutoGrabRequest{
			Mode: g.Mode, Title: g.Title, TMDBID: g.TMDBID, TVDBID: g.TVDBID,
			Season: g.SeasonNumber, Episode: g.EpisodeNumber, SeasonSpecified: g.SeasonSpecified,
			RootFolderPath:     g.RootFolderPath,
			Trigger:            TriggerRetry,
			ExistingGrabID:     g.ID,
			SearchPhases:       []prowlarr.Scope{prowlarr.ScopeUsenet},
			ExcludeReleaseKeys: triedKeys,
		})
		switch {
		case runErr != nil:
			log.Printf("autograb drain: alternate retry grab %d (%s) — RunAutoGrab error (%T)", g.ID, g.Title, rootCause(runErr))
			reparkFailedRetry(ctx, deps.AutoGrabDeps, g, runErr)
			return
		case out.Gated:
			log.Printf("autograb drain: auto-grab switched off — abandoning alternate retry pass")
			return
		case out.AlreadyGrabbing:
			duplicate := "another grab"
			if out.GrabID != 0 {
				duplicate = fmt.Sprintf("grab %d", out.GrabID)
			}
			log.Printf("autograb drain: alternate retry grab %d (%s) already downloading by %s", g.ID, g.Title, duplicate)
			reparkFailedRetry(ctx, deps.AutoGrabDeps, g, fmt.Errorf("already downloading by %s", duplicate))
			return
		case out.Grabbed:
			log.Printf("autograb drain: alternate retry grab %d (%s) dispatched", g.ID, g.Title)
			return // slot consumed — stop this tick
		default:
			// NoMatch: RunAutoGrab already parked via parkPendingRetry → days ladder, keys cleared.
			log.Printf("autograb drain: alternate retry grab %d (%s) — no qualifying alternate candidate, parked on days ladder", g.ID, g.Title)
		}
	}
}

// freeUsenetSlots counts how many Usenet download slots are available. Slot
// accounting reads the grabs table (queued+downloading rows with
// protocol='usenet'), not the in-memory usenet engine queue. Returns free
// Usenet slot count and any store error.
func freeUsenetSlots(ctx context.Context, grabsStore *grabs.Store, settingsStore *settings.Store) (int, error) {
	maxUsenet, err := getSettingInt(ctx, settingsStore, UsenetMaxConcurrentDownloadsKey, usenet.DefaultMaxConcurrentDownloads)
	if err != nil {
		return 0, err
	}
	inFlight := 0
	for _, m := range usenetRetryModes {
		list, err := grabsStore.List(ctx, m)
		if err != nil {
			return 0, err
		}
		for _, g := range list {
			if (g.Status == grabs.Queued || g.Status == grabs.Downloading) && g.Protocol == string(prowlarr.Usenet) {
				inFlight++
			}
		}
	}
	free := maxUsenet - inFlight
	if free < 0 {
		free = 0
	}
	return free, nil
}

// drainPhases returns the ordered phase list for the drain based on the Series
// mode's protocol_preference setting — first behavioural consumer of that field.
// "usenet" or "" → [ScopeUsenet, ScopeTorrent]; "torrent" → reverse order.
func drainPhases(ctx context.Context, settingsStore *settings.Store) []prowlarr.Scope {
	pref, _ := settingsStore.Get(ctx, "series_protocol_preference")
	if pref == "torrent" {
		return []prowlarr.Scope{prowlarr.ScopeTorrent, prowlarr.ScopeUsenet}
	}
	// "usenet" or "" → Usenet first (locked product decision #3).
	return []prowlarr.Scope{prowlarr.ScopeUsenet, prowlarr.ScopeTorrent}
}

// drainCandidates builds the per-tick candidate list: monitored + eligible
// episodes sorted newest-first. No TMDB calls, no catalog sync.
func drainCandidates(
	ctx context.Context,
	deps AutoGrabDrainDeps,
	seriesList []library.Series,
	excluded map[string]bool,
	today string,
	activeEpisodes map[seriesEpisodeKey]bool,
	activeSeasons map[seriesSeasonKey]bool,
) []airDateCandidate {
	var candidates []airDateCandidate
	for _, series := range seriesList {
		if excluded[excludes.Key(string(mode.Series), series.TMDBID, series.Title)] {
			continue
		}
		monitored, err := deps.LibStore.MonitoredSeasons(ctx, series.ID)
		if err != nil || len(monitored) == 0 {
			continue
		}
		missing, err := deps.LibStore.MissingEpisodes(ctx, series.ID)
		if err != nil {
			continue
		}
		for _, ep := range eligibleEpisodes(missing, monitored, today) {
			if activeSeasons[seriesSeasonKey{series.TMDBID, ep.SeasonNumber}] {
				continue
			}
			if activeEpisodes[seriesEpisodeKey{series.TMDBID, ep.SeasonNumber, ep.EpisodeNumber}] {
				continue
			}
			candidates = append(candidates, airDateCandidate{series: series, episode: ep})
		}
	}
	// Newest air date first (locked decision #1). Tie-breakers ascending for
	// determinism across runs.
	drainSortNewestFirst(candidates)
	return candidates
}

// drainSortNewestFirst sorts candidates descending by air date with ascending
// series title / season / episode as tie-breakers.
func drainSortNewestFirst(candidates []airDateCandidate) {
	n := len(candidates)
	if n <= 1 {
		return
	}
	// Simple insertion sort (small N, already-seeded per series in order).
	for i := 1; i < n; i++ {
		for j := i; j > 0 && drainCandidateLess(candidates[j], candidates[j-1]); j-- {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
		}
	}
}

// drainCandidateLess reports a < b in newest-first order.
func drainCandidateLess(a, b airDateCandidate) bool {
	if a.episode.AirDate != b.episode.AirDate {
		return a.episode.AirDate > b.episode.AirDate // newer sorts first
	}
	if a.series.Title != b.series.Title {
		return a.series.Title < b.series.Title
	}
	if a.episode.SeasonNumber != b.episode.SeasonNumber {
		return a.episode.SeasonNumber < b.episode.SeasonNumber
	}
	return a.episode.EpisodeNumber < b.episode.EpisodeNumber
}

// candidateEscalatesToTorrent reports whether c qualifies for torrent-fallback
// phase escalation: aired within maxAgeDays of now.
// A fresh candidate has no grabs row yet, so only the age predicate is
// available here. retryDueGrabs rows (which have a retry_count) read
// next_search_scope instead (see step E / usenetretry.go).
func candidateEscalatesToTorrent(c airDateCandidate, now time.Time, maxAgeDays int) bool {
	if maxAgeDays <= 0 {
		return true // unlimited
	}
	cutoff := now.UTC().AddDate(0, 0, -maxAgeDays).Format("2006-01-02")
	return c.episode.AirDate >= cutoff
}

// drainWithoutSeries returns a copy of seriesList omitting the entry for id.
func drainWithoutSeries(seriesList []library.Series, id int64) []library.Series {
	out := make([]library.Series, 0, len(seriesList))
	for _, s := range seriesList {
		if s.ID != id {
			out = append(out, s)
		}
	}
	return out
}

// drainSettingInt reads an int setting with a default, degrading to the
// default on any error — same resilience pattern as loadAutoGrabSlotValue.
func drainSettingInt(ctx context.Context, store *settings.Store, key string, def int) int {
	n, err := getSettingInt(ctx, store, key, def)
	if err != nil {
		return def
	}
	return n
}

// --- Search pacing token bucket ------------------------------------------

// budgetWindowKind classifies a searchBudget's refill period.
type budgetWindowKind int

const (
	hourlyWindow budgetWindowKind = iota
	dailyWindow
)

// searchBudget is a simple in-process token bucket for search pacing. It is
// reset on restart — it smooths, never accounts (plan Risk 3).
// The drain runs sequentially per tick, so no mutex is needed.
type searchBudget struct {
	max       int
	remaining int
	window    budgetWindowKind
	period    time.Duration
	resetAt   time.Time
}

func newSearchBudget(window budgetWindowKind, period time.Duration) *searchBudget {
	return &searchBudget{
		max:     0, // unlimited until setMax is called
		window:  window,
		period:  period,
		resetAt: time.Now().Add(period),
	}
}

func (b *searchBudget) setMax(max int) {
	if b.max == max {
		return
	}
	b.max = max
	b.remaining = max
}

// allow reports whether a search is allowed and consumes one token.
// Returns true when max == 0 (unlimited).
func (b *searchBudget) allow() bool {
	if b.max == 0 {
		return true // unlimited
	}
	now := time.Now()
	if now.After(b.resetAt) {
		b.remaining = b.max
		b.resetAt = now.Add(b.period)
	}
	if b.remaining <= 0 {
		return false
	}
	b.remaining--
	return true
}
