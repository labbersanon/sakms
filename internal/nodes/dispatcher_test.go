package nodes

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeLocal is a LocalHasher that records whether it was called and returns a
// fixed hash (or error) — the fallback under test.
type fakeLocal struct {
	called atomic.Int32
	hash   string
	err    error
}

func (f *fakeLocal) Hash(_ context.Context, _ string) (string, error) {
	f.called.Add(1)
	return f.hash, f.err
}

func TestDispatcherDispatchesToNode(t *testing.T) {
	r := New()
	jobs, _, _, disconnect := r.Connect("node-a-id", "node-a", nil)
	defer disconnect()

	local := &fakeLocal{hash: "local"}
	d := NewDispatcher(r, JobTypePhash, local, time.Second)

	// Act as the node: read the dispatched job and report a node hash.
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case j := <-jobs:
			r.ReportResult(JobResult{JobID: j.ID, Hash: "node-hash"})
		case <-time.After(time.Second):
		}
	}()

	got, err := d.Hash(context.Background(), "/srv/x.mkv")
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	if got != "node-hash" {
		t.Fatalf("got %q, want node-hash (node result, not local)", got)
	}
	if local.called.Load() != 0 {
		t.Fatal("local hasher was called despite a node result")
	}
	<-done
}

func TestDispatcherFallsBackWhenNoNode(t *testing.T) {
	r := New()
	local := &fakeLocal{hash: "local"}
	d := NewDispatcher(r, JobTypePhash, local, time.Second)

	got, err := d.Hash(context.Background(), "/srv/x.mkv")
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	if got != "local" {
		t.Fatalf("got %q, want local", got)
	}
	if local.called.Load() != 1 {
		t.Fatalf("local called %d times, want 1", local.called.Load())
	}
}

func TestDispatcherFallsBackOnTimeout(t *testing.T) {
	r := New()
	nodeID := "node-a-id"
	jobs, _, _, disconnect := r.Connect(nodeID, "node-a", nil)
	defer disconnect()

	local := &fakeLocal{hash: "local"}
	d := NewDispatcher(r, JobTypePhash, local, 20*time.Millisecond)

	// The node reads the job but never answers → dispatcher times out.
	go func() { <-jobs }()

	got, err := d.Hash(context.Background(), "/srv/x.mkv")
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	if got != "local" {
		t.Fatalf("got %q, want local (timeout fallback)", got)
	}
	if local.called.Load() != 1 {
		t.Fatalf("local called %d times, want 1", local.called.Load())
	}

	// A true timeout must have driven the circuit breaker by one.
	r.mu.Lock()
	ct := r.nodes[nodeID].consecutiveTimeouts
	r.mu.Unlock()
	if ct != 1 {
		t.Fatalf("consecutiveTimeouts = %d after one timeout, want 1", ct)
	}
}

func TestDispatcherFallsBackOnNodeError(t *testing.T) {
	r := New()
	nodeID := "node-a-id"
	jobs, _, _, disconnect := r.Connect(nodeID, "node-a", nil)
	defer disconnect()

	local := &fakeLocal{hash: "local"}
	d := NewDispatcher(r, JobTypePhash, local, time.Second)

	go func() {
		j := <-jobs
		r.ReportResult(JobResult{JobID: j.ID, Error: "decode failed"})
	}()

	got, err := d.Hash(context.Background(), "/srv/x.mkv")
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	if got != "local" {
		t.Fatalf("got %q, want local (node-error fallback)", got)
	}
	// A node-reported error is not a timeout: the breaker must stay at 0.
	r.mu.Lock()
	ct := r.nodes[nodeID].consecutiveTimeouts
	r.mu.Unlock()
	if ct != 0 {
		t.Fatalf("consecutiveTimeouts = %d after a node error, want 0", ct)
	}
}

func TestDispatcherCtxCancelDoesNotPenalizeNode(t *testing.T) {
	r := New()
	nodeID := "node-a-id"
	jobs, _, _, disconnect := r.Connect(nodeID, "node-a", nil)
	defer disconnect()

	local := &fakeLocal{hash: "local"}
	// Long timeout so ctx-cancel wins the select, not the timer.
	d := NewDispatcher(r, JobTypePhash, local, time.Minute)

	go func() { <-jobs }() // node reads but never answers

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before Hash selects

	got, err := d.Hash(ctx, "/srv/x.mkv")
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	if got != "local" {
		t.Fatalf("got %q, want local (ctx-cancel fallback)", got)
	}
	if local.called.Load() != 1 {
		t.Fatalf("local called %d times, want 1", local.called.Load())
	}

	// ctx-cancel is an operator action, not a node fault: breaker stays at 0.
	r.mu.Lock()
	ct := r.nodes[nodeID].consecutiveTimeouts
	r.mu.Unlock()
	if ct != 0 {
		t.Fatalf("consecutiveTimeouts = %d after ctx-cancel, want 0 (no penalty)", ct)
	}
}

func TestDispatcherSuccessResetsCircuitBreaker(t *testing.T) {
	r := New()
	nodeID := "node-a-id"
	jobs, _, _, disconnect := r.Connect(nodeID, "node-a", nil)
	defer disconnect()

	// Pre-load the breaker below threshold.
	r.noteTimeout(nodeID)
	r.noteTimeout(nodeID)

	local := &fakeLocal{hash: "local", err: errors.New("should not be used")}
	d := NewDispatcher(r, JobTypePhash, local, time.Second)

	go func() {
		j := <-jobs
		r.ReportResult(JobResult{JobID: j.ID, Hash: "ok"})
	}()

	if _, err := d.Hash(context.Background(), "/srv/x.mkv"); err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	r.mu.Lock()
	ct := r.nodes[nodeID].consecutiveTimeouts
	r.mu.Unlock()
	if ct != 0 {
		t.Fatalf("consecutiveTimeouts = %d after a success, want 0", ct)
	}
}
