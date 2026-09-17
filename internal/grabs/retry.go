package grabs

import (
	"context"
	"fmt"
	"time"
)

// MaxTransportRetries is the number of times a grab may be short-parked for a
// transport failure before it is escalated to the days-ladder re-search.
// Cap 4 → 2m + 5m + 15m + 30m ≈ 52 minutes of short-park attempts.
const MaxTransportRetries = 4

// TransportBackoff returns how far ahead a transport-parked row's next resume
// should be scheduled, given the transport_retry_count AFTER the current park
// (i.e. the value ParkForTransportResume just incremented to):
//
//	1 → 2m, 2 → 5m, 3 → 15m, 4 → 30m, ≥5 → 0.
//
// Zero means "the short ladder is exhausted — escalate to the days ladder".
// Unlike RetryBackoff, this deliberately returns 0 so callers can detect the
// cap and fall through to ParkWithBackoff / the normal re-search path.
//
// Claude 2026-09-17: separate ladder from RetryBackoff.
// Reason: a transport park must not advance retry_count (the days-ladder
//   driver). These two counters and their ladders are deliberately separate.
// Review if: the ladder shape gains a settings-UI control.
func TransportBackoff(n int) time.Duration {
	switch n {
	case 1:
		return 2 * time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 15 * time.Minute
	case 4:
		return 30 * time.Minute
	default:
		return 0 // exhausted: escalate to the days ladder
	}
}

// RetryBackoff returns how far ahead a pending_retry row's next attempt should
// be parked, given the retry_count the row will carry AFTER this park:
// ≤0 (a first Create park) → 24h, 1 → 3d, 2 → 10d, 3 → 30d, 4 → 60d, ≥5 → 90d.
//
// The 90d ceiling is a plateau, not a give-up — the schedule never stops and
// never returns 0, which would make the row due immediately and spin a tight
// re-search loop.
//
// Claude 2026-09-13: shared progressive schedule for every pending_retry park.
// Reason: a flat 24h re-search forever for unmet quality-floor and retrieval
// failures hammered indexers; air-date had its own ladder and everything else
// stayed flat — one schedule, one writer class.
// Troubleshooting: "why is this grab waiting 90d" → retry_count ≥ 5.
// Review if: product wants a shorter ceiling, or a consecutive-failure counter
// separate from cumulative retry_count (Relaunch keeps the count today).
func RetryBackoff(retryCount int) time.Duration {
	switch {
	case retryCount <= 0:
		return 24 * time.Hour
	case retryCount == 1:
		return 3 * 24 * time.Hour
	case retryCount == 2:
		return 10 * 24 * time.Hour
	case retryCount == 3:
		return 30 * 24 * time.Hour
	case retryCount == 4:
		return 60 * 24 * time.Hour
	default:
		return 90 * 24 * time.Hour
	}
}

// ParkWithBackoff parks grab id as pending_retry at RetryBackoff of the
// POST-increment count. It delegates to SetPendingRetry, so download_gid is
// cleared and DueForRetry keeps seeing the row.
//
// A row being MINTED by Create (retry_count 0, nothing to increment) is not
// this path: those callers park at now.Add(RetryBackoff(0)) themselves.
func (s *Store) ParkWithBackoff(ctx context.Context, id int64, now time.Time, reason string) error {
	g, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	after := now.Add(RetryBackoff(g.RetryCount + 1))
	if err := s.SetPendingRetry(ctx, id, after, reason); err != nil {
		return fmt.Errorf("parking grab %d with backoff: %w", id, err)
	}
	return nil
}
