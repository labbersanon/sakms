package nodes

import (
	"crypto/rand"
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

// connectedNode is the Registry's live view of one connected node. jobs is the
// channel the SSE handler ranges over; a live jobs channel is the node's
// dispatch eligibility, while lastHeartbeat drives display status separately.
// settings carries operator-pushed NodeSettings updates (buffer 1; non-blocking
// send, latest wins).
type connectedNode struct {
	name                string
	capabilities        []string
	jobs                chan Job
	settings            chan NodeSettings
	lastHeartbeat       time.Time
	consecutiveTimeouts int
	dispatchIneligible  bool
}

// Registry is the in-memory, mutex-guarded set of connected nodes plus the
// pending result channels for in-flight jobs. Safe for concurrent use.
type Registry struct {
	mu      sync.Mutex
	nodes   map[string]*connectedNode // key = server-assigned id
	pending map[string]chan JobResult // key = JobID; each channel buffered cap 1
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{
		nodes:   make(map[string]*connectedNode),
		pending: make(map[string]chan JobResult),
	}
}

// Connect assigns a fresh ephemeral id, registers the node's buffered job
// channel plus metadata, and returns the id, the outbound job channel the SSE
// handler ranges over, a settings channel for operator-pushed config updates,
// and a disconnect func the handler defers. The SSE handler must emit
// ConnectAck{NodeID: id} as the first event before ranging.
func (r *Registry) Connect(name string, capabilities []string) (id string, jobs <-chan Job, settings <-chan NodeSettings, disconnect func()) {
	id = rand.Text()
	jobsCh := make(chan Job, jobsBuffer)
	settingsCh := make(chan NodeSettings, 1)

	r.mu.Lock()
	r.nodes[id] = &connectedNode{
		name:          name,
		capabilities:  capabilities,
		jobs:          jobsCh,
		settings:      settingsCh,
		lastHeartbeat: time.Now(),
	}
	r.mu.Unlock()

	var once sync.Once
	disconnect = func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if _, ok := r.nodes[id]; !ok {
				return
			}
			delete(r.nodes, id)
			close(jobsCh)
			close(settingsCh)
		})
	}
	return id, jobsCh, settingsCh, disconnect
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
