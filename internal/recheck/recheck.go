// Package recheck is a DELIBERATE EXCEPTION to this project's "manual by
// default, no background pollers" convention (see CLAUDE.md), added at
// explicit user request during the Search/indexer-inversion work (Stage 8
// of the plan). It is intentionally self-contained: to remove it
// entirely, delete this package and its single start-call in cmd/sakms/main.go.
// It shares no state or machinery with any workflow package (Rename/Purge/
// Dedup/Tag) — the watchlist table it reads/writes is its own, not
// repurposed from anything else.
//
// What it does: on a fixed interval (opt-in; 0 = off, and 0 is the default),
// it re-runs the exact same availability probe Discover fires on demand
// (internal/availability's Check* functions) over the set of picks an operator
// has flagged in the availability_watch table, and records each fresh result.
// It is a PULL model, honestly: it only updates a persisted flag the UI badge
// reflects on next load — Stage 8 introduces no push notifier (ntfy/webhook/
// etc.), since none exists anywhere in this codebase and adding one would be
// separately-scoped infrastructure (see the plan's Stage 8 note).
//
// It builds its own minimal client(s) per cycle straight from connStore,
// rather than going through mode.Build, and owns its own bounded HTTP client,
// so it depends on nothing a request handler sets up.
package recheck

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/availability"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// IntervalSettingKey is the settings key holding the recheck cadence, in whole
// seconds. 0/unset/blank/negative all mean "off" — the opt-in gate, and the
// default (see LoadInterval). internal/api mirrors this string in its own
// GET/PUT /api/settings/recheck-interval handler rather than importing this
// package, so this package stays independently deletable.
const IntervalSettingKey = "recheck_interval_seconds"

// outboundTimeout bounds every probe the recheck loop makes — its own copy of
// cmd/sakms/main.go's outboundTimeout, kept local so this package shares no
// machinery with the rest of the program (see the package doc's "self-
// contained" contract).
const outboundTimeout = 15 * time.Second

// LoadInterval reads IntervalSettingKey and returns it as a Duration, or 0
// ("off") for any unset, blank, non-integer, or non-positive value — a
// tolerant read, since a corrupt or missing value must degrade to "off"
// (manual-first) rather than error out the boot path. main passes the result
// straight into Run.
func LoadInterval(ctx context.Context, settingsStore *settings.Store) time.Duration {
	v, err := settingsStore.Get(ctx, IntervalSettingKey)
	if err != nil {
		return 0 // unset (ErrNotFound) or a real store error → treat as off
	}
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// Run drives the background recheck loop until ctx is cancelled. interval is
// the boot-time cadence (from LoadInterval); if it is <= 0 — which is the
// default, since IntervalSettingKey is unset on every install until an operator
// opts in — Run returns immediately and starts NOTHING: no ticker, no
// goroutine activity, zero behavior change. That top-of-function gate is what
// makes it safe for main to call this unconditionally.
//
// When enabled, each tick re-reads the interval from settings so an operator
// can retune or disable it live (a change to 0 stops the loop cleanly);
// re-enabling from 0 needs a restart, same as first enabling it. Every cycle's
// work is delegated to runCycle, which is intentionally a separate, directly-
// callable function so its logic is testable without waiting on a wall clock.
func Run(ctx context.Context, interval time.Duration, connStore *connections.Store, settingsStore *settings.Store, watchStore *WatchStore) {
	if interval <= 0 {
		return // opt-in gate: off by default, honoring "manual first"
	}

	httpClient := &http.Client{Timeout: outboundTimeout}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("recheck: background availability recheck enabled (every %s) — a deliberate opt-in exception to manual-by-default", interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := LoadInterval(ctx, settingsStore)
			if cur <= 0 {
				log.Printf("recheck: interval set to 0 — stopping background recheck (restart to re-enable)")
				return
			}
			if cur != interval {
				interval = cur
				ticker.Reset(cur)
			}
			runCycle(ctx, httpClient, connStore, watchStore, time.Now().Add(-interval))
		}
	}
}

// TriggerOnce runs exactly one recheck pass over every watched pick RIGHT
// NOW — the on-demand counterpart to Run's periodic loop, for an operator-
// triggered "Refresh now" action. since is time.Now(), not "now minus the
// configured interval": ListDue treats anything last checked before the
// given cutoff as due, so passing the current instant means every entry
// qualifies regardless of the interval setting or how recently each was last
// checked — a full forced recheck, not a partial one. Builds its own bounded
// HTTP client, same as Run, so it shares no state with a concurrently
// running Run loop and is safe to call whether or not the background job is
// enabled.
func TriggerOnce(ctx context.Context, connStore *connections.Store, watchStore *WatchStore) {
	httpClient := &http.Client{Timeout: outboundTimeout}
	runCycle(ctx, httpClient, connStore, watchStore, time.Now())
}

// runCycle performs exactly one recheck pass and returns — the single-tick
// logic, extracted from Run's ticker loop (and reused by TriggerOnce) so
// tests exercise it directly rather than sleeping on the ticker. It
// re-probes every watch entry due for a recheck as of the given cutoff
// (never checked, or last checked before since) and records the result.
// Fault isolation matches the rest of the codebase: a listing error or a
// missing Prowlarr connection skips the whole pass (there's nothing to
// probe against), and a single entry's probe failure is logged and skipped
// without aborting the others.
func runCycle(ctx context.Context, httpClient *http.Client, connStore *connections.Store, watchStore *WatchStore, since time.Time) {
	due, err := watchStore.ListDue(ctx, since)
	if err != nil {
		log.Printf("recheck: listing due watch entries: %v", err)
		return
	}
	if len(due) == 0 {
		return
	}

	// One Prowlarr connection backs every mode's probe; without it there is
	// nothing to check against, so skip the whole pass rather than log an
	// identical "not configured" error once per entry.
	prowlarrClient, err := buildProwlarr(ctx, connStore, httpClient)
	if err != nil {
		log.Printf("recheck: loading prowlarr connection: %v", err)
		return
	}
	if prowlarrClient == nil {
		return // prowlarr not configured — nothing to recheck against yet
	}
	// TMDB is needed only by Movies/Series probes; a nil client is tolerated
	// (Adult never touches it, and a Movies/Series entry with no TMDB gets a
	// clear per-entry error from availability.Check*, logged and skipped).
	tmdbClient, err := buildTMDB(ctx, connStore, httpClient)
	if err != nil {
		log.Printf("recheck: loading tmdb connection: %v", err)
		return
	}

	for _, w := range due {
		res, err := checkOne(ctx, w, tmdbClient, prowlarrClient)
		if err != nil {
			log.Printf("recheck: probing watch entry %d (%s): %v", w.ID, w.Mode, err)
			continue
		}
		if err := watchStore.UpdateResult(ctx, w.ID, res.Available, res.CheckedAt); err != nil {
			log.Printf("recheck: recording result for watch entry %d: %v", w.ID, err)
			continue
		}
	}
}

// checkOne dispatches a watch entry to the matching availability probe by mode
// — the same three Check* functions Discover's on-demand availability handler
// calls, reused unchanged so the recheck is byte-for-byte the same question.
func checkOne(ctx context.Context, w Watch, tmdbClient *tmdb.Client, prowlarrClient *prowlarr.Client) (availability.Result, error) {
	switch w.Mode {
	case mode.Movies:
		return availability.CheckMovie(ctx, tmdbClient, prowlarrClient, w.TMDBID)
	case mode.Series:
		return availability.CheckSeries(ctx, tmdbClient, prowlarrClient, w.TMDBID, w.Season, w.Episode)
	case mode.Adult:
		return availability.CheckAdultScene(ctx, prowlarrClient, w.Studio, w.Title)
	default:
		return availability.Result{}, fmt.Errorf("unknown mode %q", w.Mode)
	}
}

// buildProwlarr constructs a Prowlarr client straight from connStore, or
// (nil, nil) if none is configured — the same tolerant, standalone
// construction mode.buildSearchPipeline does, minus the Session. A real store
// error propagates.
func buildProwlarr(ctx context.Context, connStore *connections.Store, httpClient *http.Client) (*prowlarr.Client, error) {
	conn, err := connStore.Get(ctx, "prowlarr")
	if errors.Is(err, connections.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return prowlarr.New(prowlarr.Config{BaseURL: conn.URL, APIKey: conn.APIKey}, httpClient), nil
}

// buildTMDB constructs a TMDB client straight from connStore, or (nil, nil) if
// none is configured — same tolerant construction as buildProwlarr.
func buildTMDB(ctx context.Context, connStore *connections.Store, httpClient *http.Client) (*tmdb.Client, error) {
	conn, err := connStore.Get(ctx, "tmdb")
	if errors.Is(err, connections.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// TMDB is a fixed public service — its base URL is hardcoded, not conn.URL.
	return tmdb.New(tmdb.Config{BaseURL: tmdb.DefaultBaseURL, APIKey: conn.APIKey}, httpClient), nil
}
