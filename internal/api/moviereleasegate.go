// This file is the movie-release gate — the hard predicate every Movies
// dispatch path runs before searching or dispatching. It is Layer 2 of the
// two-layer CAM-prevention design (see .omc/plans/movies-us-release-gate.md
// §2): Layer 1 (hold accuracy) keeps hold_until honest; Layer 2 (this gate)
// is the actual enforcement point, fail-closed on every uncertainty.
//
// Do NOT collapse the two layers. Layer 1 alone is defeated by a TMDB date
// correction, a clock skew, or any future writer of hold_until. Layer 2 alone
// gives correct behavior with a permanently wrong "Held until" date in the UI.
//
// This file is independently deletable: remove it and its call sites and the
// gate is gone. Nothing else in the codebase depends on it, by design.
package api

import (
	"context"
	"log"
	"time"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// unresolvedReleaseHold is the hold_until a movie carries when TMDB knows no
// typed 4/5/6 US release for it yet. A recognizable sentinel, not "now + N
// days": the refresh pass and the Requests screen both need to distinguish "we
// do not know yet" from "we know, and it is in December." A rolling date would
// render as a confident claim TMDB never made.
//
// This value is compared by prefix in the frontend (Requests.tsx) to produce
// the "awaiting an announced release" blurb instead of a literal date.
const unresolvedReleaseHold = "9999-12-31T00:00:00.000Z"

// awaitingReleaseReason is the retry_reason for a row held at the sentinel —
// operator-facing copy only. Nothing branches on it; hold_until is the
// authoritative origin marker.
const awaitingReleaseReason = "held until a US digital, physical or TV release is announced on TMDB"

// Note: heldRequestReason is declared in autograb_shared.go and reused here.

// movieRelease is the gate's result for one movie.
type movieRelease struct {
	// Acquirable is true when a typed 4/5/6 US release dated <= now exists.
	Acquirable bool
	// Known is true when a typed 4/5/6 entry exists at all (possibly future).
	Known bool
	// HoldUntil is the first promotable instant: Earliest+24h when Known, or
	// the sentinel time when !Known. Always valid; callers use it directly.
	HoldUntil time.Time
}

// sentinelTime is unresolvedReleaseHold parsed once at init. Panics on
// mis-parse rather than surfacing a broken sentinel at runtime.
var sentinelTime = func() time.Time {
	t, err := time.Parse(time.RFC3339, unresolvedReleaseHold)
	if err != nil {
		panic("moviereleasegate: failed to parse sentinel: " + err.Error())
	}
	return t
}()

// gateMovieGrab is the ONE release predicate every Movies dispatch path runs.
// Returns (result, blocked, reason).
//
// FAILS CLOSED on every uncertainty — a TMDB outage, an unconfigured TMDB, an
// unparseable date — because the cost of a false block is a delayed grab and
// the cost of a false allow is a camcorder rip in the library.
//
// Rules, in order:
//   - m != mode.Movies: allow, no TMDB call (Series has air-date monitoring;
//     Adult has no release_dates concept).
//   - tmdbID <= 0: allow, no TMDB call, log once. KNOWN HOLE: rows without a
//     TMDB id cannot be checked. In practice the exposure is narrow:
//     Search-hook rows have RuntimeSeconds=0 so GradeCandidate short-circuits
//     them and they never auto-dispatch; the only open path is a human manually
//     picking a release from the Search screen for a typed query. Named fix:
//     resolve title → TMDB id at the Search hook.
//   - client == nil: block, hold = sentinel (TMDB unconfigured; cannot confirm).
//   - TMDB call errors: block, hold = sentinel (fail closed).
//   - !known: block, hold = sentinel (theatrical-only or unannounced).
//   - known && earliest.After(now): block, hold = earliest+24h (schedulable).
//   - known && !earliest.After(now): allow.
func gateMovieGrab(ctx context.Context, client *tmdb.Client, m mode.Mode, tmdbID int) (movieRelease, bool, string) {
	if m != mode.Movies {
		return movieRelease{Acquirable: true, Known: true}, false, ""
	}
	if tmdbID <= 0 {
		// Named hole — see doc comment above.
		log.Printf("moviereleasegate: tmdbID <= 0 for a Movies row — skipping TMDB check (known hole; resolve title→TMDB id at Search hook to close it)")
		return movieRelease{Acquirable: true, Known: true}, false, ""
	}
	if client == nil {
		return movieRelease{HoldUntil: sentinelTime}, true,
			"sakms can't confirm this movie has a digital, physical or TV release yet — add TMDB in Settings so it can check before grabbing"
	}

	earliest, known, err := client.USAcquirableRelease(ctx, tmdbID)
	if err != nil {
		return movieRelease{HoldUntil: sentinelTime}, true,
			"couldn't check TMDB release dates — will retry when the next cycle runs"
	}
	if !known {
		return movieRelease{HoldUntil: sentinelTime}, true,
			"no US digital, physical or TV release found on TMDB yet — will check again each cycle"
	}
	now := time.Now()
	if earliest.After(now) {
		// +24h: preserves "first promotable instant" semantics, matching
		// calendar_prerelease.go's day-after rule (migration 0021).
		holdUntil := earliest.Add(24 * time.Hour)
		return movieRelease{Known: true, HoldUntil: holdUntil}, true,
			"US digital/physical/TV release date is in the future — will check again when it arrives"
	}
	return movieRelease{Acquirable: true, Known: true, HoldUntil: earliest}, false, ""
}
