package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// Calendar's click-to-request route: POST /api/calendar/prerelease-request.
//
// "Creates a real Request" cannot mean what the spec literally says, and the
// deviation is recorded here rather than left for a reader to rediscover.
// requestsHandler aggregates Requests LIVE ON READ from the library and the grab
// log — there is no requests table and no POST /api/requests to call. A grabs
// row that isActiveGrab admits IS the request, so creating one is the faithful
// implementation.
//
// The spec also asks for "the same flow Discover's Grab button uses". That flow
// (POST /api/modes/{mode}/autograb, TriggerOperator) searches, scores and
// dispatches IMMEDIATELY — the exact thing the pre-release hold forbids. What is
// genuinely shared is the row type and the eventual dispatch gate, not the entry
// point: the row this route mints later reaches dispatch through the same single
// RunAutoGrab path every other trigger uses (releaseDueGrabs, prerelease.go).
//
// It carries NO EXCLUSION CHECK, deliberately. Exclusion is reversible and
// releaseDueGrabs checks it at promotion time, so an excluded title can never
// dispatch anyway; requiring an *excludes.Store here would force this route off
// NewMux onto a separately mounted mux, as /api/requests already had to be.
//
// Movies only. Series click-to-request is deliberately out of scope (Upcoming's
// two halves are structurally different queries, and only Movies has a
// release-date affordance). A future Series pre-release row would carry
// TMDBID > 0 && SeasonSpecified && Episode > 0 and therefore match
// airdatemonitor.go's airDateShaped EXACTLY, so its un-monitored reap branch
// would silently flip a held request to Failed. The fix at that point is one
// conjunct — hold_until == "" in airDateShaped — which is cheap precisely
// because the column exists. Noted so the landmine is found before it is
// stepped on.

// preReleaseRequestHandler backs POST /api/calendar/prerelease-request. Three
// steps, in this order, and each one exists for a case the others cannot see.
//
//  1. OUTSTANDING-WORK CHECK (nonHeldMovieWork). Reject, MUTATING NOTHING, when
//     this TMDB id is already tracked in the Movies library or already has an
//     active-or-Imported Movies grab THAT IS NOT ITSELF A HELD REQUEST. This
//     covers the case where NO HELD ROW EVER EXISTED — the operator grabbed the
//     film from Discover, or already owns it — which step 2's query by
//     definition cannot see. The Upcoming route renders a near-identical
//     predicate as a per-title flag, but a stale tab defeats a client-side gate,
//     so the server enforces it too.
//  2. FindHeldRequest. On a hit this is a re-click, and what the hit means
//     DEPENDS ON THE ROW'S STATUS — the query is deliberately not status-scoped,
//     so a second click finds the first request however far the row has
//     travelled: held, promoted, dispatched, imported or failed. (That breadth is
//     load-bearing. Status-scoping to pending_retry would miss a row that already
//     promoted and flipped to queued, and the click would mint a SECOND row
//     carrying an already-past date — a duplicate download on the very next
//     cycle.) Two branches:
//     - Failed — TERMINAL, and a bare date refresh on it is a silent dead end,
//     not a refresh. RearmHeldRequest resurrects it. See the branch itself.
//     - anything else — still held, in flight, or already delivered. Refresh the
//     date with SetHoldUntil and report alreadyRequested. NEVER SetPendingRetry
//     here — it increments retry_count, and a re-click is not an attempt.
//     Refreshing is not busywork: TMDB release dates get corrected, and the
//     hold must follow.
//  3. MISS. parkPreReleaseRequest mints the held row — or loses a race to a
//     concurrent click and degrades to the same alreadyRequested answer step 2
//     produces. See the branch.
func preReleaseRequestHandler(grabsStore *grabs.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var req apidto.PreReleaseRequestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.TMDBID <= 0 {
			http.Error(w, "tmdbId is required", http.StatusBadRequest)
			return
		}
		if req.Title == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}
		// TMDB hands out a bare YYYY-MM-DD; parsing it as UTC midnight is what
		// keeps the hold from being a day off. grabs.FormatTime takes a time.Time,
		// so the parse cannot be skipped by passing the string through.
		until, err := time.Parse(dateOnlyLayout, req.ReleaseDate)
		if err != nil {
			http.Error(w, "releaseDate must be a YYYY-MM-DD date", http.StatusBadRequest)
			return
		}

		// Step 1 — outstanding work by some OTHER means. nonHeldMovieWork is
		// deliberately blind to held rows (see its doc): a held row for this id IS
		// this request, and letting it land here would swallow every re-click
		// before step 2 could refresh the date.
		otherWork, err := nonHeldMovieWork(ctx, grabsStore, libStore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if otherWork[req.TMDBID] {
			// MUTATES NOTHING. The operator owns the film, or is already grabbing
			// it, by a route this feature did not create — there is nothing to
			// create and nothing to refresh.
			writeJSON(w, apidto.PreReleaseRequestResponse{AlreadyRequested: true})
			return
		}

		// Step 2 — a re-click. The query is not status-scoped, so this finds the
		// first request however far it has travelled: held, promoted, dispatched,
		// imported or failed.
		//
		// REACHABILITY NOTE FOR WAVE 3 (FE-UP), recorded honestly rather than left
		// to be rediscovered: as the frontend is currently specified, THIS WHOLE
		// STEP IS ONLY REACHABLE FROM A STALE CLIENT. Upcoming/Movies renders an
		// alreadyRequested flag per title and suppresses the click affordance for
		// any film that carries it, so there is no re-click affordance at all — an
		// operator cannot intentionally ask to refresh a date. The "TMDB corrected
		// the release date" case this step was written for therefore has no user
		// flow behind it today; what actually reaches here is a tab opened before
		// the request existed. If Wave 3 wants that case to be genuinely
		// reachable, it needs (a) an explicit "refresh" affordance on an
		// already-scheduled Upcoming movie. Otherwise (b) read this step as pure
		// defensive robustness against duplicate requests — which is real value on
		// its own, since it is what stops a stale tab minting a second row — and
		// not as an active feature. Either way the step stays: dropping it is what
		// re-opens the duplicate-row hole.
		held, err := grabsStore.FindHeldRequest(ctx, mode.Movies, req.TMDBID)
		switch {
		case err == nil:
			// A Failed row is TERMINAL and needs resurrecting, not refreshing.
			// SetHoldUntil writes hold_until and nothing else, so on a Failed row it
			// would leave status=failed with a non-empty download_gid and an empty
			// retry_after — failing two of DueForRelease's guards permanently. The
			// row would never be searched, scored or dispatched again, while this
			// handler reported success; and since FindHeldRequest's ORDER BY id ASC
			// keeps returning that same row for this TMDB id, no later click could
			// route around it either. RearmHeldRequest resets all three guards.
			//
			// Reachable, not theoretical: a held row promotes and dispatches
			// (Relaunch sets queued + a GID, leaving hold_until as provenance), the
			// download takes a permanent 451/DMCA classification, and the operator —
			// seeing the film still absent — clicks it again.
			if held.Status == grabs.Failed {
				if err := grabsStore.RearmHeldRequest(ctx, held.ID, until, heldRequestReason); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			} else if err := grabsStore.SetHoldUntil(ctx, held.ID, until, heldRequestReason); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, apidto.PreReleaseRequestResponse{
				GrabID: held.ID, HeldUntil: grabs.FormatTime(until), AlreadyRequested: true,
			})
			return
		case !errors.Is(err, grabs.ErrNotFound):
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Step 3 — a first request for this film.
		//
		// Steps 2 and 3 are a check-then-act, and two genuinely concurrent clicks
		// (a double-click, or two tabs) can both miss step 2 and both arrive here.
		// Nothing downstream could repair that: nonHeldMovieWork self-excludes
		// EVERY held row rather than just the one under evaluation, so promotion
		// cannot tell two held rows apart and both would dispatch — one film,
		// downloaded twice. The partial unique index idx_grabs_held_request
		// (migration 0057) is what actually prevents it; the loser of the race
		// surfaces here as ErrHeldRequestExists and is answered with the same
		// alreadyRequested step 2 produces, because that is exactly what happened —
		// the request IS registered, just by the other click.
		created, err := parkPreReleaseRequest(ctx, grabsStore, mode.Movies, req.Title, req.TMDBID, until)
		switch {
		case errors.Is(err, grabs.ErrHeldRequestExists):
			// Re-read rather than reporting the row we failed to insert: the winner
			// owns the id and the stored date now.
			winner, findErr := grabsStore.FindHeldRequest(ctx, mode.Movies, req.TMDBID)
			if findErr != nil {
				http.Error(w, findErr.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, apidto.PreReleaseRequestResponse{
				GrabID: winner.ID, HeldUntil: winner.HoldUntil, AlreadyRequested: true,
			})
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, apidto.PreReleaseRequestResponse{GrabID: created.ID, HeldUntil: created.HoldUntil})
	}
}

// dateOnlyLayout is TMDB's bare date form. Spelled out rather than using
// time.DateOnly so the intent is legible next to grabs.FormatTime's sortable
// layout, which is a different thing entirely.
const dateOnlyLayout = "2006-01-02"
