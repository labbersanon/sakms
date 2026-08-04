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

	// Default (unset) → eager false.
	f1 := newFake()
	runCycle(context.Background(), workflowDedup, f1, settingsStore, dedupscan.New())
	for _, c := range f1.snapshotDedup() {
		if c.eager {
			t.Fatalf("dedup eager should default to false, got true for %s", c.mode)
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
		runLoop(ctx, workflowRename, reload, interval, f, settingsStore, nil)
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
