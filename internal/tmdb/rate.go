package tmdb

import (
	"context"
	"sync"

	"golang.org/x/time/rate"
)

// Claude 2026-09-23: process-wide TMDB rate limit (operator A/A/A).
// Reason: classic TMDB v3 guidance ~40 requests / 10s; backfill + Rename +
//   Discover + /poster all share one Client surface and were unbounded.
// Troubleshooting: ctx deadline while Wait-ing → raise timeouts or slow callers.
// Review if: TMDB publishes a different documented ceiling.
// Related files: client.go do()/doPOST(), poster_backfill.go (still gaps for AI/TVDB)

const (
	// defaultTMDBRate is 40 requests per 10 seconds → 4/s sustained.
	defaultTMDBRate = 40.0 / 10.0
	// defaultTMDBBurst matches one full 10s window so a short interactive burst
	// (Discover page load) is not artificially serialized.
	defaultTMDBBurst = 40
)

var (
	limiterMu     sync.Mutex
	sharedLimiter = rate.NewLimiter(rate.Limit(defaultTMDBRate), defaultTMDBBurst)
)

// waitRate blocks until the shared TMDB budget allows one outbound call, or
// until ctx is cancelled. Cache hits must not call this — only live HTTP.
func waitRate(ctx context.Context) error {
	limiterMu.Lock()
	lim := sharedLimiter
	limiterMu.Unlock()
	if lim == nil {
		return nil
	}
	return lim.Wait(ctx)
}

// SetSharedRateLimitForTest replaces the process-wide limiter. Tests that
// fire many live GETs should set rate.Inf (or a high limit) in TestMain /
// t.Cleanup so they are not serialized by the production budget.
func SetSharedRateLimitForTest(r rate.Limit, burst int) {
	limiterMu.Lock()
	defer limiterMu.Unlock()
	if burst < 1 {
		burst = 1
	}
	sharedLimiter = rate.NewLimiter(r, burst)
}

// ResetSharedRateLimitForTest restores the production 40/10s limiter.
func ResetSharedRateLimitForTest() {
	SetSharedRateLimitForTest(rate.Limit(defaultTMDBRate), defaultTMDBBurst)
}
