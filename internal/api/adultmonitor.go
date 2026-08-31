// This file is the Adult monitored-entity DISPATCH pass — the FIFTH pass inside
// runUsenetRetryCycle. It reads the adult_newest_releases pool for scenes linked
// to monitored entities whose first_seen_at > monitored_since, and dispatches
// auto-grabs for new matches via RunAutoGrab. The detection/poll cycle that
// fills the pool lives in internal/adultnewest/monitor.go.
//
// NO GOROUTINE, NO TICKER, NO INTERVAL KEY lives in this file. It is a plain
// function called as the FIFTH step of runUsenetRetryCycle, exactly as
// monitorAirDates is the third and releaseDueGrabs is the fourth. The static
// AST test (adultmonitor_static_test.go) proves this.
//
// It lives in internal/api rather than its own package for the same reason all
// the other passes do: everything it drives is package-private here
// (RunAutoGrab's deps, the trigger constants). It stays independently deletable:
// delete this file, its one call in runUsenetRetryCycle, the cancelMonitorRetries
// call from adult_monitor.go's PUT handler, and the monitoredStore parameter
// threading from runUsenetRetryCycle / RunUsenetRetry / cmd/sakms/main.go.
package api

import (
	"context"
	"log"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// TriggerAdultMonitor is the trigger for Adult monitor-originated grabs.
// It uses the same gated path as TriggerAirDate (never an explicit gate tweak)
// and is recorded as the Trigger in RunAutoGrab for logging/observability.
const TriggerAdultMonitor AutoGrabTrigger = "adultmonitor"

// maxMonitorGrabsPerCycle is the unconfigured bound on how many new dispatch
// attempts the monitor pass may start per cycle; the operator's Usenet cycle
// slots (loadUsenetCycleSlots) are what it actually runs at.
const maxMonitorGrabsPerCycle = defaultAutoGrabSlotsPerCycle

// monitorUnmonitoredReason is the retry_reason written to grabs the un-monitor
// PUT cancels. Operator-facing copy only — nothing branches on it.
const monitorUnmonitoredReason = "the entity was un-monitored, so this monitor-originated retry was cancelled"

// monitorAdultEntities is the FIFTH pass of runUsenetRetryCycle. For each
// currently-monitored entity, it reads the pool for scenes whose first_seen_at
// > monitored_since and dispatches auto-grabs for any not already being grabbed.
//
// It mirrors dispatchAirDateGrabs's structure: a session build, an active-grab
// prefilter, sequential dispatch with a per-cycle cap, and the single RunAutoGrab
// path for every attempt.
func monitorAdultEntities(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc, libStore *library.Store, monitoredStore *adultnewest.MonitoredStore, releaseStore *adultnewest.ReleaseStore, excluded map[string]bool, now time.Time) {
	if monitoredStore == nil || releaseStore == nil {
		return
	}

	monitored, err := monitoredStore.ListMonitored(ctx)
	if err != nil {
		log.Printf("adult monitor dispatch: listing monitored entities: %v", err)
		return
	}
	if len(monitored) == 0 {
		return
	}

	sess, err := build(ctx, mode.Adult)
	if err != nil {
		log.Printf("adult monitor dispatch: building the adult session failed (%T) — skipping this cycle", rootCause(err))
		return
	}
	if sess.Prowlarr == nil {
		log.Printf("adult monitor dispatch: Prowlarr isn't configured — skipping this cycle")
		return
	}

	// Active-grab prefilter: skip scenes that are already being downloaded or
	// pending_retry, keyed by title (same approach as activeSeriesGrabKeys).
	activeGrabTitles := activeAdultMonitorGrabKeys(ctx, deps)

	attempts := 0
	cycleCap := loadUsenetCycleSlots(ctx, deps.SettingsStore)
	for _, entity := range monitored {
		if attempts >= cycleCap {
			break
		}
		if entity.MonitoredSince == "" {
			continue
		}

		scenes, err := releaseStore.ScenesLinkedToEntity(ctx, adultRowType(entity.Kind), entity.EntityName)
		if err != nil {
			log.Printf("adult monitor dispatch: listing scenes for %s %q: %v", entity.Kind, entity.EntityName, err)
			continue
		}

		for _, scene := range scenes {
			if attempts >= cycleCap {
				break
			}
			// Watermark: only grab scenes added after monitoring was enabled.
			if scene.FirstSeenAt <= entity.MonitoredSince {
				continue
			}
			if scene.RowType != adultnewest.RowScene && scene.RowType != adultnewest.RowMovie {
				continue
			}

			title := scene.EntityTitle
			if excluded[excludes.Key(string(mode.Adult), 0, title)] {
				continue
			}
			if activeGrabTitles[title] {
				continue
			}
			// Already on disk — never re-grab. libStore may be nil in older tests.
			if libStore != nil && scene.EntitySource != "" && scene.EntityID != "" {
				if _, err := libStore.GetScene(ctx, scene.EntitySource, scene.EntityID); err == nil {
					continue
				}
			}

			attempts++
			monKey := adultnewest.FormatMonitorKey(entity.Kind, entity.EntitySource, entity.EntityID)

			// Claude 2026-08-24: thread the monitored entity into Studio/Performers.
			// Reason: adultIdentityWeak parks every Adult retry when both are empty;
			//   a monitored entity is the strongest identity signal available.
			// Troubleshooting: monitor hits park forever as pending_retry with no dispatch.
			// Review if: grabs table gains studio/performers columns and retry can reload them.
			studio := scene.EntityStudio
			var performers []string
			if entity.Kind == "studio" {
				studio = entity.EntityName
			} else {
				performers = []string{entity.EntityName}
			}

			out, err := RunAutoGrab(ctx, deps, sess, AutoGrabRequest{
				Mode:            mode.Adult,
				Title:           title,
				Studio:          studio,
				Performers:      performers,
				ReleaseTitle:    scene.FirstSeenReleaseTitle,
				DurationSeconds: scene.EntityDurationSeconds,
				Box:             scene.EntitySource,
				SceneID:         scene.EntityID,
				Trigger:         TriggerAdultMonitor,
			})
			switch {
			case err != nil:
				log.Printf("adult monitor dispatch: %q — auto-grab failed (%T)", title, rootCause(err))
			case out.Gated:
				log.Printf("adult monitor dispatch: usenet auto-grab is switched off — abandoning this cycle")
				return
			case out.AlreadyGrabbing:
				log.Printf("adult monitor dispatch: %q is already being downloaded", title)
			case out.Grabbed:
				log.Printf("adult monitor dispatch: %q dispatched", title)
				// Tag the newly-created grab with its monitor origin.
				if out.GrabID != 0 {
					if err := deps.GrabsStore.SetMonitorEntityKey(ctx, out.GrabID, monKey); err != nil {
						log.Printf("adult monitor dispatch: tagging grab %d with monitor key: %v", out.GrabID, err)
					}
				}
			default:
				// NoMatch: parkPendingRetry created a pending_retry row.
				// We need to tag it with the monitor key so cancelMonitorRetries can find it.
				if out.GrabID != 0 {
					if err := deps.GrabsStore.SetMonitorEntityKey(ctx, out.GrabID, monKey); err != nil {
						log.Printf("adult monitor dispatch: tagging parked grab %d with monitor key: %v", out.GrabID, err)
					}
				}
				log.Printf("adult monitor dispatch: %q has no qualifying candidate yet — parked for re-search", title)
			}
		}
	}
}

// activeAdultMonitorGrabKeys returns a set of grab titles that are currently
// active (queued, downloading, or pending_retry) for mode.Adult. Used as a
// prefilter so the dispatch pass never starts a second grab for something
// already in flight. Title-keyed (not GID-keyed) because monitor-originated
// grabs and existing operator grabs may share the same title.
func activeAdultMonitorGrabKeys(ctx context.Context, deps AutoGrabDeps) map[string]bool {
	list, err := deps.GrabsStore.List(ctx, mode.Adult)
	if err != nil {
		log.Printf("adult monitor dispatch: listing adult grabs for prefilter: %v", err)
		return nil
	}
	active := make(map[string]bool, len(list))
	for _, g := range list {
		if g.Status == grabs.Queued || g.Status == grabs.Downloading || g.Status == grabs.PendingRetry {
			active[g.Title] = true
		}
	}
	return active
}

// monitorOriginated reports whether a pending_retry grab was created by the
// Adult monitor dispatch pass (the NARROW predicate — used only for the
// un-monitor cleanup, mirroring airDateOriginated).
//
// MonitorEntityKey non-empty + mode Adult + Indexer/DownloadURL empty (never
// dispatched) is the discriminator. Do NOT broaden to "just MonitorEntityKey
// non-empty" because a dispatched monitor grab (Indexer/DownloadURL set) that
// later gets parked by the retry sweep should NOT be cancelled by the un-monitor
// toggle — it was actually dispatched and the download may be in flight.
func monitorOriginated(g grabs.Grab) bool {
	return g.Mode == mode.Adult &&
		g.MonitorEntityKey != "" &&
		g.Status == grabs.PendingRetry &&
		g.Indexer == "" &&
		g.DownloadURL == ""
}

// cancelMonitorRetries terminates every NEVER-DISPATCHED monitor-originated
// pending_retry row for the given monitor entity key. Called from the PUT
// un-monitor handler when monitored=false.
//
// Mirrors cancelAirDateRetriesForSeasons's pattern: narrow predicate, reason
// write first, then status flip, log on error and continue.
func cancelMonitorRetries(ctx context.Context, grabsStore *grabs.Store, key string) {
	if key == "" {
		return
	}
	list, err := grabsStore.List(ctx, mode.Adult)
	if err != nil {
		log.Printf("adult monitor: listing adult grabs for un-monitor cleanup: %v", err)
		return
	}
	now := time.Now()
	for _, g := range list {
		if g.MonitorEntityKey != key || !monitorOriginated(g) {
			continue
		}
		if err := grabsStore.SetRetryAfter(ctx, g.ID, now, monitorUnmonitoredReason); err != nil {
			log.Printf("adult monitor: recording un-monitor reason on grab %d: %v", g.ID, err)
			continue
		}
		if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Failed); err != nil {
			log.Printf("adult monitor: cancelling grab %d after entity was un-monitored: %v", g.ID, err)
		}
	}
}
