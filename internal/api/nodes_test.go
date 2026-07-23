package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/nodes"
)

// TestNodeHeartbeat_204 posts a heartbeat and expects 204.
func TestNodeHeartbeat_204(t *testing.T) {
	mux, reg, _, _, _, nodeKeyStore, _ := testNodesMux(t)

	// Connect a node so the id is known.
	id := "test-node-id"
	_, _, _, disconnect := reg.Connect(id, "test-node", nil)
	defer disconnect()

	_, rawKey, err := nodeKeyStore.Create(context.Background(), "some-name")
	if err != nil {
		t.Fatalf("nodeKeyStore.Create: %v", err)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{"id": id})
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/nodes/heartbeat", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rawKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST heartbeat failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

// TestNodeHeartbeat_UnknownID_NoError posts a heartbeat for an unknown id —
// the Registry treats this as a no-op, so the handler must still return 204.
func TestNodeHeartbeat_UnknownID_NoError(t *testing.T) {
	mux, _, _, _, _, nodeKeyStore, _ := testNodesMux(t)

	_, rawKey, err := nodeKeyStore.Create(context.Background(), "some-name")
	if err != nil {
		t.Fatalf("nodeKeyStore.Create: %v", err)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{"id": "nonexistent-id"})
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/nodes/heartbeat", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rawKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST heartbeat failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for unknown id, got %d", resp.StatusCode)
	}
}

// TestNodeJobResult_204 posts a job result for a pending job and expects 204.
func TestNodeJobResult_204(t *testing.T) {
	mux, reg, _, _, _, nodeKeyStore, _ := testNodesMux(t)

	// Connect a node and dispatch a job so there is a pending channel.
	_, _, _, disconnect := reg.Connect("result-node-id", "result-node", nil)
	defer disconnect()

	job := nodes.Job{ID: "job-abc", Type: nodes.JobTypePhash, ServerPath: "/data/movie.mkv"}
	nodeID, _, ok := reg.Dispatch(job)
	if !ok {
		t.Fatal("expected Dispatch to succeed with a connected node")
	}
	_ = nodeID

	_, rawKey, err := nodeKeyStore.Create(context.Background(), "some-name")
	if err != nil {
		t.Fatalf("nodeKeyStore.Create: %v", err)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := nodes.JobResult{JobID: "job-abc", Hash: "abc123"}
	body, _ := json.Marshal(res)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/nodes/jobs/job-abc/result", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rawKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST job result failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

// TestNodeJobResult_UnknownJobID_NoError posts a result for an unknown job id
// — ReportResult is a safe no-op, so the handler still returns 204.
func TestNodeJobResult_UnknownJobID_NoError(t *testing.T) {
	mux, _, _, _, _, nodeKeyStore, _ := testNodesMux(t)

	_, rawKey, err := nodeKeyStore.Create(context.Background(), "some-name")
	if err != nil {
		t.Fatalf("nodeKeyStore.Create: %v", err)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := nodes.JobResult{JobID: "nonexistent-job", Hash: "abc123"}
	body, _ := json.Marshal(res)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/nodes/jobs/nonexistent-job/result", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rawKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST job result failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for unknown job id, got %d", resp.StatusCode)
	}
}

// TestListNodes_EmptyRegistry returns an empty nodes array, not null.
func TestListNodes_EmptyRegistry(t *testing.T) {
	mux, _, _, _, _, _, apiKey := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/nodes", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("X-Api-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/nodes failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var got apidto.NodesResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Nodes == nil {
		t.Error("expected non-nil nodes array, got nil")
	}
	if len(got.Nodes) != 0 {
		t.Errorf("expected empty nodes, got %d", len(got.Nodes))
	}
}

// TestListNodes_ConnectedNode verifies the JSON shape: id, name, status,
// capabilities, lastHeartbeat all present for a connected node.
func TestListNodes_ConnectedNode(t *testing.T) {
	mux, reg, _, _, _, _, apiKey := testNodesMux(t)

	_, _, _, disconnect := reg.Connect("render-box-id", "render-box", []string{"cuda"})
	defer disconnect()

	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/nodes", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("X-Api-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/nodes failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var got apidto.NodesResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(got.Nodes))
	}
	n := got.Nodes[0]
	if n.Name != "render-box" {
		t.Errorf("expected name %q, got %q", "render-box", n.Name)
	}
	if n.Status != "online" {
		t.Errorf("expected status %q, got %q", "online", n.Status)
	}
	if len(n.Capabilities) != 1 || n.Capabilities[0] != "cuda" {
		t.Errorf("expected capabilities [cuda], got %v", n.Capabilities)
	}
	if n.ID == "" {
		t.Error("expected non-empty node id")
	}
	if n.LastHeartbeat == "" {
		t.Error("expected non-empty lastHeartbeat")
	}
}

// TestNodeStream_ConnectAck opens an SSE stream and reads the first event,
// verifying it is a named "ack" event containing a non-empty nodeId.
func TestNodeStream_ConnectAck(t *testing.T) {
	mux, _, _, _, _, nodeKeyStore, _ := testNodesMux(t)

	_, rawKey, err := nodeKeyStore.Create(context.Background(), "test-stream-node")
	if err != nil {
		t.Fatalf("nodeKeyStore.Create: %v", err)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/nodes/stream?capabilities=cpu", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected text/event-stream content-type, got %q", ct)
	}

	// Read lines until we collect a full event (blank line terminates it).
	scanner := bufio.NewScanner(resp.Body)
	var eventType, dataLine string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			dataLine = strings.TrimPrefix(line, "data: ")
		} else if line == "" && dataLine != "" {
			// Blank line ends the event.
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading SSE stream: %v", err)
	}

	if eventType != "ack" {
		t.Errorf("expected event type %q, got %q", "ack", eventType)
	}
	var ack nodes.ConnectAck
	if err := json.Unmarshal([]byte(dataLine), &ack); err != nil {
		t.Fatalf("decoding ConnectAck: %v", err)
	}
	if ack.NodeID == "" {
		t.Error("expected non-empty nodeId in ConnectAck")
	}
}
