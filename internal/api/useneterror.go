package api

import (
	"context"
	"errors"
	"log"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// UsenetErrorHandler returns the usenet Manager's onError callback: when the
// engine marks a download as error, this parks (or permanently fails) its grab
// immediately — without waiting for the usenet-retry sweep tick. Built in
// cmd/sakms and handed to usenet.Manager.SetOnError, the structural twin of
// UsenetCompleteImporter / StaleTorrentHandler. The Manager may not import
// grabs/api, so the policy half of "a usenet download failed" lives here.
//
// Deliberate differences from handleStaleTorrent:
//   - No Cancel and no file deletion — the download is already terminal;
//     staging cleanup belongs to stagingsweep (except content parks, which
//     Forget + clear owned staging via deps.NZB — see parkUsenetContentFailure).
//   - No nzb- GID prefix check — the caller is the usenet engine.
//   - Dispatches nothing, so usenet_autograb_enabled is honored for free
//     (parked rows wait for retryDueGrabs → RunAutoGrab).
//
// sweepUsenetFailures remains authoritative for restart recovery (unknown GIDs
// after boot). This handler is the live fast path only.
//
// Claude 2026-09-17: nzb is required so content parks can Forget + wipe staging.
// Reason: passing nil left terminal downloads in the engine queue after onError.
// Review if: UsenetErrorHandler is constructed anywhere besides cmd/sakms.
func UsenetErrorHandler(settingsStore *settings.Store, grabsStore *grabs.Store, nzb *usenet.Manager) func(gid string, failure error) {
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore, NZB: nzb}
	return func(gid string, failure error) {
		handleUsenetError(context.Background(), deps, gid, failure, parkGrabForRetry)
	}
}

// handleUsenetError is UsenetErrorHandler's body, split out so tests can inject
// a failing grabParker without breaking the store.
func handleUsenetError(ctx context.Context, deps AutoGrabDeps, gid string, failure error, park grabParker) {
	if failure == nil {
		return
	}
	g, err := deps.GrabsStore.GetByDownloadGID(ctx, gid)
	if err != nil {
		if !errors.Is(err, grabs.ErrNotFound) {
			log.Printf("usenet error: looking up the grab for gid %s: %v", gid, err)
		}
		return
	}
	if g.Status != grabs.Queued && g.Status != grabs.Downloading {
		return // already imported, failed, or parked — sweep tick is a no-op too
	}

	status, err := applyUsenetFailure(ctx, deps, *g, failure, park)
	if err != nil {
		log.Printf("usenet error: applying failure for grab %d (gid %s): %v", g.ID, gid, err)
		return
	}
	switch status {
	case grabs.PendingRetry:
		if parked, err := deps.GrabsStore.Get(ctx, g.ID); err != nil {
			log.Printf("usenet error: grab %d parked, but re-reading it for the log failed: %v", g.ID, err)
		} else {
			log.Printf("usenet error: grab %d (%s) parked as %s — %s (re-search due %s)",
				g.ID, g.Title, parked.Status, usenetRetrievalReason(failure), parked.RetryAfter)
		}
	case grabs.Failed:
		log.Printf("usenet error: grab %d (%s) failed permanently: %v", g.ID, g.Title, failure)
	}
}
