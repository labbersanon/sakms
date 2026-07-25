package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/sysinfo"
)

// flushRecorder is an httptest.ResponseRecorder that also implements
// http.Flusher, since the SSE handler type-asserts w to http.Flusher and
// bails with 500 otherwise. flushed counts flushes so tests can wait on
// events landing without racing on the ticker.
type flushRecorder struct {
	*httptest.ResponseRecorder
	mu      sync.Mutex
	flushed int
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (f *flushRecorder) Flush() {
	f.mu.Lock()
	f.flushed++
	f.mu.Unlock()
}

func (f *flushRecorder) flushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushed
}

func (f *flushRecorder) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ResponseRecorder.Body.String()
}

// waitForFlushes blocks until the recorder has flushed at least n times or the
// deadline passes.
func waitForFlushes(t *testing.T, rec *flushRecorder, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if rec.flushCount() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d flushes (got %d)", n, rec.flushCount())
}

// TestFirstTickInterval pins firstTickInterval's two behaviors: below-or-at
// firstTickThreshold (every existing test interval) → 0, meaning "no fast
// first tick, take the nil-channel path"; above it (production's 2s default)
// → a quarter of the interval.
func TestFirstTickInterval(t *testing.T) {
	cases := []struct {
		interval time.Duration
		want     time.Duration
	}{
		{5 * time.Millisecond, 0},       // this file's own test interval
		{999 * time.Millisecond, 0},     // just under the threshold
		{firstTickThreshold, 0},         // exactly at the threshold — still 0 (<=)
		{2 * time.Second, 500 * time.Millisecond}, // the real production default
		{4 * time.Second, time.Second},
	}
	for _, c := range cases {
		if got := firstTickInterval(c.interval); got != c.want {
			t.Errorf("firstTickInterval(%v) = %v, want %v", c.interval, got, c.want)
		}
	}
}

// TestSysinfoStream_FastFirstTick_BeatsTheFullInterval is the load-bearing
// proof for the fix: with a production-scale interval (here 1.1s, just over
// firstTickThreshold so the fast-first path activates, but still fast enough
// for a unit test), the FIRST real data snapshot must arrive well before a
// full interval has elapsed — proving an operator opening the Dashboard
// doesn't sit on a blank "Waiting for the first live reading…" screen for
// the full 2s in production.
func TestSysinfoStream_FastFirstTick_BeatsTheFullInterval(t *testing.T) {
	var calls int
	var mu sync.Mutex
	sampleFn := func(_ []sysinfo.MountSpec) (sysinfo.RawSample, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		n := int64(calls)
		return sysinfo.RawSample{
			CapturedAt:     time.Now(),
			CPUUsageMicros: n * 1000,
			MemLimitBytes:  1 << 30,
		}, nil
	}
	mockMounts := func(_ context.Context) []sysinfo.MountSpec {
		return []sysinfo.MountSpec{{Name: "App data", Path: t.TempDir()}}
	}

	const interval = 1100 * time.Millisecond // > firstTickThreshold (1s)
	handler := sysinfoStreamHandler(sampleFn, mockMounts, interval)

	rec := newFlushRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sysinfo/stream", nil).WithContext(ctx)

	start := time.Now()
	done := make(chan struct{})
	go func() {
		handler(rec, req)
		close(done)
	}()

	// The fast-first tick fires at interval/4 (~275ms). Give it generous
	// slack (well under half the full interval) so this can never pass by
	// accident even on a loaded CI box, while still proving it beat the full
	// 1.1s tick.
	waitForFlushes(t, rec, 1, interval/2)
	elapsed := time.Since(start)
	cancel()
	<-done

	if elapsed >= interval {
		t.Errorf("first snapshot took %v, expected well under the full %v interval (fast-first-tick did not fire)", elapsed, interval)
	}
	if !strings.Contains(rec.body(), "data: ") {
		t.Errorf("expected a data event in the body, got: %q", rec.body())
	}
}

func TestSysinfoStream_WritesEvents(t *testing.T) {
	var calls int
	var mu sync.Mutex
	sampleFn := func(_ []sysinfo.MountSpec) (sysinfo.RawSample, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		// Incrementing cumulative counters so ComputeRates yields non-trivial
		// rates on each successive pair.
		n := int64(calls)
		return sysinfo.RawSample{
			CapturedAt:          time.Now(),
			CPUUsageMicros:      n * 1000,
			MemUsedBytes:        n * 100,
			MemLimitBytes:       1 << 30,
			NetRxBytes:          n * 500,
			NetTxBytes:          n * 250,
			ContainerDiskRBytes: n * 10,
			ContainerDiskWBytes: n * 20,
			ServerDisks:         []sysinfo.DiskRaw{{Name: "sda", RBytes: n * 4096, WBytes: n * 8192}},
		}, nil
	}
	mockMounts := func(_ context.Context) []sysinfo.MountSpec {
		return []sysinfo.MountSpec{{Name: "App data", Path: t.TempDir()}}
	}

	handler := sysinfoStreamHandler(sampleFn, mockMounts, 5*time.Millisecond)

	rec := newFlushRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sysinfo/stream", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		handler(rec, req)
		close(done)
	}()

	// Wait for at least two data flushes (the initial sample is not flushed;
	// each tick produces one).
	waitForFlushes(t, rec, 2, 2*time.Second)
	cancel()
	<-done

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// Parse the `data:` lines; each must be valid SysinfoSnapshot JSON.
	var snapshots []apidto.SysinfoSnapshot
	for _, line := range strings.Split(rec.body(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var snap apidto.SysinfoSnapshot
		if err := json.Unmarshal([]byte(payload), &snap); err != nil {
			t.Fatalf("data line is not valid SysinfoSnapshot JSON: %v (%q)", err, payload)
		}
		snapshots = append(snapshots, snap)
	}
	if len(snapshots) < 2 {
		t.Fatalf("got %d snapshots, want >= 2", len(snapshots))
	}
	if len(snapshots[0].ServerDisks) != 1 || snapshots[0].ServerDisks[0].Name != "sda" {
		t.Errorf("first snapshot ServerDisks = %+v, want one sda entry", snapshots[0].ServerDisks)
	}
}

func TestSysinfoStream_SampleError_WritesErrorEvent(t *testing.T) {
	var calls int
	var mu sync.Mutex
	sampleFn := func(_ []sysinfo.MountSpec) (sysinfo.RawSample, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls >= 2 {
			return sysinfo.RawSample{}, errSampleBoom
		}
		return sysinfo.RawSample{CapturedAt: time.Now()}, nil
	}
	mockMounts := func(_ context.Context) []sysinfo.MountSpec {
		return []sysinfo.MountSpec{{Name: "App data", Path: t.TempDir()}}
	}

	handler := sysinfoStreamHandler(sampleFn, mockMounts, 5*time.Millisecond)

	rec := newFlushRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sysinfo/stream", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		handler(rec, req)
		close(done)
	}()

	// First call ok (not flushed), second call errors → one error-event flush.
	waitForFlushes(t, rec, 1, 2*time.Second)
	cancel()
	<-done

	if !strings.Contains(rec.body(), "event: sampleError") {
		t.Errorf("body missing sampleError event, got: %q", rec.body())
	}
	if !strings.Contains(rec.body(), errSampleBoom.Error()) {
		t.Errorf("body missing error message %q, got: %q", errSampleBoom.Error(), rec.body())
	}
}

// TestSysinfoStream_FirstSampleError_ClosesWithErrorEvent covers the connect-
// time sample failure path (the handler emits one error event and returns).
func TestSysinfoStream_FirstSampleError_ClosesWithErrorEvent(t *testing.T) {
	sampleFn := func(_ []sysinfo.MountSpec) (sysinfo.RawSample, error) {
		return sysinfo.RawSample{}, errSampleBoom
	}
	mockMounts := func(_ context.Context) []sysinfo.MountSpec {
		return []sysinfo.MountSpec{{Name: "App data", Path: t.TempDir()}}
	}
	handler := sysinfoStreamHandler(sampleFn, mockMounts, 5*time.Millisecond)

	rec := newFlushRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sysinfo/stream", nil)
	handler(rec, req) // returns immediately on first-sample error

	if !strings.Contains(rec.body(), "event: sampleError") {
		t.Errorf("body missing sampleError event, got: %q", rec.body())
	}
}

// errSampleBoom is a fixed sentinel so tests can assert its message survives to
// the SSE data payload.
var errSampleBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "sample boom" }
