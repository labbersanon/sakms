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

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
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
	if errors.Is(failure, library.ErrNoVideoFile) {
		return contentNoVideoReason
	}
	if errors.Is(failure, usenet.ErrPasswordProtected) {
		return contentPasswordReason
	}
	if errors.Is(failure, usenet.ErrArticleNotFound) {
		return contentArticlesMissingReason
	}
	return contentUnpackFailedReason
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
//   exclude dead NZBs. A hard "3 alt parks" cap was a download-time fallback
//   the operator retired — requeue through precheck instead.
// Troubleshooting: content/430 → pending_retry due-now → drainAlternateReleaseRetries.
// Review if: a park-episode cap returns for indexer churn.
//
// Side effects on success (best-effort — failure is logged, not fatal):
//   - clearOwnedUsenetStaging: the staged bytes are proven useless; leaving
//     them would trigger hollow reconcile imports on restart.
//   - The engine error row is kept on Downloads (no Forget) so 430/unpack
//     failures are visible until the operator Cancels. Complete rows still
//     auto-dismiss via scheduleDismissComplete.
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
	//   excludes every candidate for that title. Engine Filename is the name passed to
	//   AddNZB/AddArticleSet (picked.Title / X-DNZB-Name). Empty filename → URL key only.
	// Troubleshooting: 430 parks then the next search still picks the same NZB, or
	//   every NZB for the show is excluded via a t: key of the show name.
	// Review if: grabs persist the NZB title as its own column.
	releaseTitle := ""
	if engine != nil {
		releaseTitle = engine.FindFilename(g.DownloadGID)
	}
	newKeys := grabs.ReleaseKeys(g.DownloadURL, releaseTitle)
	reason := contentFailureReason(failure)
	if err := deps.GrabsStore.ParkForAlternateRelease(ctx, g.ID, time.Now(), reason, newKeys); err != nil {
		return false, fmt.Errorf("content park grab %d: %w", g.ID, err)
	}

	log.Printf("usenet content: grab %d (%s) requeued for precheck (alternate release) — %s",
		g.ID, g.Title, reason)

	if engine != nil {
		// Claude 2026-09-22: do not Forget the error row on content park.
		// Reason: Forget dropped the terminal engine entry so Downloads looked silent
		//   after 430/unpack/no-video; the operator asked to keep the error visible
		//   until manual Cancel. Staging bytes are still useless and are wiped below.
		// Troubleshooting: 430 parks grab pending_retry but Downloads still shows the
		//   error until Cancel. Complete rows still auto-dismiss via scheduleDismissComplete.
		// Review if: operator wants errors auto-forgotten after a glance window.
		// if !engine.Forget(g.DownloadGID) {
		// 	log.Printf("usenet content: Forget(%s) for grab %d returned false (already absent)", g.DownloadGID, g.ID)
		// }
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
		// Claude 2026-09-22: keep the error row on days-ladder fallthrough too.
		// Reason: same silent-Downloads failure as the content-park success path;
		//   operator asked to keep errors visible until dismiss/cancel.
		// Troubleshooting: cap-reached 430 still shows on Downloads until Cancel.
		// Review if: days-ladder fallthrough should Forget after a glance window.
		// if !engine.Forget(g.DownloadGID) {
		// 	log.Printf("usenet content: Forget(%s) on days-ladder fallthrough for grab %d returned false", g.DownloadGID, g.ID)
		// }
		clearOwnedUsenetStagingEngine(engine, g.DownloadGID)
	}
	return parkGrabForRetry(ctx, deps, g.ID, usenetRetrievalReason(failure))
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
