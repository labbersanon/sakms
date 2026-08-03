package api

import (
	"time"

	"github.com/labbersanon/sakms/internal/sectionlock"
)

// streamRecheckInterval resolves §4.5's re-check period, honouring a
// test-injected override.
//
// The override is threaded as a variadic on each SSE handler, exactly
// mirroring sysinfoStreamHandler's tickInterval — the only ticker this
// package had before §4.5 — so every production registration in handler.go
// stays a clean one-arg call and only a test ever passes a second.
//
// Without an injectable interval the SSE termination tests would each have
// to sleep 30 seconds.
func streamRecheckInterval(override []time.Duration) time.Duration {
	if len(override) > 0 && override[0] > 0 {
		return override[0]
	}
	return sectionlock.StreamRecheckInterval
}
