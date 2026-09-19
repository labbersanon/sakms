package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
	"github.com/labbersanon/sakms/internal/usenetsearch"
	"github.com/labbersanon/sakms/internal/webhooks"
)

// AutoGrabTrigger names why an auto-grab ran. It is NOT cosmetic: it decides
// whether the usenet_autograb_enabled toggle applies.
type AutoGrabTrigger string

const (
	// TriggerOperator is Discover's shipped one-click Grab. It is UNGATED and
	// must stay that way: the operator's click IS the approval (CLAUDE.md's
	// 2026-07-24 bulk-grab amendment), so this path already ships and has to
	// keep working with the toggle off. Gating it would silently break a live
	// feature the moment this lands with the default-off toggle.
	TriggerOperator AutoGrabTrigger = "operator"
	// TriggerRequest is the toggle-gated Usenet Search path (runToggleGatedSearch,
	// reached from both GET /api/modes/{mode}/search and GET /api/modes/adult/search).
	// No human is in the loop, which is exactly what the toggle exists to gate.
	TriggerRequest AutoGrabTrigger = "request"
	// TriggerRetry is the unattended re-search cycle. Gated for the same reason.
	TriggerRetry AutoGrabTrigger = "retry"
	// TriggerAirDate is Series air-date monitoring: an aired-but-missing
	// episode of a monitored season, detected and dispatched by
	// monitorAirDates (internal/api/airdatemonitor.go), which runs as the
	// THIRD pass inside runUsenetRetryCycle — not as a scheduler of its own.
	// Gated for the same reason as the two above: the gate below applies to
	// it by construction, because it applies to every non-TriggerOperator
	// trigger. CORRECTED 2026-08-01 — superseded claim, quoted: "reserved for
	// a future series-monitoring trigger… Deliberately unimplemented here."
	// It shipped 2026-08-01, and the promise the old comment made held: the
	// whole diff to this file is this doc comment, with no second scoring,
	// dispatch or gating path added anywhere.
	TriggerAirDate AutoGrabTrigger = "airdate"
	// TriggerPreRelease is a Calendar pre-release request whose hold_until has
	// arrived: an operator clicked an unreleased movie in Calendar's Upcoming view,
	// and the release date has now passed. Detected and dispatched by
	// releaseDueGrabs (internal/api/prerelease.go), the FOURTH pass of
	// runUsenetRetryCycle. Gated like every non-TriggerOperator trigger, by
	// construction — the gate below applies to it without a second gating path.
	//
	// The click and the dispatch are separated by days-to-months, which is what
	// makes this a genuinely new category ("deferred operator approval", §6.1) and
	// not a member of TriggerOperator's immediate-click exemption.
	TriggerPreRelease AutoGrabTrigger = "prerelease"
)

const (
	// usenetAutoGrabEnabledKey is the global unattended-auto-grab toggle,
	// default false. RunAutoGrab is the one place the GATE is enforced; a
	// handler may read the same key to pick a response shape (see
	// searchHandler/adultSearchHandler), which is why both use this constant
	// rather than a second literal that could drift.
	usenetAutoGrabEnabledKey = "usenet_autograb_enabled"
	// usenetRetryIntervalSecondsKey is mirrored here rather than imported from
	// the retry scheduler, the same convention every other interval-backed job
	// in this package follows (see interval.go).
	usenetRetryIntervalSecondsKey = "usenet_retry_interval_seconds"
	// defaultUsenetRetryIntervalSeconds is the spec's 24-hour fallback for an
	// unset or switched-off retry interval. It no longer backs retry_after —
	// parks moved to grabs.RetryBackoff (see the note above parkGrabForRetry).
	defaultUsenetRetryIntervalSeconds = 86400

	// autoGrabDrainIntervalKey is the drain worker's cadence setting.
	// 0 = off (default), written to 60 by putUsenetAutoGrabEnabledHandler
	// alongside usenet_retry_interval_seconds so the two stay coupled.
	// When > 0 the drain owns air-date dispatch; the daily cycle's pass only
	// runs catalog sync + backoff sweep. See autograbdrain.go.
	autoGrabDrainIntervalKey = "autograb_drain_interval_seconds"
)

const (
	// noQualifyingCandidateReason is the retry_reason for a Selection.Fallback
	// row: the scorer graded every candidate and none cleared the floor.
	noQualifyingCandidateReason = "no candidate cleared the quality floor"
	// articlesUnavailableReason is the retry_reason for a retrieval failure
	// where every configured subscription answered 430.
	articlesUnavailableReason = "no configured usenet subscription holds this release's articles"
	// maxDispatchAttempts caps how many ranked candidates one RunAutoGrab
	// cycle will try when precheck rejects NZBs (indexer grab-quota bound).
	maxDispatchAttempts = 3
	// weakIdentityReason is the retry_reason when Adult identity signals are
	// too thin for silent unattended dispatch (adultIdentityWeak returned true).
	//
	// A4: TriggerRetry ALWAYS produces this reason for Adult — the grabs row
	// stores neither studio nor performers (the grab-at-dispatch origin), so
	// adultIdentityWeak returns true unconditionally for every retry attempt.
	// A5: TriggerOperator also parks a pending_retry (no approve-and-dispatch
	// button on the Requests view for weak rows; the operator re-grabs manually
	// from Requests or Discover). Do NOT branch on this string: it is
	// operator-facing copy only — use grabs row fields for logic.
	weakIdentityReason = "identity signals too thin for unattended dispatch"
	// heldRequestReason is the retry_reason a Calendar pre-release request
	// carries while it waits for its release date.
	//
	// OPERATOR-FACING COPY ONLY. Nothing anywhere branches on it, and nothing
	// ever may: hold_until is this feature's origin marker precisely so that
	// origin is a queryable column rather than an inference from a reason string
	// — the pattern that caused a HIGH-severity misclassification bug in series
	// air-date monitoring (see airDateShaped's doc on why a reason-string marker
	// cannot even survive one cycle). An executor who reintroduces a
	// `RetryReason == heldRequestReason` test has re-created that fragility
	// class.
	heldRequestReason = "held until the day after its release date"
)

// AutoGrabDeps is the fixed dependency set every trigger needs. The mode
// Session is passed separately (not held here) so this struct stays reusable
// across modes within one request — runToggleGatedSearch's signature already
// takes sess on its own.
//
// Webhooks is threaded for parity with grabHandler's dependency set, which
// BE-5's route registration mirrors. It is deliberately NOT dispatched from
// here: the shipped one-click auto-grab path never fired a webhook, and adding
// one would be a behaviour change this extraction is not allowed to make.
type AutoGrabDeps struct {
	SettingsStore *settings.Store
	NZB           *usenet.Manager
	GrabsStore    *grabs.Store
	Webhooks      *webhooks.Store
	// ReleaseStore is the Adult release cache store; nil degrades to a live
	// Prowlarr search on every call.
	ReleaseStore *adultnewest.ReleaseStore
	// UsenetSearch is the optional native NNTP discovery backend. Nil = inert
	// (flag-off / not wired). Never errors a grab when unset or not ready.
	UsenetSearch *usenetsearch.Service
}

// AutoGrabRequest is the mode-agnostic description of what to auto-grab.
//
// Distinct from apidto.AutoGrabRequest, which is the WIRE shape Discover's
// one-click endpoint decodes; this one is the internal call shape every
// trigger builds, including triggers that have no HTTP request at all.
type AutoGrabRequest struct {
	Mode            mode.Mode
	Title           string
	TMDBID          int
	TVDBID          int
	Season          int
	Episode         int
	SeasonSpecified bool
	// RootFolderPath, when empty, is resolved from the mode's configured
	// library root — but only on the qualifying path, exactly where
	// autoGrabHandler resolved it before this extraction, so a fallback still
	// returns a pick list on an install with no root folder set.
	RootFolderPath string
	Trigger        AutoGrabTrigger

	// Studio/ReleaseTitle/DurationSeconds/Box/SceneID/Performers are Adult-only
	// inputs to the internal search; ignored when Releases is supplied.
	//
	// Box/SceneID carry the catalog scene identity for cache-key derivation.
	// Populated by autoGrabHandler from the wire request; retryDueGrabs cannot
	// supply them because the grabs table stores neither — see A4/§7.5.
	//
	// Performers is a SOFT identity signal — adultIdentityWeak uses it to
	// decide whether to stage for approval. NEVER a hard-reject predicate.
	Studio          string
	ReleaseTitle    string
	DurationSeconds int
	Box             string
	SceneID         string
	Performers      []string

	// Releases, when non-nil, is an already-fetched, already-deduped candidate
	// list; RunAutoGrab then SKIPS its internal autoGrabSearch and scores these.
	//
	// This is mandatory for the Search hook, not an optimisation: autoGrabSearch
	// is TMDB-id-driven for Movies/Series (MovieDetails/ExternalIDs) while both
	// Search routes carry only ?q=, and Adult may spend exactly one Prowlarr
	// call per submit. A caller supplying a pre-fetched list MUST pass a
	// non-nil slice even when the search returned nothing — a nil slice means
	// "search for me", and zero candidates must reach Selection.Fallback rather
	// than an internal search that cannot work here.
	Releases []prowlarr.Release
	// RuntimeSeconds is the bitrate scorer's runtime scalar; 0 = unknown.
	RuntimeSeconds float64

	// ExistingGrabID, when non-zero, is the pending_retry row this run is a
	// retry OF. A qualifying dispatch then RE-ARMS that row (grabs.Relaunch)
	// instead of recording a second one.
	//
	// Load-bearing for the retry cycle, not a convenience: DueForRetry's
	// contract is that a successfully retried row gains a GID and rejoins the
	// normal ActiveByDownloadGID-guarded lifecycle. Creating a fresh row would
	// leave the original still pending_retry with a retry_after in the past, so
	// the next cycle would grab the same release again — and AddNZB mints a
	// fresh GID per call, so the GID dedup guard cannot catch that duplicate.
	// Zero on every non-retry trigger, which keeps their Create path unchanged.
	ExistingGrabID int64

	// SearchPhases, when non-empty, drives the two-phase Usenet-first search
	// inside RunAutoGrab. Each element is a prowlarr.Scope sentinel passed to
	// autoGrabSearch; RunAutoGrab tries them in order and stops at the first
	// phase that produces a qualifying (non-Fallback) SelectBest result.
	//
	// Empty value means today's single all-indexer search (ScopeAll) — every
	// existing trigger (operator one-click, batch, Search hook, pre-release,
	// Adult, retry) passes no phases and keeps its current wire contract
	// unchanged. Only the drain worker and the torrent-escalation path set it.
	//
	// When phase 2 runs but also misses, out.Releases/out.Selection describe
	// the last phase's results — documented here because rankedAutoGrabCandidates
	// renders them for TriggerOperator and the Requests view.
	//
	// Claude 2026-09-16: first consumer of {mode}_protocol_preference setting.
	// Reason: the drain derives its default phase order from the mode's stored
	//   protocol preference; "usenet" or "" → [ScopeUsenet, ScopeTorrent];
	//   "torrent" → [ScopeTorrent, ScopeUsenet]. This makes the previously
	//   inert setting live — note in PR.
	SearchPhases []prowlarr.Scope

	// ExcludeReleaseKeys, when non-empty, is the set of hashed URL+title keys
	// derived from releases that have already been tried and found content-unusable
	// for this grab. scoreOnePhase drops any candidate whose own ReleaseKeys
	// intersects this set before scoring, so the same bad NZB is never
	// re-dispatched within one alternate-release episode.
	//
	// Values are in the "u:<16 hex>" / "t:<16 hex>" format produced by
	// grabs.ReleaseKeys. The filter is applied after the search (or after
	// adopting req.Releases) and before FilterSeasonScope/buildAutoGrabCandidates.
	// Zero survivors after filtering is not a special case: SelectBest returns
	// Fallback, and parkPendingRetry → ParkWithBackoff parks on the days ladder,
	// clearing the keys.
	//
	// Claude 2026-09-17: never written for ScopeTorrent phases or ScopeAll;
	//   only set by the content-failure alternate-release path (Usenet-only).
	ExcludeReleaseKeys []string

	// SkipNativePhase prevents re-prepending ScopeNative. Set when falling
	// back from native precheck exhaustion to remaining Prowlarr phases (§7.3).
	SkipNativePhase bool
}

// AutoGrabOutcome is what happened. Exactly one of Grabbed / AlreadyGrabbing /
// NoMatch / Gated is true on a successful call.
type AutoGrabOutcome struct {
	// Grabbed: a candidate qualified and was dispatched to a download client.
	Grabbed bool
	GrabID  int64
	// Grab is the recorded grab (or, with AlreadyGrabbing, the in-flight one).
	Grab *apidto.Grab
	// AlreadyGrabbing: the dispatch returned a GID an in-flight grab already
	// holds, so no duplicate row was recorded.
	AlreadyGrabbing bool
	// NoMatch: Selection.Fallback — nothing cleared the quality floor. On a
	// gated trigger a pending_retry row was created or refreshed; on
	// TriggerOperator nothing was persisted and Selection/Releases carry the
	// ranked manual pick list.
	NoMatch bool
	// Gated: a non-operator trigger ran while usenet_autograb_enabled was off.
	// Nothing was searched, scored, dispatched or recorded.
	Gated bool
	// MovieBlocked: the movie-release gate blocked dispatch because no US
	// digital/physical/TV release (types 4/5/6) exists yet or is confirmed
	// on TMDB. Unlike Gated, this does NOT abort the whole releaseDueGrabs
	// cycle — one blocked movie is re-held and the cycle continues with the
	// next row.
	MovieBlocked bool

	Selection autograb.Selection
	// Releases is the candidate list actually scored, index-aligned with
	// Selection's indices.
	Releases []prowlarr.Release

	// RetryStatus is the status the pending_retry row landed on. It is always
	// PendingRetry: SetPendingRetry always parks with a real retry_after, so
	// the Failed branch this field once documented is unreachable.
	// CORRECTED 2026-08-01 — superseded claim, quoted: "PendingRetry
	// normally, or Failed once SetPendingRetry's retry cap is exceeded." The
	// retry-attempt cap was removed 2026-08-01, an explicit product decision;
	// there is no cap left to exceed. Same correction as parkPendingRetry's
	// read-back comment below, which had already been fixed — this field doc
	// was the surviving copy of the same falsehood.
	RetryStatus grabs.Status
	RetryReason string

	// Status is the HTTP status a caller should surface when Err is set.
	Status int
	// Err mirrors the error RunAutoGrab returns, so a caller that keeps only
	// the Outcome still carries the failure.
	Err error
}

// RunAutoGrab is the ONE gated scoring-and-dispatch path: gate -> candidates ->
// autograb.Select -> dispatch, or park a pending_retry row when nothing
// qualifies. Every trigger goes through here and nothing else, which is what
// lets a future trigger be a single new const plus a caller.
//
// The toggle gate lives HERE rather than at each call site — that is the whole
// point of the extraction. TriggerOperator bypasses it (see the const's doc);
// every other trigger is gated.
//
// sess is separate from deps because one Session is mode-scoped while deps are
// not, and because runToggleGatedSearch's own signature takes it separately.
func RunAutoGrab(ctx context.Context, deps AutoGrabDeps, sess *mode.Session, req AutoGrabRequest) (AutoGrabOutcome, error) {
	deps = WithUsenetSearch(deps)
	if req.Trigger != TriggerOperator {
		enabled, err := deps.SettingsStore.GetBool(ctx, usenetAutoGrabEnabledKey, false)
		if err != nil {
			return AutoGrabOutcome{Status: http.StatusInternalServerError, Err: err}, err
		}
		if !enabled {
			return AutoGrabOutcome{Gated: true, Status: http.StatusOK}, nil
		}
	}

	// Claude 2026-09-16: movie-release gate — Layer 2 of CAM-prevention.
	// Reason: a theatrical-only movie must never be searched or dispatched.
	//   This gate runs after the toggle (which proves the operator wants unattended
	//   grab) but before autoGrabSearch (so no indexer is ever contacted for an
	//   unreleased film). TriggerOperator is NOT exempt: an operator clicking
	//   Discover Grab on a film still in cinemas is a mistake the gate prevents.
	//   See moviereleasegate.go for the full decision table.
	// Troubleshooting: unexpected 409s on movie auto-grabs → check TMDB
	//   release_dates for the film (types 4/5/6 US entries).
	// Review if: the acquirable definition or the gate's call sites change.
	if rel, blocked, reason := gateMovieGrab(ctx, sess.TMDB, req.Mode, req.TMDBID); blocked {
		log.Printf("movie-release gate: blocked %q (tmdbID=%d): %s", req.Title, req.TMDBID, reason)
		if req.ExistingGrabID != 0 && deps.GrabsStore != nil {
			// Re-hold the promoted row so it stays on the release track (not
			// the retry track) and comes back when the date arrives.
			if err := deps.GrabsStore.HoldForRelease(ctx, req.ExistingGrabID, rel.HoldUntil, awaitingReleaseReason); err != nil {
				log.Printf("movie-release gate: failed to re-hold grab %d: %v", req.ExistingGrabID, err)
			}
		}
		return AutoGrabOutcome{MovieBlocked: true, Status: http.StatusConflict}, nil
	}

	// Claude 2026-09-16: two-phase Usenet-first search.
	// Reason: SearchPhases drives protocol-scoped indexer queries — phase 1 is
	//   Usenet-only (indexerIds=-1), phase 2 torrent-only (indexerIds=-2) — so
	//   torrent indexers are never queried on a Usenet hit. Empty SearchPhases
	//   means today's single ScopeAll search, preserving every existing trigger's
	//   wire contract. See AutoGrabRequest.SearchPhases for phase semantics.
	// Troubleshooting: unexpected extra Prowlarr calls → check SearchPhases at
	//   the call site; "out.Releases describes the wrong phase" → see below.
	// Review if: phase ordering logic moves out of the drain into the caller.
	phases := req.SearchPhases
	if len(phases) == 0 {
		phases = []prowlarr.Scope{prowlarr.ScopeAll}
	}
	// Claude 2026-09-17: prepend native NNTP phase when flags allow.
	// Reason: native-first, Prowlarr fallback on miss; never change flag-off behaviour.
	// Troubleshooting: native miss should log and continue; never 502.
	// Review if: SearchPhases becomes a struct with Native bool.
	if nativePhaseWanted(ctx, deps, req.Mode) && !req.SkipNativePhase {
		phases = prependNativePhase(phases)
	}

	// scoreOnePhase runs one phase's search → filter → score and returns the
	// releases and Selection for that phase. Extracted here rather than as a
	// named function to keep the closure over req/sess/deps without threading
	// every parameter through a signature.
	type phaseResult struct {
		releases       []prowlarr.Release
		runtimeSeconds float64
		sel            autograb.Selection
		native         bool
	}
	scoreOnePhase := func(scope prowlarr.Scope) (phaseResult, error) {
		var pr phaseResult
		pr.native = scope.IsNative()

		if scope.IsNative() {
			// Pre-fetched releases skip native (Search hook already has a list).
			if req.Releases != nil {
				pr.releases = req.Releases
				pr.runtimeSeconds = req.RuntimeSeconds
			} else {
				rels, err := nativeAutoGrabSearch(ctx, deps, req)
				if err != nil {
					return pr, err
				}
				pr.releases = rels
				pr.runtimeSeconds = req.RuntimeSeconds
			}
		} else if req.Releases != nil {
			pr.releases = req.Releases
			pr.runtimeSeconds = req.RuntimeSeconds
		} else {
			var err error
			pr.releases, pr.runtimeSeconds, err = autoGrabSearch(ctx, sess, req.Mode, deps.ReleaseStore, scope, apidto.AutoGrabRequest{
				Title: req.Title, TMDBID: req.TMDBID, Studio: req.Studio,
				SeasonNumber: req.Season, EpisodeNumber: req.Episode,
				SeasonSpecified: req.SeasonSpecified, DurationSeconds: req.DurationSeconds,
				ReleaseTitle: req.ReleaseTitle,
				Box:          req.Box, SceneID: req.SceneID, Performers: req.Performers,
			})
			if err != nil {
				return pr, err
			}
		}

		if len(req.ExcludeReleaseKeys) > 0 {
			pr.releases = filterExcludedReleases(pr.releases, req.ExcludeReleaseKeys)
		}

		// Claude 2026-09-19: drop blocked release groups + password-titled NZBs (2b/3c).
		// Reason: unattended path must not pick TupaC-style password RARs or 430-bait.
		// Troubleshooting: Advanced → blocked release groups; default includes TupaC.
		// Review if: manual Search should share the same filter via Profile.BlockedGroups.
		if blocked, err := loadBlockedReleaseGroups(ctx, deps.SettingsStore); err == nil {
			pr.releases = filterBlockedReleaseGroups(pr.releases, blocked)
		}

		if req.Mode == mode.Series {
			pr.releases = FilterSeasonScope(pr.releases, req.Season, req.Episode, req.SeasonSpecified)
		}

		neutralizeSeasonPacks := req.Mode == mode.Series && pr.runtimeSeconds > 0
		candidates := buildAutoGrabCandidates(pr.releases, pr.runtimeSeconds, neutralizeSeasonPacks)
		pr.sel = autograb.SelectBest(candidates, autoGrabTiers(ctx, deps.SettingsStore, req.Mode), minSeedersFor(req.Mode))
		return pr, nil
	}

	// Phase loop: try each phase in order, stop at the first qualifying result.
	// out.Releases/out.Selection describe the LAST phase run (the one acted on),
	// which is the qualifying one on a hit or the final phase on a total miss.
	var (
		releases       []prowlarr.Release
		runtimeSeconds float64
		sel            autograb.Selection
		nativePhase    bool
	)
	for i, phase := range phases {
		pr, err := scoreOnePhase(phase)
		if err != nil {
			// Claude 2026-09-17: native phase errors degrade to next phase (§7.2).
			if phase.IsNative() {
				log.Printf("auto-grab: native phase error for %q: %v — continuing", req.Title, err)
				continue
			}
			return AutoGrabOutcome{Status: http.StatusBadGateway, Err: err}, err
		}
		releases = pr.releases
		runtimeSeconds = pr.runtimeSeconds
		sel = pr.sel
		nativePhase = pr.native
		if !sel.Fallback || req.Releases != nil || i == len(phases)-1 {
			break
		}
		log.Printf("auto-grab: phase %d (%v) miss for %q — trying next phase", i+1, phase, req.Title)
	}
	_ = runtimeSeconds // retained for future scoreOnePhase callers if needed

	out := AutoGrabOutcome{Selection: sel, Releases: releases, Status: http.StatusOK}

	if sel.Fallback {
		out.NoMatch = true
		if req.Trigger == TriggerOperator {
			return out, nil
		}
		g, err := parkPendingRetry(ctx, deps, req, noQualifyingCandidateReason)
		if err != nil {
			out.Status, out.Err = http.StatusInternalServerError, err
			return out, err
		}
		out.GrabID, out.RetryStatus, out.RetryReason = g.ID, g.Status, g.RetryReason
		return out, nil
	}

	if req.Mode == mode.Adult && adultIdentityWeak(req.Studio, req.Performers, releases[sel.PickIndex].Title) {
		out.NoMatch = true
		g, err := parkPendingRetry(ctx, deps, req, weakIdentityReason)
		if err != nil {
			out.Status, out.Err = http.StatusInternalServerError, err
			return out, err
		}
		out.GrabID, out.RetryStatus, out.RetryReason = g.ID, g.Status, g.RetryReason
		return out, nil
	}

	rootFolder := req.RootFolderPath
	if rootFolder == "" {
		var err error
		rootFolder, err = autoGrabRootFolder(ctx, deps.SettingsStore, req.Mode)
		if err != nil {
			out.Status, out.Err = http.StatusBadRequest, err
			return out, err
		}
	}
	var (
		picked         = releases[sel.PickIndex]
		downloadClient string
		gid            string
		err            error
	)
	order := qualifiedCandidateOrder(sel)
	for _, idx := range order[:min(len(order), maxDispatchAttempts)] {
		picked = releases[idx]
		var status int
		downloadClient, gid, status, err = dispatchToDownloadClient(ctx, deps.SettingsStore, sess, req.Mode, deps.NZB, deps.UsenetSearch, string(picked.Protocol), picked.DownloadURL, picked.Title)
		if err == nil {
			sel.PickIndex = idx
			out.Selection = sel
			break
		}
		if !errors.Is(err, usenet.ErrArticlesUnavailable) {
			out.Status, out.Err = status, err
			return out, err
		}
		log.Printf("usenet precheck: candidate %d (%s) unavailable — trying next", idx, picked.Title)
	}
	// Claude 2026-09-17: native precheck exhaustion → remaining Prowlarr phases (§7.3).
	if err != nil && nativePhase && errors.Is(err, usenet.ErrArticlesUnavailable) {
		rest := phasesAfterNative(phases)
		if len(rest) > 0 && req.Releases == nil {
			log.Printf("auto-grab: native candidates exhausted for %q — falling back to Prowlarr phases", req.Title)
			req2 := req
			req2.SearchPhases = stripNativePhase(rest)
			req2.SkipNativePhase = true
			return RunAutoGrab(ctx, deps, sess, req2)
		}
	}
	if err != nil {
		out.NoMatch = true
		if req.Trigger == TriggerOperator {
			return out, nil
		}
		g, parkErr := parkPendingRetry(ctx, deps, req, articlesUnavailableReason)
		if parkErr != nil {
			out.Status, out.Err = http.StatusInternalServerError, parkErr
			return out, parkErr
		}
		out.GrabID, out.RetryStatus, out.RetryReason = g.ID, g.Status, g.RetryReason
		return out, nil
	}

	if existing, dup, status, err := activeGrabForGID(ctx, deps.GrabsStore, req.Mode, gid); dup || err != nil {
		if err != nil {
			out.Status, out.Err = status, err
			return out, err
		}
		out.AlreadyGrabbing, out.Grab = true, existing
		if existing != nil {
			out.GrabID = existing.ID
		}
		return out, nil
	}

	// A retry re-arms its own row rather than recording a second one — see
	// AutoGrabRequest.ExistingGrabID and grabs.Relaunch for why a fresh row here
	// would make every following cycle re-grab the same release.
	if req.ExistingGrabID != 0 {
		if err := deps.GrabsStore.Relaunch(ctx, req.ExistingGrabID, grabs.Dispatch{
			Indexer: picked.Indexer, Protocol: string(picked.Protocol),
			DownloadClient: downloadClient, RootFolderPath: rootFolder,
			DownloadURL: picked.DownloadURL, GID: gid,
		}); err != nil {
			out.Status, out.Err = http.StatusInternalServerError, err
			return out, err
		}
		relaunched, err := deps.GrabsStore.Get(ctx, req.ExistingGrabID)
		if err != nil {
			out.Status, out.Err = http.StatusInternalServerError, err
			return out, err
		}
		dto := toDTOGrab(*relaunched)
		out.Grabbed, out.GrabID, out.Grab = true, relaunched.ID, &dto
		return out, nil
	}

	created, err := deps.GrabsStore.Create(ctx, grabs.Grab{
		Mode: req.Mode, Title: req.Title, TMDBID: req.TMDBID, TVDBID: req.TVDBID,
		SeasonNumber: req.Season, EpisodeNumber: req.Episode, SeasonSpecified: req.SeasonSpecified,
		Indexer: picked.Indexer, Protocol: string(picked.Protocol),
		DownloadClient: downloadClient, RootFolderPath: rootFolder,
		DownloadURL: picked.DownloadURL,
	})
	if err != nil {
		out.Status, out.Err = http.StatusInternalServerError, err
		return out, err
	}
	if gid != "" {
		if err := deps.GrabsStore.SetDownloadGID(ctx, created.ID, gid); err != nil {
			out.Status, out.Err = http.StatusInternalServerError, err
			return out, err
		}
		created.DownloadGID = gid
	}

	dto := toDTOGrab(created)
	out.Grabbed, out.GrabID, out.Grab = true, created.ID, &dto
	return out, nil
}

// parkPendingRetry creates — or refreshes — the GID-less pending_retry row a
// gated trigger leaves behind when nothing qualified.
//
// activeGrabForGID cannot guard this row (there is no GID), so FindPendingRetry
// is the dedup key: a re-submitted request and every subsequent retry cycle
// update the one existing row instead of inserting duplicates. A first-time row
// is ONE atomic Create at retry_count 0 — SetPendingRetry increments, so
// Create-then-SetPendingRetry would land a brand-new row at 1, an inaccurate
// attempt count for a row that was never actually retried yet.
//
// req.ExistingGrabID WINS OVER FindPendingRetry when it is non-zero, and that
// preference is a correctness fix rather than an optimisation (added 2026-08-02
// with the pre-release hold). FindPendingRetry is keyed on
// (mode, tmdb_id, season…) and orders id ASC, so it resolves to the OLDEST
// matching row — which, once Calendar can mint a held request, need not be the
// row this run is a retry OF:
//
//	H = a held pre-release row for tmdb_id N (retry_after '', hold_until future)
//	R = a separately Discover-grabbed row for the SAME N, at a higher id
//
// R's retrieval fails, R comes due, its re-search finds nothing — and
// FindPendingRetry hands back H. R's failure would then overwrite H's
// retry_after, retry_reason and retry_count: an unreleased film searched early
// (which grabs.DueForRetry's hold conjunct also independently blocks) and, more
// to the point, a held request whose state is silently corrupted by an
// unrelated grab. Both existing callers are unaffected — retryDueGrabs already
// passes ExistingGrabID: g.ID and FindPendingRetry resolves to that same row
// today, so this is a no-op there; dispatchAirDateGrabs passes zero and falls
// through unchanged.
//
// Do NOT "simplify" this by adding AND hold_until = ” to FindPendingRetry
// instead. It looks like the one-line version of the same fix and it BREAKS
// PROMOTION IDEMPOTENCE: a promoted held row would stop matching the update arm
// below, fall into the Create arm, and mint a duplicate.
//
// A non-zero ExistingGrabID that no longer resolves returns ErrNotFound rather
// than falling through to Create. The caller named a specific row; minting a
// different one instead is the very class of thing this preference closes.
func parkPendingRetry(ctx context.Context, deps AutoGrabDeps, req AutoGrabRequest, reason string) (*grabs.Grab, error) {
	// Claude 2026-09-13: park delay is grabs.RetryBackoff, not a flat interval.
	// Reason: an unmet floor must back off (24h→3d→10d→30d→60d→90d) on the same
	//   ladder as every other pending_retry park.
	// Troubleshooting: "retries every day forever" → a park path computing its
	//   own retry_after instead of going through ParkWithBackoff.
	// Review if: a consecutive-failure counter replaces cumulative retry_count.
	now := time.Now()

	if req.ExistingGrabID != 0 {
		if err := deps.GrabsStore.ParkWithBackoff(ctx, req.ExistingGrabID, now, reason); err != nil {
			return nil, err
		}
		return deps.GrabsStore.Get(ctx, req.ExistingGrabID)
	}

	existing, err := deps.GrabsStore.FindPendingRetry(ctx, req.Mode, req.TMDBID, req.Title, req.Season, req.SeasonSpecified, req.Episode)
	switch {
	case err == nil:
		if err := deps.GrabsStore.ParkWithBackoff(ctx, existing.ID, now, reason); err != nil {
			return nil, err
		}
		// Re-read: ParkWithBackoff always parks to pending_retry with a real
		// retry_after (§4.4.1: the retry-attempt cap was removed 2026-08-01,
		// an explicit product decision). Read back to log the scheduled retry
		// time honestly, not to branch on a Failed status (unreachable).
		return deps.GrabsStore.Get(ctx, existing.ID)
	case errors.Is(err, grabs.ErrNotFound):
		// RetryBackoff(0), not ParkWithBackoff: Create writes retry_count 0 and
		// increments nothing, so this row's first delay is the 24h rung.
		after := now.Add(grabs.RetryBackoff(0))
		created, err := deps.GrabsStore.Create(ctx, grabs.Grab{
			Mode: req.Mode, Title: req.Title, TMDBID: req.TMDBID, TVDBID: req.TVDBID,
			SeasonNumber: req.Season, EpisodeNumber: req.Episode, SeasonSpecified: req.SeasonSpecified,
			RootFolderPath: req.RootFolderPath,
			Status:         grabs.PendingRetry,
			RetryAfter:     grabs.FormatTime(after),
			RetryReason:    reason,
		})
		if err != nil {
			return nil, err
		}
		return &created, nil
	default:
		return nil, err
	}
}

// parkPreReleaseRequest mints the held row a Calendar pre-release click leaves
// behind: an operator asked for a movie that is not out yet, and the request
// must sit dormant until its release date.
//
// It lives HERE, beside parkPendingRetry, so exactly ONE file mints GID-less
// pending_retry rows. That placement is deliberate (plan §5.2.3): the DISPATCH
// half of this feature is textbook Pattern B (a new AutoGrabTrigger const plus a
// caller that goes through RunAutoGrab — see releaseDueGrabs), while the
// CREATION half is a parker, because a held row is minted by a click and not by
// a search. CLAUDE.md's "do not use both patterns for the same trigger source"
// forbids two DISPATCH paths for one source; there is exactly one here.
//
// THE EMPTY retry_after IS THE WHOLE HOLD, and it is the single most
// load-bearing detail in this row's shape. grabs.DueForRetry selects on
// `retry_after != ” AND retry_after <= ?`, and its own doc explains the first
// conjunct: the column defaults to the empty string, which sorts below every
// real timestamp. So a row born with no retry_after is invisible to the retry
// cycle unconditionally — not "until its date arrives", but always, until
// something writes a retry_after onto it. Writing one here (parking the hold as
// a retry_after, the design this feature deliberately did NOT build) would make
// the hold and the re-search schedule the same field and force origin to be
// re-inferred from row shape.
//
// hold_until carries the date instead, and doubles as the ORIGIN MARKER: nothing
// else in this codebase produces a non-empty hold_until. reason is
// heldRequestReason, operator-facing copy only — nothing branches on it.
//
// Status PendingRetry is stored as given because grabs.Create makes exactly this
// exception for PendingRetry rather than forcing Queued; every other status
// would be reset, hold_until included.
//
// It can return grabs.ErrHeldRequestExists, and a caller MUST handle that rather
// than treating it as a server error: it means a concurrent click won the race to
// mint this film's held row (the partial unique index idx_grabs_held_request
// refusing a second), which is an already-requested answer, not a failure. See
// the call site in calendar_prerelease.go's step 3.
func parkPreReleaseRequest(ctx context.Context, grabsStore *grabs.Store, m mode.Mode, title string, tmdbID int, until time.Time) (grabs.Grab, error) {
	return grabsStore.Create(ctx, grabs.Grab{
		Mode: m, Title: title, TMDBID: tmdbID,
		Status: grabs.PendingRetry,
		// RetryAfter is deliberately LEFT EMPTY — see the doc above.
		HoldUntil:   grabs.FormatTime(until),
		RetryReason: heldRequestReason,
	})
}

// Claude 2026-09-13: usenetRetryInterval is gone — parks take their delay from
//   grabs.RetryBackoff via ParkWithBackoff.
// Reason: the progressive ladder must not share usenet_retry_interval_seconds
//   with the scheduler tick and opt-in gate, which LoadUsenetRetryInterval
//   still owns.
// Troubleshooting: a park that always lands 24h out means a caller is still
//   computing its own retry_after from an interval setting.
// Review if: defaultUsenetRetryIntervalSeconds is dropped — it lost its last
//   reader with this change, and this block goes with it.

// parkGrabForRetry moves an existing grab into pending_retry after an
// ASYNCHRONOUS retrieval failure (as opposed to parkPendingRetry, which covers
// "nothing qualified" at scoring time). It exists so the fast path
// (checkImportHandler) and the retry scheduler's GID sweep share one
// transition instead of each inventing its own retry_after.
func parkGrabForRetry(ctx context.Context, deps AutoGrabDeps, id int64, reason string) error {
	return deps.GrabsStore.ParkWithBackoff(ctx, id, time.Now(), reason)
}

// qualifiedCandidateOrder returns PickIndex first, then other Ranked indices
// that cleared the quality floor (Qualified). Used by the precheck fallback loop.
func qualifiedCandidateOrder(sel autograb.Selection) []int {
	if sel.PickIndex < 0 {
		return nil
	}
	out := []int{sel.PickIndex}
	seen := map[int]bool{sel.PickIndex: true}
	for _, idx := range sel.Ranked {
		if seen[idx] {
			continue
		}
		if idx < 0 || idx >= len(sel.Grades) || !sel.Grades[idx].Qualified {
			continue
		}
		seen[idx] = true
		out = append(out, idx)
	}
	return out
}

// filterExcludedReleases drops any candidate whose ReleaseKeys intersect
// excludeKeys. Called by scoreOnePhase when req.ExcludeReleaseKeys is non-empty.
func filterExcludedReleases(releases []prowlarr.Release, excludeKeys []string) []prowlarr.Release {
	if len(excludeKeys) == 0 {
		return releases
	}
	keySet := make(map[string]bool, len(excludeKeys))
	for _, k := range excludeKeys {
		keySet[k] = true
	}
	out := releases[:0:0]
	for _, r := range releases {
		rk := grabs.ReleaseKeys(r.DownloadURL, r.Title)
		excluded := false
		for _, k := range rk {
			if keySet[k] {
				excluded = true
				break
			}
		}
		if !excluded {
			out = append(out, r)
		}
	}
	return out
}
