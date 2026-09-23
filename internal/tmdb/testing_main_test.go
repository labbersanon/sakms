package tmdb_test

import (
	"os"
	"testing"

	"github.com/labbersanon/sakms/internal/tmdb"
	"golang.org/x/time/rate"
)

func TestMain(m *testing.M) {
	// Default tests to unlimited so existing client suites are not serialized
	// by the production 40/10s budget. Rate-limit tests re-enable a tight
	// limiter themselves and ResetSharedRateLimitForTest in Cleanup.
	tmdb.SetSharedRateLimitForTest(rate.Inf, 1)
	os.Exit(m.Run())
}
