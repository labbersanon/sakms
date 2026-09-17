package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
)

// UsenetResumeEngineForBoot is an alias for usenetResumeEngine exported so that
// cmd/sakms/main.go can pass *usenet.Manager to RunBootParkHygiene without
// importing the internal usenetResumeEngine type directly.
type UsenetResumeEngineForBoot = usenetResumeEngine

// RunBootParkHygiene is the exported entry point that cmd/sakms/main.go calls
// once at boot, next to ReconcileInFlightDownloads. It runs runParkHygiene with
// the current time and logs on each stranded/malformed row it repairs.
func RunBootParkHygiene(ctx context.Context, deps AutoGrabDeps, engine UsenetResumeEngineForBoot) {
	runParkHygiene(ctx, deps, engine, time.Now())
}

// testParkReapedReason is the retry_reason written when a hygiene reap flips
// a row to Failed. Operator-visible on the Requests screen; kept distinct from
// retrievalFailedReason so an operator can see "this was reaped as test debris".
const testParkReapedReason = "reaped as test/E2E park debris"

// allowedOrigins is the set the hygiene handler's tag action accepts.
var allowedOrigins = map[string]bool{"": true, "e2e": true}

// runParkHygiene is the automatic (non-destructive) maintenance pass that runs
// at the end of runUsenetRetryCycle and once at boot from cmd/sakms/main.go.
//
// It performs three repairs, all non-destructive:
//
//  1. Stranded transport-resume recovery: pending_retry + download_gid ≠ '' +
//     parseable retry_after + now - retry_after > strandedResumeGrace →
//     parkGrabForRetry (clears GID, joins days-ladder re-search). Log each row.
//
//  2. Malformed schedule repair: pending_retry + retry_after ≠ '' + unparseable
//     → SetRetryAfter at RetryBackoff(retryCount+1) from now. Log each row.
//     (4 live rows today with "HH24" corruption from an external writer.)
//
//  3. Census log line (always, even when nothing was repaired).
//
// It never reaps, never deletes, and never touches a row whose status is not
// pending_retry.
//
// Claude 2026-09-17: stranded-recovery ensures a transport-parked row can never
// be invisible-forever when auto-grab drain is off (drain never calls DueForResume).
// Review if: hygiene gains its own shorter interval separate from the retry cycle.
func runParkHygiene(ctx context.Context, deps AutoGrabDeps, engine usenetResumeEngine, now time.Time) {
	for _, m := range []mode.Mode{mode.Movies, mode.Series, mode.Adult} {
		list, err := deps.GrabsStore.List(ctx, m)
		if err != nil {
			log.Printf("park hygiene: listing %s grabs: %v", m, err)
			continue
		}
		for _, g := range list {
			if g.Status != grabs.PendingRetry {
				continue
			}

			// Rule 1: stranded transport-resume recovery.
			if g.DownloadGID != "" && g.RetryAfter != "" {
				retryT, parseErr := grabs.ParseTime(g.RetryAfter)
				if parseErr == nil && now.Sub(retryT) > strandedResumeGrace {
					log.Printf("park hygiene: grab %d (%s) stranded transport-resume (overdue by %s) — clearing GID, joining re-search",
						g.ID, g.Title, formatDuration(now.Sub(retryT)-strandedResumeGrace))
					if err := parkGrabForRetry(ctx, deps, g.ID, retrievalFailedReason); err != nil {
						log.Printf("park hygiene: stranded recovery for grab %d: %v", g.ID, err)
					}
					continue
				}
			}

			// Rule 2: malformed schedule repair (only rows without a GID — transport
			// parks are handled by rule 1 above, and we must not disturb their GID).
			if g.DownloadGID == "" && g.RetryAfter != "" {
				if _, parseErr := grabs.ParseTime(g.RetryAfter); parseErr != nil {
					after := now.Add(grabs.RetryBackoff(g.RetryCount + 1))
					log.Printf("park hygiene: grab %d (%s) malformed retry_after %q — rescheduling to %s",
						g.ID, g.Title, g.RetryAfter, grabs.FormatTime(after))
					if err := deps.GrabsStore.SetRetryAfter(ctx, g.ID, after, g.RetryReason); err != nil {
						log.Printf("park hygiene: repairing grab %d: %v", g.ID, err)
					}
				}
			}
		}
	}
	// Rule 3: census log line.
	census := computeParkCensus(ctx, deps.GrabsStore, now)
	logParkCensus("hygiene pass", census)
}

// parkHygieneRequest is the body for POST /api/requests/park-hygiene.
type parkHygieneRequest struct {
	Action        string  `json:"action"`        // "tag" or "reap"
	Origin        string  `json:"origin"`        // required for tag/reap; allowlist {"","e2e"}
	IDs           []int64 `json:"ids"`           // explicit id filter (optional)
	ReasonContains string  `json:"reasonContains"` // dry-run-only filter
	Apply         bool    `json:"apply"`         // false = dry run (default)
}

// parkHygieneResponse is the body for the 200 response.
type parkHygieneResponse struct {
	Matched int     `json:"matched"`
	Applied bool    `json:"applied"`
	IDs     []int64 `json:"ids"`
}

// parkHygieneHandler backs POST /api/requests/park-hygiene.
// Classified {queue} for free by sectionlock (route prefix /api/requests/).
//
// Rules:
//   - apply defaults false → dry run: match, log, return ids, mutate nothing.
//   - Only status == pending_retry rows are ever matched.
//   - action:"tag" sets origin (allowlist {"", "e2e"}; else → 400).
//   - action:"reap" requires a non-empty origin OR an explicit ids list.
//     reasonContains alone is dry-run-only; it cannot drive a reap.
//   - Reap = SetRetryAfter(testParkReapedReason) + UpdateStatus(Failed).
//     No DELETE FROM grabs — the codebase contains none.
//
// Claude 2026-09-17: reap writes Failed + reason so the row is terminal,
// invisible to DueForRetry, and auditable. Staging ages out via stagingsweep.
// Review if: automatic reaping of tagged rows is added in v2.
func parkHygieneHandler(grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req parkHygieneRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Action != "tag" && req.Action != "reap" {
			http.Error(w, `action must be "tag" or "reap"`, http.StatusBadRequest)
			return
		}
		if !allowedOrigins[req.Origin] {
			http.Error(w, fmt.Sprintf(`origin must be "" or "e2e", got %q`, req.Origin), http.StatusBadRequest)
			return
		}
		// reap requires origin or explicit ids.
		if req.Action == "reap" && req.Origin == "" && len(req.IDs) == 0 {
			http.Error(w, `reap requires a non-empty origin or an explicit ids list (reasonContains alone is dry-run-only)`, http.StatusBadRequest)
			return
		}

		ctx := r.Context()
		now := time.Now()

		// Build explicit id set for fast membership test.
		explicitIDs := make(map[int64]bool, len(req.IDs))
		for _, id := range req.IDs {
			explicitIDs[id] = true
		}

		var matched []int64
		for _, m := range []mode.Mode{mode.Movies, mode.Series, mode.Adult} {
			list, err := grabsStore.List(ctx, m)
			if err != nil {
				http.Error(w, fmt.Sprintf("listing %s grabs: %v", m, err), http.StatusInternalServerError)
				return
			}
			for _, g := range list {
				if g.Status != grabs.PendingRetry {
					continue
				}
				// ID filter: if explicit list provided, must be in it.
				if len(explicitIDs) > 0 && !explicitIDs[g.ID] {
					continue
				}
				// reasonContains filter: dry-run only (cannot drive reap alone).
				if req.ReasonContains != "" {
					if !containsIgnoreCase(g.RetryReason, req.ReasonContains) {
						continue
					}
				}
				// Origin filter: for tag (target = what to set) and reap (target = what to reap).
				// For tag: any origin value is a valid target.
				// For reap: if origin filter set, row must match it.
				if req.Action == "reap" && req.Origin != "" && g.Origin != req.Origin {
					continue
				}
				matched = append(matched, g.ID)

				if !req.Apply {
					continue // dry run
				}

				switch req.Action {
				case "tag":
					if err := grabsStore.SetOrigin(ctx, g.ID, req.Origin); err != nil {
						log.Printf("park hygiene handler: tag grab %d: %v", g.ID, err)
					} else {
						log.Printf("park hygiene handler: tagged grab %d (%s) origin=%q", g.ID, g.Title, req.Origin)
					}
				case "reap":
					if err := grabsStore.SetRetryAfter(ctx, g.ID, now, testParkReapedReason); err != nil {
						log.Printf("park hygiene handler: reap set-reason grab %d: %v", g.ID, err)
						continue
					}
					if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Failed); err != nil {
						log.Printf("park hygiene handler: reap flip-status grab %d: %v", g.ID, err)
						continue
					}
					log.Printf("park hygiene handler: reaped grab %d (%s) as test/E2E debris", g.ID, g.Title)
				}
			}
		}

		ids := matched
		if ids == nil {
			ids = []int64{}
		}
		writeJSON(w, parkHygieneResponse{
			Matched: len(matched),
			Applied: req.Apply,
			IDs:     ids,
		})
	}
}

// containsIgnoreCase reports whether s contains substr in a case-insensitive
// comparison. Only used for the reasonContains dry-run filter.
func containsIgnoreCase(s, substr string) bool {
	if substr == "" {
		return true
	}
	sLow := toLower(s)
	subLow := toLower(substr)
	return contains(sLow, subLow)
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
