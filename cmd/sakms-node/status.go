package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
)

// nodeState is the current lifecycle state of the node daemon.
type nodeState string

const (
	stateDisconnected nodeState = "disconnected"
	statePending      nodeState = "pending"
	stateConnected    nodeState = "connected"
)

// statusSnapshot is the JSON payload returned by GET /status.
type statusSnapshot struct {
	State       nodeState `json:"state"`
	PairingCode string    `json:"pairingCode,omitempty"` // non-empty only when pending
	ServerURL   string    `json:"serverURL"`
	DeviceName  string    `json:"deviceName"`
	NodeID      string    `json:"nodeID,omitempty"` // non-empty when connected

	// Warning is the security-hardening addendum's Safeguard 2 surfacing:
	// the mediaRoots grace-period notice ("mediaRoots is not configured")
	// or the most recent rejected settings push's reason. Net-new field —
	// this reaches only an operator with local or tray access to this
	// specific node; it does NOT reach an operator working from the
	// server's web UI on a headless node (a container, a remote/borrowed
	// GPU box) — that gap is closed by the fuller correlation-ID ack, a
	// deferred Follow-up, not this field.
	Warning string `json:"warning,omitempty"`

	// MediaRootScopes reports, per configured mediaRoots entry, its Phase 2
	// (OS-level namespace containment) state: app_level_only,
	// namespace_scoped, or namespace_scoped_but_unbound (see mediaRootScope).
	// Computed fresh on each GET /status (mediaroots_scope.go) — it depends on
	// the apply marker and the daemon's live /proc/self/mountinfo view, both of
	// which change independently of the daemon's connection lifecycle. Empty
	// when mediaRoots is unset (the grace period).
	MediaRootScopes []mediaRootStatus `json:"mediaRootScopes,omitempty"`

	// CPUCapPercent is the node's configured max-CPU governor ("% of total CPU",
	// 0 = unlimited/unset), read fresh from cfg on each GET /status.
	CPUCapPercent int `json:"cpuCapPercent"`
	// Enforcement is the STATIC capability report (available|unavailable): whether
	// a real cgroup CPU cap can work on this node at all. Deliberately DISTINCT
	// from CPUCapApply — a mechanism being present at startup does not mean any
	// specific apply succeeded, so this must never stand in for actual
	// enforcement. Empty until the governor probe has run.
	Enforcement string `json:"enforcement,omitempty"`
	// CPUCapApply is the LAST-APPLY result: the quota actually in force right now
	// plus any error from the most recent apply attempt — reality, not intent.
	// Kept as two fields, never collapsed with Enforcement, so a forced apply
	// failure surfaces as available + an error, never a silent success.
	CPUCapApply cpuCapApplyResult `json:"cpuCapApply"`
}

// statusServer exposes GET /status on localhost:port so the tray app can poll
// the daemon's current lifecycle state without any auth or file I/O.
type statusServer struct {
	mu   sync.RWMutex
	snap statusSnapshot
	cfg  *NodeConfig
	// capState carries the CPU-governor reporting values (static enforcement +
	// last-apply result). Attached once at startup via attachCapState, after the
	// status server is constructed but before it serves the CPU-cap fields. nil
	// until attached (e.g. in unit tests that only exercise the pre-existing
	// fields), in which case the CPU-cap fields report their zero values.
	capState *capState
}

func newStatusServer(cfg *NodeConfig) *statusServer {
	return &statusServer{
		cfg: cfg,
		snap: statusSnapshot{
			State:      stateDisconnected,
			ServerURL:  cfg.ServerURL,
			DeviceName: cfg.NodeName,
		},
	}
}

// update transitions the daemon state and updates the snapshot atomically.
// pairingCode is only meaningful when state == statePending.
// nodeID is only meaningful when state == stateConnected.
func (s *statusServer) update(state nodeState, pairingCode, nodeID string) {
	s.mu.Lock()
	s.snap.State = state
	s.snap.PairingCode = pairingCode
	s.snap.NodeID = nodeID
	s.snap.ServerURL = s.cfg.ServerURL
	s.snap.DeviceName = s.cfg.NodeName
	s.mu.Unlock()
}

// attachCapState wires the CPU-governor reporting holder into the status server.
// Called once at startup, before ListenAndServe begins serving CPU-cap fields.
func (s *statusServer) attachCapState(cs *capState) {
	s.mu.Lock()
	s.capState = cs
	s.mu.Unlock()
}

// setWarning records the security-hardening addendum's most recent
// Safeguard 2 notice (the mediaRoots grace-period warning, or a rejected
// settings push's reason) for display via GET /status. Independent of
// update() — a warning persists across connection-state transitions (e.g.
// a reconnect) until superseded by a newer call, rather than being cleared
// on every state change.
func (s *statusServer) setWarning(msg string) {
	s.mu.Lock()
	s.snap.Warning = msg
	s.mu.Unlock()
}

// ListenAndServe starts the local HTTP server and blocks until ctx is
// cancelled.
func (s *statusServer) ListenAndServe(ctx context.Context) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		snap := s.snap
		cs := s.capState
		s.mu.RUnlock()
		// Computed at read time: the marker and /proc/self/mountinfo change
		// independently of the daemon's connection lifecycle, so this must not
		// be baked in at update()/setWarning() time. MediaRoots is mutated at
		// runtime (401 re-pair, pairing, and the control socket), so it is read
		// through cfg.snapshot() under the config lock, not directly.
		// NON-AUTHORITATIVE observability only: this field and the apply marker it reflects are never a security-decision input — the marker is forgeable (unprivileged sakms-node owns /etc/sakms-node and can unlink/recreate the root-owned 0640 file), so real enforcement lives solely in mediaroots.go's withinMediaRoots/validateSettingsPush, which reads live config, never the marker.
		_, mediaRoots := s.cfg.snapshot()
		snap.MediaRootScopes = mediaRootScopes(mediaRoots)
		// CPU governor: configured percent read fresh from cfg (mutated at
		// runtime under the config lock), the static enforcement + last-apply
		// result from the capState holder (populated by the async applier /
		// startup re-apply). Kept as two DISTINCT reporting values so a
		// forced/simulated apply failure surfaces as available + an error, never
		// silent success.
		snap.CPUCapPercent = s.cfg.cpuCapSnapshot()
		if cs != nil {
			snap.Enforcement, snap.CPUCapApply = cs.snapshot()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snap) //nolint:errcheck
	})

	addr := fmt.Sprintf("127.0.0.1:%d", s.cfg.statusPort())
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background()) //nolint:errcheck
	}()

	log.Printf("sakms-node: status server on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("sakms-node: status server: %v", err)
	}
}
