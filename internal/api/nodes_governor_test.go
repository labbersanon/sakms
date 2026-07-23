package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/nodes"
	"github.com/labbersanon/sakms/internal/nodesettings"
)

// TestNodeHeartbeatHandler_PassesGovernorFieldsThrough proves the server-side
// decode of the Stage-3b heartbeat: a body carrying the node's live governor
// status (built exactly as cmd/sakms-node's postHeartbeat builds it) is decoded
// and handed to Registry.Heartbeat, then surfaced through ListNodes. Calls the
// handler directly with a recorder to bypass NodeKeyMiddleware (the wire-format
// decode, not auth, is what this exercises), sidestepping the unrelated
// testNodeMux harness bug.
func TestNodeHeartbeatHandler_PassesGovernorFieldsThrough(t *testing.T) {
	reg := nodes.New()
	id := "hb-node-id"
	_, _, _, disconnect := reg.Connect(id, "hb-node", nil)
	defer disconnect()

	// Identical wire shape to cmd/sakms-node/postHeartbeat's json.Marshal.
	body, _ := json.Marshal(map[string]any{
		"id":               id,
		"enforcement":      "available",
		"effectivePercent": 40,
		"error":            "",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/heartbeat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	nodeHeartbeatHandler(reg)(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	list := reg.ListNodes()
	if len(list) != 1 {
		t.Fatalf("expected 1 node, got %d", len(list))
	}
	n := list[0]
	if n.Enforcement != "available" {
		t.Errorf("Enforcement = %q, want %q", n.Enforcement, "available")
	}
	if n.CPUCapEffective != 40 {
		t.Errorf("CPUCapEffective = %d, want 40", n.CPUCapEffective)
	}
	if n.CPUCapApplyErr != "" {
		t.Errorf("CPUCapApplyErr = %q, want empty", n.CPUCapApplyErr)
	}
}

// TestNodeHeartbeatHandler_OmittedGovernorFields_BackwardCompatible proves the
// wire-format change is backward compatible: a heartbeat body from an older
// node binary (only {"id":...}) still returns 204 and leaves the governor
// fields at their honest zero value — no crash, no fabricated "available".
func TestNodeHeartbeatHandler_OmittedGovernorFields_BackwardCompatible(t *testing.T) {
	reg := nodes.New()
	id := "old-node-id"
	_, _, _, disconnect := reg.Connect(id, "old-node", nil)
	defer disconnect()

	body, _ := json.Marshal(map[string]string{"id": id}) // older node: id only
	req := httptest.NewRequest(http.MethodPost, "/api/nodes/heartbeat", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	nodeHeartbeatHandler(reg)(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for a legacy id-only heartbeat, got %d", rec.Code)
	}
	n := reg.ListNodes()[0]
	if n.Enforcement != "" || n.CPUCapEffective != 0 || n.CPUCapApplyErr != "" {
		t.Fatalf("expected honest zero-value governor status for a legacy heartbeat, got %+v", n)
	}
}

// TestListNodesHandler_ReturnsGovernorStatus proves the server-side mapping:
// the live governor status stored on a node (via Heartbeat) is surfaced in
// apidto.NodeInfo.Enforcement + CPUCapApply. Calls listNodesHandler directly
// with a recorder.
func TestListNodesHandler_ReturnsGovernorStatus(t *testing.T) {
	reg := nodes.New()
	pairingReg := nodes.NewPairingRegistry()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer sqlDB.Close()
	nodeSettingsStore := nodesettings.New(sqlDB)

	id := "gov-node"
	_, _, _, disconnect := reg.Connect(id, "render-box", []string{"cuda"})
	defer disconnect()

	// A node that is capable but whose last apply failed: enforcement available,
	// an effective percent still in force, plus a non-empty error.
	reg.Heartbeat(id, "available", 75, "writing cpu.max: permission denied")

	req := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
	rec := httptest.NewRecorder()
	listNodesHandler(reg, pairingReg, nodeSettingsStore)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got apidto.NodesResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(got.Nodes))
	}
	n := got.Nodes[0]
	if n.Enforcement != "available" {
		t.Errorf("Enforcement = %q, want %q", n.Enforcement, "available")
	}
	if n.CPUCapApply.EffectivePercent != 75 {
		t.Errorf("CPUCapApply.EffectivePercent = %d, want 75", n.CPUCapApply.EffectivePercent)
	}
	if n.CPUCapApply.Error != "writing cpu.max: permission denied" {
		t.Errorf("CPUCapApply.Error = %q, want the reported error", n.CPUCapApply.Error)
	}
}
