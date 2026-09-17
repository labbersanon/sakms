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
	return errors.Is(err, usenet.ErrContentUnusable) || errors.Is(err, library.ErrNoVideoFile)
}

// contentFailureReason maps a content-unusable failure to its operator-facing
// reason string.
func contentFailureReason(failure error) string {
	if errors.Is(failure, library.ErrNoVideoFile) {
		return contentNoVideoReason
	}
	return contentUnpackFailedReason
}

// contentForgetEngine is the narrow interface parkUsenetContentFailure needs
// from the usenet Manager. *usenet.Manager satisfies it.
type contentForgetEngine interface {
	Forget(gid string) bool
	StagingDir() string
	ClearResumeMirror(gid string)
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
	existing := grabs.ParseTriedReleaseKeys(g.TriedReleaseKeys)
	if grabs.AlternateAttempts(existing) >= grabs.MaxAlternateReleaseAttempts {
		log.Printf("usenet content: grab %d (%s) cap reached (%d/%d) — falling back to days ladder",
			g.ID, g.Title, grabs.AlternateAttempts(existing), grabs.MaxAlternateReleaseAttempts)
		return false, nil
	}

	newKeys := grabs.ReleaseKeys(g.DownloadURL, g.Title)
	reason := contentFailureReason(failure)
	if err := deps.GrabsStore.ParkForAlternateRelease(ctx, g.ID, time.Now(), reason, newKeys); err != nil {
		return false, fmt.Errorf("content park grab %d: %w", g.ID, err)
	}

	log.Printf("usenet content: grab %d (%s) parked for alternate release (attempt %d/%d) — %s",
		g.ID, g.Title, grabs.AlternateAttempts(existing)+1, grabs.MaxAlternateReleaseAttempts, reason)

	// Best-effort cleanup. Failure is logged but does not undo the park.
	if engine != nil {
		if !engine.Forget(g.DownloadGID) {
			log.Printf("usenet content: Forget(%s) for grab %d returned false (already absent)", g.DownloadGID, g.ID)
		}
		clearOwnedUsenetStagingEngine(engine, g.DownloadGID)
	}

	return true, nil
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
	engine.ClearResumeMirror(gid)
}
