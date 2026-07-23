package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/nodes"
	"github.com/labbersanon/sakms/internal/nodesettings"
)

// connectCapturingNode connects a fake node that answers browse requests with
// entries named after names (like connectFakeNode) but ALSO exposes its
// operator-pushed settings channel, so a test can assert exactly what was
// SSE-pushed. The settings channel is cap-1 and SendSettings is non-blocking,
// so a single push stays buffered until the test reads it.
func connectCapturingNode(t *testing.T, reg *nodes.Registry, nodeID string, names []string) <-chan nodes.NodeSettings {
	t.Helper()
	_, settings, browse, disconnect := reg.Connect(nodeID, nodeID, nil)
	t.Cleanup(disconnect)
	go func() {
		for req := range browse {
			entries := make([]nodes.BrowseEntry, 0, len(names))
			for _, n := range names {
				entries = append(entries, nodes.BrowseEntry{Name: n, Path: filepath.Join(req.Path, n)})
			}
			reg.ReportBrowseResult(nodes.BrowseResult{RequestID: req.ID, Entries: entries})
		}
	}()
	return settings
}

// readSettingsPush reads one SSE settings push or fails the test on timeout.
func readSettingsPush(t *testing.T, ch <-chan nodes.NodeSettings) nodes.NodeSettings {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an SSE settings push")
		return nodes.NodeSettings{}
	}
}

// nodeAuthPut issues a node-bearer PUT /api/nodes/{urlID}/settings.
func nodeAuthPut(t *testing.T, srvURL, urlID, rawKey string, body apidto.NodeSettingsRequest) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut, srvURL+"/api/nodes/"+urlID+"/settings", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("node-auth PUT failed: %v", err)
	}
	return resp
}

// TestUpdateNodeSettings_NodeAuth_PreservesStoredMaxJobs is acceptance (b): a
// node-bearer PathMap push leaves the stored MaxJobs unchanged in BOTH the
// persisted row AND the SSE-pushed settings, even though the request body
// carries MaxJobs=0 (the exact value that would zero the node's cap if
// forwarded, D3).
func TestUpdateNodeSettings_NodeAuth_PreservesStoredMaxJobs(t *testing.T) {
	mux, reg, _, settingsStore, nodeSettingsStore, nodeKeyStore, _ := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	serverDir := t.TempDir()
	for _, name := range []string{"Movie A", "Movie B", "Movie C"} {
		if err := os.Mkdir(filepath.Join(serverDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), serverDir); err != nil {
		t.Fatalf("settingsStore.Set: %v", err)
	}

	id, rawKey, err := nodeKeyStore.Create(ctx, "node-a")
	if err != nil {
		t.Fatalf("nodekeys.Create: %v", err)
	}
	// Stored MaxJobs = 4 (the operator-owned value the node must never touch).
	if err := nodeSettingsStore.Set(ctx, id, nodesettings.Settings{MaxJobs: 4}); err != nil {
		t.Fatalf("pre-seed MaxJobs: %v", err)
	}
	settings := connectCapturingNode(t, reg, id, []string{"Movie A", "Movie B", "Movie C"})

	resp := nodeAuthPut(t, srv.URL, id, rawKey, apidto.NodeSettingsRequest{
		PathMap: []apidto.NodePathMappingInput{
			{Key: apidto.LibraryPathMoviesRoot, NodePath: "/mnt/movies"},
		},
		MaxJobs:    0, // body value that MUST be ignored
		MediaRoots: []string{"/mnt/media"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	got, _, err := nodeSettingsStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MaxJobs != 4 {
		t.Errorf("persisted MaxJobs: got %d, want 4 (the node body's MaxJobs=0 must not persist)", got.MaxJobs)
	}

	push := readSettingsPush(t, settings)
	if push.MaxJobs != 4 {
		t.Errorf("SSE-pushed MaxJobs: got %d, want 4 (a node push must not zero the live cap over SSE)", push.MaxJobs)
	}
}

// TestUpdateNodeSettings_OperatorAuth_PersistsCPUCapPercent_AndFrameCarriesIt
// is the Stage-2 operator-path acceptance for the CPU governor: an operator PUT
// carrying cpuCapPercent persists it in node_max_jobs AND the authoritative SSE
// settings frame re-pushed to the live node carries it — the operator-owned cap
// travels the same write+push path as MaxJobs.
func TestUpdateNodeSettings_OperatorAuth_PersistsCPUCapPercent_AndFrameCarriesIt(t *testing.T) {
	mux, reg, _, _, nodeSettingsStore, _, apiKey := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	// Node already approved with a prior cap, connected to capture the push.
	if err := nodeSettingsStore.Set(ctx, "node-a", nodesettings.Settings{MaxJobs: 2, CPUCapPercent: 10}); err != nil {
		t.Fatalf("pre-seed: %v", err)
	}
	settings := connectCapturingNode(t, reg, "node-a", nil)

	body, _ := json.Marshal(apidto.NodeSettingsRequest{
		MaxJobs:       7,
		CPUCapPercent: 50,
	})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/nodes/node-a/settings", bytes.NewReader(body))
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	got, _, err := nodeSettingsStore.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CPUCapPercent != 50 {
		t.Errorf("persisted CPUCapPercent: got %d, want 50", got.CPUCapPercent)
	}
	if got.MaxJobs != 7 {
		t.Errorf("persisted MaxJobs: got %d, want 7", got.MaxJobs)
	}

	push := readSettingsPush(t, settings)
	if push.CPUCapPercent != 50 {
		t.Errorf("SSE-pushed CPUCapPercent: got %d, want 50 (the frame must carry the operator's cap)", push.CPUCapPercent)
	}
}

// TestUpdateNodeSettings_NodeAuth_PreservesStoredCPUCapPercent is the no-wipe
// guard for the CPU governor: a node-bearer PathMap push must leave the stored,
// operator-owned cpuCapPercent unchanged in BOTH the persisted row AND the SSE
// frame — even though the node request body carries no cap. This is the exact
// bug class MaxJobs already guards (TestUpdateNodeSettings_NodeAuth_Preserves-
// StoredMaxJobs): verifyAndBuildPersistedSettings must thread the stored cap
// through, or Set's unconditional column upsert would zero it to 0 on any
// node-authored path-map save.
func TestUpdateNodeSettings_NodeAuth_PreservesStoredCPUCapPercent(t *testing.T) {
	mux, reg, _, settingsStore, nodeSettingsStore, nodeKeyStore, _ := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	serverDir := t.TempDir()
	for _, name := range []string{"Movie A", "Movie B", "Movie C"} {
		if err := os.Mkdir(filepath.Join(serverDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), serverDir); err != nil {
		t.Fatalf("settingsStore.Set: %v", err)
	}

	id, rawKey, err := nodeKeyStore.Create(ctx, "node-a")
	if err != nil {
		t.Fatalf("nodekeys.Create: %v", err)
	}
	// Stored cap = 50 (the operator-owned value the node must never touch).
	if err := nodeSettingsStore.Set(ctx, id, nodesettings.Settings{MaxJobs: 4, CPUCapPercent: 50}); err != nil {
		t.Fatalf("pre-seed CPUCapPercent: %v", err)
	}
	settings := connectCapturingNode(t, reg, id, []string{"Movie A", "Movie B", "Movie C"})

	resp := nodeAuthPut(t, srv.URL, id, rawKey, apidto.NodeSettingsRequest{
		PathMap: []apidto.NodePathMappingInput{
			{Key: apidto.LibraryPathMoviesRoot, NodePath: "/mnt/movies"},
		},
		// No CPUCapPercent in the node body — it must NOT zero the stored cap.
		MediaRoots: []string{"/mnt/media"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	got, _, err := nodeSettingsStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CPUCapPercent != 50 {
		t.Errorf("persisted CPUCapPercent: got %d, want 50 (a node path-map save must not zero the operator's cap)", got.CPUCapPercent)
	}

	push := readSettingsPush(t, settings)
	if push.CPUCapPercent != 50 {
		t.Errorf("SSE-pushed CPUCapPercent: got %d, want 50 (a node push must not zero the cap over SSE)", push.CPUCapPercent)
	}
}

// TestUpdateNodeSettings_NodeAuth_URLIdIgnored_CannotWriteAnotherNode is
// acceptance (c), the security-critical test: node A, authenticated as A, puts
// node B's real id in the URL. A's row must be written; B's must never be.
func TestUpdateNodeSettings_NodeAuth_URLIdIgnored_CannotWriteAnotherNode(t *testing.T) {
	mux, reg, _, settingsStore, nodeSettingsStore, nodeKeyStore, _ := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	serverDir := t.TempDir()
	for _, name := range []string{"Movie A", "Movie B", "Movie C"} {
		if err := os.Mkdir(filepath.Join(serverDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), serverDir); err != nil {
		t.Fatalf("settingsStore.Set: %v", err)
	}

	idA, rawKeyA, err := nodeKeyStore.Create(ctx, "node-a")
	if err != nil {
		t.Fatalf("nodekeys.Create A: %v", err)
	}
	idB, _, err := nodeKeyStore.Create(ctx, "node-b")
	if err != nil {
		t.Fatalf("nodekeys.Create B: %v", err)
	}
	// A is connected and answers browse; B is a real but not-connected node.
	connectFakeNode(t, reg, idA, []string{"Movie A", "Movie B", "Movie C"})

	// A authenticates with its own key but targets B's id in the URL.
	resp := nodeAuthPut(t, srv.URL, idB, rawKeyA, apidto.NodeSettingsRequest{
		PathMap: []apidto.NodePathMappingInput{
			{Key: apidto.LibraryPathMoviesRoot, NodePath: "/mnt/movies-A"},
		},
		MediaRoots: []string{"/mnt/media"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 (write lands on A's own row), got %d", resp.StatusCode)
	}

	gotA, okA, err := nodeSettingsStore.Get(ctx, idA)
	if err != nil {
		t.Fatalf("Get A: %v", err)
	}
	if !okA || len(gotA.PathMappings) != 1 || gotA.PathMappings[0].NodePath != "/mnt/movies-A" {
		t.Errorf("expected A's own row written, got ok=%v %+v", okA, gotA)
	}
	if _, okB, _ := nodeSettingsStore.Get(ctx, idB); okB {
		t.Fatal("SECURITY: node B's row was written via a URL-id spoof — the handler must key strictly by the bearer identity")
	}
}

// TestUpdateNodeSettings_OperatorAuth_ChangesOnlyMaxJobs is acceptance (d): an
// operator PUT changes MaxJobs and leaves the node-owned PathMap intact —
// PathMap in an operator body is ignored. It ALSO proves the mediaRoots gate
// (empty/trivial → 422) applies ONLY to the node-auth path: this operator
// request carries NO MediaRoots at all and still succeeds (204), confirming the
// operator MaxJobs write is never gated on mediaRoots.
func TestUpdateNodeSettings_OperatorAuth_ChangesOnlyMaxJobs(t *testing.T) {
	mux, _, _, settingsStore, nodeSettingsStore, _, apiKey := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), "/data/movies"); err != nil {
		t.Fatalf("settingsStore.Set: %v", err)
	}
	// Node-authored state already in place.
	if err := nodeSettingsStore.Set(ctx, "node-a", nodesettings.Settings{
		PathMappings: []nodesettings.PathMappingEntry{
			{LibraryPathKey: string(apidto.LibraryPathMoviesRoot), NodePath: "/mnt/node-owned", VerificationStatus: nodesettings.VerificationVerified},
		},
		MaxJobs: 2,
	}); err != nil {
		t.Fatalf("pre-seed: %v", err)
	}

	body, _ := json.Marshal(apidto.NodeSettingsRequest{
		// Operator tries to change PathMap too; it must be IGNORED.
		PathMap: []apidto.NodePathMappingInput{
			{Key: apidto.LibraryPathMoviesRoot, NodePath: "/mnt/operator-tried-to-change-this"},
		},
		MaxJobs: 7,
	})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/nodes/node-a/settings", bytes.NewReader(body))
	req.Header.Set("X-Api-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	got, _, err := nodeSettingsStore.Get(ctx, "node-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MaxJobs != 7 {
		t.Errorf("MaxJobs: got %d, want 7 (operator change)", got.MaxJobs)
	}
	if len(got.PathMappings) != 1 || got.PathMappings[0].NodePath != "/mnt/node-owned" {
		t.Errorf("PathMap must survive an operator MaxJobs save untouched, got %+v", got.PathMappings)
	}
}

// TestPushPersistedNodeSettings_PopulatesWireKey proves the reconnect
// conversion site (pushPersistedNodeSettings) carries the persisted
// LibraryPathKey onto the wire PathMapping. This is the load-bearing site for
// the legacy-mapping display fix: a mapping set via the OLD server-side operator
// UI lives only in nodeSettingsStore (never in the node's own AuthoredPaths), so
// its wire Key can ONLY come from here on reconnect — that Key is what lets the
// tray render it as mapped-with-its-real-path instead of "not set".
func TestPushPersistedNodeSettings_PopulatesWireKey(t *testing.T) {
	_, reg, _, settingsStore, nodeSettingsStore, nodeKeyStore, _ := testNodesMux(t)

	ctx := context.Background()
	if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), "/data/movies"); err != nil {
		t.Fatalf("settingsStore.Set: %v", err)
	}
	id, _, err := nodeKeyStore.Create(ctx, "node-a")
	if err != nil {
		t.Fatalf("nodekeys.Create: %v", err)
	}
	// A persisted (server-side) mapping — the legacy shape: present in
	// nodeSettingsStore, and the node has no local AuthoredPaths record of it.
	if err := nodeSettingsStore.Set(ctx, id, nodesettings.Settings{
		PathMappings: []nodesettings.PathMappingEntry{
			{LibraryPathKey: string(apidto.LibraryPathMoviesRoot), NodePath: "/mnt/movies", VerificationStatus: nodesettings.VerificationVerified},
		},
		MaxJobs: 2,
	}); err != nil {
		t.Fatalf("pre-seed: %v", err)
	}

	settings := connectCapturingNode(t, reg, id, nil)
	if err := pushPersistedNodeSettings(ctx, reg, settingsStore, nodeSettingsStore, id); err != nil {
		t.Fatalf("pushPersistedNodeSettings: %v", err)
	}
	push := readSettingsPush(t, settings)
	if len(push.PathMap) != 1 {
		t.Fatalf("expected 1 pushed mapping, got %+v", push.PathMap)
	}
	if push.PathMap[0].Key != string(apidto.LibraryPathMoviesRoot) {
		t.Errorf("wire Key = %q, want %q", push.PathMap[0].Key, string(apidto.LibraryPathMoviesRoot))
	}
	if push.PathMap[0].Server != "/data/movies" || push.PathMap[0].Local != "/mnt/movies" {
		t.Errorf("Server/Local = %q/%q, want /data/movies//mnt/movies", push.PathMap[0].Server, push.PathMap[0].Local)
	}
}

// TestUpdateNodeSettings_NodeAuth_Clear_RemovesRowAndDoesNotReappear is
// acceptance (f): a node clear (explicit Clear field) deletes the (node,key)
// row, and it does NOT reappear on the node's next reconnect push.
func TestUpdateNodeSettings_NodeAuth_Clear_RemovesRowAndDoesNotReappear(t *testing.T) {
	mux, reg, _, settingsStore, nodeSettingsStore, nodeKeyStore, _ := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), "/data/movies"); err != nil {
		t.Fatalf("settingsStore.Set: %v", err)
	}
	id, rawKey, err := nodeKeyStore.Create(ctx, "node-a")
	if err != nil {
		t.Fatalf("nodekeys.Create: %v", err)
	}
	// A previously-verified mapping is in place.
	if err := nodeSettingsStore.Set(ctx, id, nodesettings.Settings{
		PathMappings: []nodesettings.PathMappingEntry{
			{LibraryPathKey: string(apidto.LibraryPathMoviesRoot), NodePath: "/mnt/stale", VerificationStatus: nodesettings.VerificationVerified},
		},
		MaxJobs: 2,
	}); err != nil {
		t.Fatalf("pre-seed: %v", err)
	}

	// A clear does not need the node connected (no verification round-trip).
	resp := nodeAuthPut(t, srv.URL, id, rawKey, apidto.NodeSettingsRequest{
		PathMap: []apidto.NodePathMappingInput{
			{Key: apidto.LibraryPathMoviesRoot, Clear: true},
		},
		MediaRoots: []string{"/mnt/media"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	got, _, err := nodeSettingsStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, e := range got.PathMappings {
		if e.LibraryPathKey == string(apidto.LibraryPathMoviesRoot) {
			t.Fatalf("expected the movies row to be deleted, still present: %+v", e)
		}
	}

	// Reconnect: pushPersistedNodeSettings pushes only persisted rows, so the
	// cleared key must NOT reappear in the push.
	settings := connectCapturingNode(t, reg, id, nil)
	if err := pushPersistedNodeSettings(ctx, reg, settingsStore, nodeSettingsStore, id); err != nil {
		t.Fatalf("pushPersistedNodeSettings: %v", err)
	}
	push := readSettingsPush(t, settings)
	for _, pm := range push.PathMap {
		if pm.Local == "/mnt/stale" {
			t.Errorf("cleared mapping reappeared on reconnect push: %+v", pm)
		}
	}
}

// TestUpdateNodeSettings_NodeAuth_BlankNodePathIsSkipNotDelete is acceptance
// (f2): a blank NodePath with no Clear field is a no-op skip — it does NOT
// delete the row, proving blank is not overloaded as the delete signal.
func TestUpdateNodeSettings_NodeAuth_BlankNodePathIsSkipNotDelete(t *testing.T) {
	mux, _, _, settingsStore, nodeSettingsStore, nodeKeyStore, _ := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), "/data/movies"); err != nil {
		t.Fatalf("settingsStore.Set: %v", err)
	}
	id, rawKey, err := nodeKeyStore.Create(ctx, "node-a")
	if err != nil {
		t.Fatalf("nodekeys.Create: %v", err)
	}
	if err := nodeSettingsStore.Set(ctx, id, nodesettings.Settings{
		PathMappings: []nodesettings.PathMappingEntry{
			{LibraryPathKey: string(apidto.LibraryPathMoviesRoot), NodePath: "/mnt/keep-me", VerificationStatus: nodesettings.VerificationVerified},
		},
		MaxJobs: 2,
	}); err != nil {
		t.Fatalf("pre-seed: %v", err)
	}

	resp := nodeAuthPut(t, srv.URL, id, rawKey, apidto.NodeSettingsRequest{
		PathMap: []apidto.NodePathMappingInput{
			{Key: apidto.LibraryPathMoviesRoot, NodePath: "", Clear: false}, // blank, no clear
		},
		MediaRoots: []string{"/mnt/media"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	got, _, err := nodeSettingsStore.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.PathMappings) != 1 || got.PathMappings[0].NodePath != "/mnt/keep-me" {
		t.Errorf("a blank NodePath must leave the row untouched (blank != delete), got %+v", got.PathMappings)
	}
}

// TestUpdateNodeSettings_NodeAuth_RejectedWhenZeroMediaRoots is acceptance (g):
// a node-auth set is hard-rejected when the request reports zero mediaRoots,
// before any verification runs, and nothing is persisted.
func TestUpdateNodeSettings_NodeAuth_RejectedWhenZeroMediaRoots(t *testing.T) {
	mux, reg, _, settingsStore, nodeSettingsStore, nodeKeyStore, _ := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	serverDir := t.TempDir()
	for _, name := range []string{"Movie A", "Movie B", "Movie C"} {
		if err := os.Mkdir(filepath.Join(serverDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), serverDir); err != nil {
		t.Fatalf("settingsStore.Set: %v", err)
	}
	id, rawKey, err := nodeKeyStore.Create(ctx, "node-a")
	if err != nil {
		t.Fatalf("nodekeys.Create: %v", err)
	}
	// Node is connected and WOULD verify fine — proving the reject is the
	// mediaRoots gate, not a verification failure.
	connectFakeNode(t, reg, id, []string{"Movie A", "Movie B", "Movie C"})

	resp := nodeAuthPut(t, srv.URL, id, rawKey, apidto.NodeSettingsRequest{
		PathMap: []apidto.NodePathMappingInput{
			{Key: apidto.LibraryPathMoviesRoot, NodePath: "/mnt/movies"},
		},
		// MediaRoots deliberately omitted (zero).
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 when the node has zero mediaRoots, got %d", resp.StatusCode)
	}

	if _, ok, _ := nodeSettingsStore.Get(ctx, id); ok {
		t.Error("nothing must be persisted when the mediaRoots gate rejects the request")
	}
}

// TestUpdateNodeSettings_NodeAuth_RejectedWhenMediaRootIsTrivial hardens
// acceptance (g) per D9: a non-empty but TRIVIAL mediaRoot ("/" or a shallow
// one-segment mount) provides zero real containment and must be rejected the
// same way as an empty list — 422, before verification, nothing persisted.
func TestUpdateNodeSettings_NodeAuth_RejectedWhenMediaRootIsTrivial(t *testing.T) {
	for _, trivial := range []string{"/", "/mnt", "//", "/mnt/"} {
		t.Run(trivial, func(t *testing.T) {
			mux, reg, _, settingsStore, nodeSettingsStore, nodeKeyStore, _ := testNodesMux(t)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			ctx := context.Background()
			serverDir := t.TempDir()
			for _, name := range []string{"Movie A", "Movie B", "Movie C"} {
				if err := os.Mkdir(filepath.Join(serverDir, name), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), serverDir); err != nil {
				t.Fatalf("settingsStore.Set: %v", err)
			}
			id, rawKey, err := nodeKeyStore.Create(ctx, "node-a")
			if err != nil {
				t.Fatalf("nodekeys.Create: %v", err)
			}
			// Connected and WOULD verify fine — proving the reject is the
			// triviality gate, not a verification failure.
			connectFakeNode(t, reg, id, []string{"Movie A", "Movie B", "Movie C"})

			resp := nodeAuthPut(t, srv.URL, id, rawKey, apidto.NodeSettingsRequest{
				PathMap: []apidto.NodePathMappingInput{
					{Key: apidto.LibraryPathMoviesRoot, NodePath: "/mnt/media/movies"},
				},
				MediaRoots: []string{trivial},
			})
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422 for trivial mediaRoot %q, got %d", trivial, resp.StatusCode)
			}
			if _, ok, _ := nodeSettingsStore.Get(ctx, id); ok {
				t.Errorf("nothing must be persisted when a trivial mediaRoot %q is rejected", trivial)
			}
		})
	}
}

// TestUpdateNodeSettings_NodeAuth_RejectedWhenNodePathIsTrivial is the
// server-side half of Stage 4's nodePath guardrail: a non-blank but TRIVIAL
// nodePath ("/" or a shallow one-segment mount) provides zero containment and is
// hard-rejected (422) BEFORE the verification gate runs, with nothing persisted.
// The mediaRoots self-report here is valid, proving the reject is specifically
// the nodePath triviality check (the same shared nodepath.Trivial rule the
// mediaRoots gate and the node use), not the mediaRoots gate or a verification
// failure. Empty nodePath is deliberately NOT covered here — blank is a no-op
// skip server-side (D7), enforced node-side instead.
func TestUpdateNodeSettings_NodeAuth_RejectedWhenNodePathIsTrivial(t *testing.T) {
	for _, trivial := range []string{"/", "/mnt", "//", "/mnt/"} {
		t.Run(trivial, func(t *testing.T) {
			mux, reg, _, settingsStore, nodeSettingsStore, nodeKeyStore, _ := testNodesMux(t)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			ctx := context.Background()
			serverDir := t.TempDir()
			for _, name := range []string{"Movie A", "Movie B", "Movie C"} {
				if err := os.Mkdir(filepath.Join(serverDir, name), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), serverDir); err != nil {
				t.Fatalf("settingsStore.Set: %v", err)
			}
			id, rawKey, err := nodeKeyStore.Create(ctx, "node-a")
			if err != nil {
				t.Fatalf("nodekeys.Create: %v", err)
			}
			// Connected and WOULD verify fine — proving the reject is the nodePath
			// triviality gate, not a verification failure.
			connectFakeNode(t, reg, id, []string{"Movie A", "Movie B", "Movie C"})

			resp := nodeAuthPut(t, srv.URL, id, rawKey, apidto.NodeSettingsRequest{
				PathMap: []apidto.NodePathMappingInput{
					{Key: apidto.LibraryPathMoviesRoot, NodePath: trivial},
				},
				MediaRoots: []string{"/mnt/media"}, // valid, so only nodePath is at fault
			})
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422 for trivial nodePath %q, got %d", trivial, resp.StatusCode)
			}
			if _, ok, _ := nodeSettingsStore.Get(ctx, id); ok {
				t.Errorf("nothing must be persisted when a trivial nodePath %q is rejected", trivial)
			}
		})
	}
}

// TestNodeStream_ConnectAck_CarriesLibraryPathKeyCatalog verifies D4: the
// server exposes the bounded library-path-key catalog to the node by
// piggybacking it on the connect ack, so the node can render pickers for
// unconfigured keys without a separate endpoint. (The stale
// TestNodeStream_ConnectAck in nodes_test.go cannot cover this — it runs
// against NewMux, which never registers the node routes.)
func TestNodeStream_ConnectAck_CarriesLibraryPathKeyCatalog(t *testing.T) {
	mux, _, _, _, _, nodeKeyStore, _ := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, rawKey, err := nodeKeyStore.Create(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("nodekeys.Create: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/nodes/stream", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	var eventType, dataLine string
readLoop:
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			dataLine = strings.TrimPrefix(line, "data: ")
		case line == "" && dataLine != "":
			break readLoop
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading SSE stream: %v", err)
	}
	if eventType != "ack" {
		t.Fatalf("expected first event %q, got %q", "ack", eventType)
	}

	var ack nodes.ConnectAck
	if err := json.Unmarshal([]byte(dataLine), &ack); err != nil {
		t.Fatalf("decoding ConnectAck: %v", err)
	}
	want := []string{
		string(apidto.LibraryPathMoviesRoot),
		string(apidto.LibraryPathSeriesRoot),
		string(apidto.LibraryPathAdultRoot),
		string(apidto.LibraryPathMoviesKids),
		string(apidto.LibraryPathSeriesKids),
	}
	if !reflect.DeepEqual(ack.LibraryPathKeys, want) {
		t.Errorf("ConnectAck.LibraryPathKeys = %v, want the 5-key catalog %v", ack.LibraryPathKeys, want)
	}
}
