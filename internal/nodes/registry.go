package nodes

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

const (
	// circuitBreakerThreshold is the number of consecutive dispatch timeouts
	// after which a node is marked dispatch-ineligible, so a wedged-but-
	// connected node stops taxing every Scan with a full per-job timeout.
	circuitBreakerThreshold = 3

	// offlineAfter is how long since the last heartbeat before ListNodes
	// reports a node offline (display status only, independent of dispatch
	// eligibility).
	offlineAfter = 90 * time.Second

	// jobsBuffer sizes each node's outbound job channel. Dispatch does a
	// non-blocking send under the lock, so the channel must be buffered for a
	// dispatch to land at all; the buffer also absorbs bursts of concurrent
	// dispatches without spuriously falling back to local.
	jobsBuffer = 64
)

// browseTimeout bounds how long RequestBrowse waits for a node's answer. This
// is an interactive, operator-initiated click (populating a directory
// picker), not a batch phash job — it should read as near-instant on a
// healthy connected node, so this is deliberately much shorter than the
// phash Dispatchers' multi-minute timeouts (main.go), which exist for
// genuinely long-running hash computation, not a directory listing.
// A package-level var, not a const, so tests can temporarily shrink it
// rather than block for the real 10s on a timeout test.
var browseTimeout = 10 * time.Second

// connectedNode is the Registry's live view of one connected node. jobs is the
// channel the SSE handler ranges over; a live jobs channel is the node's
// dispatch eligibility, while lastHeartbeat drives display status separately.
// settings carries operator-pushed NodeSettings updates (buffer 1; non-blocking
// send, latest wins). browse carries operator-initiated BrowseRequests —
// deliberately its own channel, not multiplexed onto jobs, so an interactive
// directory-browse click never shares state, buffering, or circuit-breaker
// behavior with phash job dispatch (see BrowseRequest's doc comment).
type connectedNode struct {
	name                string
	capabilities        []string
	jobs                chan Job
	settings            chan NodeSettings
	browse              chan BrowseRequest
	lastHeartbeat       time.Time
	consecutiveTimeouts int
	dispatchIneligible  bool
}

// Registry is the in-memory, mutex-guarded set of connected nodes plus the
// pending result channels for in-flight jobs and in-flight browse requests.
// Safe for concurrent use.
type Registry struct {
	mu            sync.Mutex
	nodes         map[string]*connectedNode    // key = durable node id (from the node's bearer key)
	pending       map[string]chan JobResult    // key = JobID; each channel buffered cap 1
	pendingBrowse map[string]chan BrowseResult // key = BrowseRequest.ID; each channel buffered cap 1 — structurally identical to pending but kept separate, matching the browse channel's isolation from jobs
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{
		nodes:         make(map[string]*connectedNode),
		pending:       make(map[string]chan JobResult),
		pendingBrowse: make(map[string]chan BrowseResult),
	}
}

// Connect registers the node's buffered job channel plus metadata under id —
// the durable node identity resolved from the validated bearer key, stable
// across every reconnect for this node — and returns the outbound job channel
// the SSE handler ranges over, a settings channel for operator-pushed config
// updates, and a disconnect func the handler defers. The SSE handler must emit
// ConnectAck{NodeID: id} as the first event before ranging.
//
// Because id is durable rather than freshly minted per call, a node that
// reconnects before its prior connection's disconnect has run would otherwise
// let the old connection's deferred disconnect race the new one and delete
// the new entry. disconnect() guards against this by only ever removing the
// exact *connectedNode this call created, never whatever is currently in the
// map under id.
func (r *Registry) Connect(id, name string, capabilities []string) (jobs <-chan Job, settings <-chan NodeSettings, browse <-chan BrowseRequest, disconnect func()) {
	jobsCh := make(chan Job, jobsBuffer)
	settingsCh := make(chan NodeSettings, 1)
	browseCh := make(chan BrowseRequest, 1)
	self := &connectedNode{
		name:          name,
		capabilities:  capabilities,
		jobs:          jobsCh,
		settings:      settingsCh,
		browse:        browseCh,
		lastHeartbeat: time.Now(),
	}

	r.mu.Lock()
	r.nodes[id] = self
	r.mu.Unlock()

	var once sync.Once
	disconnect = func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if cur, ok := r.nodes[id]; !ok || cur != self {
				return
			}
			delete(r.nodes, id)
			close(jobsCh)
			close(settingsCh)
			close(browseCh)
		})
	}
	return jobsCh, settingsCh, browseCh, disconnect
}

// SendSettings pushes an updated NodeSettings to the connected node identified
// by nodeID via its SSE stream. Returns false when the node is not connected or
// its settings channel is already full (the node will receive updated settings
// on its next reconnect instead).
func (r *Registry) SendSettings(nodeID string, s NodeSettings) bool {
	r.mu.Lock()
	n, ok := r.nodes[nodeID]
	if !ok {
		r.mu.Unlock()
		return false
	}
	select {
	case n.settings <- s:
		r.mu.Unlock()
		return true
	default:
		r.mu.Unlock()
		return false
	}
}

// RequestBrowse asks one specific, already-connected node to list a
// directory, and blocks until it answers or browseTimeout elapses.
// Deliberately has NO local-fallback branch, unlike Dispatch: there is no
// sensible "local" equivalent of listing a specific node's filesystem, so a
// timeout or missing node is reported as an honest error to the caller
// instead of silently substituting something else.
func (r *Registry) RequestBrowse(nodeID, path string) (BrowseResult, error) {
	r.mu.Lock()
	n, ok := r.nodes[nodeID]
	if !ok {
		r.mu.Unlock()
		return BrowseResult{}, fmt.Errorf("node %s not connected", nodeID)
	}

	reqID := rand.Text()
	res := make(chan BrowseResult, 1)
	r.pendingBrowse[reqID] = res

	select {
	case n.browse <- BrowseRequest{ID: reqID, Path: path}:
		r.mu.Unlock()
	default:
		delete(r.pendingBrowse, reqID)
		r.mu.Unlock()
		return BrowseResult{}, fmt.Errorf("node %s already has a browse request in flight", nodeID)
	}

	defer func() {
		r.mu.Lock()
		delete(r.pendingBrowse, reqID)
		r.mu.Unlock()
	}()

	select {
	case result := <-res:
		if result.Error != "" {
			return BrowseResult{}, fmt.Errorf("node reported: %s", result.Error)
		}
		return result, nil
	case <-time.After(browseTimeout):
		return BrowseResult{}, fmt.Errorf("node %s did not respond within %s — is it still connected?", nodeID, browseTimeout)
	}
}

// ReportBrowseResult delivers a node's BrowseResult to the waiting
// RequestBrowse caller by RequestID via a non-blocking send into the cap-1
// pendingBrowse channel. Safe no-op for an unknown or already-cleaned-up
// RequestID — never panics, never blocks — mirroring ReportResult's
// contract for the phash path.
func (r *Registry) ReportBrowseResult(res BrowseResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.pendingBrowse[res.RequestID]
	if !ok {
		return
	}
	select {
	case ch <- res:
	default:
	}
}

// Heartbeat updates LastHeartbeat for a node id (display liveness only).
// No-op for an unknown/expired id.
func (r *Registry) Heartbeat(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := r.nodes[id]; ok {
		n.lastHeartbeat = time.Now()
	}
}

// Dispatch selects a dispatch-eligible node, registers a pending result
// channel (buffered cap 1), and non-blocking-sends the Job onto that node's
// buffered job channel. On success it returns the selected node's id, the
// result channel, and ok=true. It returns ok=false when no node is eligible or
// the selected node's job buffer is full — in both cases the caller runs local.
func (r *Registry) Dispatch(job Job) (nodeID string, result <-chan JobResult, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var target *connectedNode
	for id, n := range r.nodes {
		if n.dispatchIneligible {
			continue
		}
		nodeID = id
		target = n
		break
	}
	if target == nil {
		return "", nil, false
	}

	res := make(chan JobResult, 1)
	r.pending[job.ID] = res

	// Non-blocking send under the lock: a full buffer is back-pressure, not a
	// reason to block the whole Registry. Unwind the pending entry and fall
	// back to local when the buffer is full.
	select {
	case target.jobs <- job:
		return nodeID, res, true
	default:
		delete(r.pending, job.ID)
		return "", nil, false
	}
}

// noteTimeout increments the given node's consecutive-timeout counter and, at
// circuitBreakerThreshold, marks it dispatch-ineligible. No-op for unknown id.
func (r *Registry) noteTimeout(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[nodeID]
	if !ok {
		return
	}
	n.consecutiveTimeouts++
	if n.consecutiveTimeouts >= circuitBreakerThreshold {
		n.dispatchIneligible = true
	}
}

// noteSuccess resets the given node's consecutive-timeout counter and clears
// its dispatch-ineligible flag, re-enabling it for future dispatch. Called by
// the Dispatcher's success branch, where nodeID is in scope. No-op for unknown
// id. ReportResult cannot do this: it has only a JobID, not a nodeID.
func (r *Registry) noteSuccess(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n, ok := r.nodes[nodeID]; ok {
		n.consecutiveTimeouts = 0
		n.dispatchIneligible = false
	}
}

// ClearPending removes a JobID's pending entry. Idempotent: a second call for
// an already-removed JobID is a safe no-op. The Dispatcher must call this on
// every exit path so the pending map cannot leak.
func (r *Registry) ClearPending(jobID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, jobID)
}

// ReportResult delivers a node's JobResult to the waiting Dispatch caller by
// JobID via a non-blocking send into the cap-1 pending channel. Safe no-op for
// an unknown or already-cleaned-up JobID — never panics, never blocks. Does
// NOT reset the circuit breaker; that reset lives in the Dispatcher's success
// branch, which knows the responding node.
func (r *Registry) ReportResult(res JobResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch, ok := r.pending[res.JobID]
	if !ok {
		return
	}
	// Non-blocking send: the cap-1 buffer accepts the first result even if the
	// waiter has already given up; a second (duplicate) result is dropped.
	select {
	case ch <- res:
	default:
	}
}

// ListNodes returns a snapshot for the Settings tab. Capabilities is copied so
// callers cannot mutate registry state. Callers derive online/offline from
// LastHeartbeat via offlineAfter.
func (r *Registry) ListNodes() []NodeInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]NodeInfo, 0, len(r.nodes))
	for id, n := range r.nodes {
		caps := append([]string(nil), n.capabilities...)
		out = append(out, NodeInfo{
			ID:            id,
			Name:          n.name,
			Capabilities:  caps,
			LastHeartbeat: n.lastHeartbeat,
		})
	}
	return out
}

// Offline reports whether a heartbeat time is stale enough to display the node
// as offline. Exposed so callers deriving status share one threshold.
func Offline(lastHeartbeat time.Time) bool {
	return time.Since(lastHeartbeat) > offlineAfter
}
