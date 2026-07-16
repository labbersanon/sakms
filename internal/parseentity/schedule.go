package parseentity

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
	"github.com/curtiswtaylorjr/sakms/internal/stashapi"
	"github.com/curtiswtaylorjr/sakms/internal/stashbox"
	"github.com/curtiswtaylorjr/sakms/internal/tpdbrest"
)

// IntervalSettingKey is the settings key holding ONE shared background sync
// cadence for all four entity sources (Stash/TPDB/StashDB/FansDB) combined —
// a single interval, not four independent ones — in whole seconds.
// 0/unset/blank/negative all mean "off", and off is the default: entity sync
// was purely manual (per-source "Sync now" buttons only, see sync.go) before
// this job existed, so an unset key must NOT start anything automatically for
// an existing install. This mirrors recheck.IntervalSettingKey's off-by-default
// convention, deliberately NOT internal/adultnewest's default-active-24h one —
// that default was a separately-decided, explicit exception for a different
// feature; entity sync has no such standing decision.
const IntervalSettingKey = "entity_sync_interval_seconds"

// outboundTimeout bounds every request this job's own HTTP client makes —
// its own copy of the same constant every other background job in this
// codebase (recheck, adultnewest) keeps locally, so this stays a
// self-contained, independently-removable feature.
const outboundTimeout = 15 * time.Second

// LoadInterval reads IntervalSettingKey and returns it as a Duration, or 0
// ("off") for any unset, blank, non-integer, or non-positive value — a
// tolerant read, since a corrupt or missing value must degrade to "off"
// rather than error out the boot path. Mirrors recheck.LoadInterval exactly.
func LoadInterval(ctx context.Context, settingsStore *settings.Store) time.Duration {
	v, err := settingsStore.Get(ctx, IntervalSettingKey)
	if err != nil {
		return 0
	}
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// Run drives the background entity-sync loop until ctx is cancelled, syncing
// all four sources on the one shared cadence. interval <= 0 — the default,
// since IntervalSettingKey is unset on every install until an operator opts
// in — starts NOTHING: no ticker, no goroutine activity. That top-of-function
// gate is what makes it safe for main to call this unconditionally, same as
// recheck.Run.
//
// This runs ALONGSIDE the existing manual per-source "Sync now" buttons
// (triggerEntitySyncHandler) — it is additive, not a replacement for them.
func Run(ctx context.Context, interval time.Duration, connStore *connections.Store, settingsStore *settings.Store, store EntityStore) {
	if interval <= 0 {
		return // opt-in gate: off by default, honoring "manual first"
	}

	httpClient := &http.Client{Timeout: outboundTimeout}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("parseentity: background entity sync enabled (every %s) — a deliberate opt-in exception to manual-by-default", interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := LoadInterval(ctx, settingsStore)
			if cur <= 0 {
				log.Printf("parseentity: interval set to 0 — stopping background entity sync (restart to re-enable)")
				return
			}
			if cur != interval {
				interval = cur
				ticker.Reset(cur)
			}
			runCycle(ctx, httpClient, connStore, store)
		}
	}
}

// runCycle syncs each of the four sources once, in sequence — not
// concurrent, matching this project's existing recheck/adultnewest
// single-sequential-loop convention. A source whose connection isn't
// configured is skipped, not fatal (logged and the cycle continues with the
// rest); a source's own sync error is likewise logged and skipped rather than
// aborting its siblings — same fault-isolation convention as
// recheck.runCycle. Client construction mirrors triggerEntitySyncHandler's
// per-source branches exactly (same fixed public endpoints for TPDB/
// StashDB/FansDB, same stash connection shape).
func runCycle(ctx context.Context, httpClient *http.Client, connStore *connections.Store, store EntityStore) {
	if conn, err := connStore.Get(ctx, "stash"); err == nil {
		client := stashapi.New(stashapi.Config{URL: conn.URL, APIKey: conn.APIKey}, httpClient)
		if err := SyncFromStash(ctx, store, client); err != nil {
			log.Printf("parseentity: background sync (stash): %v", err)
		}
	} else if !errors.Is(err, connections.ErrNotFound) {
		log.Printf("parseentity: loading stash connection: %v", err)
	}

	if conn, err := connStore.Get(ctx, "tpdb"); err == nil {
		client := tpdbrest.New(tpdbrest.DefaultBaseURL, conn.APIKey, httpClient)
		if err := SyncFromTPDB(ctx, store, client, DefaultSyncPages); err != nil {
			log.Printf("parseentity: background sync (tpdb): %v", err)
		}
	} else if !errors.Is(err, connections.ErrNotFound) {
		log.Printf("parseentity: loading tpdb connection: %v", err)
	}

	for _, source := range []string{"stashdb", "fansdb"} {
		conn, err := connStore.Get(ctx, source)
		if err != nil {
			if !errors.Is(err, connections.ErrNotFound) {
				log.Printf("parseentity: loading %s connection: %v", source, err)
			}
			continue
		}
		endpoint, _ := stashbox.URLForBox(source)
		client := stashbox.New(stashbox.Config{
			Endpoint: endpoint, APIKey: conn.APIKey, IsBearer: false, HasVoteField: true,
		}, httpClient)
		if err := SyncFromStashBox(ctx, store, client, source, DefaultSyncPages); err != nil {
			log.Printf("parseentity: background sync (%s): %v", source, err)
		}
	}
}
