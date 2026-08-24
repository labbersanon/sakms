// This file is the Usenet retry scheduler — a further deliberate, opt-in
// exception to this project's "manual by default, no background pollers" rule
// (see CLAUDE.md's AMENDED 2026-07-23 note; its enumeration is already stale,
// so this file deliberately does not pick a number).
//
// It is also the FIRST scheduler here that is not Scan/propose-only: that same
// AMENDED note claims "every scheduler in it is Scan/propose/passive-flag only
// — nothing is auto-Applied", and this one DISPATCHES DOWNLOADS. That is the
// documented auto-grab exception, not an oversight, and it is bounded the same
// way the feature is: an off-by-default toggle RunAutoGrab enforces on every
// cycle, a scorer that refuses to grab an ungradeable release, no grab without
// a scored match, a permanent-failure (451) path that never retries, and
// indefinite retries with no attempt cap (the cap was removed 2026-08-01, an
// explicit product decision). CLAUDE.md's Scan-only claim must be corrected
// alongside the auto-grab amendment.
//
// It lives in internal/api rather than its own package because everything it
// drives is package-private here — RunAutoGrab's deps, parkGrabForRetry,
// classifyDownloadState, the retry reason strings — exactly the reason
// RunWatchFolders (watchfolders.go) is structured the same way. It stays
// independently deletable: delete this file, its four route registrations in
// handler.go, and its one start-call in cmd/sakms/main.go.
//
// It is gated OFF by default and, unlike the other schedulers, its interval is
// NOT a user-facing knob: usenet_retry_interval_seconds is written as a coupled
// side effect of the auto-grab toggle (on -> 86400, off -> 0). Retry only has
// meaning while auto-grab is on, and RunAutoGrab gates every retry on the same
// toggle anyway, so two independent switches could only ever disagree.
//
// Each cycle does FOUR things, in order. CORRECTED 2026-08-02 — superseded
// claim, quoted: "Each cycle does THREE things, in order." The Calendar
// pre-release hold was added as a fourth pass; see item 4. The correction it
// itself carries is retained: "CORRECTED 2026-08-01 — superseded claim, quoted:
// 'Each cycle does two things, in order.' Series air-date monitoring was added
// as a third pass; see item 3 and the placement note under it, which explains
// why a non-usenet concern lives in this file."
//
//  1. The GID sweep. A usenet retrieval failure is ASYNCHRONOUS — AddNZB
//     returns a GID immediately and the failure lands long after the HTTP
//     response was written. The only other consumer of that state is
//     checkImportHandler, which runs solely when a browser polls it, so on a
//     headless instance nothing would ever move a failed retrieval out of
//     "downloading". This sweep is therefore the AUTHORITATIVE transition
//     (plan §2.5 M3), not the polling path.
//  2. The due re-search. Every pending_retry row whose retry_after has arrived
//     goes back through RunAutoGrab with TriggerRetry — the same single gated
//     scoring-and-dispatch path every other trigger uses, so a retry can never
//     bypass scoring or the toggle.
//  3. Series air-date monitoring (monitorAirDates, airdatemonitor.go). Every
//     aired-but-missing episode of a monitored season goes through RunAutoGrab
//     with TriggerAirDate — again the same single gated path, so this pass
//     adds no scoring, dispatch or gating surface of its own. It runs THIRD on
//     purpose; runUsenetRetryCycle's own doc explains why the ordering is
//     load-bearing.
//  4. The pre-release promotion pass (releaseDueGrabs, prerelease.go). Every
//     held Calendar pre-release request whose hold_until has arrived goes
//     through RunAutoGrab with TriggerPreRelease — again the same single gated
//     path, adding no scoring, dispatch or gating surface of its own. It runs
//     FOURTH and LAST so nothing downstream can re-handle a row it just
//     promoted, and it adds NO scheduler: the codebase still has six.
//
// The third and fourth passes are DELIBERATELY NOT USENET CONCERNS, and live in this
// usenet-named file anyway. That is a resolved tension between two documents,
// not an oversight. The sibling torrent-behavior plan's §5 file-boundary
// guidance wanted a Pattern B caller in its own file with its own interval
// key and its own cmd/sakms/main.go launch; this feature's spec forbids all
// three outright (Constraint 4 and AC5: no new goroutine, no new interval, no
// new detection-specific toggle). The spec is authoritative, so the FILE
// boundary §5 wanted is kept — airdatemonitor.go, independently deletable for
// exactly the reason stated below — and the LAUNCH it wanted is dropped: the
// pass is a plain function call inside runUsenetRetryCycle, so nothing new is
// scheduled and the codebase's scheduler count is unchanged. §5's real
// objection was a naming/doc one (this file's passes are usenet-specific by
// name and doc, and air-date monitoring is not), and correcting this doc is
// the answer to it — NOT renaming runUsenetRetryCycle, which CLAUDE.md and
// that plan both name by hand and which would invalidate two documents to fix
// a comment.
//
// Honest scope note, because it changes what "working" means here. CORRECTED
// 2026-08-01 — superseded claim, quoted: "the rows this loop can actually
// convert into grabs are the ones the GID sweep parks (real dispatched grabs,
// with a real TMDB id)." Pass 3 adds a second, far larger source: an air-date
// row carries a real TMDB id AND a real per-episode runtime
// (seriesEpisodeRuntimeSeconds, Episode > 0), so it clears GradeCandidate's
// size/runtime short-circuit and can genuinely dispatch. What is new is
// VOLUME, not kind — a GID-swept row exists only because an operator already
// grabbed that release once, while an air-date row is minted by the system
// with no prior operator action for that episode. The rest of this note is
// unchanged and still true. Rows parked by the toggle-ON
// Search hook carry TMDBID 0 and no runtime, which is unfixable at that hook
// (see runToggleGatedSearch's comment) — for those, what this loop provides is
// an indefinite, unsuccessful retry loop (2026-08-01: the retry-attempt cap
// was removed — auto-grab now keeps trying until a match is found rather than
// eventually giving up), not eventual success.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
	"github.com/labbersanon/sakms/internal/webhooks"
)

// usenetAutoGrabIntervalSeconds is what the auto-grab toggle writes to
// usenet_retry_interval_seconds when it is switched ON: the spec's 24 hours.
// Switching the toggle off writes 0, which is the scheduler's opt-in gate.
const usenetAutoGrabIntervalSeconds = 86400

// usenetGIDPrefix is the usenet engine's GID namespace. Every other GID belongs
// to the torrent engine, whose Download carries no classified error to sweep —
// same routing rule checkImportHandler uses.
const usenetGIDPrefix = "nzb-"

const (
	// retrievalFailedReason is the retry_reason for an UNCLASSIFIED retrieval
	// failure — a dial timeout, a decode error, or the mixed case where one
	// subscription answered 451 and another was unreachable. Distinct from
	// articlesUnavailableReason on purpose: the Requests screen renders this,
	// and "no subscription holds the articles" is a different (and, here,
	// unproven) claim from "we could not reach a subscription to ask".
	retrievalFailedReason = "the download could not be retrieved — see the server log"
	// retrySearchFailedReason is the retry_reason for a retry attempt whose
	// re-search itself failed.
	//
	// SECURITY: deliberately detail-free. It was `fmt.Sprintf("...: %v", cause)`,
	// which stringified an unwrapped *url.Error — a TMDB URL carrying api_key,
	// or an indexer NZB download link carrying its own API key — into the
	// plaintext grabs.retry_reason column (next to the deliberately encrypted
	// download_url_encrypted), into the server log, and into the browser via
	// the Requests screen. The failure is classified, not stringified, matching
	// the detail-free contract the stored-connection-test handlers already use.
	retrySearchFailedReason = "the retry re-search failed — see the server log"
	// staleTorrentReason is the retry_reason for a torrent auto-cancelled after
	// making zero progress with no peers for the configured stale threshold.
	//
	// SECURITY: detail-free by the same convention as retrySearchFailedReason
	// above — it is rendered in the browser via the Requests screen and must
	// never carry a credentialed URL.
	staleTorrentReason = "the torrent stalled with no progress and no peers — cancelled and requeued"
)

// usenetRetrievalReason maps a classified retrieval failure to its
// operator-facing retry_reason. It never embeds the error itself: an NNTP
// transport error can name a configured subscription's host and account.
func usenetRetrievalReason(failure error) string {
	if errors.Is(failure, usenet.ErrArticleNotFound) {
		return articlesUnavailableReason
	}
	return retrievalFailedReason
}

// usenetRetryModes is the fixed mode set the GID sweep walks, matching
// requests.go's requestsModes.
var usenetRetryModes = []mode.Mode{mode.Movies, mode.Series, mode.Adult}

// sessionBuilderFunc / usenetDownloadLookup are the two seams that make
// runUsenetRetryCycle testable without a wall clock or a live NNTP server. They
// are function types rather than interfaces on purpose: each has exactly one
// production implementation (mode.Build and Manager.FindByGID), so an interface
// would be an abstraction with no second backend to justify it.
type sessionBuilderFunc func(ctx context.Context, m mode.Mode) (*mode.Session, error)

type usenetDownloadLookup func(gid string) (*usenet.Download, error)

// LoadUsenetRetryInterval reads the retry cadence, returning 0 ("off") for any
// unset, blank, non-integer or non-positive value — the same tolerant read
// recheck.LoadInterval does, for the same reason: a corrupt value must degrade
// to off rather than error out the boot path.
//
// Deliberately NOT the same degradation as usenetRetryInterval
// (autograb_shared.go), which falls back to 24h so a parked row is not due the
// instant it is written. Here 0 must stay 0: it IS the opt-in gate.
func LoadUsenetRetryInterval(ctx context.Context, settingsStore *settings.Store) time.Duration {
	seconds, err := loadIntervalSeconds(ctx, settingsStore, usenetRetryIntervalSecondsKey, 0)
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// RunUsenetRetry drives the retry loop until ctx is cancelled. interval is the
// boot-time cadence (from LoadUsenetRetryInterval); when it is <= 0 — the
// default on every install — Run returns immediately having started NOTHING, so
// main can call it unconditionally.
//
// Each tick re-reads the interval so an operator switching auto-grab off stops
// the loop cleanly on the next tick. Re-enabling from 0 needs a restart, same
// as recheck/adultnewest/parseentity — worth knowing before writing UI copy
// that promises the toggle takes effect instantly.
func RunUsenetRetry(ctx context.Context, interval time.Duration, httpClient *http.Client,
	connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store,
	grabsStore *grabs.Store, excludesStore *excludes.Store, whStore *webhooks.Store,
	libStore *library.Store, dl *downloader.Manager, nzb *usenet.Manager,
	monitoredStore *adultnewest.MonitoredStore, releaseStore *adultnewest.ReleaseStore) {

	if interval <= 0 {
		return // opt-in gate: off by default, honoring "manual first"
	}

	deps := AutoGrabDeps{SettingsStore: settingsStore, NZB: nzb, GrabsStore: grabsStore, Webhooks: whStore}
	// dl, not nil: a retry re-runs the whole pipeline from the search, so its
	// winner may well be a torrent, and dispatchToDownloadClient rejects a
	// torrent dispatch on a nil Session.Downloader.
	build := func(ctx context.Context, m mode.Mode) (*mode.Session, error) {
		return mode.Build(ctx, connStore, scStore, settingsStore, httpClient, dl, m)
	}
	var lookup usenetDownloadLookup
	if nzb != nil {
		lookup = nzb.FindByGID
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("usenet retry: background re-search enabled (every %s) — a deliberate opt-in exception to manual-by-default, coupled to the usenet auto-grab toggle", interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := LoadUsenetRetryInterval(ctx, settingsStore)
			if cur <= 0 {
				log.Printf("usenet retry: interval set to 0 — stopping background re-search (restart to re-enable)")
				return
			}
			if cur != interval {
				interval = cur
				ticker.Reset(cur)
			}
			runUsenetRetryCycle(ctx, deps, build, lookup, libStore, monitoredStore, releaseStore, excludedRequestKeys(ctx, excludesStore), time.Now())
		}
	}
}

// runUsenetRetryCycle performs exactly one pass and returns — the single-tick
// logic, extracted from the ticker loop so tests exercise it directly. The
// sweep runs FIRST: it is what converts an asynchronous retrieval failure into
// a pending_retry row, and a row it parks this cycle is deliberately not due
// until the next one (parkGrabForRetry sets retry_after to now + interval).
//
// Fault isolation matches the rest of this codebase: one grab's failure is
// logged and skipped, never fatal to the pass.
//
// monitorAirDates runs THIRD, and the position is load-bearing rather than
// arbitrary. (CORRECTED 2026-08-02 — superseded claim, quoted: "monitorAirDates
// runs THIRD and LAST". It is still third, and everything below about WHY is
// unchanged; releaseDueGrabs is now the last pass, and the two orderings do not
// interact — see its own note further down.)
// Its active-grab pre-filter must see the rows retryDueGrabs
// just re-armed, or a just-retried episode could be grabbed twice in one cycle;
// and its backoff sweep must have the LAST WORD on retry_after, because
// retryDueGrabs re-parks every row it re-searches at the flat interval and the
// sweep is what overrides that with the widening air-date schedule. Reordering
// these two would silently discard the backoff every cycle while every unit
// test of airDateRetryBackoff still passed.
//
// releaseDueGrabs runs FOURTH and LAST, and that position is chosen too, on a
// simpler argument than monitorAirDates': running last means no downstream pass
// can re-handle a row it just promoted, by construction, with no reasoning about
// DueForRetry's guards required to see it. It cannot disturb monitorAirDates'
// final-word-on-retry_after guarantee either, because that guarantee is about
// Series rows while a pre-release row is always Movies with hold_until set —
// two provably disjoint populations (airDateShaped requires mode.Series).
//
// libStore may be nil — a caller that only wants the two usenet passes (the
// tests that predate air-date monitoring) passes nil, and BOTH monitorAirDates
// and releaseDueGrabs return immediately, exactly as sweepUsenetFailures does
// for a nil lookup.
func runUsenetRetryCycle(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc,
	lookup usenetDownloadLookup, libStore *library.Store, monitoredStore *adultnewest.MonitoredStore, releaseStore *adultnewest.ReleaseStore, excluded map[string]bool, now time.Time) {

	sweepUsenetFailures(ctx, deps, lookup)
	retryDueGrabs(ctx, deps, build, excluded, now)
	monitorAirDates(ctx, deps, build, libStore, excluded, now)
	releaseDueGrabs(ctx, deps, build, libStore, excluded, now)
	// Claude 2026-08-24: fifth pass — Adult monitored-entity dispatch.
	// Reads pool for scenes added since monitored_since, dispatches auto-grabs.
	monitorAdultEntities(ctx, deps, build, monitoredStore, releaseStore, excluded, now)
}

// sweepUsenetFailures is the AUTHORITATIVE M3 transition: it asks the usenet
// engine for the live state of every in-flight usenet grab and converts a
// classified retrieval failure into the right terminal-or-retry state, with no
// client attached.
//
// Three things it deliberately does NOT do:
//   - A GID the Manager does not know (FindByGID returns nil, nil) is SKIPPED,
//     never parked. The Manager's queue is in-memory, so after a restart every
//     in-flight grab looks unknown — parking those would silently re-search
//     every active download on each boot.
//   - A download with no classified error (dl.Err == nil) is left alone.
//     Completion and import belong to the engine's onComplete callback; this
//     sweep only ever handles failures.
//   - It invents no second status vocabulary: classification is delegated to
//     classifyDownloadState, the same function the polling fast path uses, so
//     the two can never disagree about a 430 vs a 451.
func sweepUsenetFailures(ctx context.Context, deps AutoGrabDeps, lookup usenetDownloadLookup) {
	if lookup == nil {
		return // the usenet engine isn't running — nothing to sweep
	}
	for _, m := range usenetRetryModes {
		list, err := deps.GrabsStore.List(ctx, m)
		if err != nil {
			log.Printf("usenet retry: listing %s grabs for the failure sweep: %v", m, err)
			continue
		}
		for _, g := range list {
			if g.Status != grabs.Queued && g.Status != grabs.Downloading {
				continue
			}
			if !strings.HasPrefix(g.DownloadGID, usenetGIDPrefix) {
				continue
			}
			dl, err := lookup(g.DownloadGID)
			if err != nil {
				log.Printf("usenet retry: looking up download %q for grab %d: %v", g.DownloadGID, g.ID, err)
				continue
			}
			if dl == nil || dl.Err == nil {
				continue
			}
			switch classifyDownloadState(dl.Status, dl.Err) {
			case grabs.PendingRetry:
				// 430 from every configured subscription, or an unclassified
				// transient failure — retryable either way. Parking (not a bare
				// UpdateStatus) is required: DueForRetry ignores a
				// pending_retry row with an empty retry_after, and parking also
				// clears the now-stale GID so the row is visible to it at all.
				reason := usenetRetrievalReason(dl.Err)
				if err := parkGrabForRetry(ctx, deps, g.ID, reason); err != nil {
					log.Printf("usenet retry: parking grab %d after a retrieval failure: %v", g.ID, err)
					continue
				}
				log.Printf("usenet retry: grab %d (%s) parked for re-search — %s", g.ID, g.Title, reason)
			case grabs.Failed:
				// Only a 451 ErrArticleRemoved reaches here: it is a permanent
				// takedown and is NEVER retried. Every other classified failure
				// is retryable above, exactly as the polling path treats it.
				if err := deps.GrabsStore.UpdateStatus(ctx, g.ID, grabs.Failed); err != nil {
					log.Printf("usenet retry: failing grab %d: %v", g.ID, err)
					continue
				}
				log.Printf("usenet retry: grab %d (%s) failed permanently: %v", g.ID, g.Title, dl.Err)
			}
		}
	}
}

// retryDueGrabs re-runs the full auto-grab pipeline for every row whose
// retry_after has arrived. It never dispatches anything itself: RunAutoGrab
// remains the single gated scoring-and-dispatch path, which is what keeps
// "retry never bypasses scoring" true by construction rather than by review.
func retryDueGrabs(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc,
	excluded map[string]bool, now time.Time) {

	due, err := deps.GrabsStore.DueForRetry(ctx, now)
	if err != nil {
		log.Printf("usenet retry: listing grabs due for retry: %v", err)
		return
	}
	for _, g := range due {
		// An operator who excluded this title from the Requests worklist has
		// said "stop working on this". Re-searching it anyway would be the
		// scheduler overriding an explicit human decision. Skipped rather than
		// failed: exclusion is reversible, and a skip leaves the row exactly as
		// the operator left it.
		if excluded[excludes.Key(string(g.Mode), g.TMDBID, g.Title)] {
			log.Printf("usenet retry: skipping grab %d (%s) — its title is excluded from the worklist", g.ID, g.Title)
			continue
		}

		sess, err := build(ctx, g.Mode)
		if err != nil {
			reparkFailedRetry(ctx, deps, g, fmt.Errorf("building the %s session: %w", g.Mode, err))
			continue
		}

		out, err := RunAutoGrab(ctx, deps, sess, AutoGrabRequest{
			Mode: g.Mode, Title: g.Title, TMDBID: g.TMDBID, TVDBID: g.TVDBID,
			Season: g.SeasonNumber, Episode: g.EpisodeNumber, SeasonSpecified: g.SeasonSpecified,
			RootFolderPath: g.RootFolderPath,
			Trigger:        TriggerRetry,
			// Re-arm THIS row on success instead of recording a second one.
			ExistingGrabID: g.ID,
			// Releases nil = "search for me". Unlike the Search hook, a retry
			// row carries the TMDB id RunAutoGrab's internal search needs.
		})
		switch {
		case err != nil:
			// A failed attempt must still cost an attempt (retry_count, for
			// observability) and re-park on the normal interval. Left unparked,
			// the row would stay due immediately and this loop would re-run the
			// same broken search in a tight loop instead of on the normal
			// interval — which is exactly what happens to a TMDB-id-less row
			// parked by the toggle-ON Search hook.
			reparkFailedRetry(ctx, deps, g, err)
		case out.Gated:
			// The toggle went off between the interval read and the gate. Every
			// remaining row would be gated too, so stop rather than log once
			// per row.
			log.Printf("usenet retry: usenet auto-grab is switched off — abandoning this cycle")
			return
		case out.AlreadyGrabbing:
			// Another row already holds this release's GID (a torrent client
			// dedupes by infohash back to the same GID). This row is redundant;
			// park it so the attempt is counted and the row stops being due
			// every cycle. CORRECTED 2026-08-01 — superseded claim, quoted:
			// "the cap eventually retires it rather than leaving it due
			// forever." Nothing retires it. The retry-attempt cap was removed
			// 2026-08-01, so this row re-parks on the normal interval
			// indefinitely; only a terminal 451 classification or an explicit
			// operator action ever ends it.
			duplicate := "another grab"
			if out.GrabID != 0 {
				duplicate = fmt.Sprintf("grab %d", out.GrabID)
			}
			// Logged in full here, unlike reparkFailedRetry's type-only line:
			// this cause is composed right above from an int, so it cannot
			// carry a credentialed URL the way a transport error can.
			log.Printf("usenet retry: grab %d (%s) is already being downloaded by %s", g.ID, g.Title, duplicate)
			reparkFailedRetry(ctx, deps, g, fmt.Errorf("this release is already being downloaded by %s", duplicate))
		case out.Grabbed:
			log.Printf("usenet retry: grab %d (%s) re-searched and dispatched", g.ID, g.Title)
		default:
			// out.NoMatch — RunAutoGrab already re-parked the row through
			// parkPendingRetry, including the retry_count bump. CORRECTED
			// 2026-08-01 — superseded claim, quoted: "already re-parked (or
			// gave up on) the row". There is no give-up branch: since the
			// retry-attempt cap was removed, parkPendingRetry always parks to
			// pending_retry (see its read-back comment), so out.RetryStatus
			// logged below is always PendingRetry.
			log.Printf("usenet retry: grab %d (%s) still has no qualifying candidate (%s)", g.ID, g.Title, out.RetryStatus)
		}
	}
}

// reparkFailedRetry records a failed retry ATTEMPT, so the attempt is counted
// (observability — 2026-08-01: retries are no longer capped, so this no
// longer drives a give-up decision) and the row re-parks on the normal
// interval instead of retrying in a tight loop. The
// reason is retrySearchFailedReason rather than articlesUnavailableReason — the
// Requests screen renders it, and a search failure is not an article
// availability failure — and it is a fixed string rather than the cause's text,
// because cause routinely carries credentials (see retrySearchFailedReason).
func reparkFailedRetry(ctx context.Context, deps AutoGrabDeps, g grabs.Grab, cause error) {
	if err := parkGrabForRetry(ctx, deps, g.ID, retrySearchFailedReason); err != nil {
		log.Printf("usenet retry: re-parking grab %d after a failed retry: %v", g.ID, err)
		return
	}
	// The innermost error's TYPE, never its message: a *url.Error's Error()
	// renders the whole URL, api_key query parameter included. The type still
	// separates a transport failure from a config or store one.
	root := cause
	for {
		next := errors.Unwrap(root)
		if next == nil {
			break
		}
		root = next
	}
	log.Printf("usenet retry: grab %d (%s) — %s (%T)", g.ID, g.Title, retrySearchFailedReason, root)
}

// excludedRequestKeys loads the operator's exclusion set once per cycle, keyed
// exactly as requestKey/FindPendingRetry key a grab. A store error degrades to
// "nothing is excluded" (the pre-exclusion behaviour) rather than skipping the
// whole cycle: a broken exclusions read must not silently stop every retry.
func excludedRequestKeys(ctx context.Context, excludesStore *excludes.Store) map[string]bool {
	if excludesStore == nil {
		return nil
	}
	keys, err := excludesStore.Keys(ctx)
	if err != nil {
		log.Printf("usenet retry: loading excluded request keys: %v", err)
		return nil
	}
	return keys
}

type usenetAutoGrabEnabledResponse struct {
	Enabled bool `json:"enabled"`
}

type usenetAutoGrabEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

type usenetRetryIntervalResponse struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

type usenetRetryIntervalRequest struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

// getUsenetAutoGrabEnabledHandler reports the unattended auto-grab toggle,
// false by default.
func getUsenetAutoGrabEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled, err := settingsStore.GetBool(r.Context(), usenetAutoGrabEnabledKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, usenetAutoGrabEnabledResponse{Enabled: enabled})
	}
}

// putUsenetAutoGrabEnabledHandler sets the toggle AND, in the same request, the
// coupled retry interval: on -> 86400, off -> 0.
//
// The coupling is server-side on purpose. Retry only has meaning while
// auto-grab is on (RunAutoGrab gates TriggerRetry on the same key), so two
// independently settable switches could only ever disagree — and a client that
// forgot the second write would leave auto-grab on with the retry loop dead,
// stranding every pending_retry row forever. One request, one source of truth.
//
// Note for UI copy: switching the toggle ON does not start the loop in the
// running process — RunUsenetRetry's opt-in gate is read at boot, same as every
// other scheduler here. It takes effect on restart. Switching it OFF does stop
// a running loop, on its next tick.
func putUsenetAutoGrabEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req usenetAutoGrabEnabledRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if err := settingsStore.SetBool(ctx, usenetAutoGrabEnabledKey, req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		interval := 0
		if req.Enabled {
			interval = usenetAutoGrabIntervalSeconds
		}
		if _, err := storeIntervalSeconds(ctx, settingsStore, usenetRetryIntervalSecondsKey, interval, 0); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getUsenetRetryIntervalHandler returns the retry cadence in seconds, 0 when
// off. Read-mostly: the value is owned by the auto-grab toggle above, so this
// pair exists for parity with every other interval-backed job and for
// diagnostics — there is deliberately no user-facing interval control.
func getUsenetRetryIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secs, err := loadIntervalSeconds(r.Context(), settingsStore, usenetRetryIntervalSecondsKey, 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, usenetRetryIntervalResponse{IntervalSeconds: secs})
	}
}

// putUsenetRetryIntervalHandler stores the retry cadence. 0 disables the loop;
// a negative value is rejected. Setting it here does NOT change the auto-grab
// toggle — that direction of the coupling is one-way by design, so an operator
// (or a support session) can stop the loop without silently re-enabling manual
// approval semantics for the Search path.
func putUsenetRetryIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req usenetRetryIntervalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		badRequest, err := storeIntervalSeconds(r.Context(), settingsStore, usenetRetryIntervalSecondsKey, req.IntervalSeconds, 0)
		if err != nil {
			status := http.StatusInternalServerError
			if badRequest {
				status = http.StatusBadRequest
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
