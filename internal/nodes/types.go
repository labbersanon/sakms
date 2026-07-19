// Package nodes holds the server-side worker-node infrastructure: the
// in-memory Registry of connected nodes and the Dispatcher that bridges the
// synchronous PHasher interface (called inside Scan loops) to asynchronous
// SSE job dispatch, with transparent local fallback. The node is an
// accelerator, never a dependency — every dispatch path falls back to local
// execution when no eligible node is connected, a job times out, the operator
// cancels, or a node drops mid-job.
package nodes

import "time"

// JobType enumerates the kinds of work dispatched to a node. Extensible: v1
// carries phash and videophash; thumbnail/transcode slot in later without
// touching the dispatch/registry/transport core.
type JobType string

const (
	JobTypePhash      JobType = "phash"      // internal/phash (Movies/Series)
	JobTypeVideoPhash JobType = "videophash" // internal/videophash (Adult)
)

// Job is one unit of work dispatched to a node over SSE.
type Job struct {
	ID         string  `json:"id"`         // fresh crypto/rand.Text() per job; no uuid dependency
	Type       JobType `json:"type"`       //
	ServerPath string  `json:"serverPath"` // absolute path on server; node remaps before opening
}

// JobResult is a node's POSTed answer for one Job. Exactly one of Hash/Error
// is meaningful: a non-empty Hash is a success, a non-empty Error tells the
// server to fall back to local execution for that job.
type JobResult struct {
	JobID string `json:"jobId"`
	Hash  string `json:"hash,omitempty"`
	Error string `json:"error,omitempty"`
}

// NodeInfo is the server's live view of one connected node, returned by
// ListNodes for the Settings → Nodes tab.
type NodeInfo struct {
	ID            string    // server-assigned connection id (crypto/rand.Text(); ephemeral)
	Name          string    // node self-reported
	Capabilities  []string  // hwaccels reported at connect, e.g. ["cuda"]
	LastHeartbeat time.Time //
}

// ConnectAck is the first SSE event the server sends on a new stream, before
// any Job, handing the node its server-assigned id for use in subsequent
// heartbeat and result POSTs.
type ConnectAck struct {
	NodeID string `json:"nodeId"`
}
