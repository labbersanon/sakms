package api

import (
	"context"
	"errors"
	"log"

	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/settings"
)

// StaleTorrentHandler returns the downloader Manager's onStale callback: when
// the torrent engine decides a download is dead (zero progress, zero peers, for
// the configured threshold), this parks its grab for a later re-search and then
// cancels the download, deleting the partial files.
//
// Built in cmd/sakms and handed to downloader.Manager.SetOnStale — the
// structural twin of DownloadCompleteImporter (import.go), and for the same
// reason: the Manager may not import grabs/mode/api, so the policy half of
// "a torrent went stale" lives here.
//
// WHY THE CANCEL AND THE REQUEUE ARE SPLIT ACROSS TWO LAYERS. Auto-cancelling a
// definitively-dead download is cleanup and correctly runs ungated. The
// re-grab is where a new download decision gets made, and that stays behind
// usenet_autograb_enabled — which it does for free here, because this handler
// dispatches NOTHING. It only parks a pending_retry row; the existing
// retryDueGrabs sweep (usenetretry.go) picks it up on a later cycle and runs it
// through RunAutoGrab, the single gated scoring-and-dispatch path. That is the
// "new parker" extension pattern, and it is why RunAutoGrab needs no change.
//
// Every failure path is log-only: this runs in the Manager's poll goroutine
// with no HTTP response to write, and a stale download that cannot be requeued
// must never crash the loop.
func StaleTorrentHandler(settingsStore *settings.Store, grabsStore *grabs.Store, dl *downloader.Manager) func(gid string) {
	deps := AutoGrabDeps{SettingsStore: settingsStore, GrabsStore: grabsStore}
	return func(gid string) {
		handleStaleTorrent(context.Background(), deps, dl, gid, parkGrabForRetry)
	}
}

// grabParker is parkGrabForRetry's shape, injected so a test can drive the
// park-failed branch without breaking the store the rest of the handler reads.
// A function type rather than an interface, matching the convention this
// package already documents for sessionBuilderFunc/usenetDownloadLookup
// (usenetretry.go): there is exactly one production implementation.
type grabParker func(ctx context.Context, deps AutoGrabDeps, id int64, reason string) error

// handleStaleTorrent is StaleTorrentHandler's body, split out for the seam above.
//
// ORDER IS LOAD-BEARING: park FIRST, cancel SECOND.
//
// Manager.Cancel deletes the entry from its map, so the poll loop can never
// re-fire for this gid. Cancelling first and then failing to park would leave
// the download gone while the grab row sat at "downloading" with a dead GID,
// and nothing would ever move it — a permanently stuck row that DueForRetry
// cannot see. Parking first inverts the failure mode into a recoverable one: a
// successful park followed by a failed cancel leaves partial files in staging,
// which the operator can see in Requests and clear by hand.
//
// On a park error we therefore return WITHOUT cancelling. The entry keeps its
// staleFired mark so this does not thrash every tick, and the operator retains
// manual control.
func handleStaleTorrent(ctx context.Context, deps AutoGrabDeps, dl *downloader.Manager, gid string, park grabParker) {
	g, err := deps.GrabsStore.GetByDownloadGID(ctx, gid)
	if err != nil {
		if !errors.Is(err, grabs.ErrNotFound) {
			log.Printf("stale torrent: looking up the grab for gid %s: %v", gid, err)
		}
		return // a download SAK didn't initiate, or already gone
	}
	if g.Status != grabs.Queued && g.Status != grabs.Downloading {
		return // already imported, already failed, or already parked
	}

	if err := park(ctx, deps, g.ID, staleTorrentReason); err != nil {
		log.Printf("stale torrent: parking grab %d (gid %s) for re-search: %v — leaving the download in place so it stays cancellable by hand", g.ID, gid, err)
		return
	}

	// Re-read for LOGGING ONLY, so the log line states when the re-search is
	// actually due. Deliberately no branch on the result: since the
	// retry-attempt cap was removed (2026-08-01) SetPendingRetry always lands
	// on pending_retry, so a "gave up" branch here would be dead code. A
	// read-back failure must NOT skip the cancel below — the park already
	// succeeded and the dead torrent has to go regardless.
	if parked, err := deps.GrabsStore.Get(ctx, g.ID); err != nil {
		log.Printf("stale torrent: grab %d parked, but re-reading it for the log failed: %v", g.ID, err)
	} else {
		log.Printf("stale torrent: grab %d (%s) stalled — parked as %s, re-search due %s", g.ID, g.Title, parked.Status, parked.RetryAfter)
	}

	if err := dl.Cancel(gid); err != nil {
		log.Printf("stale torrent: cancelling download %s for grab %d: %v — partial files may remain in staging", gid, g.ID, err)
	}
}
