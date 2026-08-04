package api

import (
	"context"
	"net/http"

	"github.com/labbersanon/sakms/internal/discoverrefresh"
)

// NewDiscoverRefreshTriggerMux is a small, separately-dependent mux for the
// discover-refresh feature's manual "Refresh now" trigger — same precedent
// as NewRecheckTriggerMux (recheck_trigger.go): a route needing a dependency
// shape NewMux doesn't already carry gets its own small mux, rather than
// growing NewMux's signature for a route almost none of its existing test
// call sites exercise.
//
// It necessarily imports internal/discoverrefresh, same as
// NewRecheckTriggerMux imports internal/recheck — there is no way to trigger
// a discover refresh without the code that performs one.
func NewDiscoverRefreshTriggerMux(d discoverrefresh.Deps) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/discover-refresh/trigger", triggerDiscoverRefreshHandler(d))
	return mux
}

// triggerDiscoverRefreshHandler fires an on-demand, forced discover-cache
// refresh cycle (discoverrefresh.TriggerAsync) — every key, every source,
// ignoring both refreshed_at and the TMDB LRU (§3.5, §5.1).
//
// Claude 2026-08-03: calls TriggerAsync, not TriggerOnce (BE-15, plan §5.1).
// Reason: the plan's own text says the handler "checks synchronously"
// for 409 and otherwise "go-launches the cycle ... and returns 202" —
// but discoverrefresh.TriggerOnce (BE-8, already covered by its own
// T-12 test) is deliberately fully synchronous: a successful call blocks
// until the whole forced cycle completes (up to ~300 external calls,
// §3.6), which would hold this HTTP request open for minutes instead of
// answering 202 immediately. TriggerAsync claims the identical
// cycleRunning single-flight flag (so TriggerOnce, Run's boot poll/ticks,
// and this handler all remain mutually exclusive) but launches the forced
// RefreshAll in its own goroutine and reports the CAS outcome right away —
// the only way to satisfy both halves of §5.1's contract at once.
// Troubleshooting: if this 202s but Discover never repopulates, check
// discoverrefresh.RefreshAll's own log lines, not this handler — it never
// inspects the cycle's outcome.
// Review if: TriggerOnce's own contract becomes non-blocking, at which
// point this handler should call it directly instead.
func triggerDiscoverRefreshHandler(d discoverrefresh.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !discoverrefresh.TriggerAsync(context.Background(), d) {
			http.Error(w, "a discover refresh is already running", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}
