package api

import (
	"os"
	"testing"

	"github.com/labbersanon/sakms/internal/tmdb"
	"golang.org/x/time/rate"
)

// Claude 2026-10-21: api TestMain lifts the process-wide TMDB limiter.
// Reason: production is 40 req / 10s. httptest TMDB in this package shared
//   that budget, so go test ./internal/api died at 60s on
//   TestCalendarUpcomingMovies_TypedDateOutsideWindow (next in line, 0s);
//   TestDiscoverDetail_CapsSeasonPrefetch alone waited ~7s. tmdb's TestMain
//   already sets Inf; api did not.
// Troubleshooting: go test ./internal/api timeout with fake TMDB → this
//   TestMain missing. Review if: a test here must assert the production budget.
func TestMain(m *testing.M) {
	tmdb.SetSharedRateLimitForTest(rate.Inf, 1)
	os.Exit(m.Run())
}
