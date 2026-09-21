package usenet

import (
	"context"
	"errors"
	"sync"
)

// Claude 2026-09-20: true pause gate — suspend without cancelling download ctx.
// Reason: Pause used to cancel context, leaving grabs.queued with a dead GID that
//   filled freeUsenetSlots (max=1) so autograb stopped; Resume was unsupported.
// Troubleshooting: global Pause then Resume all; per-row Resume on Downloads.
// Review if: pause should release the job semaphore so another NZB can fetch.
// Related: manager.go Pause/Resume; api putPauseStateHandler resumeAllPaused.

// ErrStagingGone is returned by Resume when the download's staging directory is
// missing. Callers must not pretend to continue — fireOnError parks for re-search.
var ErrStagingGone = errors.New("usenet: staging directory missing — cannot resume; re-search required")

type pauseGate struct {
	mu      sync.Mutex
	paused  bool
	waiters []chan struct{}
}

func newPauseGate() *pauseGate {
	return &pauseGate{}
}

func (g *pauseGate) Pause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.paused = true
}

func (g *pauseGate) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.paused = false
	for _, ch := range g.waiters {
		close(ch)
	}
	g.waiters = nil
}

// Wait blocks while paused. ctx cancel (Cancel / staging-gone) returns ctx.Err().
func (g *pauseGate) Wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		g.mu.Lock()
		if !g.paused {
			g.mu.Unlock()
			return nil
		}
		ch := make(chan struct{})
		g.waiters = append(g.waiters, ch)
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}
