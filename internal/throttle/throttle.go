// Package throttle implements per-host minimum call spacing shared across all
// external service clients.
package throttle

import (
	"context"
	"sync"
	"time"
)

type Throttle struct {
	mu          sync.Mutex
	next        map[string]time.Time
	minInterval time.Duration
}

func New(minInterval time.Duration) *Throttle {
	return &Throttle{next: make(map[string]time.Time), minInterval: minInterval}
}

// Wait blocks until it's this host's turn, reserving the NEXT slot before
// releasing the lock and sleeping.
//
// Reserve-then-release-then-sleep is the correct order — sleeping WHILE
// holding the lock would block every other host's throttling on whichever
// host happens to be waiting, serializing concurrency that should be
// independent per host.
func (t *Throttle) Wait(ctx context.Context, host string) error {
	t.mu.Lock()
	now := time.Now()
	next, ok := t.next[host]
	if !ok || next.Before(now) {
		next = now
	}
	wait := next.Sub(now)
	t.next[host] = next.Add(t.minInterval)
	t.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
