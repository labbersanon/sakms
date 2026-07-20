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
	jobs, _, _, disconnect := r.Connect("node-a-id", "node-a", []string{"cuda"})
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
	nodeID := "node-a-id"
	_, _, _, disconnect := r.Connect(nodeID, "node-a", nil)
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
	jobs, _, _, disconnect := r.Connect("node-a-id", "node-a", nil)
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
	id := "node-a-id"
	_, _, _, disconnect := r.Connect(id, "node-a", nil)
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
	jobs, _, _, disconnect := r.Connect("node-a-id", "node-a", nil)

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

// TestStaleDisconnectDoesNotEvictNewerConnection confirms the identity guard
// added when Connect switched from a fresh-per-call id to a durable, reused
// id: a stale disconnect from an old connection must never remove a newer
// connection's entry for the same id. Before this guard, a node reconnecting
// under the same durable id while its old connection's disconnect was still
// pending would have this exact race, since both closures captured the same
// map key.
func TestStaleDisconnectDoesNotEvictNewerConnection(t *testing.T) {
	r := New()
	const id = "node-a-id"

	oldJobs, _, _, oldDisconnect := r.Connect(id, "node-a", nil)
	_ = oldJobs

	// Node reconnects under the same durable id before the old connection's
	// disconnect has run (e.g. a brief overlap during a reconnect).
	newJobs, _, _, newDisconnect := r.Connect(id, "node-a", nil)
	defer newDisconnect()

	// The stale old disconnect must be a no-op now — it must NOT evict the
	// newer connection's entry.
	oldDisconnect()

	r.mu.Lock()
	_, stillPresent := r.nodes[id]
	r.mu.Unlock()
	if !stillPresent {
		t.Fatal("stale disconnect evicted the newer connection's entry")
	}

	// The newer connection's jobs channel must still be open and usable.
	job := Job{ID: "j1", Type: JobTypePhash}
	if _, _, ok := r.Dispatch(job); !ok {
		t.Fatal("Dispatch failed after a stale disconnect wrongly closed the live node's channel")
	}
	drainOneJob(t, newJobs)
	r.ClearPending("j1")
}

// TestRequestBrowse_HappyPath confirms the targeted lookup: RequestBrowse
// reaches the one specific connected node by durable id and returns its
// answer.
func TestRequestBrowse_HappyPath(t *testing.T) {
	r := New()
	_, _, browse, disconnect := r.Connect("node-a-id", "node-a", nil)
	defer disconnect()

	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case req := <-browse:
			if req.Path != "/mnt/media" {
				t.Errorf("node received path %q, want /mnt/media", req.Path)
			}
			r.ReportBrowseResult(BrowseResult{
				RequestID: req.ID,
				Entries:   []BrowseEntry{{Name: "movies", Path: "/mnt/media/movies"}},
			})
		case <-time.After(time.Second):
			t.Error("node never received the browse request")
		}
	}()

	result, err := r.RequestBrowse("node-a-id", "/mnt/media")
	if err != nil {
		t.Fatalf("RequestBrowse returned error: %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Name != "movies" {
		t.Fatalf("got entries %+v, want one entry named movies", result.Entries)
	}
	<-done
}

// TestRequestBrowse_UnknownNodeID confirms an immediate, clear error for a
// node ID that isn't connected — not a hang, not a panic.
func TestRequestBrowse_UnknownNodeID(t *testing.T) {
	r := New()
	_, err := r.RequestBrowse("nonexistent-id", "/mnt/media")
	if err == nil {
		t.Fatal("expected an error for an unconnected node ID")
	}
}

// TestRequestBrowse_TimeoutNoFallback confirms RequestBrowse has NO local
// fallback (unlike Dispatch): when the node never answers, it returns an
// honest timeout error rather than substituting a local result.
func TestRequestBrowse_TimeoutNoFallback(t *testing.T) {
	old := browseTimeout
	browseTimeout = 20 * time.Millisecond
	defer func() { browseTimeout = old }()

	r := New()
	_, _, browse, disconnect := r.Connect("node-a-id", "node-a", nil)
	defer disconnect()

	go func() { <-browse }() // node reads the request but never answers

	_, err := r.RequestBrowse("node-a-id", "/mnt/media")
	if err == nil {
		t.Fatal("expected a timeout error, got nil (RequestBrowse must not silently fall back to anything)")
	}
}

// TestRequestBrowse_DoesNotShareStateWithJobDispatch confirms the isolated
// lane's core property: a pending browse request does not touch the phash
// pending map, and a phash Dispatch does not touch pendingBrowse.
func TestRequestBrowse_DoesNotShareStateWithJobDispatch(t *testing.T) {
	r := New()
	jobs, _, browse, disconnect := r.Connect("node-a-id", "node-a", nil)
	defer disconnect()

	go func() {
		req := <-browse
		r.ReportBrowseResult(BrowseResult{RequestID: req.ID, Entries: []BrowseEntry{{Name: "x", Path: "/x"}}})
	}()
	if _, err := r.RequestBrowse("node-a-id", "/mnt/media"); err != nil {
		t.Fatalf("RequestBrowse: %v", err)
	}

	r.mu.Lock()
	jobPendingCount := len(r.pending)
	r.mu.Unlock()
	if jobPendingCount != 0 {
		t.Fatalf("phash pending map has %d entries after a browse request, want 0 — browse and job dispatch must not share state", jobPendingCount)
	}

	// And the reverse: a normal job dispatch doesn't touch pendingBrowse.
	job := Job{ID: "j1", Type: JobTypePhash}
	if _, _, ok := r.Dispatch(job); !ok {
		t.Fatal("Dispatch ok=false unexpectedly")
	}
	drainOneJob(t, jobs)
	r.ReportResult(JobResult{JobID: "j1", Hash: "abc"})
	r.ClearPending("j1")

	r.mu.Lock()
	browsePendingCount := len(r.pendingBrowse)
	r.mu.Unlock()
	if browsePendingCount != 0 {
		t.Fatalf("pendingBrowse has %d entries after a job dispatch, want 0", browsePendingCount)
	}
}

func TestConcurrentDispatches(t *testing.T) {
	r := New()
	jobs, _, _, disconnect := r.Connect("node-a-id", "node-a", nil)
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
