package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/apidto"
	"github.com/curtiswtaylorjr/sakms/internal/nodes"
)

// nodeStreamHandler handles GET /api/nodes/stream. It registers the connecting
// node in the Registry, emits a ConnectAck as a named "ack" SSE event, then
// streams Job frames until the client disconnects.
func nodeStreamHandler(reg *nodes.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		name := r.URL.Query().Get("name")

		// capabilities may be comma-separated or repeated: ?capabilities=cuda
		// or ?capabilities=cuda&capabilities=vulkan or ?capabilities=cuda,vulkan
		var capabilities []string
		for _, raw := range r.URL.Query()["capabilities"] {
			for _, part := range strings.Split(raw, ",") {
				part = strings.TrimSpace(part)
				if part != "" {
					capabilities = append(capabilities, part)
				}
			}
		}

		id, jobs, disconnect := reg.Connect(name, capabilities)
		defer disconnect()

		log.Printf("nodes: connected %s (capabilities=%v)", name, capabilities)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")

		// First event: ConnectAck so the node learns its server-assigned id.
		// ConnectAck contains only a string field so Marshal cannot fail in
		// practice, but a silent drop would leave the node with no id and
		// permanently broken heartbeats, so fail loudly instead.
		ackData, err := json.Marshal(nodes.ConnectAck{NodeID: id})
		if err != nil {
			log.Printf("nodes: marshal ConnectAck: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "event: ack\ndata: %s\n\n", ackData)
		flusher.Flush()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case job, ok := <-jobs:
				if !ok {
					return
				}
				writeSSEData(w, flusher, job)
			}
		}
	}
}

// nodeHeartbeatHandler handles POST /api/nodes/heartbeat. Updates the node's
// last-seen timestamp for display-status purposes.
func nodeHeartbeatHandler(reg *nodes.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		reg.Heartbeat(body.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// nodeJobResultHandler handles POST /api/nodes/jobs/{id}/result. Delivers the
// node's result for one in-flight job back to the waiting Dispatcher.
func nodeJobResultHandler(reg *nodes.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobID := r.PathValue("id")
		var res nodes.JobResult
		if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// Let the path value win if the body omits jobId.
		if res.JobID == "" {
			res.JobID = jobID
		}
		reg.ReportResult(res)
		w.WriteHeader(http.StatusNoContent)
	}
}

// listNodesHandler handles GET /api/nodes. Returns the Registry's live node
// snapshot mapped to the apidto shape, with Status derived from heartbeat
// freshness.
func listNodesHandler(reg *nodes.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := reg.ListNodes()
		dtoNodes := make([]apidto.NodeInfo, 0, len(raw))
		for _, n := range raw {
			status := "online"
			if nodes.Offline(n.LastHeartbeat) {
				status = "offline"
			}
			caps := n.Capabilities
			if caps == nil {
				caps = []string{}
			}
			dtoNodes = append(dtoNodes, apidto.NodeInfo{
				ID:            n.ID,
				Name:          n.Name,
				Status:        status,
				Capabilities:  caps,
				LastHeartbeat: n.LastHeartbeat.Format(time.RFC3339),
			})
		}
		writeJSON(w, apidto.NodesResponse{Nodes: dtoNodes})
	}
}
