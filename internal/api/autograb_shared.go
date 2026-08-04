package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
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
	// defaultUsenetRetryIntervalSeconds backs retry_after when the interval
	// setting is unset or off. A pending_retry row parked at "now" would be due
	// immediately, so an unset interval falls back to the spec's 24 hours
	// rather than to 0.
	defaultUsenetRetryIntervalSeconds = 86400
)

const (
	// noQualifyingCandidateReason is the retry_reason for a Selection.Fallback
	// row: the scorer graded every candidate and none cleared the floor.
	noQualifyingCandidateReason = "no candidate cleared the quality floor"
	// articlesUnavailableReason is the retry_reason for a retrieval failure
	// where every configured subscription answered 430.
	articlesUnavailableReason = "no configured usenet subscription holds this release's articles"
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
	heldRequestReason = "held until its release date"
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

	// Studio/ReleaseTitle/DurationSeconds are Adult-only inputs to the internal
	// search; ignored when Releases is supplied.
	Studio          string
	ReleaseTitle    string
	DurationSeconds int

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
	if req.Trigger != TriggerOperator {
		enabled, err := deps.SettingsStore.GetBool(ctx, usenetAutoGrabEnabledKey, false)
		if err != nil {
			return AutoGrabOutcome{Status: http.StatusInternalServerError, Err: err}, err
		}
		if !enabled {
			return AutoGrabOutcome{Gated: true, Status: http.StatusOK}, nil
		}
	}

	releases, runtimeSeconds := req.Releases, req.RuntimeSeconds
	if releases == nil {
		var err error
		releases, runtimeSeconds, err = autoGrabSearch(ctx, sess, req.Mode, apidto.AutoGrabRequest{
			Title: req.Title, TMDBID: req.TMDBID, Studio: req.Studio,
			SeasonNumber: req.Season, EpisodeNumber: req.Episode,
			SeasonSpecified: req.SeasonSpecified, DurationSeconds: req.DurationSeconds,
			ReleaseTitle: req.ReleaseTitle,
		})
		if err != nil {
			return AutoGrabOutcome{Status: http.StatusBadGateway, Err: err}, err
		}
	}

	// Claude 2026-08-03: apply FilterSeasonScope before scoring.
	// Reason: half of the "picking Season 4 grabs S1E1" fix — see
	// FilterSeasonScope's doc (releasematch.go) for the full root-cause
	// chain. Runs regardless of whether releases came from autoGrabSearch
	// above or arrived pre-fetched via req.Releases (the Search-hook path):
	// either source can carry a wrong-season release Prowlarr's own
	// query-scoping didn't exclude. A no-op when req.SeasonSpecified is
	// false (FilterSeasonScope's own contract).
	// Troubleshooting: if a Series auto-grab dispatches the wrong season
	// again, confirm this call is still present before assuming the
	// regression is back in prowlarr.SearchByID itself.
	if req.Mode == mode.Series {
		releases = FilterSeasonScope(releases, req.Season, req.Episode, req.SeasonSpecified)
	}

	// A real per-episode runtime mustn't be applied to season packs the indexer
	// returned for the episode query — neutralize those so they can't
	// over-qualify (see buildAutoGrabCandidates).
	neutralizeSeasonPacks := req.Mode == mode.Series && runtimeSeconds > 0
	candidates := buildAutoGrabCandidates(releases, runtimeSeconds, neutralizeSeasonPacks)
	sel := autograb.Select(candidates, autoGrabTier(ctx, deps.SettingsStore, req.Mode), minSeedersFor(req.Mode))

	out := AutoGrabOutcome{Selection: sel, Releases: releases, Status: http.StatusOK}

	if sel.Fallback {
		out.NoMatch = true
		// TriggerOperator's fallback is a MANUAL PICK LIST, not a retry row:
		// a human is sitting in front of it and picks one. Writing a
		// pending_retry row here would queue a phantom unattended retry behind
		// every one-click that missed the floor.
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

	rootFolder := req.RootFolderPath
	if rootFolder == "" {
		var err error
		rootFolder, err = autoGrabRootFolder(ctx, deps.SettingsStore, req.Mode)
		if err != nil {
			out.Status, out.Err = http.StatusBadRequest, err
			return out, err
		}
	}
	picked := releases[sel.PickIndex]

	downloadClient, gid, status, err := dispatchToDownloadClient(ctx, deps.SettingsStore, sess, req.Mode, deps.NZB, string(picked.Protocol), picked.DownloadURL, picked.Title)
	if err != nil {
		out.Status, out.Err = status, err
		return out, err
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
	after := time.Now().Add(usenetRetryInterval(ctx, deps.SettingsStore))

	if req.ExistingGrabID != 0 {
		if err := deps.GrabsStore.SetPendingRetry(ctx, req.ExistingGrabID, after, reason); err != nil {
			return nil, err
		}
		return deps.GrabsStore.Get(ctx, req.ExistingGrabID)
	}

	existing, err := deps.GrabsStore.FindPendingRetry(ctx, req.Mode, req.TMDBID, req.Title, req.Season, req.SeasonSpecified, req.Episode)
	switch {
	case err == nil:
		if err := deps.GrabsStore.SetPendingRetry(ctx, existing.ID, after, reason); err != nil {
			return nil, err
		}
		// Re-read: SetPendingRetry always parks to pending_retry with a real
		// retry_after (§4.4.1: the retry-attempt cap was removed 2026-08-01,
		// an explicit product decision). Read back to log the scheduled retry
		// time honestly, not to branch on a Failed status (unreachable).
		return deps.GrabsStore.Get(ctx, existing.ID)
	case errors.Is(err, grabs.ErrNotFound):
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

// usenetRetryInterval is how far ahead retry_after is parked. An unset or
// switched-off interval degrades to 24 hours rather than to zero, which would
// make every parked row due immediately.
func usenetRetryInterval(ctx context.Context, settingsStore *settings.Store) time.Duration {
	seconds, err := loadIntervalSeconds(ctx, settingsStore, usenetRetryIntervalSecondsKey, defaultUsenetRetryIntervalSeconds)
	if err != nil || seconds <= 0 {
		seconds = defaultUsenetRetryIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

// parkGrabForRetry moves an existing grab into pending_retry after an
// ASYNCHRONOUS retrieval failure (as opposed to parkPendingRetry, which covers
// "nothing qualified" at scoring time). It exists so the fast path
// (checkImportHandler) and the retry scheduler's GID sweep share one
// transition instead of each inventing its own retry_after.
func parkGrabForRetry(ctx context.Context, deps AutoGrabDeps, id int64, reason string) error {
	return deps.GrabsStore.SetPendingRetry(ctx, id, time.Now().Add(usenetRetryInterval(ctx, deps.SettingsStore)), reason)
}
