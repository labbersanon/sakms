package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/nodes"
)

// TestPostHeartbeat_ReportsGovernorStatusToServer is the end-to-end symmetric
// trace (Stage 3b): a non-zero capState.snapshot() on the NODE side actually
// reaches the server's Registry via the heartbeat wire format. The test stands
// up an httptest server whose handler decodes the SAME field names
// (enforcement/effectivePercent/error) the real internal/api nodeHeartbeatHandler
// decodes, feeds a real internal/nodes Registry, and reads the value back
// through ListNodes — so a field-name typo on either side would fail here.
func TestPostHeartbeat_ReportsGovernorStatusToServer(t *testing.T) {
	// Node-side live governor status: capable, last apply of 60% in force.
	cs := &capState{enforcement: enforcementAvailable}
	cs.recordApply(60, nil)

	reg := nodes.New()
	id := "trace-node-id"
	_, _, _, disconnect := reg.Connect(id, "trace-node", nil)
	defer disconnect()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mirror internal/api.nodeHeartbeatHandler's decode contract exactly.
		var body struct {
			ID               string `json:"id"`
			Enforcement      string `json:"enforcement,omitempty"`
			EffectivePercent int    `json:"effectivePercent,omitempty"`
			Error            string `json:"error,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		reg.Heartbeat(body.ID, body.Enforcement, body.EffectivePercent, body.Error)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := &NodeConfig{ServerURL: srv.URL, APIKey: "test-key"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := postHeartbeat(ctx, id, cfg, srv.Client(), cs); err != nil {
		t.Fatalf("postHeartbeat: %v", err)
	}

	list := reg.ListNodes()
	if len(list) != 1 {
		t.Fatalf("expected 1 node, got %d", len(list))
	}
	n := list[0]
	if n.Enforcement != enforcementAvailable {
		t.Errorf("server-side Enforcement = %q, want %q", n.Enforcement, enforcementAvailable)
	}
	if n.CPUCapEffective != 60 {
		t.Errorf("server-side CPUCapEffective = %d, want 60", n.CPUCapEffective)
	}
	if n.CPUCapApplyErr != "" {
		t.Errorf("server-side CPUCapApplyErr = %q, want empty", n.CPUCapApplyErr)
	}
}
