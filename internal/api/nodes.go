package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/nodekeys"
	"github.com/labbersanon/sakms/internal/nodes"
)

// nodeStreamHandler handles GET /api/nodes/stream. It registers the connecting
// node in the Registry, emits a ConnectAck as a named "ack" SSE event, then
// streams Job frames until the client disconnects. Operator-pushed NodeSettings
// are forwarded as named "settings" SSE events on the same stream.
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

		id, jobs, settings, disconnect := reg.Connect(name, capabilities)
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
			case s, ok := <-settings:
				if !ok {
					return
				}
				settingsData, err := json.Marshal(s)
				if err != nil {
					log.Printf("nodes: marshal settings: %v", err)
					continue
				}
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", nodes.EventSettings, settingsData)
				flusher.Flush()
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

// listNodesHandler handles GET /api/nodes. Returns connected nodes plus any
// pending-pairing nodes, all mapped to the apidto shape.
func listNodesHandler(reg *nodes.Registry, pairingReg *nodes.PairingRegistry) http.HandlerFunc {
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

		pending := pairingReg.ListPending()
		dtoPending := make([]apidto.PendingNodeInfo, 0, len(pending))
		for _, pn := range pending {
			dtoPending = append(dtoPending, apidto.PendingNodeInfo{
				ID:          pn.ID,
				Name:        pn.Name,
				PairingCode: pn.PairingCode,
				RequestedAt: pn.RequestedAt.Format(time.RFC3339),
			})
		}

		writeJSON(w, apidto.NodesResponse{Nodes: dtoNodes, Pending: dtoPending})
	}
}

// approveNodeHandler handles POST /api/nodes/{id}/approve. Generates a unique
// bearer key for the pending node, pushes the key + settings via the pre-auth
// SSE stream, and removes the node from the pending registry.
func approveNodeHandler(pairingReg *nodes.PairingRegistry, nodeKeyStore *nodekeys.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pendingID := r.PathValue("id")

		name, ok := pairingReg.Name(pendingID)
		if !ok {
			http.Error(w, "pending node not found", http.StatusNotFound)
			return
		}

		var body apidto.ApproveNodeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		keyID, rawKey, err := nodeKeyStore.Create(r.Context(), name)
		if err != nil {
			log.Printf("nodes/approve: create key: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		pathMap := make([]nodes.PathMapping, len(body.PathMap))
		for i, pm := range body.PathMap {
			pathMap[i] = nodes.PathMapping{Server: pm.Server, Local: pm.Local}
		}

		cfg := nodes.PairConfig{
			APIKey: rawKey,
			Settings: nodes.NodeSettings{
				PathMap: pathMap,
				MaxJobs: body.MaxJobs,
			},
		}

		if !pairingReg.Approve(pendingID, cfg) {
			// Node disconnected in the window between Name() and Approve().
			// Revoke the key we just created so it cannot be used.
			if err := nodeKeyStore.Revoke(r.Context(), keyID); err != nil {
				log.Printf("nodes/approve: revoke orphaned key %s: %v", keyID, err)
			}
			http.Error(w, "pending node not found", http.StatusNotFound)
			return
		}

		writeJSON(w, map[string]string{"status": "approved"})
	}
}

// rejectPendingHandler handles DELETE /api/nodes/{id}/pending. Signals the
// pending node's SSE stream to close and removes it from the registry.
func rejectPendingHandler(pairingReg *nodes.PairingRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !pairingReg.Reject(r.PathValue("id")) {
			http.Error(w, "pending node not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// updateNodeSettingsHandler handles PUT /api/nodes/{id}/settings. Pushes
// updated path mappings and concurrency cap to an already-connected node over
// its existing authenticated SSE stream.
func updateNodeSettingsHandler(reg *nodes.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		var body apidto.NodeSettingsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		pathMap := make([]nodes.PathMapping, len(body.PathMap))
		for i, pm := range body.PathMap {
			pathMap[i] = nodes.PathMapping{Server: pm.Server, Local: pm.Local}
		}

		if !reg.SendSettings(nodeID, nodes.NodeSettings{PathMap: pathMap, MaxJobs: body.MaxJobs}) {
			http.Error(w, "node not connected", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
