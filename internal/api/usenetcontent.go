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
	StagingDir() string
}

// parkUsenetContentFailure parks g for a different-release Usenet retry.
// Returns (true, nil) when the park was written; (false, nil) when a fail-closed
// guard rejected it — the caller should fall through to park() (days ladder).
//
// Fail-closed guards:
//  1. g.DownloadGID must have the "nzb-" prefix.
//  2. g.DownloadURL must be non-empty (the "u:" key requires it).
//  3. AlternateAttempts(existing keys) < MaxAlternateReleaseAttempts.
//
// Side effects on success (both best-effort — failure is logged, not fatal):
//   - engine.Forget(gid): drops the terminal in-memory entry so the engine's
//     queue and usenetRelaunchSlots do not carry a dead download.
//   - clearOwnedUsenetStaging: the staged bytes are proven useless; leaving
//     them would trigger hollow reconcile imports on restart.
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
	attempts := grabs.AlternateAttempts(grabs.ParseTriedReleaseKeys(g.TriedReleaseKeys))
	if attempts >= grabs.MaxAlternateReleaseAttempts {
		log.Printf("usenet content: grab %d (%s) cap reached (%d/%d) — falling back to days ladder",
			g.ID, g.Title, attempts, grabs.MaxAlternateReleaseAttempts)
		return false, nil
	}

	newKeys := grabs.ReleaseKeys(g.DownloadURL, g.Title)
	reason := contentFailureReason(failure)
	if err := deps.GrabsStore.ParkForAlternateRelease(ctx, g.ID, time.Now(), reason, newKeys); err != nil {
		return false, fmt.Errorf("content park grab %d: %w", g.ID, err)
	}

	log.Printf("usenet content: grab %d (%s) parked for alternate release (attempt %d/%d) — %s",
		g.ID, g.Title, attempts+1, grabs.MaxAlternateReleaseAttempts, reason)

	if engine != nil {
		if !engine.Forget(g.DownloadGID) {
			log.Printf("usenet content: Forget(%s) for grab %d returned false (already absent)", g.DownloadGID, g.ID)
		}
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
	// Cap / fail-closed decline: parkUsenetContentFailure skipped Forget+wipe.
	// The staging is still hollow — clean it before the days-ladder park.
	if engine != nil && strings.HasPrefix(g.DownloadGID, usenetGIDPrefix) {
		if !engine.Forget(g.DownloadGID) {
			log.Printf("usenet content: Forget(%s) on days-ladder fallthrough for grab %d returned false", g.DownloadGID, g.ID)
		}
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
