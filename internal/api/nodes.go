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
	"github.com/labbersanon/sakms/internal/nodepath"
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
		out = append(out, nodes.PathMapping{Server: serverPath, Local: pm.NodePath, Key: string(pm.Key)})
	}
	return out
}

// toApprovalPersistedSettings converts approve-time input into persisted
// settings. Every row gets VerificationStatus=unverified_approval — a
// pending node has no live, authenticated channel yet, so Safeguard 1's
// live comparison structurally cannot run at this point (see the
// Reachability constraint in the security-hardening addendum: RequestBrowse
// only reaches an already-authenticated connectedNode, and a pending node
// is never in that map).
func toApprovalPersistedSettings(in []apidto.NodePathMappingInput, maxJobs int) nodesettings.Settings {
	entries := make([]nodesettings.PathMappingEntry, 0, len(in))
	for _, pm := range in {
		entries = append(entries, nodesettings.PathMappingEntry{
			LibraryPathKey:     string(pm.Key),
			NodePath:           pm.NodePath,
			VerificationStatus: nodesettings.VerificationUnverifiedApproval,
		})
	}
	return nodesettings.Settings{PathMappings: entries, MaxJobs: maxJobs}
}

// verifyAndBuildPersistedSettings is Safeguard 1's save-time hard gate: for
// every non-blank row in in, it runs verifyNodePathMapping (a live
// directory-listing comparison between the server's own ServerPath and the
// node's live NodePath listing) and only returns a Settings value once
// EVERY row has reached an accept outcome. A blank NodePath, or a row whose
// library path isn't configured yet, is left unverified (nothing meaningful
// to compare) rather than rejected — matching resolvePathMap's own existing
// skip logic for those cases.
//
// Ordering contract (pinned per the security-hardening addendum, after a
// Critic-review finding that this was previously unstated): the caller MUST
// NOT call nodeSettingsStore.Set until this function returns successfully.
// A row that fails verification is never persisted, not merely never
// pushed — on any mismatch, this returns an error and an empty Settings,
// never a partial result.
func verifyAndBuildPersistedSettings(ctx context.Context, reg *nodes.Registry, settingsStore *settings.Store, nodeID string, in []apidto.NodePathMappingInput, maxJobs, cpuCapPercent int) (nodesettings.Settings, error) {
	entries := make([]nodesettings.PathMappingEntry, 0, len(in))
	var mismatches []string
	now := time.Now().UTC()

	for _, pm := range in {
		entry := nodesettings.PathMappingEntry{LibraryPathKey: string(pm.Key), NodePath: pm.NodePath}

		if pm.NodePath == "" {
			entries = append(entries, entry)
			continue
		}

		serverPath, err := settingsStore.Get(ctx, string(pm.Key))
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			return nodesettings.Settings{}, fmt.Errorf("resolving library path %q: %w", pm.Key, err)
		}
		if serverPath == "" {
			// Library path isn't configured yet -- nothing to compare
			// against, same case resolvePathMap already skips for the wire
			// push. Not confirmed correct, so bootstrap status, not verified.
			entry.VerificationStatus = nodesettings.VerificationUnverifiedBootstrap
			entries = append(entries, entry)
			continue
		}

		status, err := verifyNodePathMapping(ctx, reg, nodeID, serverPath, pm.NodePath)
		if err != nil {
			// Distinguish a genuine content mismatch (errMappingMismatch —
			// collected below, reported as a 422 the operator can fix by
			// picking a different path) from an operational failure (the
			// server couldn't read its own ServerPath, or the node is
			// offline/timed out) — an operational failure must not be
			// reported as "your mapping looks wrong," per the plan's own
			// observability requirement (distinguishing "safeguard blocked
			// this" from "node unreachable").
			var mismatch *errMappingMismatch
			if errors.As(err, &mismatch) {
				mismatches = append(mismatches, err.Error())
				continue
			}
			return nodesettings.Settings{}, fmt.Errorf("verifying mapping for %q: %w", pm.Key, err)
		}
		entry.VerificationStatus = status
		if status == nodesettings.VerificationVerified {
			verifiedAt := now
			entry.VerifiedAt = &verifiedAt
		}
		entries = append(entries, entry)
	}

	if len(mismatches) > 0 {
		return nodesettings.Settings{}, &errMappingMismatch{msg: strings.Join(mismatches, "; ")}
	}
	// CPUCapPercent (operator-owned, like MaxJobs) is threaded through from the
	// caller's STORED value and re-persisted here — a node-authored path-map save
	// must never zero the operator's cap via Set's unconditional column upsert.
	return nodesettings.Settings{PathMappings: entries, MaxJobs: maxJobs, CPUCapPercent: cpuCapPercent}, nil
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
		pathMap = append(pathMap, nodes.PathMapping{Server: serverPath, Local: e.NodePath, Key: e.LibraryPathKey})
	}

	// PauseDispatch MUST come from the STORED value, not a zero value: this is
	// the main NodeSettings sender (reconnect re-push + the pause endpoint's own
	// echo), so omitting it would clear the node's cached pause display on every
	// reconnect (P7).
	reg.SendSettings(nodeID, nodes.NodeSettings{PathMap: pathMap, MaxJobs: persisted.MaxJobs, CPUCapPercent: persisted.CPUCapPercent, PauseDispatch: persisted.PauseDispatch})
	return nil
}

// seedNodePause applies nodeID's persisted pause_dispatch bit to the live
// connectedNode.paused on (re)connect, so a server restart or node reconnect
// re-establishes the durable operator pause on the fresh connectedNode
// (P4c). This is a DISTINCT operation from pushPersistedNodeSettings: that
// sends the node an SSE settings frame for display, whereas this seeds the
// server-side dispatch-exclusion state — the actual authority for whether the
// node receives new work. A node with nothing persisted (ok=false) has no
// pause, so the fresh connectedNode's zero-value paused=false is already
// correct and no write is needed.
func seedNodePause(ctx context.Context, reg *nodes.Registry, nodeSettingsStore *nodesettings.Store, nodeID string) error {
	persisted, ok, err := nodeSettingsStore.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	reg.SetNodePaused(nodeID, persisted.PauseDispatch)
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

		// Seed the live connectedNode.paused from the persisted pause bit first,
		// before the settings push, so a server restart or node reconnect
		// re-applies the operator's dispatch exclusion to the fresh connection
		// (P4c) as early as possible after Connect — tightening the window in
		// which a concurrent Dispatch could pick a should-be-paused node.
		// Distinct from the SSE settings push below: this seeds the server-side
		// dispatch authority, not the node's display cache.
		if err := seedNodePause(r.Context(), reg, nodeSettingsStore, id); err != nil {
			log.Printf("nodes: reconnect pause seed for %s (id=%s): %v", name, id, err)
		}

		if err := pushPersistedNodeSettings(r.Context(), reg, settingsStore, nodeSettingsStore, id); err != nil {
			log.Printf("nodes: reconnect settings push for %s (id=%s): %v", name, id, err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")

		// First event: ConnectAck so the node learns its durable id AND the
		// bounded library-path-key catalog it may author mappings for (D4 —
		// piggybacked here rather than on a separate node-auth endpoint, since
		// the catalog is a compile-time constant, static per node). Marshal
		// cannot fail in practice, but a silent drop would leave the node with
		// no id and permanently broken heartbeats, so fail loudly instead.
		catalog := make([]string, len(libraryPathKeys))
		for i, k := range libraryPathKeys {
			catalog[i] = string(k)
		}
		ackData, err := json.Marshal(nodes.ConnectAck{NodeID: id, LibraryPathKeys: catalog})
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
// last-seen timestamp for display-status purposes and, in the same beat, the
// node's live CPU governor status (enforcement + last-apply result the node
// reports via its capState.snapshot()). The governor fields are optional on the
// wire: an older node binary that only sends {"id":...} decodes them to their
// zero values, which the Registry stores honestly as "nothing reported yet" —
// never a fabricated enforcement/success.
func nodeHeartbeatHandler(reg *nodes.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID               string `json:"id"`
			Enforcement      string `json:"enforcement,omitempty"`
			EffectivePercent int    `json:"effectivePercent,omitempty"`
			Error            string `json:"error,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		reg.Heartbeat(body.ID, body.Enforcement, body.EffectivePercent, body.Error)
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

		result, err := reg.RequestBrowse(nodeID, path, false)
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
// pending-pairing nodes, all mapped to the apidto shape. Each node's MaxJobs
// is looked up from nodeSettingsStore (the same operator-owned value
// updateNodeSettingsOperatorAuth writes) so the frontend can preload the
// real stored concurrency cap into EditSettingsModal instead of defaulting
// to 0 — see apidto.NodeInfo's doc comment for why that default was a bug.
func listNodesHandler(reg *nodes.Registry, pairingReg *nodes.PairingRegistry, nodeSettingsStore *nodesettings.Store) http.HandlerFunc {
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
			stored, _, err := nodeSettingsStore.Get(r.Context(), n.ID)
			if err != nil {
				log.Printf("nodes: get stored settings for %s: %v", n.ID, err)
			}
			dtoNodes = append(dtoNodes, apidto.NodeInfo{
				ID:            n.ID,
				Name:          n.Name,
				Status:        status,
				Capabilities:  caps,
				LastHeartbeat: n.LastHeartbeat.Format(time.RFC3339),
				MaxJobs:       stored.MaxJobs,
				PauseDispatch: stored.PauseDispatch,
				// CPUCapPercent is the stored, operator-owned cap (preload).
				CPUCapPercent: stored.CPUCapPercent,
				// Enforcement + CPUCapApply are the node's LIVE governor status,
				// carried back on every heartbeat (Stage 3b) and read here straight
				// from ListNodes(). A node that has not yet reported (or an older
				// binary that omits the fields) leaves them zero-valued, which reads
				// honestly as "nothing enforced yet" — never a fabricated success.
				Enforcement: n.Enforcement,
				CPUCapApply: apidto.NodeCPUCapApply{
					EffectivePercent: n.CPUCapEffective,
					Error:            n.CPUCapApplyErr,
				},
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
		if err := nodeSettingsStore.Set(r.Context(), keyID, toApprovalPersistedSettings(body.PathMap, body.MaxJobs)); err != nil {
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
				// CPUCapPercent (and PauseDispatch) are intentionally left at their
				// zero value here: a freshly-approved node has no persisted cap yet,
				// so 0 (unlimited) is correct — the documented approval/pairing
				// exception to "always send the STORED value" (see NodeSettings doc).
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

// updateNodeSettingsHandler handles PUT /api/nodes/{id}/settings under DUAL
// auth (D1): the route accepts EITHER operator credentials OR a node bearer
// key, and the write is partitioned by which authenticated it —
//
//   - node bearer  → the node authors its OWN PathMap (single-key delta or
//     Clear), keyed by the bearer identity (NOT the URL {id}, D2), always
//     preserving the operator-owned MaxJobs (D3).
//   - operator     → writes ONLY MaxJobs, preserving the now-node-owned
//     PathMap (D3); any PathMap in the body is ignored.
//
// The presence of a node identity in the request context (injected only by
// NodeKeyMiddleware) is what distinguishes the two — an operator request never
// carries one.
func updateNodeSettingsHandler(reg *nodes.Registry, settingsStore *settings.Store, nodeSettingsStore *nodesettings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body apidto.NodeSettingsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if nodeID, _, ok := auth.NodeIdentityFromContext(r.Context()); ok {
			// D2 (security-critical): key by the authenticated bearer identity,
			// IGNORING r.PathValue("id"), so node A cannot write node B's row by
			// putting B's id in the URL.
			updateNodeSettingsNodeAuth(w, r, reg, settingsStore, nodeSettingsStore, nodeID, body)
			return
		}
		updateNodeSettingsOperatorAuth(w, r, reg, settingsStore, nodeSettingsStore, r.PathValue("id"), body)
	}
}

// updateNodeSettingsNodeAuth is the node-bearer write path: the node authors
// its own path mappings as single-key deltas (or clears, D7), verified against
// the server's own listing before persistence, always preserving the
// operator-owned MaxJobs (D3).
func updateNodeSettingsNodeAuth(w http.ResponseWriter, r *http.Request, reg *nodes.Registry, settingsStore *settings.Store, nodeSettingsStore *nodesettings.Store, nodeID string, body apidto.NodeSettingsRequest) {
	ctx := r.Context()

	// Stage 4 / (g) hard reject: a mapping may only be authored once the node
	// has at least one real, locally-asserted mediaRoot — the independent
	// containment boundary that offsets the reduced verification independence
	// under node authorship (§3a). mediaRoots is node-local, so the node
	// reports it on its own push; a missing OR trivial list means reject
	// BEFORE any verification round-trip, so nothing is persisted or pushed.
	//
	// "Non-empty" alone is insufficient (D9): a trivial root like "/" or
	// "/mnt" satisfies non-empty while providing zero containment — the node's
	// own withinMediaRoots would pass any nodePath against it. Reject if the
	// list is empty OR if ANY entry is trivial (a single "/" entry alone
	// re-opens the unrestricted hole, since withinMediaRoots passes a path
	// contained in ANY configured root).
	if len(body.MediaRoots) == 0 {
		http.Error(w, "node has no configured mediaRoots; add a media root before authoring a path mapping", http.StatusUnprocessableEntity)
		return
	}
	for _, root := range body.MediaRoots {
		if nodepath.Trivial(root) {
			http.Error(w, fmt.Sprintf("node reported a trivial mediaRoot %q; a media root must be a real directory (at least %d path segments), not a filesystem root or shallow mount, before authoring a path mapping", root, nodepath.MinDepth), http.StatusUnprocessableEntity)
			return
		}
	}

	// D3: MaxJobs is operator-owned. Load the stored value and use it for BOTH
	// persistence and the SSE push, so a node body carrying MaxJobs=0 can never
	// zero the node's live job cap — neither in the DB nor over the wire.
	stored, _, err := nodeSettingsStore.Get(ctx, nodeID)
	if err != nil {
		log.Printf("nodes/settings: get stored settings for %s: %v", nodeID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	storedMaxJobs := stored.MaxJobs
	// CPUCapPercent is operator-owned too (D3, same as MaxJobs): load the stored
	// value and use it for BOTH persistence and the SSE push, so a node-authored
	// path-map save can never zero the operator's cap in the DB or over the wire.
	storedCPUCapPercent := stored.CPUCapPercent

	// Partition the request (D7): a Clear=true entry deletes its row; a
	// non-blank NodePath is a set (runs the verification gate); a blank
	// NodePath with Clear=false is a no-op skip — blank is NOT the delete
	// signal. Blanks are dropped here, never handed to verify+Set: that
	// function appends blank rows and Set upserts node_path="", which would
	// WIPE the existing value — the exact wipe-by-blank bug resolvePathMap
	// guards against on the wire push.
	var sets []apidto.NodePathMappingInput
	var clears []apidto.LibraryPathKey
	for _, pm := range body.PathMap {
		switch {
		case pm.Clear:
			clears = append(clears, pm.Key)
		case pm.NodePath == "":
			// skip — leave this key untouched
		default:
			sets = append(sets, pm)
		}
	}

	// Stage 4 (authoritative backstop): validate the nodePath VALUE of every set
	// before the verification gate runs, using the same canonical rule
	// (nodepath.Trivial) the mediaRoots gate and the node itself use. A "/" or
	// too-shallow nodePath provides no containment and must never reach the DB —
	// the node rejects these locally too, but the server does not trust that.
	//
	// A blank nodePath is NOT checked here: it was already partitioned out as a
	// no-op skip above (D7 — blank means "leave untouched", never "set" and never
	// "clear"), so "empty nodePath" is enforced node-side (validateMediaRootPath)
	// rather than rejected here, which would break the blank-is-skip contract.
	// Containment (withinMediaRoots) is likewise node-only: the server has no
	// filesystem access to the node's paths (D9), so it cannot re-run it.
	for _, pm := range sets {
		if nodepath.Trivial(pm.NodePath) {
			http.Error(w, fmt.Sprintf("nodePath %q for %q is too shallow (need at least %d path segments); a filesystem root or shallow mount provides no containment", pm.NodePath, pm.Key, nodepath.MinDepth), http.StatusUnprocessableEntity)
			return
		}
	}

	// Verify + persist the sets. Single-key delta: Store.Set upserts only these
	// keys and never wipes siblings, so only the changed key(s) are re-verified
	// via a live RequestBrowse (D6: that browse runs over the SSE stream while
	// this HTTP push is still in flight — the node's push goroutine and its SSE
	// reader are independent, so this is not a deadlock). On any mismatch,
	// nothing is persisted — the store is left exactly as it was.
	if len(sets) > 0 {
		toPersist, err := verifyAndBuildPersistedSettings(ctx, reg, settingsStore, nodeID, sets, storedMaxJobs, storedCPUCapPercent)
		if err != nil {
			var mismatch *errMappingMismatch
			var unreachable *errNodeUnreachable
			switch {
			case errors.As(err, &mismatch):
				http.Error(w, mismatch.Error(), http.StatusUnprocessableEntity)
			case errors.As(err, &unreachable):
				log.Printf("nodes/settings: node unreachable during verification for %s: %v", nodeID, err)
				http.Error(w, unreachable.Error(), http.StatusBadGateway)
			default:
				log.Printf("nodes/settings: verifying mapping for %s: %v", nodeID, err)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
			return
		}
		if err := nodeSettingsStore.Set(ctx, nodeID, toPersist); err != nil {
			log.Printf("nodes/settings: persist settings for %s: %v", nodeID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// Apply the clears (D7): a real row delete — Store.Set cannot express
	// deletion (it upserts and blank-skips), so without this a stale verified
	// mapping would survive a reimage and re-push to the node on reconnect.
	for _, key := range clears {
		if err := nodeSettingsStore.Delete(ctx, nodeID, string(key)); err != nil {
			log.Printf("nodes/settings: delete mapping %q for %s: %v", key, nodeID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// Push the single-key delta to the live node (D3: STORED MaxJobs, never
	// body.MaxJobs). resolvePathMap skips blanks and clears, so only the changed
	// set key(s) are pushed; the server cannot push a delete DOWN
	// (mergePathMap is add/replace-only — the accepted node-authoritative
	// reconciliation invariant, D7). Best-effort: if the node isn't connected,
	// the persisted state is authoritative and re-pushes on reconnect.
	pathMap := resolvePathMap(ctx, settingsStore, sets)
	// P7: this hand-built frame must carry the STORED PauseDispatch alongside the
	// STORED MaxJobs it already carries — otherwise a node-authored path-map
	// change on a paused node would emit a zero-value pause=false and silently
	// clear the node's cached pause display. stored is the same row loaded above
	// for storedMaxJobs, so no extra lookup is needed.
	reg.SendSettings(nodeID, nodes.NodeSettings{PathMap: pathMap, MaxJobs: storedMaxJobs, CPUCapPercent: storedCPUCapPercent, PauseDispatch: stored.PauseDispatch})
	w.WriteHeader(http.StatusNoContent)
}

// updateNodeSettingsOperatorAuth is the operator write path: it touches ONLY
// MaxJobs and preserves the node-authored PathMap untouched (D3). PathMap in an
// operator body is ignored — path mappings are node-owned now (Stage 5 makes
// the frontend read-only, but the backend partition is already authoritative
// so no operator-submitted PathMap is ever trusted going forward).
func updateNodeSettingsOperatorAuth(w http.ResponseWriter, r *http.Request, reg *nodes.Registry, settingsStore *settings.Store, nodeSettingsStore *nodesettings.Store, nodeID string, body apidto.NodeSettingsRequest) {
	ctx := r.Context()

	stored, _, err := nodeSettingsStore.Get(ctx, nodeID)
	if err != nil {
		log.Printf("nodes/settings: get stored settings for %s: %v", nodeID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// CPUCapPercent, like MaxJobs, is operator-owned and written unconditionally
	// from the body here (both are the fields operator auth is meant to write). The
	// frontend preloads the stored value into NodeInfo.CPUCapPercent so an
	// untouched Save re-sends the current cap rather than zeroing it.
	if err := nodeSettingsStore.Set(ctx, nodeID, nodesettings.Settings{PathMappings: stored.PathMappings, MaxJobs: body.MaxJobs, CPUCapPercent: body.CPUCapPercent}); err != nil {
		log.Printf("nodes/settings: persist settings for %s: %v", nodeID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Re-push the (unchanged) stored PathMap plus the new MaxJobs to the live
	// node. Best-effort, same as the node-auth path — the persisted value is
	// authoritative and re-pushes on the node's next reconnect if it is
	// currently offline.
	if err := pushPersistedNodeSettings(ctx, reg, settingsStore, nodeSettingsStore, nodeID); err != nil {
		log.Printf("nodes/settings: push settings for %s: %v", nodeID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// updateNodePauseHandler handles PUT /api/nodes/{id}/pause under DUAL auth
// (P1), a dedicated route reusing the same dualAuth wrapper as PUT /settings
// (NOT a branch inside the settings handler — pause is a clean,
// verification-free concern that must not entangle with the path-mapping
// verification gate). The write is keyed by which credential authenticated it,
// mirroring updateNodeSettingsHandler:
//
//   - node bearer → keyed by the authenticated bearer identity, IGNORING
//     r.PathValue("id") entirely (D2, the security-critical property: node A
//     cannot flip node B's pause by putting B's id in the URL).
//   - operator     → keyed by r.PathValue("id").
//
// Both converge on updateNodePause, which persists (SetPauseDispatch), applies
// the live dispatch effect (SetNodePaused), and echoes the authoritative
// settings to the node over SSE for tray display.
func updateNodePauseHandler(reg *nodes.Registry, settingsStore *settings.Store, nodeSettingsStore *nodesettings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body apidto.NodePauseRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if nodeID, _, ok := auth.NodeIdentityFromContext(r.Context()); ok {
			// D2 (security-critical): key by the authenticated bearer identity,
			// IGNORING r.PathValue("id").
			updateNodePause(w, r, reg, settingsStore, nodeSettingsStore, nodeID, body.Paused)
			return
		}
		updateNodePause(w, r, reg, settingsStore, nodeSettingsStore, r.PathValue("id"), body.Paused)
	}
}

// updateNodePause is the shared body of both auth branches of the pause
// endpoint. The three writes are all required (P4): persist the durable bit,
// flip the live connectedNode.paused for immediate dispatch effect, then echo
// the authoritative settings (now including the new pause) to the node for
// display. The echo goes through pushPersistedNodeSettings so the frame carries
// the full stored state (pathMap + maxJobs + pause), not a pause-only frame
// that would zero the node's other cached fields.
func updateNodePause(w http.ResponseWriter, r *http.Request, reg *nodes.Registry, settingsStore *settings.Store, nodeSettingsStore *nodesettings.Store, nodeID string, paused bool) {
	ctx := r.Context()

	if err := nodeSettingsStore.SetPauseDispatch(ctx, nodeID, paused); err != nil {
		log.Printf("nodes/pause: persist pause for %s: %v", nodeID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Live dispatch effect: exclude (or re-include) the node from the running
	// dispatcher immediately. No-op for an offline node — its pause is re-seeded
	// from the persisted store on reconnect (seedNodePause).
	reg.SetNodePaused(nodeID, paused)

	// Echo the authoritative settings to the node over SSE for tray display.
	// Best-effort, like the settings handlers — the persisted bit is
	// authoritative and re-pushes on reconnect if the node is offline.
	if err := pushPersistedNodeSettings(ctx, reg, settingsStore, nodeSettingsStore, nodeID); err != nil {
		log.Printf("nodes/pause: push settings for %s: %v", nodeID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
