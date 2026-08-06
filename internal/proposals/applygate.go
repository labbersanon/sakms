// Package proposals persists the review queue every workflow stages into.
package proposals

// applygate.go — apply-in-flight gate that defers ReplacePending calls while
// an apply is running for the same (mode, workflow), preventing stale-ID
// failures when a watchfolder scan races a batch apply.
//
// Claude 2026-08-06: new file.
// Reason: watchfolder scan during apply-batch caused "no proposal with that id"
//   errors because ReplacePending deleted+reinserted live rows (new IDs) while
//   apply-batch was looking them up. This gate defers the replace until apply
//   idle, then fires a catch-up rescan via RegisterApplyIdle hooks.
// Troubleshooting: if a scan silently skips, check ApplyInFlight for the
//   (mode, workflow) pair and confirm EndApply was called after apply completed.
// Review if: applies become idempotent (upsert + same IDs) and the gate is
//   no longer needed as a safety net.

import (
	"errors"
	"sync"

	"github.com/labbersanon/sakms/internal/mode"
)

// ErrReplaceDeferred is returned by ReplacePending when an apply is in flight
// for the same (mode, workflow). The caller should treat it as a soft skip —
// the existing live queue is unchanged, and a catch-up scan fires automatically
// via RegisterApplyIdle when the apply completes.
var ErrReplaceDeferred = errors.New("proposals: replace deferred — apply in flight")

type gateKey struct {
	m  mode.Mode
	wf Workflow
}

// Gate tracks in-flight apply operations per (mode, workflow) and fires
// registered idle hooks when the count drops to zero after a deferred replace.
type Gate struct {
	mu       sync.Mutex
	counts   map[gateKey]int
	deferred map[gateKey]bool
	hooks    map[gateKey][]func()
}

func newGate() *Gate {
	return &Gate{
		counts:   make(map[gateKey]int),
		deferred: make(map[gateKey]bool),
		hooks:    make(map[gateKey][]func()),
	}
}

// DefaultGate is the package-level gate used by the live server and by
// ReplacePending's apply-in-flight check.
var DefaultGate = newGate()

// BeginApply records that an apply is starting for (m, wf). The caller must
// pair every BeginApply with exactly one EndApply (typically via defer).
func (g *Gate) BeginApply(m mode.Mode, wf Workflow) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.counts[gateKey{m, wf}]++
}

// EndApply records that an apply finished for (m, wf). When the refcount drops
// to zero and a deferred replace was recorded (a ReplacePending call was
// skipped while the apply was in flight), it clears the deferred flag and fires
// all registered idle hooks in separate goroutines to trigger a catch-up scan.
func (g *Gate) EndApply(m mode.Mode, wf Workflow) {
	g.mu.Lock()
	k := gateKey{m, wf}
	g.counts[k]--
	if g.counts[k] > 0 {
		g.mu.Unlock()
		return
	}
	delete(g.counts, k)
	wasDeferred := g.deferred[k]
	if wasDeferred {
		delete(g.deferred, k)
	}
	// Copy hooks slice under lock; fire outside to avoid deadlock.
	hooks := make([]func(), len(g.hooks[k]))
	copy(hooks, g.hooks[k])
	g.mu.Unlock()

	if wasDeferred {
		for _, fn := range hooks {
			go fn()
		}
	}
}

// ApplyInFlight reports whether at least one apply is currently running for
// (m, wf). Called by ReplacePending before mutating the queue.
func (g *Gate) ApplyInFlight(m mode.Mode, wf Workflow) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.counts[gateKey{m, wf}] > 0
}

// markDeferred records that a ReplacePending call was skipped because an apply
// was in flight. EndApply reads this to decide whether to fire idle hooks.
func (g *Gate) markDeferred(m mode.Mode, wf Workflow) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deferred[gateKey{m, wf}] = true
}

// RegisterApplyIdle registers fn to be called (in a new goroutine) the next
// time an apply for (m, wf) completes AND a ReplacePending was deferred while
// it was in flight. Intended for watchfolder catch-up rescans. Hooks
// accumulate — calling RegisterApplyIdle multiple times adds multiple hooks.
func (g *Gate) RegisterApplyIdle(m mode.Mode, wf Workflow, fn func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := gateKey{m, wf}
	g.hooks[k] = append(g.hooks[k], fn)
}

// reset clears all gate state. For tests only — do not call in production.
func (g *Gate) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.counts = make(map[gateKey]int)
	g.deferred = make(map[gateKey]bool)
	g.hooks = make(map[gateKey][]func())
}
