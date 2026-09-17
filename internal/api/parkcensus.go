package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
)

// strandedResumeGrace is how long after retry_after a transport-parked row
// must wait before the hygiene pass considers it stranded and clears its GID.
// 6 hours covers any plausible restart/drain delay while being short enough
// that stranded rows do not stay invisible for days.
const strandedResumeGrace = 6 * time.Hour

// parkCensusResponse is the handler-local response struct for GET /api/requests/park-census.
// Handler-local deliberately — no apidto mirror (plan §response struct rule).
type parkCensusResponse struct {
	Total                   int    `json:"total"`
	DueNow                  int    `json:"dueNow"`
	DueWithin1h             int    `json:"dueWithin1h"`
	ParkedWithin24h         int    `json:"parkedWithin24h"`
	ParkedWithin7d          int    `json:"parkedWithin7d"`
	ParkedFar               int    `json:"parkedFar"`
	AwaitingResume          int    `json:"awaitingResume"`
	AwaitingResumeOverdue   int    `json:"awaitingResumeOverdue"`
	HeldPreRelease          int    `json:"heldPreRelease"`
	AirDateShaped           int    `json:"airDateShaped"`
	TestOrigin              int    `json:"testOrigin"`
	MalformedSchedule       int    `json:"malformedSchedule"`
	GeneratedAt             string `json:"generatedAt"`
}

// computeParkCensus builds the census by iterating over all pending_retry rows
// across usenetRetryModes. No new SQL — it reuses List (same census pattern
// freeUsenetSlots already uses).
func computeParkCensus(ctx context.Context, grabsStore *grabs.Store, now time.Time) parkCensusResponse {
	res := parkCensusResponse{GeneratedAt: grabs.FormatTime(now)}
	for _, m := range []mode.Mode{mode.Movies, mode.Series, mode.Adult} {
		list, err := grabsStore.List(ctx, m)
		if err != nil {
			log.Printf("park census: listing %s grabs: %v", m, err)
			continue
		}
		for _, g := range list {
			if g.Status != grabs.PendingRetry {
				continue
			}
			res.Total++

			// Malformed schedule: non-empty retry_after that does not parse.
			if g.RetryAfter != "" {
				if _, parseErr := grabs.ParseTime(g.RetryAfter); parseErr != nil {
					res.MalformedSchedule++
					continue // skip time-based buckets for unparseable rows
				}
			}

			// Origin marker.
			if g.Origin != "" {
				res.TestOrigin++
			}

			// Held pre-release: hold_until parses and > now.
			if g.HoldUntil != "" {
				if holdT, err := grabs.ParseTime(g.HoldUntil); err == nil && holdT.After(now) {
					res.HeldPreRelease++
				}
			}

			// Air-date shaped (Series per-episode rows).
			if airDateShaped(g) {
				res.AirDateShaped++
			}

			// Awaiting resume: transport-parked rows (download_gid non-empty).
			if g.DownloadGID != "" {
				res.AwaitingResume++
				if g.RetryAfter != "" {
					if retryT, err := grabs.ParseTime(g.RetryAfter); err == nil {
						if now.Sub(retryT) > strandedResumeGrace {
							res.AwaitingResumeOverdue++
						}
					}
				}
				continue // transport rows don't bucket by retry_after distance
			}

			// Rows without retry_after are not on the retry track (held-only).
			if g.RetryAfter == "" {
				continue
			}

			retryT, _ := grabs.ParseTime(g.RetryAfter)
			dist := retryT.Sub(now)
			switch {
			case dist <= 0:
				res.DueNow++
			case dist <= time.Hour:
				res.DueWithin1h++
			case dist <= 24*time.Hour:
				res.ParkedWithin24h++
			case dist <= 7*24*time.Hour:
				res.ParkedWithin7d++
			default:
				res.ParkedFar++
			}
		}
	}
	return res
}

// logParkCensus emits the single structured log line the plan requires.
// The "where" string identifies the call site (e.g. "retry cycle", "boot").
func logParkCensus(where string, c parkCensusResponse) {
	log.Printf("usenet park census (%s): total=%d due_now=%d due_1h=%d lt24h=%d lt7d=%d far=%d awaiting_resume=%d resume_overdue=%d held=%d airdate=%d test_origin=%d malformed=%d",
		where,
		c.Total, c.DueNow, c.DueWithin1h, c.ParkedWithin24h, c.ParkedWithin7d, c.ParkedFar,
		c.AwaitingResume, c.AwaitingResumeOverdue, c.HeldPreRelease, c.AirDateShaped,
		c.TestOrigin, c.MalformedSchedule)
}

// parkCensusHandler backs GET /api/requests/park-census.
// Classified {queue} for free by sectionlock (route prefix /api/requests/).
func parkCensusHandler(grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		census := computeParkCensus(r.Context(), grabsStore, now)
		writeJSON(w, census)
	}
}

// formatDuration renders a duration in a short human-readable form for log output.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}
