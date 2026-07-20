package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/auth"
	"github.com/labbersanon/sakms/internal/nodekeys"
	"github.com/labbersanon/sakms/internal/nodes"
	"github.com/labbersanon/sakms/internal/nodesettings"
	"github.com/labbersanon/sakms/internal/settings"
)

// resolvePathMap converts the operator-submitted, library-path-keyed input
// into the wire shape actually pushed to the node: for each entry, ServerPath
// is looked up fresh from Library settings by Key (never accepted from the
// client — Library settings owns that value), and Local is the operator's
// chosen node-side path. An entry whose library path isn't configured yet
// (ServerPath empty) is skipped entirely — there is nothing meaningful to map
// it to. An entry with a blank NodePath is ALSO skipped, not pushed as
// Local: "" — this must agree with pushPersistedNodeSettings' own skip-if-
// empty-NodePath rule (internal/nodesettings), or a save that leaves a
// configured-but-unfilled row blank would push an empty Local for that key on
// this live save yet skip it entirely on the next reconnect, silently
// wiping cmd/sakms-node's mergePathMap-preserved value for that key on save
// yet leaving it alone on reconnect — the same wipe-by-omission bug class
// this feature already fixed once for PathMap and once for MaxJobs. Blank
// means "leave this key untouched," consistently, on every push path.
//
// Known consequence of that consistency, accepted by design rather than
// fixed: there is currently no way to CLEAR a previously-pushed mapping by
// blanking the field and saving — the node keeps its last real value
// forever (mergePathMap never deletes, and nothing here ever sends an
// explicit "remove this key" signal). GET /api/nodes/{id}/path-mappings can
// therefore show a blank NodePath for a row the node is still actively
// remapping with a stale value. Making blank mean "clear" instead would
// reopen the exact save-vs-reconnect divergence bug this function was fixed
// to close, so this is a real, known limitation rather than an oversight —
// clearing a mapping would need a distinct wire signal, not an overload of
// "blank."
func resolvePathMap(ctx context.Context, settingsStore *settings.Store, in []apidto.NodePathMappingInput) []nodes.PathMapping {
	out := make([]nodes.PathMapping, 0, len(in))
	for _, pm := range in {
		if pm.NodePath == "" {
			continue
		}
		serverPath, err := settingsStore.Get(ctx, string(pm.Key))
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			log.Printf("nodes: resolving library path %q: %v", pm.Key, err)
			continue
		}
		if serverPath == "" {
			continue
		}
		out = append(out, nodes.PathMapping{Server: serverPath, Local: pm.NodePath})
	}
	return out
}

// toPersistedSettings converts the operator-submitted input into the shape
// nodesettings.Store persists — every submitted row, keyed by LibraryPathKey,
// regardless of whether that library path currently resolves to a value (it
// may be configured later, and the persisted NodePath should still be there
// for the reconnect re-push and the settings form's prefill when it is).
func toPersistedSettings(in []apidto.NodePathMappingInput, maxJobs int) nodesettings.Settings {
	entries := make([]nodesettings.PathMappingEntry, 0, len(in))
	for _, pm := range in {
		entries = append(entries, nodesettings.PathMappingEntry{
			LibraryPathKey: string(pm.Key),
			NodePath:       pm.NodePath,
		})
	}
	return nodesettings.Settings{PathMappings: entries, MaxJobs: maxJobs}
}

// pushPersistedNodeSettings looks up id's persisted settings and, if anything
// was ever saved for it, re-pushes an authoritative NodeSettings over the
// just-established SSE stream — the reconnect gap that motivated
// cmd/sakms-node's merge-by-key fix: without this, a restarted node only ever
// has whatever was in its local config.json, never learning about mappings
// saved while it was offline. Only rows with a non-empty persisted NodePath
// are included; rows omitted here (not on the node) are what let
// mergePathMap leave the node's existing value for that key untouched.
func pushPersistedNodeSettings(ctx context.Context, reg *nodes.Registry, settingsStore *settings.Store, nodeSettingsStore *nodesettings.Store, nodeID string) error {
	persisted, ok, err := nodeSettingsStore.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	pathMap := make([]nodes.PathMapping, 0, len(persisted.PathMappings))
	for _, e := range persisted.PathMappings {
		if e.NodePath == "" {
			continue
		}
		serverPath, err := settingsStore.Get(ctx, e.LibraryPathKey)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			return err
		}
		if serverPath == "" {
			continue
		}
		pathMap = append(pathMap, nodes.PathMapping{Server: serverPath, Local: e.NodePath})
	}

	reg.SendSettings(nodeID, nodes.NodeSettings{PathMap: pathMap, MaxJobs: persisted.MaxJobs})
	return nil
}

// nodeStreamHandler handles GET /api/nodes/stream. It registers the connecting
// node in the Registry, emits a ConnectAck as a named "ack" SSE event, then
// streams Job frames until the client disconnects. Operator-pushed NodeSettings
// are forwarded as named "settings" SSE events, and operator-initiated browse
// requests (Registry.RequestBrowse) as named "browseRequest" events, on the
// same stream.
func nodeStreamHandler(reg *nodes.Registry, settingsStore *settings.Store, nodeSettingsStore *nodesettings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		// id and name come from the validated bearer key (NodeKeyMiddleware),
		// not the URL query string — the query string is self-reported and
		// unauthenticated, so it must never be used for identity.
		id, name, ok := auth.NodeIdentityFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

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

		jobs, settings, browse, disconnect := reg.Connect(id, name, capabilities)
		defer disconnect()

		log.Printf("nodes: connected %s (id=%s, capabilities=%v)", name, id, capabilities)

		if err := pushPersistedNodeSettings(r.Context(), reg, settingsStore, nodeSettingsStore, id); err != nil {
			log.Printf("nodes: reconnect settings push for %s (id=%s): %v", name, id, err)
		}

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
			case br, ok := <-browse:
				if !ok {
					return
				}
				browseData, err := json.Marshal(br)
				if err != nil {
					log.Printf("nodes: marshal browse request: %v", err)
					continue
				}
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", nodes.EventBrowseRequest, browseData)
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

// nodeBrowseResultHandler handles POST /api/nodes/browse/{requestId}/result —
// a node's answer to one BrowseRequest, delivered via the isolated browse
// lane (Registry.ReportBrowseResult), not the phash job-result path.
func nodeBrowseResultHandler(reg *nodes.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := r.PathValue("requestId")
		var res nodes.BrowseResult
		if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// Let the path value win if the body omits requestId.
		if res.RequestID == "" {
			res.RequestID = requestID
		}
		reg.ReportBrowseResult(res)
		w.WriteHeader(http.StatusNoContent)
	}
}

// libraryPathKeys is every library root-folder-type setting a node's path
// mapping can correspond to (fixed set, see apidto.LibraryPathKey's own doc
// comment for why there are 5, not 6).
var libraryPathKeys = []apidto.LibraryPathKey{
	apidto.LibraryPathMoviesRoot,
	apidto.LibraryPathSeriesRoot,
	apidto.LibraryPathAdultRoot,
	apidto.LibraryPathMoviesKids,
	apidto.LibraryPathSeriesKids,
}

// nodePathMappingsHandler handles GET /api/nodes/{id}/path-mappings.
// Read-only: always returns exactly the 5 fixed rows, each with its current
// server-side library value (for the label) and its persisted NodePath, if
// any was ever saved for this node. See NodeSettingsRequest's doc comment for
// why there is no corresponding write endpoint at this path.
func nodePathMappingsHandler(settingsStore *settings.Store, nodeSettingsStore *nodesettings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")

		persisted, _, err := nodeSettingsStore.Get(r.Context(), nodeID)
		if err != nil {
			log.Printf("nodes/path-mappings: get persisted settings for %s: %v", nodeID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		nodePathByKey := make(map[string]string, len(persisted.PathMappings))
		for _, e := range persisted.PathMappings {
			nodePathByKey[e.LibraryPathKey] = e.NodePath
		}

		entries := make([]apidto.NodePathMappingEntry, 0, len(libraryPathKeys))
		for _, key := range libraryPathKeys {
			serverPath, err := settingsStore.Get(r.Context(), string(key))
			if err != nil && !errors.Is(err, settings.ErrNotFound) {
				// A transient lookup error must still render this row (as
				// unconfigured) rather than drop it — callers rely on
				// exactly 5 rows always coming back, per this handler's own
				// doc comment.
				log.Printf("nodes/path-mappings: resolving library path %q: %v", key, err)
				serverPath = ""
			}
			entries = append(entries, apidto.NodePathMappingEntry{
				Key:        key,
				ServerPath: serverPath,
				NodePath:   nodePathByKey[string(key)],
				Configured: serverPath != "",
			})
		}

		writeJSON(w, apidto.NodePathMappingsResponse{Entries: entries})
	}
}

// nodeBrowseHandler handles GET /api/nodes/{id}/browse?path=.... Targets one
// specific connected node via Registry.RequestBrowse and blocks until it
// answers or times out. Returns a clear 502 (not a 404/500 that looks like a
// server bug) when the node isn't connected or doesn't respond in time.
func nodeBrowseHandler(reg *nodes.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		path := r.URL.Query().Get("path")

		result, err := reg.RequestBrowse(nodeID, path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		entries := make([]apidto.NodeBrowseEntry, 0, len(result.Entries))
		for _, e := range result.Entries {
			entries = append(entries, apidto.NodeBrowseEntry{Name: e.Name, Path: e.Path})
		}
		writeJSON(w, apidto.NodeBrowseResponse{Path: path, Entries: entries})
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
func approveNodeHandler(pairingReg *nodes.PairingRegistry, nodeKeyStore *nodekeys.Store, settingsStore *settings.Store, nodeSettingsStore *nodesettings.Store) http.HandlerFunc {
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

		// Persist by the durable key id (keyID) — the same id nodekeys.Validate
		// resolves and Registry.Connect keys its live entry by, so a later
		// reconnect's pushPersistedNodeSettings lookup finds this record.
		if err := nodeSettingsStore.Set(r.Context(), keyID, toPersistedSettings(body.PathMap, body.MaxJobs)); err != nil {
			log.Printf("nodes/approve: persist settings for %s: %v", keyID, err)
			if err := nodeKeyStore.Revoke(r.Context(), keyID); err != nil {
				log.Printf("nodes/approve: revoke key %s after persist failure: %v", keyID, err)
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		pathMap := resolvePathMap(r.Context(), settingsStore, body.PathMap)

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
func updateNodeSettingsHandler(reg *nodes.Registry, settingsStore *settings.Store, nodeSettingsStore *nodesettings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		var body apidto.NodeSettingsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if err := nodeSettingsStore.Set(r.Context(), nodeID, toPersistedSettings(body.PathMap, body.MaxJobs)); err != nil {
			log.Printf("nodes/settings: persist settings for %s: %v", nodeID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		pathMap := resolvePathMap(r.Context(), settingsStore, body.PathMap)

		if !reg.SendSettings(nodeID, nodes.NodeSettings{PathMap: pathMap, MaxJobs: body.MaxJobs}) {
			http.Error(w, "node not connected", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
