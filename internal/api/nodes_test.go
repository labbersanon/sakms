package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/nodes"
)

// testNewMux returns a mux wired with a real nodes.Registry and all other
// stores from testStores — the minimal setup the four node routes need.
func testNodeMux(t *testing.T, reg *nodes.Registry) *http.ServeMux {
	t.Helper()
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	return NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil)
}

// TestNodeHeartbeat_204 posts a heartbeat and expects 204.
func TestNodeHeartbeat_204(t *testing.T) {
	reg := nodes.New()
	// Connect a node so the id is known.
	id, _, _, disconnect := reg.Connect("test-node", nil)
	defer disconnect()

	srv := httptest.NewServer(testNodeMux(t, reg))
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{"id": id})
	resp, err := http.Post(srv.URL+"/api/nodes/heartbeat", "application/json", bytes.NewReader(body))
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
	reg := nodes.New()
	srv := httptest.NewServer(testNodeMux(t, reg))
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{"id": "nonexistent-id"})
	resp, err := http.Post(srv.URL+"/api/nodes/heartbeat", "application/json", bytes.NewReader(body))
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
	reg := nodes.New()
	// Connect a node and dispatch a job so there is a pending channel.
	_, _, _, disconnect := reg.Connect("result-node", nil)
	defer disconnect()

	job := nodes.Job{ID: "job-abc", Type: nodes.JobTypePhash, ServerPath: "/data/movie.mkv"}
	nodeID, _, ok := reg.Dispatch(job)
	if !ok {
		t.Fatal("expected Dispatch to succeed with a connected node")
	}
	_ = nodeID

	srv := httptest.NewServer(testNodeMux(t, reg))
	defer srv.Close()

	res := nodes.JobResult{JobID: "job-abc", Hash: "abc123"}
	body, _ := json.Marshal(res)
	resp, err := http.Post(srv.URL+"/api/nodes/jobs/job-abc/result", "application/json", bytes.NewReader(body))
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
	reg := nodes.New()
	srv := httptest.NewServer(testNodeMux(t, reg))
	defer srv.Close()

	res := nodes.JobResult{JobID: "nonexistent-job", Hash: "abc123"}
	body, _ := json.Marshal(res)
	resp, err := http.Post(srv.URL+"/api/nodes/jobs/nonexistent-job/result", "application/json", bytes.NewReader(body))
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
	reg := nodes.New()
	srv := httptest.NewServer(testNodeMux(t, reg))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/nodes")
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
	reg := nodes.New()
	_, _, _, disconnect := reg.Connect("render-box", []string{"cuda"})
	defer disconnect()

	srv := httptest.NewServer(testNodeMux(t, reg))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/nodes")
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
	reg := nodes.New()
	srv := httptest.NewServer(testNodeMux(t, reg))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/nodes/stream?name=test-stream-node&capabilities=cpu", nil)
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
