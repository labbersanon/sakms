// This file is the pre-release promotion pass — the FOURTH and last step of
// runUsenetRetryCycle. An operator clicked an unreleased movie in Calendar's
// Upcoming view; that click minted a held grab row (parkPreReleaseRequest,
// autograb_shared.go) which sits dormant until its release date. This pass is
// what notices the date has arrived and sends the row through the ordinary
// gated dispatch path.
//
// NO NEW SCHEDULER, NO GOROUTINE, NO TICKER, NO INTERVAL KEY lives here, and
// saying so loudly is the point: "held dormant until the release date" is the
// most schedule-suggesting phrase in this feature's spec, and it adds nothing to
// the codebase's scheduler count. There are still SIX interval-driven
// schedulers. releaseDueGrabs is a plain function call inside the cycle that
// already exists, exactly as monitorAirDates is; the existing 24h cycle notices
// the date using a WHERE clause (grabs.DueForRelease).
//
// THE HOLD IS THE hold_until COLUMN, NEVER retry_after. A held row is born with
// an EMPTY retry_after, which is what makes it invisible to grabs.DueForRetry
// unconditionally (see parkPreReleaseRequest's doc). Nothing here — or anywhere
// else in this feature — infers a row's origin from retry_reason or retry_count;
// hold_until is a queryable origin marker precisely so that inference is never
// needed again, after a HIGH-severity misclassification bug in series air-date
// monitoring proved how badly a reason-string discriminator fails.
//
// DISPATCH IS PATTERN B, CREATION IS A PARKER, and that split is legal. Pattern
// B is a new AutoGrabTrigger const plus a caller that goes through RunAutoGrab,
// which is exactly what this file is (TriggerPreRelease). The row's CREATION is
// a parker instead, because a held row is minted by a click and not by a search.
// CLAUDE.md's "do not use both patterns for the same trigger source" forbids two
// DISPATCH paths for one source; there is one here, and it is this one.
//
// It lives in internal/api rather than its own package for the same reason
// usenetretry.go and airdatemonitor.go do: everything it drives is
// package-private here (RunAutoGrab's deps, isActiveGrab, reparkFailedRetry,
// the retry reason strings). It stays independently deletable: delete this file
// and its one call in runUsenetRetryCycle.
package api

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

const (
	// maxPreReleaseGrabsPerCycle is the unconfigured bound on how many held
	// requests one cycle may dispatch; releaseDueGrabs runs at the operator's
	// Usenet cycle slots (loadUsenetCycleSlots) instead. Either way, each attempt
	// costs one Prowlarr search plus TMDB calls, SEQUENTIALLY — never an errgroup
	// fan-out, which is the concurrent-indexer pattern that got the per-card
	// availability badge permanently banned.
	//
	// It matters most at one specific moment. Unattended auto-grab is off by
	// default, and while it is off the retry cycle never runs at all, so held
	// rows simply accumulate — an operator can spend months click-requesting
	// upcoming films before anything can ever promote them. The cycle after the
	// toggle is switched on would otherwise dispatch that whole backlog at once.
	// The burst is real; this is what bounds it.
	//
	// grabs.DueForRelease deliberately has NO LIMIT, so the bound is enforced
	// here, in the per-row loop, as a counter incremented only on a REAL
	// dispatch attempt. An exclusion skip or a duplicate suppression must not
	// consume budget: neither costs a Prowlarr search, which is what the cap
	// exists to bound.
	maxPreReleaseGrabsPerCycle = defaultAutoGrabSlotsPerCycle

	// preReleaseSupersededReason explains a held request this pass terminated
	// because the operator got the film some other way before its release date
	// arrived.
	//
	// Writing it is NOT cosmetic, for exactly the reason airDateUnmonitoredReason
	// gives: grabs.Failed means "this release was taken down" (a 451/DMCA
	// classification) everywhere else in this app, so a row flipping to Failed
	// with no explanation reads to an operator as a takedown of a film they
	// simply already own. retry_reason is the field that exists to explain a
	// row's state, and UpdateStatus takes only (id, status) and cannot carry
	// one — which is why the reason is a separate, EARLIER call.
	preReleaseSupersededReason = "the film was already grabbed or added to the library, so this request was cancelled"
)

// releaseDueGrabs promotes every held Calendar pre-release request whose
// hold_until has arrived, re-arming its existing row through RunAutoGrab.
//
// IT RUNS FOURTH AND LAST inside runUsenetRetryCycle, and the position is
// chosen rather than arbitrary. Running last means no downstream pass can
// re-handle a row this one just promoted — a by-construction property that
// needs no reasoning about DueForRetry's guards to see. It also cannot disturb
// monitorAirDates' documented "final word on retry_after" guarantee, because
// that guarantee is about SERIES rows while a pre-release row is always MOVIES
// with hold_until set: the two populations are provably disjoint (airDateShaped
// requires g.Mode == mode.Series).
//
// libStore MUST be nil-guarded, exactly as monitorAirDates is. Six existing
// tests pass nil for it, and this pass needs it for the duplicate-suppression
// library check below. The guard is vacuously true today (no nil-libStore
// fixture has a held row) and becomes a real nil dereference the moment one
// grows one.
//
// It never dispatches, scores or gates anything itself: RunAutoGrab stays the
// single gated scoring-and-dispatch path, which is what keeps "a pre-release
// request can never bypass scoring or the toggle" true by construction rather
// than by review.
func releaseDueGrabs(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc,
	libStore *library.Store, excluded map[string]bool, now time.Time) {

	if libStore == nil {
		return // the library store isn't wired — nothing to check duplicates against
	}

	due, err := deps.GrabsStore.DueForRelease(ctx, now)
	if err != nil {
		log.Printf("pre-release: listing requests due for release: %v", err)
		return
	}
	if len(due) == 0 {
		return
	}

	superseded, err := nonHeldMovieWork(ctx, deps.GrabsStore, libStore)
	if err != nil {
		log.Printf("pre-release: computing duplicate suppression: %v", err)
		// Deliberately ABANDONS the pass rather than degrading to "suppress
		// nothing", which is the opposite of what activeSeriesGrabKeys does with
		// the same class of failure. The asymmetry is intentional: this pre-filter
		// is the only thing standing between a promoted request and a second copy
		// of a film the operator already has, and a held row that waits one more
		// cycle loses nothing. A read failure must not be allowed to buy a
		// duplicate download.
		return
	}

	attempts := 0
	cycleCap := loadUsenetCycleSlots(ctx, deps.SettingsStore)
	for _, g := range due {
		// An operator who excluded this title from the Requests worklist has said
		// "stop working on this" — identical treatment retryDueGrabs gives it, and
		// for the same reason. Skipped rather than failed: exclusion is reversible,
		// and a skip leaves the row exactly as the operator left it (still held,
		// still due, promoted the moment the exclusion is lifted).
		if excluded[excludes.Key(string(g.Mode), g.TMDBID, g.Title)] {
			log.Printf("pre-release: skipping grab %d (%s) — its title is excluded from the worklist", g.ID, g.Title)
			continue
		}

		if g.TMDBID > 0 && superseded[g.TMDBID] {
			log.Printf("pre-release: cancelling grab %d (%s) — the film is already tracked or already being grabbed", g.ID, g.Title)
			terminateSupersededRequest(ctx, deps.GrabsStore, g, now)
			continue
		}

		// Checked here, AFTER the two skips above, so neither consumes budget —
		// and incremented before the session build, because a build failure
		// re-parks the row (writing a retry_after) and therefore costs the row its
		// promotion just as surely as a real search does.
		if attempts >= cycleCap {
			log.Printf("pre-release: reached this cycle's dispatch cap (%d) — the rest stay held for the next one", cycleCap)
			return
		}
		attempts++

		sess, err := build(ctx, g.Mode)
		if err != nil {
			reparkFailedRetry(ctx, deps, g, err)
			continue
		}

		out, err := RunAutoGrab(ctx, deps, sess, AutoGrabRequest{
			Mode: g.Mode, Title: g.Title, TMDBID: g.TMDBID, TVDBID: g.TVDBID,
			Season: g.SeasonNumber, Episode: g.EpisodeNumber, SeasonSpecified: g.SeasonSpecified,
			RootFolderPath: g.RootFolderPath,
			Trigger:        TriggerPreRelease,
			// Re-arm THIS row instead of recording a second one. Mandatory, not an
			// optimisation: a fresh row would leave the held one still promotable
			// with a past hold_until, and AddNZB mints a fresh GID per call, so the
			// GID dedup guard could not catch the resulting duplicate.
			ExistingGrabID: g.ID,
			// Releases nil = "search for me". A held row carries the TMDB id
			// RunAutoGrab's internal search needs, unlike the Search hook's rows.
		})
		switch {
		case err != nil:
			// Re-parks on the normal retry interval, which also ends this row's
			// eligibility for promotion (DueForRelease requires retry_after = '').
			// The request is not lost — it has simply joined the ordinary retry
			// track, which is where a released film belongs.
			reparkFailedRetry(ctx, deps, g, err)
		case out.Gated:
			// The toggle went off between the cycle's interval read and the gate.
			// Every remaining row would be gated too, so stop rather than log once
			// per row. Nothing was searched, scored, dispatched or recorded, so the
			// rows stay held and promote on a later cycle.
			log.Printf("pre-release: usenet auto-grab is switched off — abandoning this cycle")
			return
		case out.AlreadyGrabbing:
			// The dispatch returned a GID some other in-flight grab already
			// holds, so RunAutoGrab recorded nothing. Park this row exactly as
			// retryDueGrabs' identical branch does (usenetretry.go): a bare log
			// writes NO state, and retry_after = '' is one of DueForRelease's
			// four guards — so the row would come back every single cycle,
			// forever, permanently consuming one of this cap's twenty slots.
			// Twenty such rows halt the whole feature.
			duplicate := "another grab"
			if out.GrabID != 0 {
				duplicate = fmt.Sprintf("grab %d", out.GrabID)
			}
			// Logged in full here, unlike reparkFailedRetry's type-only line:
			// this cause is composed right above from an int, so it cannot
			// carry a credentialed URL the way a transport error can.
			log.Printf("pre-release: grab %d (%s) is already being downloaded by %s", g.ID, g.Title, duplicate)
			reparkFailedRetry(ctx, deps, g, fmt.Errorf("this release is already being downloaded by %s", duplicate))
		case out.Grabbed:
			log.Printf("pre-release: grab %d (%s) reached its release date and was dispatched", g.ID, g.Title)
		default:
			// out.NoMatch — RunAutoGrab already re-parked the row through
			// parkPendingRetry, so it now carries a real retry_after and continues
			// on the ordinary retry track.
			log.Printf("pre-release: grab %d (%s) reached its release date but no candidate qualified (%s)", g.ID, g.Title, out.RetryStatus)
		}
	}
}

// nonHeldMovieWork is the set of Movies TMDB ids the operator already has
// outstanding work for BY SOME MEANS OTHER THAN a Calendar pre-release request.
// It is this feature's one duplicate-suppression predicate, shared by both of
// the places that need it — and sharing it is the point, so the two can never
// disagree about what "already handled" means:
//
//   - the pre-release ROUTE's step 1, which rejects a click for a film the
//     operator already owns or is already grabbing;
//   - releaseDueGrabs' PROMOTION pre-filter above, which cancels a held request
//     the operator satisfied another way during the months it was waiting.
//
// Both are needed. A click can only be judged against the state at click time,
// and a hold lasts months.
//
// Two sources, both local, NEVER Prowlarr (CLAUDE.md: a "do I already own this"
// signal on a browse surface comes from the tracked library, never an indexer):
//
//   - the tracked Movies library — the operator owns it now;
//   - every ACTIVE or IMPORTED, NON-HELD grab — something is already fetching it,
//     or already did. Failed grabs deliberately do NOT count: a failed grab is a
//     title the operator can no longer get any other way, and counting it would
//     permanently suppress the request.
//
// hold_until == "" IS THE SELF-EXCLUSION and it is load-bearing in both callers.
// A held row is itself an active pending_retry grab for its own TMDB id, so
// without that clause the promotion filter would suppress every row against
// itself and nothing would ever promote, while the route's step 1 would swallow
// every re-click before it could refresh the date. It also means two held rows
// for one id could not suppress each other — which cannot arise, because the
// route resolves a second click through grabs.FindHeldRequest and refreshes the
// first row instead of minting a second.
//
// Ids <= 0 are skipped: a tracked row TMDB never matched carries id 0, and
// admitting it would collide with every other unmatched row.
func nonHeldMovieWork(ctx context.Context, grabsStore *grabs.Store, libStore *library.Store) (map[int]bool, error) {
	out := map[int]bool{}
	tracked, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		return nil, err
	}
	for _, it := range tracked {
		if it.TMDBID > 0 {
			out[it.TMDBID] = true
		}
	}
	grabList, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		return nil, err
	}
	for _, g := range grabList {
		if g.TMDBID <= 0 || g.HoldUntil != "" {
			continue
		}
		if isActiveGrab(g.Status) || g.Status == grabs.Imported {
			out[g.TMDBID] = true
		}
	}
	return out, nil
}

// terminateSupersededRequest ends a held request the operator superseded, with
// an explanation attached.
//
// The reason is written FIRST and the status flipped SECOND, following
// cancelAirDateRetry's precedent exactly: UpdateStatus cannot carry a reason, so
// a bare flip to Failed would read as a DMCA takedown of a film the operator
// simply already owns.
//
// Both writes are logged-and-continued rather than fatal, and the partial-write
// case leaves the row RECOVERABLE but not SAFE: a row whose reason landed but
// whose status flip did not keeps status pending_retry with a real retry_after
// and a past hold_until, so DueForRelease no longer returns it
// (retry_after != ”) while DueForRetry does — it rejoins the ordinary retry
// track rather than being silently stuck.
//
// CORRECTED 2026-08-02 — the claim that ended the paragraph above was FALSE.
// Quoted verbatim so a future session can find it: "not a duplicate risk, since
// the next cycle's re-search goes through the same gated path." The gate it
// names is the usenet_autograb_enabled toggle, which bounds WHETHER anything
// dispatches unattended — it is not, and never was, a duplicate check.
// retryDueGrabs performs NO library/already-owned suppression of any kind. The
// only such check in this feature is nonHeldMovieWork above, and it runs at
// PROMOTION TIME ONLY.
//
// So state the real property plainly: THE DUPLICATE SUPPRESSION IS ONE-SHOT.
// It is scoped to the promotion attempt, and nothing re-checks afterwards. This
// is not special to the partial-write case — it is true of every promoted row.
// Once a held row takes any outcome that parks it with a real retry_after (a
// NoMatch, a failed session build, an AlreadyGrabbing), DueForRelease's
// retry_after = ” guard removes it permanently, and from then on it is
// indistinguishable from any other ordinary retry row: it can re-search and
// dispatch a film the operator has since acquired by some other route, with no
// further duplicate check ever again. That is a pre-existing property of
// retryDueGrabs' uncapped, unfiltered loop, which this feature inherits rather
// than introduces — recorded here because the superseded sentence claimed the
// opposite, and a reader would otherwise trust it.
func terminateSupersededRequest(ctx context.Context, grabsStore *grabs.Store, g grabs.Grab, now time.Time) {
	if err := grabsStore.SetRetryAfter(ctx, g.ID, now, preReleaseSupersededReason); err != nil {
		log.Printf("pre-release: recording the superseded reason on grab %d: %v", g.ID, err)
		return
	}
	if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Failed); err != nil {
		log.Printf("pre-release: cancelling superseded grab %d: %v", g.ID, err)
	}
}
