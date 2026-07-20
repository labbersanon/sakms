package nodes

import (
	"sync"
	"testing"
	"time"
)

// drainOneJob reads a single job off a node's SSE job channel (the SSE handler
// would range over this), so tests can act as the node.
func drainOneJob(t *testing.T, jobs <-chan Job) Job {
	t.Helper()
	select {
	case j := <-jobs:
		return j
	case <-time.After(time.Second):
		t.Fatal("expected a job on the node channel, got none")
		return Job{}
	}
}

func TestDispatchThenResultHappyPath(t *testing.T) {
	r := New()
	_, jobs, _, disconnect := r.Connect("node-a", []string{"cuda"})
	defer disconnect()

	job := Job{ID: "j1", Type: JobTypePhash, ServerPath: "/srv/x.mkv"}
	nodeID, result, ok := r.Dispatch(job)
	if !ok {
		t.Fatal("Dispatch returned ok=false with a connected node")
	}
	if nodeID == "" {
		t.Fatal("Dispatch returned empty nodeID")
	}

	got := drainOneJob(t, jobs)
	if got.ID != "j1" {
		t.Fatalf("node received job %q, want j1", got.ID)
	}

	r.ReportResult(JobResult{JobID: "j1", Hash: "abc"})
	select {
	case res := <-result:
		if res.Hash != "abc" {
			t.Fatalf("result hash %q, want abc", res.Hash)
		}
	case <-time.After(time.Second):
		t.Fatal("result never delivered")
	}
	r.ClearPending("j1")
}

func TestDispatchNoNodeFallback(t *testing.T) {
	r := New()
	_, _, ok := r.Dispatch(Job{ID: "j1", Type: JobTypePhash})
	if ok {
		t.Fatal("Dispatch returned ok=true with no connected node")
	}
}

func TestDispatchIneligibleNodeSkipped(t *testing.T) {
	r := New()
	nodeID, _, _, disconnect := r.Connect("node-a", nil)
	defer disconnect()

	// Drive the circuit breaker to threshold.
	for i := 0; i < circuitBreakerThreshold; i++ {
		r.noteTimeout(nodeID)
	}
	if _, _, ok := r.Dispatch(Job{ID: "j1"}); ok {
		t.Fatal("Dispatch selected a circuit-broken node")
	}

	// noteSuccess re-enables it.
	r.noteSuccess(nodeID)
	if _, _, ok := r.Dispatch(Job{ID: "j2"}); !ok {
		t.Fatal("Dispatch skipped a recovered node")
	}
	r.ClearPending("j2")
}

func TestReportResultUnknownJobIDNoOp(t *testing.T) {
	r := New()
	// Must not panic or block for a JobID that was never created.
	done := make(chan struct{})
	go func() {
		r.ReportResult(JobResult{JobID: "never-existed", Hash: "x"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ReportResult blocked on an unknown JobID")
	}
}

func TestLateResultAfterFallbackNoOp(t *testing.T) {
	r := New()
	_, jobs, _, disconnect := r.Connect("node-a", nil)
	defer disconnect()

	job := Job{ID: "j1", Type: JobTypePhash}
	_, _, ok := r.Dispatch(job)
	if !ok {
		t.Fatal("Dispatch ok=false unexpectedly")
	}
	drainOneJob(t, jobs)

	// Simulate the Dispatcher timing out and falling back: it reaps the entry.
	r.ClearPending("j1")

	// The node POSTs its result late. This must be a safe no-op.
	done := make(chan struct{})
	go func() {
		r.ReportResult(JobResult{JobID: "j1", Hash: "late"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("late ReportResult blocked after the entry was cleared")
	}
}

func TestHeartbeatDrivenOnlineOffline(t *testing.T) {
	r := New()
	id, _, _, disconnect := r.Connect("node-a", nil)
	defer disconnect()

	nodesList := r.ListNodes()
	if len(nodesList) != 1 {
		t.Fatalf("ListNodes len %d, want 1", len(nodesList))
	}
	if Offline(nodesList[0].LastHeartbeat) {
		t.Fatal("fresh node reported offline")
	}

	// Force a stale heartbeat by reaching into the node under the lock.
	r.mu.Lock()
	r.nodes[id].lastHeartbeat = time.Now().Add(-2 * offlineAfter)
	r.mu.Unlock()

	nodesList = r.ListNodes()
	if !Offline(nodesList[0].LastHeartbeat) {
		t.Fatal("stale node reported online")
	}

	// A fresh heartbeat brings it back online.
	r.Heartbeat(id)
	nodesList = r.ListNodes()
	if Offline(nodesList[0].LastHeartbeat) {
		t.Fatal("heartbeated node still offline")
	}
}

func TestHeartbeatUnknownIDNoOp(t *testing.T) {
	r := New()
	r.Heartbeat("nope") // must not panic
}

func TestDisconnectMidJobFallback(t *testing.T) {
	r := New()
	_, jobs, _, disconnect := r.Connect("node-a", nil)

	job := Job{ID: "j1", Type: JobTypePhash}
	_, result, ok := r.Dispatch(job)
	if !ok {
		t.Fatal("Dispatch ok=false unexpectedly")
	}
	drainOneJob(t, jobs)

	// Node drops mid-job: disconnect closes the jobs channel and removes the
	// node. The pending result channel is never fed, so a Dispatcher would time
	// out and fall back. Assert disconnect is safe and the result never lands.
	disconnect()
	disconnect() // idempotent second call must not panic (no double close)

	select {
	case res := <-result:
		t.Fatalf("unexpected result after disconnect: %+v", res)
	case <-time.After(50 * time.Millisecond):
		// expected: nothing delivered
	}
	r.ClearPending("j1")
}

func TestConcurrentDispatches(t *testing.T) {
	r := New()
	_, jobs, _, disconnect := r.Connect("node-a", nil)
	defer disconnect()

	const n = 4
	var wg sync.WaitGroup
	results := make([]<-chan JobResult, n)
	oks := make([]bool, n)
	ids := []string{"c0", "c1", "c2", "c3"}

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, res, ok := r.Dispatch(Job{ID: ids[i], Type: JobTypePhash})
			results[i], oks[i] = res, ok
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if !oks[i] {
			t.Fatalf("dispatch %d fell back despite a live node with a %d-deep buffer", i, jobsBuffer)
		}
	}

	// Drain all four jobs the node was handed and answer each.
	seen := make(map[string]bool)
	for i := 0; i < n; i++ {
		j := drainOneJob(t, jobs)
		seen[j.ID] = true
		r.ReportResult(JobResult{JobID: j.ID, Hash: "h-" + j.ID})
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("node never received job %q", id)
		}
	}
	for i := 0; i < n; i++ {
		select {
		case res := <-results[i]:
			if res.Hash == "" {
				t.Fatalf("dispatch %d got empty hash", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("dispatch %d never got a result", i)
		}
		r.ClearPending(ids[i])
	}
}
