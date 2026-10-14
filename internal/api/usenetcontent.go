package api

// Claude 2026-09-17: content-failure routing — park for a different Usenet release.
// Reason: PAR2/unpack/no-video failures are a property of THIS NZB, not the
//   network. The correct response is "try a different release", not "retry later".
//   parkUsenetContentFailure is the single park function for all three triggers;
//   contentUnusableFailure is the routing predicate used in applyUsenetFailure.
// Troubleshooting: journal "usenet content: grab N parked for alternate release".
// Review if: a fourth content-failure kind is added (add it to contentUnusableFailure).
// Related files: internal/usenet/content.go (sentinels),
//   internal/library/library.go (ErrNoVideoFile),
//   internal/api/usenetretry.go (applyUsenetFailure branch),
//   internal/api/autograbdrain.go (drainAlternateReleaseRetries).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/release"
	"github.com/labbersanon/sakms/internal/usenet"
)

const (
	// contentUnpackFailedReason is the operator-facing retry_reason for a
	// PAR2/unpack content park. Detail-free by convention: it is rendered on the
	// Requests screen and must never carry a staging path or unpack error detail.
	contentUnpackFailedReason = "the downloaded release could not be unpacked or repaired — trying a different release"

	// contentNoVideoReason is the operator-facing retry_reason for a no-video
	// import content park. Distinct from contentUnpackFailedReason so an operator
	// can tell the two failure types apart on the Requests screen.
	contentNoVideoReason = "the download contained no usable video — trying a different release"

	// Claude 2026-09-19: distinct password reason (still alternate-release park).
	// Reason: Requests screen should show why this NZB was abandoned.
	// Review if: password-file support lands.
	contentPasswordReason = "the release is password-protected (unsupported) — trying a different release"

	// Claude 2026-09-19: 430 mid-download → alternate Usenet NZB (not torrent).
	// Reason: missing articles are property of THIS NZB; a different release has
	//   different message-IDs. Re-hitting the same articles on Usenet is futile;
	//   switching release group is not.
	// Review if: dual-path (Usenet alternate then torrent) is wanted after cap.
	contentArticlesMissingReason = "articles missing on Usenet for this release (430) — trying a different release"

	// Claude 2026-10-13: stale-progress / transport cap → different NZB.
	// Reason: four short parks on one GID mean this article set is stuck;
	//   another release has different message-IDs.
	// Troubleshooting: grab 1843 parked attempt 1/4 forever before the count stuck.
	// Review if: MaxTransportRetries becomes a settings knob.
	contentStalledReleaseReason = "this release stalled repeatedly — trying a different release"
)

// contentUnusableFailure reports whether failure should route to the
// "try a different release" park. Transport errors always outrank content
// (§7.1): a failure wrapping ErrTransport is a network blip, not a bad release.
func contentUnusableFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, usenet.ErrTransport) {
		return false // transport wins any tie
	}
	if errors.Is(err, usenet.ErrUnpackToolMissing) {
		return false // environment fault — different release cannot fix it
	}
	// Claude 2026-09-19: 430 is a bad-THIS-NZB signal for alternate routing.
	return errors.Is(err, usenet.ErrArticleNotFound) ||
		errors.Is(err, usenet.ErrContentUnusable) ||
		errors.Is(err, library.ErrNoVideoFile)
}

// contentFailureReason maps a content-unusable failure to its operator-facing
// reason string.
func contentFailureReason(failure error) string {
	if errors.Is(failure, usenet.ErrTransport) {
		return contentStalledReleaseReason
	}
	if errors.Is(failure, usenet.ErrPasswordProtected) {
		return contentPasswordReason
	}
	if errors.Is(failure, library.ErrNoVideoFile) || errors.Is(failure, usenet.ErrNoVideoUnpacked) {
		return contentNoVideoReason
	}
	if errors.Is(failure, usenet.ErrArticleNotFound) {
		return contentArticlesMissingReason
	}
	if errors.Is(failure, autograb.ErrBelowQualityFloor) {
		return contentBelowFloorReason
	}
	return contentUnpackFailedReason
}

func parkNZBTitle(engineFilename, mediaTitle string) string {
	eng := strings.TrimSpace(engineFilename)
	if eng == "" {
		return ""
	}
	if grabs.NormaliseTitle(eng) == grabs.NormaliseTitle(mediaTitle) {
		return ""
	}
	return eng
}

// contentForgetEngine is the narrow interface parkUsenetContentFailure needs
// from the usenet Manager. *usenet.Manager satisfies it.
type contentForgetEngine interface {
	Forget(gid string) bool
	// FindFilename is the NZB/release display name (AddNZB name / X-DNZB-Name),
	// not grabs.Grab.Title. Empty when gid is unknown.
	FindFilename(gid string) string
	StagingDir() string
}

var _ contentForgetEngine = (*usenet.Manager)(nil)

// parkUsenetContentFailure parks g for a different-release Usenet retry that
// will re-enter RunAutoGrab (precheck owns picking the next NZB). Returns
// (true, nil) when the park was written; (false, nil) when a fail-closed guard
// rejected it — the caller should fall through to park() (days ladder).
//
// Fail-closed guards:
//  1. g.DownloadGID must have the "nzb-" prefix.
//  2. g.DownloadURL must be non-empty (the "u:" key requires it).
//
// Claude 2026-09-22: MaxAlternateReleaseAttempts gate removed.
// Reason: precheck exhausts the graded list each cycle; tried_release_keys
//
//	exclude dead NZBs. A hard "3 alt parks" cap was a download-time fallback
//	the operator retired — requeue through precheck instead.
//
// Troubleshooting: content/430 → pending_retry due-now → drainAlternateReleaseRetries.
// Review if: a park-episode cap returns for indexer churn.
//
// Side effects on success (best-effort — failure is logged, not fatal):
//   - clearOwnedUsenetStaging: the staged bytes are proven useless; leaving
//     them would trigger hollow reconcile imports on restart.
//   - Forget the engine error row so Downloads does not show Failed; the grab
//     is already pending_retry (queue) for an alternate or the days ladder.
func parkUsenetContentFailure(
	ctx context.Context,
	deps AutoGrabDeps,
	g grabs.Grab,
	failure error,
	engine contentForgetEngine,
) (bool, error) {
	if !strings.HasPrefix(g.DownloadGID, usenetGIDPrefix) {
		return false, nil
	}
	if strings.TrimSpace(g.DownloadURL) == "" {
		return false, nil
	}
	// Claude 2026-09-22: fingerprint the NZB/release title, never g.Title.
	// Reason: g.Title is the media/show name (e.g. "Burn Notice"); filterExcludedReleases
	//   hashes r.Title which is the Prowlarr/NZB release name. Hashing the show name
	//   excludes every candidate for that title. Engine FindFilename is the AddNZB
	//   argument (picked.Title), not obfuscated X-DNZB-Name. Empty → URL key only.
	// Troubleshooting: 430 parks then the next search still picks the same NZB, or
	//   every NZB for the show is excluded via a t: key of the show name.
	// Review if: grabs persist the NZB title as its own column.
	releaseTitle := ""
	if engine != nil {
		releaseTitle = engine.FindFilename(g.DownloadGID)
	}
	// Claude 2026-10-11: never hash g.Title (show name) as t:.
	// Reason: RelaunchNZB used to store the media title as Filename, so park
	//   wrote t:hash("the red skelton show") which matches no Prowlarr title.
	// Troubleshooting: same WEBTUBE NZB class re-dispatched after content park.
	releaseTitle = parkNZBTitle(releaseTitle, g.Title)
	newKeys := grabs.ReleaseKeys(g.DownloadURL, releaseTitle)
	for _, tok := range release.GroupTokens(releaseTitle) {
		newKeys = append(newKeys, grabs.GroupKey(tok))
	}
	reason := contentFailureReason(failure)
	if err := deps.GrabsStore.ParkForAlternateRelease(ctx, g.ID, time.Now(), reason, newKeys); err != nil {
		return false, fmt.Errorf("content park grab %d: %w", g.ID, err)
	}

	log.Printf("usenet content: grab %d (%s) requeued for precheck (alternate release) — %s",
		g.ID, g.Title, reason)

	if engine != nil {
		// Claude 2026-10-11: Forget the error row on content park.
		// Reason: 430/unpack parks pending_retry (alternate or days ladder). Leaving
		//   the engine error made Downloads show Failed while Requests already queued
		//   the retry. Precheck-abort already dropPrecheckFailed (no Failed pill);
		//   BODY 430 must match. Staging is still wiped below.
		// Troubleshooting: Downloads "article not found" after grab 1904 430 park.
		// Review if: a glance window for unpack errors is wanted separately from 430.
		forgetContentEngineRow(engine, g.DownloadGID, g.ID, "content park")
		clearOwnedUsenetStagingEngine(engine, g.DownloadGID)
	}

	return true, nil
}

// parkContentFailureOrDaysLadder is the import/reconcile twin of
// parkRetrievalFailure's content branch: try the alternate-release park, and
// when a fail-closed guard declines it (cap reached, empty URL, …) fall through
// to the days ladder so the grab does not stay queued forever holding a slot.
//
// On BOTH outcomes the hollow staging dir is cleaned when engine != nil — the
// delivered bytes are proven useless either way.
//
// Claude 2026-09-17: closes the live Love Is Blind stuck-queued bug.
// Reason: UsenetCompleteImporter treated (false, nil) from parkUsenetContentFailure
//
//	as "done" and returned without parking — grab stayed queued with a hollow GID.
//
// Troubleshooting: journal "cap reached … falling back to days ladder" with the
//
//	grab still status=queued → this helper was missing on the import path.
//
// Review if: import/reconcile share applyUsenetFailure directly instead.
func parkContentFailureOrDaysLadder(ctx context.Context, deps AutoGrabDeps, g grabs.Grab, failure error, engine contentForgetEngine) error {
	handled, err := parkUsenetContentFailure(ctx, deps, g, failure, engine)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}
	// Cap / fail-closed decline: parkUsenetContentFailure skipped wipe.
	// The staging is still hollow — clean it before the days-ladder park.
	if engine != nil && strings.HasPrefix(g.DownloadGID, usenetGIDPrefix) {
		forgetContentEngineRow(engine, g.DownloadGID, g.ID, "days-ladder fallthrough")
		clearOwnedUsenetStagingEngine(engine, g.DownloadGID)
	}
	return parkGrabForRetry(ctx, deps, g.ID, usenetRetrievalReason(failure))
}

// forgetContentEngineRow drops the engine's terminal row for gid so Downloads
// does not show Failed for a grab that is already parked for retry.
func forgetContentEngineRow(engine contentForgetEngine, gid string, grabID int64, why string) {
	if engine == nil || gid == "" {
		return
	}
	if !engine.Forget(gid) {
		log.Printf("usenet content: Forget(%s) for grab %d (%s) returned false (already absent)", gid, grabID, why)
	}
}

// clearOwnedUsenetStagingEngine is clearOwnedUsenetStaging using the narrow
// contentForgetEngine interface. Called from parkUsenetContentFailure so tests
// can inject a fake engine without needing a real *usenet.Manager.
func clearOwnedUsenetStagingEngine(engine contentForgetEngine, gid string) {
	if engine == nil || gid == "" {
		return
	}
	root := engine.StagingDir()
	gidDir := filepath.Join(root, gid)
	if err := usenet.RemoveOwnedStagingDir(root, gidDir); err != nil {
		log.Printf("usenet content: staging cleanup %s: %v", gidDir, err)
	}
}
