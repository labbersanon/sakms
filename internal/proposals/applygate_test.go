package proposals

import (
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/mode"
)

// freshGate returns a new Gate isolated from DefaultGate, so tests can run in
// parallel without interfering with each other.
func freshGate() *Gate { return newGate() }

func TestGate_ApplyInFlightFalseByDefault(t *testing.T) {
	g := freshGate()
	if g.ApplyInFlight(mode.Movies, Rename) {
		t.Error("expected ApplyInFlight false before any BeginApply")
	}
}

func TestGate_ApplyInFlightTrueAfterBegin(t *testing.T) {
	g := freshGate()
	g.BeginApply(mode.Movies, Rename)
	if !g.ApplyInFlight(mode.Movies, Rename) {
		t.Error("expected ApplyInFlight true after BeginApply")
	}
	g.EndApply(mode.Movies, Rename)
}

func TestGate_ApplyInFlightFalseAfterEnd(t *testing.T) {
	g := freshGate()
	g.BeginApply(mode.Movies, Rename)
	g.EndApply(mode.Movies, Rename)
	if g.ApplyInFlight(mode.Movies, Rename) {
		t.Error("expected ApplyInFlight false after matching EndApply")
	}
}

func TestGate_RefcountSupportsNested(t *testing.T) {
	g := freshGate()
	g.BeginApply(mode.Movies, Rename)
	g.BeginApply(mode.Movies, Rename) // simulate two concurrent applies
	g.EndApply(mode.Movies, Rename)
	if !g.ApplyInFlight(mode.Movies, Rename) {
		t.Error("expected ApplyInFlight true with refcount still at 1")
	}
	g.EndApply(mode.Movies, Rename)
	if g.ApplyInFlight(mode.Movies, Rename) {
		t.Error("expected ApplyInFlight false after second EndApply")
	}
}

func TestGate_ScopedByModeAndWorkflow(t *testing.T) {
	g := freshGate()
	g.BeginApply(mode.Movies, Rename)
	if g.ApplyInFlight(mode.Series, Rename) {
		t.Error("expected Movies apply not to affect Series")
	}
	if g.ApplyInFlight(mode.Movies, Purge) {
		t.Error("expected Movies/Rename apply not to affect Movies/Purge")
	}
	g.EndApply(mode.Movies, Rename)
}

func TestGate_IdleHookNotFiredWithoutDeferred(t *testing.T) {
	g := freshGate()
	fired := false
	g.RegisterApplyIdle(mode.Movies, Rename, func() { fired = true })
	g.BeginApply(mode.Movies, Rename)
	g.EndApply(mode.Movies, Rename)
	// No markDeferred was called — hook must NOT fire.
	time.Sleep(20 * time.Millisecond)
	if fired {
		t.Error("expected idle hook not to fire when no deferred replace was recorded")
	}
}

func TestGate_IdleHookFiredWhenDeferred(t *testing.T) {
	g := freshGate()
	var mu sync.Mutex
	fired := false
	g.RegisterApplyIdle(mode.Movies, Rename, func() {
		mu.Lock()
		fired = true
		mu.Unlock()
	})
	g.BeginApply(mode.Movies, Rename)
	g.markDeferred(mode.Movies, Rename)
	g.EndApply(mode.Movies, Rename) // should fire the hook in a goroutine

	// Wait for the goroutine to run.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := fired
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if !fired {
		t.Error("expected idle hook to fire after EndApply when deferred was set")
	}
}

func TestGate_DeferredFlagClearedAfterEnd(t *testing.T) {
	g := freshGate()
	called := 0
	g.RegisterApplyIdle(mode.Movies, Rename, func() { called++ })

	// First apply cycle with a deferred replace.
	g.BeginApply(mode.Movies, Rename)
	g.markDeferred(mode.Movies, Rename)
	g.EndApply(mode.Movies, Rename)
	time.Sleep(50 * time.Millisecond)

	// Second apply cycle with NO deferred replace — hook must not fire again.
	g.BeginApply(mode.Movies, Rename)
	g.EndApply(mode.Movies, Rename)
	time.Sleep(50 * time.Millisecond)

	if called != 1 {
		t.Errorf("expected idle hook to fire exactly once, got %d", called)
	}
}

func TestGate_MultipleHooksAllFired(t *testing.T) {
	g := freshGate()
	var mu sync.Mutex
	count := 0
	for range 3 {
		g.RegisterApplyIdle(mode.Movies, Rename, func() {
			mu.Lock()
			count++
			mu.Unlock()
		})
	}
	g.BeginApply(mode.Movies, Rename)
	g.markDeferred(mode.Movies, Rename)
	g.EndApply(mode.Movies, Rename)

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := count
		mu.Unlock()
		if n == 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if count != 3 {
		t.Errorf("expected all 3 hooks to fire, got %d", count)
	}
}

func TestGate_HookScopedByModeWorkflow(t *testing.T) {
	g := freshGate()
	moviesFired := false
	seriesFired := false
	g.RegisterApplyIdle(mode.Movies, Rename, func() { moviesFired = true })
	g.RegisterApplyIdle(mode.Series, Rename, func() { seriesFired = true })

	// Only Movies apply completes with a deferred replace.
	g.BeginApply(mode.Movies, Rename)
	g.markDeferred(mode.Movies, Rename)
	g.EndApply(mode.Movies, Rename)

	time.Sleep(50 * time.Millisecond)
	if !moviesFired {
		t.Error("expected Movies hook to fire")
	}
	if seriesFired {
		t.Error("expected Series hook NOT to fire when only Movies had a deferred replace")
	}
}
