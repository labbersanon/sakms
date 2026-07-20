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
