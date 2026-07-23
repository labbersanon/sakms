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
	EventPending       = "pending"       // server → node: pairing code assigned
	EventConfig        = "config"        // server → node: approved; carries apiKey + settings
	EventSettings      = "settings"      // server → node: operator updated path mappings / maxJobs
	EventBrowseRequest = "browseRequest" // server → node: operator wants a directory listing
)

// PathMapping translates one server-side path prefix to its local equivalent
// on the worker node's filesystem.
//
// Key is the library-path key this prefix pair was derived from (e.g.
// movies_library_root_folder). It is INERT display metadata only: the node's
// Remap (longest-Server-prefix match) and mergePathMap (add/replace-by-Server
// dedup) never key off it — it exists purely so the node-side tray can label a
// live Remap row with its originating key, including legacy operator-authored
// mappings the node never recorded in its own AuthoredPaths. Populating it does
// NOT make merge key-aware/delete-capable (D7 stays intact). Modeled as string
// (not apidto.LibraryPathKey) so this package keeps its zero apidto dependency.
type PathMapping struct {
	Server string `json:"server"`
	Local  string `json:"local"`
	Key    string `json:"key,omitempty"`
}

// NodeSettings is the operator-controlled configuration pushed to a node at
// approval time and on any subsequent settings update.
type NodeSettings struct {
	PathMap []PathMapping `json:"pathMap"`
	MaxJobs int           `json:"maxJobs"` // 0 = unlimited
	// CPUCapPercent is the operator-owned max-CPU governor ("% of total CPU",
	// 0 = unlimited) pushed to the node so its Stage-3 daemon can enforce a real
	// cgroup CPU ceiling. Like MaxJobs, every sender of a NodeSettings frame MUST
	// populate this from the node's STORED cap, not a zero value — an omitted
	// field marshals 0 and would silently clear the node's applied cap. The lone
	// exception is the approval/pairing push, where a freshly-approved node has
	// no persisted cap yet and the zero value 0 (unlimited) is correct.
	CPUCapPercent int `json:"cpuCapPercent"` // 0 = unlimited
	// PauseDispatch is the server-owned dispatch-exclusion bit echoed to the
	// node for tray display only (the authoritative dispatch decision is the
	// server-side connectedNode.paused, never this frame). Every sender of a
	// NodeSettings frame MUST populate this from the node's STORED pause value,
	// not a zero value — an omitted field marshals false and would silently
	// clear the node's cached pause display (P7). The lone exception is the
	// approval/pairing push, where a freshly-approved node has no persisted
	// pause yet and the zero value false is correct.
	PauseDispatch bool `json:"pauseDispatch"`
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
	ID            string    // durable node_keys.id (minted once by nodekeys.Create, stable across every reconnect)
	Name          string    // node self-reported
	Capabilities  []string  // hwaccels reported at connect, e.g. ["cuda"]
	LastHeartbeat time.Time //
	// Enforcement, CPUCapEffective, and CPUCapApplyErr carry the node's live CPU
	// governor status, reported on each heartbeat (the node's own
	// capState.snapshot()). Enforcement is the STATIC capability
	// ("available"|"unavailable"|"" not yet reported); CPUCapEffective +
	// CPUCapApplyErr are the LAST-APPLY result (quota actually in force + last
	// error). Zero values honestly read as "nothing reported yet", never a
	// fabricated success — an older node binary that omits them leaves them zero.
	Enforcement     string
	CPUCapEffective int
	CPUCapApplyErr  string
}

// ConnectAck is the first SSE event the server sends on a new stream, before
// any Job, handing the node its durable node_keys.id — the same id minted
// once at approval time and reused unchanged on every subsequent reconnect —
// for use in subsequent heartbeat and result POSTs.
//
// LibraryPathKeys carries the server's bounded library-path-key catalog (D4):
// the fixed set of keys a node may author a path mapping for, so the node-side
// UI can render pickers for the ones it hasn't configured yet. It is a
// compile-time constant server-side (see internal/api's libraryPathKeys), so
// piggybacking it on the connect ack — sent once, on connect — avoids a
// separate node-auth catalog endpoint. Modeled as []string (not
// []apidto.LibraryPathKey) so this package takes no dependency on apidto; the
// api layer converts when it populates the ack.
type ConnectAck struct {
	NodeID          string   `json:"nodeId"`
	LibraryPathKeys []string `json:"libraryPathKeys,omitempty"`
}

// BrowseRequest is one directory-listing request pushed to a specific,
// already-connected node — a deliberately isolated lane, not a Job: browsing
// has no meaningful local fallback (there is nothing sensible to fall back
// to when the operator wants to see THIS node's filesystem), so it does not
// share state, circuit-breaker behavior, or wire shape with the phash
// Job/JobResult path. ID correlates the eventual BrowseResult back to the
// waiting RequestBrowse caller.
type BrowseRequest struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// IncludeFiles requests files as well as directories in the response.
	// Default false preserves the operator-facing folder picker's existing
	// dirs-only UX; the security-hardening addendum's mapping-verification
	// safeguard is the only caller that sets this true, since a flat
	// (file-only, no-subdirectories) library must not silently compare as
	// empty on every save.
	IncludeFiles bool `json:"includeFiles,omitempty"`
}

// BrowseResult is a node's POSTed answer for one BrowseRequest. Exactly one
// of Entries/Error is meaningful, mirroring JobResult's Hash/Error
// convention.
type BrowseResult struct {
	RequestID string        `json:"requestId"`
	Entries   []BrowseEntry `json:"entries,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// BrowseEntry is one directory or file a node reports back for a
// BrowseRequest — a file only appears when the request set IncludeFiles.
type BrowseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
