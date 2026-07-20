// Package nodes holds the server-side worker-node infrastructure: the
// in-memory Registry of connected nodes and the Dispatcher that bridges the
// synchronous PHasher interface (called inside Scan loops) to asynchronous
// SSE job dispatch, with transparent local fallback. The node is an
// accelerator, never a dependency — every dispatch path falls back to local
// execution when no eligible node is connected, a job times out, the operator
// cancels, or a node drops mid-job.
package nodes

import "time"

// SSE event name constants for the pre-auth pairing stream and the
// authenticated job stream.
const (
	EventPending  = "pending"  // server → node: pairing code assigned
	EventConfig   = "config"   // server → node: approved; carries apiKey + settings
	EventSettings = "settings" // server → node: operator updated path mappings / maxJobs
)

// PathMapping translates one server-side path prefix to its local equivalent
// on the worker node's filesystem.
type PathMapping struct {
	Server string `json:"server"`
	Local  string `json:"local"`
}

// NodeSettings is the operator-controlled configuration pushed to a node at
// approval time and on any subsequent settings update.
type NodeSettings struct {
	PathMap []PathMapping `json:"pathMap"`
	MaxJobs int           `json:"maxJobs"` // 0 = unlimited
}

// PairConfig is the payload carried in the SSE "config" event that closes the
// pre-auth pairing stream. The node persists APIKey to disk and reconnects on
// the authenticated stream.
type PairConfig struct {
	APIKey   string       `json:"apiKey"`
	Settings NodeSettings `json:"settings"`
}

// PendingNodeInfo is the server's external view of one unconfirmed node,
// returned by ListPending for the Settings → Nodes UI.
type PendingNodeInfo struct {
	ID          string
	Name        string
	PairingCode string
	RequestedAt time.Time
}

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
