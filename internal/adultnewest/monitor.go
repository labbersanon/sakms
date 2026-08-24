// This file is the Adult monitored-entity detection cycle — a Prowlarr-search-
// based pass that runs for each monitored performer/studio, processes new
// releases into the pool cache, and records poll results. The auto-grab dispatch
// that follows is in internal/api/adultmonitor.go (the 5th pass of
// runUsenetRetryCycle), which reads the pool for scenes added since
// monitored_since.
//
// Wired as the THIRD ticker case in scan.go's Run, sharing the dependencies the
// browse and feed passes already take — no new goroutine, no independent
// scheduler. Independently deletable: remove this file, the monitorC ticker case
// in scan.go's Run, and the MonitoredStore parameter from Run's signature (and
// its construction site in cmd/sakms/main.go).
//
// DO NOT import internal/api from this file.
package adultnewest

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/parseentity"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// MonitorIntervalSettingKey is the settings key holding the monitor poll cadence,
// in whole seconds. Mirrors IntervalSettingKey's import-avoidance convention — the
// API handler mirrors this value rather than importing this package. See
// LoadMonitorInterval for how unset/0 are interpreted.
const MonitorIntervalSettingKey = "adult_monitor_interval_seconds"

// defaultMonitorIntervalSeconds is the monitor poll cadence when the key has
// never been set — 24 hours.
const defaultMonitorIntervalSeconds = 24 * 60 * 60

// maxMonitoredPerCycle caps how many monitored entities are polled in one pass.
// Each entity costs one Prowlarr search call (sequential, never concurrent).
const maxMonitoredPerCycle = 20

// LoadMonitorInterval reads MonitorIntervalSettingKey from the settings store
// and returns the cadence as a Duration. Returns 0 (off) on error or when
// the key is explicitly set to 0; returns the 24h default when the key was
// never set. Mirrors LoadInterval (browse) and LoadFeedInterval exactly.
func LoadMonitorInterval(ctx context.Context, settingsStore *settings.Store) time.Duration {
	// Claude 2026-08-24: a non-integer stored value degrades to off, not to the
	// default, because that shape only happens via direct DB tampering — never
	// through the Settings UI. Same choice as LoadInterval.
	v, err := settingsStore.Get(ctx, MonitorIntervalSettingKey)
	if errors.Is(err, settings.ErrNotFound) {
		return time.Duration(defaultMonitorIntervalSeconds) * time.Second
	}
	if err != nil {
		return 0
	}
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// runMonitorCycle performs one pass: queries each due monitored entity against
// Prowlarr, processes new releases into the pool cache, and records poll results.
// Mirrors runCycle's defensive style (skip on config error, log and continue on
// per-entity error).
func runMonitorCycle(ctx context.Context, httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, releaseStore *ReleaseStore, monitoredStore *MonitoredStore, entityStore parseentity.EntityStore) {
	if monitoredStore == nil {
		return
	}

	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Adult)
	if err != nil {
		log.Printf("adultnewest monitor: building session: %T — skipping this cycle", rootCauseOf(err))
		return
	}
	if sess.Prowlarr == nil {
		log.Printf("adultnewest monitor: Prowlarr isn't configured — skipping this cycle")
		return
	}
	if sess.Identify != nil {
		sess.Identify.EntityStore = entityStore
	}

	now := time.Now()
	due, err := monitoredStore.ListDue(ctx, now, maxMonitoredPerCycle)
	if err != nil {
		log.Printf("adultnewest monitor: listing due entities: %v", err)
		return
	}
	if len(due) == 0 {
		return
	}

	log.Printf("adultnewest monitor: polling %d due entity/entities", len(due))

	for _, entity := range due {
		found := runEntityPoll(ctx, sess, releaseStore, monitoredStore, entity, now)
		log.Printf("adultnewest monitor: polled kind=%q name=%q — %d new release(s)", entity.Kind, entity.EntityName, found)
	}
}

// runEntityPoll runs one Prowlarr search for a single monitored entity, processes
// new (unseen) releases into the pool, records the poll, and returns how many new
// releases were processed. Never returns an error — errors are logged and
// contribute 0 to the found count.
func runEntityPoll(ctx context.Context, sess *mode.Session, releaseStore *ReleaseStore, monitoredStore *MonitoredStore, entity MonitoredEntity, now time.Time) (found int) {
	defer func() {
		if err := monitoredStore.RecordPoll(ctx, entity.ID, found, now); err != nil {
			log.Printf("adultnewest monitor: recording poll for entity %d (%s): %v", entity.ID, entity.EntityName, err)
		}
	}()

	query := normalizeAdultQuery(entity.EntityName)
	releases, err := sess.Prowlarr.Search(ctx, query, []int{adultCategory})
	if err != nil {
		log.Printf("adultnewest monitor: searching prowlarr for %q: %v", entity.EntityName, err)
		return 0
	}
	if len(releases) == 0 {
		return 0
	}

	guids := make([]string, len(releases))
	for i, r := range releases {
		guids[i] = r.GUID
	}
	seen, err := releaseStore.SeenGUIDs(ctx, guids)
	if err != nil {
		log.Printf("adultnewest monitor: checking seen guids for %q: %v", entity.EntityName, err)
		return 0
	}

	for _, r := range releases {
		if seen[r.GUID] || r.GUID == "" {
			continue
		}
		if sess.Identify != nil {
			if err := processRelease(ctx, sess.Identify, sess.Prowlarr, releaseStore, r); err != nil {
				log.Printf("adultnewest monitor: processing release %q for entity %q: %v", r.Title, entity.EntityName, err)
			}
		}
		// Mark seen regardless of match outcome — same convention as runCycle.
		if err := releaseStore.MarkSeen(ctx, r.GUID); err != nil {
			log.Printf("adultnewest monitor: marking release %q seen: %v", r.Title, err)
		}
		found++
	}
	return found
}

// rootCauseOf unwraps to the innermost error for type-only logging (avoids
// printing credentialed URLs from *url.Error). Local copy to keep monitor.go
// independently deletable without depending on scan.go's rootCause.
func rootCauseOf(err error) error {
	type unwrapper interface{ Unwrap() error }
	for {
		uw, ok := err.(unwrapper)
		if !ok {
			return err
		}
		next := uw.Unwrap()
		if next == nil {
			return err
		}
		err = next
	}
}
