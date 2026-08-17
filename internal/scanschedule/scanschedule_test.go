package scanschedule

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/dedupscan"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
)

// newTestSettings opens a fresh migrated DB and returns a settings store, the
// same pattern internal/settings' own tests use.
func newTestSettings(t *testing.T) *settings.Store {
	t.Helper()
	sqlDB := dbtest.New(t)
	return settings.New(sqlDB)
}

// --- fake Scanner ---

type dedupCall struct {
	mode  mode.Mode
	eager bool
}

// scanEvent records the wall-clock window of a single Scan call, for the AC15
// skip-not-queue timing test.
type scanEvent struct {
	mode  mode.Mode
	start time.Time
	end   time.Time
}

// fakeScanner is a fully controllable Scanner: it records every call, can be
// told to sleep/err/panic per (workflow, mode), and timestamps each call for
// the timing test. The interface boundary being real (not a runtime test hook)
// is exactly what makes this fake meaningful — runCycle drives the SAME
// contract the production adapter satisfies.
type fakeScanner struct {
	mu sync.Mutex

	renameModes []mode.Mode
	purgeModes  []mode.Mode
	dedupCalls  []dedupCall
	events      []scanEvent

	sleep   time.Duration
	errOn   map[string]error
	panicOn map[string]bool
}

func newFake() *fakeScanner {
	return &fakeScanner{errOn: map[string]error{}, panicOn: map[string]bool{}}
}

func (f *fakeScanner) record(kind string, m mode.Mode) {
	start := time.Now()
	if f.sleep > 0 {
		time.Sleep(f.sleep)
	}
	f.mu.Lock()
	f.events = append(f.events, scanEvent{mode: m, start: start, end: time.Now()})
	f.mu.Unlock()

	key := kind + ":" + string(m)
	if f.panicOn[key] {
		panic("fakeScanner induced panic for " + key)
	}
}

func (f *fakeScanner) ScanRename(ctx context.Context, m mode.Mode) error {
	f.mu.Lock()
	f.renameModes = append(f.renameModes, m)
	f.mu.Unlock()
	f.record("rename", m)
	return f.errOn["rename:"+string(m)]
}

func (f *fakeScanner) ScanPurge(ctx context.Context, m mode.Mode) error {
	f.mu.Lock()
	f.purgeModes = append(f.purgeModes, m)
	f.mu.Unlock()
	f.record("purge", m)
	return f.errOn["purge:"+string(m)]
}

func (f *fakeScanner) ScanDedup(ctx context.Context, m mode.Mode, eagerVMAF bool) error {
	f.mu.Lock()
	f.dedupCalls = append(f.dedupCalls, dedupCall{mode: m, eager: eagerVMAF})
	f.mu.Unlock()
	f.record("dedup", m)
	return f.errOn["dedup:"+string(m)]
}

func (f *fakeScanner) snapshotRename() []mode.Mode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mode.Mode(nil), f.renameModes...)
}

func (f *fakeScanner) snapshotDedup() []dedupCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dedupCall(nil), f.dedupCalls...)
}

// --- runCycle behavior ---

func TestRunCycle_Rename_ScansAllModesInOrder(t *testing.T) {
	f := newFake()
	runCycle(context.Background(), workflowRename, f, newTestSettings(t), dedupscan.New())

	got := f.snapshotRename()
	want := []mode.Mode{mode.Movies, mode.Series, mode.Adult}
	if len(got) != len(want) {
		t.Fatalf("rename scanned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rename order %v, want %v", got, want)
		}
	}
}

func TestRunCycle_Rename_FaultIsolation(t *testing.T) {
	f := newFake()
	f.errOn["rename:series"] = errors.New("boom") // an error must not abort the pass
	runCycle(context.Background(), workflowRename, f, newTestSettings(t), dedupscan.New())

	if got := f.snapshotRename(); len(got) != 3 {
		t.Fatalf("an error on one mode aborted the pass: scanned %v, want all three", got)
	}
}

func TestRunCycle_Rename_PanicIsolation(t *testing.T) {
	f := newFake()
	f.panicOn["rename:series"] = true // a panic must be recovered and not abort the pass
	// Must not crash the test process:
	runCycle(context.Background(), workflowRename, f, newTestSettings(t), dedupscan.New())

	got := f.snapshotRename()
	// series is recorded before it panics; adult must still run after recovery.
	if len(got) != 3 || got[2] != mode.Adult {
		t.Fatalf("panic on one mode aborted the pass or skipped the rest: scanned %v", got)
	}
}

func TestRunCycle_Dedup_EagerFlagFromSettings(t *testing.T) {
	settingsStore := newTestSettings(t)

	// Default (unset) → eager true.
	f1 := newFake()
	runCycle(context.Background(), workflowDedup, f1, settingsStore, dedupscan.New())
	for _, c := range f1.snapshotDedup() {
		if !c.eager {
			t.Fatalf("dedup eager should default to true, got false for %s", c.mode)
		}
	}

	// Explicit off → eager false for every mode.
	if err := settingsStore.SetBool(context.Background(), DedupVMAFEnabledKey, false); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	fOff := newFake()
	runCycle(context.Background(), workflowDedup, fOff, settingsStore, dedupscan.New())
	for _, c := range fOff.snapshotDedup() {
		if c.eager {
			t.Fatalf("dedup eager should be false when disabled, got true for %s", c.mode)
		}
	}

	// Enabled → eager true for every mode.
	if err := settingsStore.SetBool(context.Background(), DedupVMAFEnabledKey, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	f2 := newFake()
	runCycle(context.Background(), workflowDedup, f2, settingsStore, dedupscan.New())
	calls := f2.snapshotDedup()
	if len(calls) != 3 {
		t.Fatalf("dedup scanned %d modes, want 3", len(calls))
	}
	for _, c := range calls {
		if !c.eager {
			t.Fatalf("dedup eager should be true when enabled, got false for %s", c.mode)
		}
	}
}

// TestRunCycle_Dedup_HubGuard is AC14: a scheduled Dedup cycle acquires the same
// dedupscan.Hub guard as a manual trigger, so a mode already scanning (manual or
// scheduled) is skipped, and the in-flight flag is always cleared afterward for
// the modes it did scan.
func TestRunCycle_Dedup_HubGuard(t *testing.T) {
	hub := dedupscan.New()
	// Simulate a manual (or already-running) Movies dedup scan holding the guard.
	if !hub.TryStart(string(mode.Movies)) {
		t.Fatal("precondition: hub should start clean")
	}

	f := newFake()
	runCycle(context.Background(), workflowDedup, f, newTestSettings(t), hub)

	// Movies must have been skipped (guard held); Series + Adult must have run.
	got := f.snapshotDedup()
	var scanned []mode.Mode
	for _, c := range got {
		scanned = append(scanned, c.mode)
	}
	if len(scanned) != 2 || scanned[0] != mode.Series || scanned[1] != mode.Adult {
		t.Fatalf("Hub guard failed: dedup scanned %v, want [series adult] (movies guarded out)", scanned)
	}

	// The guard we hold for Movies is still in flight; the modes the cycle ran
	// must have had their guard released (Finish).
	if !hub.Inflight(string(mode.Movies)) {
		t.Error("Movies guard should still be held by the simulated manual scan")
	}
	if hub.Inflight(string(mode.Series)) {
		t.Error("Series in-flight not cleared after cycle — a wedge would 409 all future scans")
	}
	if hub.Inflight(string(mode.Adult)) {
		t.Error("Adult in-flight not cleared after cycle — a wedge would 409 all future scans")
	}
}

// --- LoadInterval's unset-vs-stored distinction ---

// TestLoadInterval_UnsetVsStored is the mechanism behind "on by default," and
// the distinction it pins is the one that must never be collapsed: a key nobody
// has ever written gets defaultSeconds, while a key an operator explicitly
// zeroed (or that holds garbage) reads as 0/off. Collapsing them would
// re-enable a schedule the operator deliberately turned off, on every boot.
func TestLoadInterval_UnsetVsStored(t *testing.T) {
	ctx := context.Background()

	t.Run("genuinely unset returns the default", func(t *testing.T) {
		store := newTestSettings(t)
		got := LoadInterval(ctx, store, RenameIntervalKey, DefaultIntervalSeconds)
		if want := time.Duration(DefaultIntervalSeconds) * time.Second; got != want {
			t.Errorf("LoadInterval on an unset key = %s, want %s", got, want)
		}
	})

	for _, c := range []struct {
		name   string
		stored string
		want   time.Duration
	}{
		{"explicit zero stays off", "0", 0},
		{"negative stays off", "-1", 0},
		{"blank stays off", "  ", 0},
		{"garbage stays off", "soon", 0},
		{"a stored positive wins over the default", "3600", time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := newTestSettings(t)
			if err := store.Set(ctx, RenameIntervalKey, c.stored); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if got := LoadInterval(ctx, store, RenameIntervalKey, DefaultIntervalSeconds); got != c.want {
				t.Errorf("LoadInterval(stored %q) = %s, want %s", c.stored, got, c.want)
			}
		})
	}
}

// --- the enabled × interval gate ---

// runLoopUntilReturn runs runLoop in a goroutine and reports whether it
// returned on its own within d (as opposed to still ticking). Used by the gate
// tests below, which are all deliberately sub-second so they never join
// TestRunLoop_SkipNotQueue in being timing-sensitive.
func runLoopUntilReturn(t *testing.T, d time.Duration, run func()) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		run()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// TestRunLoop_BootGates covers both halves of the boot gate, including the
// spec's explicit edge case: enabled=true with interval=0 is "off," not an
// error. Either gate alone must start nothing — no ticker, no scan.
func TestRunLoop_BootGates(t *testing.T) {
	for _, c := range []struct {
		name        string
		interval    time.Duration
		bootEnabled bool
	}{
		{"disabled with a positive interval", 20 * time.Millisecond, false},
		{"enabled with a zero interval (edge case: off, not an error)", 0, true},
		{"disabled and zero", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := newFake()
			// Built on the TEST goroutine, never inside the closure below:
			// dbtest.New skips (runtime.Goexit) when no Postgres DSN is
			// configured, and a Goexit inside the spawned goroutine would
			// deadlock runLoopUntilReturn instead of skipping the test.
			settingsStore := newTestSettings(t)

			returned := runLoopUntilReturn(t, 250*time.Millisecond, func() {
				runLoop(ctx, workflowRename, func() time.Duration { return c.interval },
					c.interval, c.bootEnabled, RenameEnabledKey, f, settingsStore, nil)
			})
			if !returned {
				t.Fatal("runLoop did not return immediately — a gated-off workflow started a ticker")
			}
			if got := f.snapshotRename(); len(got) != 0 {
				t.Errorf("a gated-off workflow scanned %v, want nothing", got)
			}
		})
	}
}

// TestRunLoop_EnabledAndIntervalPositiveRuns is the positive control for the
// gate tests above: with both gates satisfied, cycles actually happen. Without
// it, a bug that stopped every loop unconditionally would pass all three
// negative cases.
func TestRunLoop_EnabledAndIntervalPositiveRuns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newFake()
	const interval = 20 * time.Millisecond

	go runLoop(ctx, workflowRename, func() time.Duration { return interval },
		interval, true, RenameEnabledKey, f, newTestSettings(t), nil)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.snapshotRename()) >= 3 {
			return // one full three-mode cycle observed
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no full cycle ran with enabled=true and interval>0 (scanned %v)", f.snapshotRename())
}

// TestRunLoop_LiveDisableStopsLoop is the per-tick half of the gate: flipping
// the enabled key to false while a loop is running stops it on its next tick,
// the same live behavior setting the interval to 0 already had.
func TestRunLoop_LiveDisableStopsLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	settingsStore := newTestSettings(t)
	f := newFake()
	const interval = 20 * time.Millisecond

	if err := settingsStore.SetBool(context.Background(), RenameEnabledKey, false); err != nil {
		t.Fatalf("SetBool: %v", err)
	}

	returned := runLoopUntilReturn(t, 2*time.Second, func() {
		// bootEnabled is true (as it was at boot); the store already says
		// false, so the FIRST tick's live read must stop the loop.
		runLoop(ctx, workflowRename, func() time.Duration { return interval },
			interval, true, RenameEnabledKey, f, settingsStore, nil)
	})
	if !returned {
		t.Fatal("runLoop kept ticking after the enabled key was set to false")
	}
	if got := f.snapshotRename(); len(got) != 0 {
		t.Errorf("a live-disabled loop still scanned %v — the enabled check must precede runCycle", got)
	}
}

// TestRunLoop_SkipNotQueue is AC15: when a cycle outruns the interval, the tick
// that fired during it is skipped, not queued — so cycles never re-fire
// back-to-back with no idle gap. Without the drain in runLoop, the one buffered
// tick fires immediately on cycle end (idle gap ≈ 0); with it, the next cycle
// waits for a fresh tick (idle gap > 0).
func TestRunLoop_SkipNotQueue(t *testing.T) {
	settingsStore := newTestSettings(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const interval = 40 * time.Millisecond
	f := newFake()
	f.sleep = 30 * time.Millisecond // per-mode; a 3-mode cycle (~90ms) outruns the 40ms interval

	// reload returns the fixed sub-second cadence so live-retune never resets
	// the ticker (LoadInterval only supports whole seconds); it stays > 0 so the
	// loop never disables itself mid-test.
	reload := func() time.Duration { return interval }

	done := make(chan struct{})
	go func() {
		// bootEnabled=true and an unset RenameEnabledKey (so the per-tick
		// GetBool returns its true default every tick) keep this test measuring
		// only the skip-not-queue drain, unchanged by the enabled gate.
		runLoop(ctx, workflowRename, reload, interval, true, RenameEnabledKey, f, settingsStore, nil)
		close(done)
	}()

	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	// Reconstruct cycles from the recorded events (each cycle is movies→series→
	// adult, in order) and measure the idle gap between the end of one cycle's
	// last scan and the start of the next cycle's first scan.
	f.mu.Lock()
	events := append([]scanEvent(nil), f.events...)
	f.mu.Unlock()

	if len(events) < 6 {
		t.Fatalf("expected at least two full cycles (6 events), got %d", len(events))
	}
	// Group into cycles of 3.
	var idles []time.Duration
	for i := 3; i+2 < len(events); i += 3 {
		prevCycleEnd := events[i-1].end   // adult end of the previous cycle
		nextCycleStart := events[i].start // movies start of the next cycle
		idles = append(idles, nextCycleStart.Sub(prevCycleEnd))
	}
	if len(idles) == 0 {
		t.Fatalf("not enough cycles to measure an idle gap (events=%d)", len(events))
	}
	// With skip-not-queue, every idle gap is a real wait for the next tick
	// (roughly up to `interval`); the bug (immediate re-fire off a buffered
	// tick) would make these ≈ 0. A generous 12ms floor separates the two
	// without being flaky.
	const floor = 12 * time.Millisecond
	for i, idle := range idles {
		if idle < floor {
			t.Errorf("cycle %d→%d idle gap %v < %v — an overrunning cycle re-fired back-to-back (tick queued, not skipped)", i, i+1, idle, floor)
		}
	}
}
