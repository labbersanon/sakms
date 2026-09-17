package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/usenet"
)

// transportRetryReason is the operator-facing retry_reason for a transport park.
// Detail-free by the same convention as retrySearchFailedReason — it is rendered
// on Requests and must never carry a host, username, or URL fragment.
const transportRetryReason = "the connection to the usenet server dropped — resuming shortly"

// parkUsenetTransportFailure decides whether a retrieval failure should be
// short-parked for a transport resume rather than falling through to today's
// ParkWithBackoff (multi-day re-search ladder). Returns (true, nil) when the
// transport park was written; returns (false, nil) when the failure does not
// qualify and the caller should continue with its normal park logic.
//
// Fail-closed: returns false (caller falls through) unless ALL four hold:
//  1. errors.Is(failure, usenet.ErrTransport)
//  2. g.DownloadGID has the "nzb-" prefix (a dispatched usenet grab)
//  3. g.DownloadURL is non-empty (needed for RelaunchNZB)
//  4. g.TransportRetryCount < grabs.MaxTransportRetries
//
// Claude 2026-09-17: transport park keeps download_gid (see plan §4.1).
// Reason: RelaunchNZB resumes into the SAME staging dir named by the GID; a
//   park that cleared the GID would orphan the sidecar on the 7-day timer.
// Review if: a resume_gid column is added (currently unnecessary; existing
//   column semantics already cover the requirement).
func parkUsenetTransportFailure(ctx context.Context, deps AutoGrabDeps, g grabs.Grab, failure error, now time.Time) (bool, error) {
	if !errors.Is(failure, usenet.ErrTransport) {
		return false, nil
	}
	if !strings.HasPrefix(g.DownloadGID, usenetGIDPrefix) {
		return false, nil
	}
	if strings.TrimSpace(g.DownloadURL) == "" {
		return false, nil
	}
	if g.TransportRetryCount >= grabs.MaxTransportRetries {
		return false, nil
	}
	after := now.Add(grabs.TransportBackoff(g.TransportRetryCount + 1))
	if err := deps.GrabsStore.ParkForTransportResume(ctx, g.ID, after, transportRetryReason); err != nil {
		return false, fmt.Errorf("transport park grab %d: %w", g.ID, err)
	}
	log.Printf("usenet transport: grab %d (%s) parked for resume in %s (attempt %d/%d)",
		g.ID, g.Title, grabs.TransportBackoff(g.TransportRetryCount+1), g.TransportRetryCount+1, grabs.MaxTransportRetries)
	return true, nil
}

// usenetResumeEngine is the minimal interface resumeDueTransportRetries needs
// from the usenet.Manager. *usenet.Manager satisfies it.
//
// A narrow interface (not the full *usenet.Manager) is what makes the resume
// pass unit-testable without driving a real NNTP server.
type usenetResumeEngine interface {
	FindByGID(gid string) (*usenet.Download, error)
	Forget(gid string) bool
	RelaunchNZB(ctx context.Context, gid, url, name string) error
}

// resumeDueTransportRetries picks up every transport-parked grab whose
// retry_after has arrived and attempts to resume the NZB download via
// RelaunchNZB, which re-fetches the NZB at the stored URL and resumes into
// the existing staging dir (skipping segments already recorded in the sidecar).
//
// Per due row:
//  1. Skip excluded titles.
//  2. Gate on free Usenet slots — return immediately when full (honouring the
//     slot budget; escalating to torrent here would be D, out of scope).
//  3. Inspect the live engine state via FindByGID:
//     - terminal (error/complete/removed) → Forget(gid) so RelaunchNZB can accept it.
//     - non-terminal → the engine is already running it; re-arm the row via
//       Relaunch (marks queued, clears retry fields) and continue.
//  4. RelaunchNZB outcomes:
//     - nil → success; Relaunch the row (queued, retry fields cleared).
//     - ErrArticlesUnavailable → articles gone; clear GID, join days-ladder re-search.
//     - other error → advance the ladder; escalate to days-ladder at the cap.
func resumeDueTransportRetries(ctx context.Context, deps AutoGrabDeps, engine usenetResumeEngine, excluded map[string]bool, now time.Time) {
	if engine == nil {
		return
	}
	due, err := deps.GrabsStore.DueForResume(ctx, now)
	if err != nil {
		log.Printf("usenet transport: listing due transport-resume grabs: %v", err)
		return
	}
	for _, g := range due {
		if excluded[excludes.Key(string(g.Mode), g.TMDBID, g.Title)] {
			log.Printf("usenet transport: skipping grab %d (%s) — excluded from worklist", g.ID, g.Title)
			continue
		}

		// Gate: free Usenet slots.
		free, slotErr := freeUsenetSlots(ctx, deps.GrabsStore, deps.SettingsStore)
		if slotErr != nil {
			log.Printf("usenet transport: counting in-flight slots: %v", slotErr)
			return
		}
		if free <= 0 {
			return // wait for next tick; do not escalate to torrent (D is out of scope)
		}

		// Check live engine state.
		live, findErr := engine.FindByGID(g.DownloadGID)
		if findErr != nil {
			log.Printf("usenet transport: FindByGID %q for grab %d: %v", g.DownloadGID, g.ID, findErr)
			continue
		}
		if live != nil {
			switch live.Status {
			case "error", "complete", "removed":
				engine.Forget(g.DownloadGID)
				// Fall through to RelaunchNZB below.
			default:
				// Active or paused — the engine is already running it. Re-arm the
				// row to queued so it stops being due-for-resume.
				d := grabs.Dispatch{
					Indexer: g.Indexer, Protocol: g.Protocol,
					DownloadClient: g.DownloadClient, RootFolderPath: g.RootFolderPath,
					DownloadURL: g.DownloadURL, GID: g.DownloadGID,
				}
				if err := deps.GrabsStore.Relaunch(ctx, g.ID, d); err != nil {
					log.Printf("usenet transport: re-arming grab %d (engine active): %v", g.ID, err)
				}
				continue
			}
		}

		// Attempt the resume.
		if err := engine.RelaunchNZB(ctx, g.DownloadGID, g.DownloadURL, g.Title); err != nil {
			if errors.Is(err, usenet.ErrArticlesUnavailable) {
				// Articles are gone; clear the GID and join the days-ladder re-search.
				log.Printf("usenet transport: grab %d (%s) articles unavailable on resume — escalating to re-search", g.ID, g.Title)
				if parkErr := parkGrabForRetry(ctx, deps, g.ID, articlesUnavailableReason); parkErr != nil {
					log.Printf("usenet transport: parking grab %d after unavailable resume: %v", g.ID, parkErr)
				}
				continue
			}
			// Other error. Try the next rung; fall to days-ladder at the cap.
			nextRung := grabs.TransportBackoff(g.TransportRetryCount + 1)
			if nextRung > 0 {
				log.Printf("usenet transport: grab %d (%s) resume error — next rung in %s: %v", g.ID, g.Title, nextRung, err)
				if parkErr := deps.GrabsStore.ParkForTransportResume(ctx, g.ID, now.Add(nextRung), transportRetryReason); parkErr != nil {
					log.Printf("usenet transport: re-parking grab %d: %v", g.ID, parkErr)
				}
			} else {
				log.Printf("usenet transport: grab %d (%s) transport ladder exhausted — escalating to re-search: %v", g.ID, g.Title, err)
				if parkErr := parkGrabForRetry(ctx, deps, g.ID, retrievalFailedReason); parkErr != nil {
					log.Printf("usenet transport: parking grab %d after ladder exhaustion: %v", g.ID, parkErr)
				}
			}
			continue
		}

		// RelaunchNZB succeeded — re-arm the row.
		d := grabs.Dispatch{
			Indexer: g.Indexer, Protocol: g.Protocol,
			DownloadClient: g.DownloadClient, RootFolderPath: g.RootFolderPath,
			DownloadURL: g.DownloadURL, GID: g.DownloadGID,
		}
		if err := deps.GrabsStore.Relaunch(ctx, g.ID, d); err != nil {
			log.Printf("usenet transport: Relaunch grab %d after resume: %v", g.ID, err)
			continue
		}
		log.Printf("usenet transport: grab %d (%s) resumed into %s", g.ID, g.Title, g.DownloadGID)
	}
}
