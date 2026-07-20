package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// sceneUUID is embedded in the scene folder name so identification resolves
// via the direct UUID lookup path (Identify.tryUUIDLookup) — skipping Ollama,
// the similarity gate, and all-but-the-first throttle wait, which keeps this
// one test (the only one running the real mode.Build with its 1s production
// throttle) fast and non-flaky. Shared with adult_dedup_test.go.
const sceneUUID = "a29768db-b3cd-4a71-a75e-4294373207bb"

// fakeStashboxFindScene serves the StashDB GraphQL findScene-by-id query for
// sceneUUID (mirrors identify_test.go's stashboxWithFindScene shape, which is
// unexported cross-package). Shared with adult_dedup_test.go.
func fakeStashboxFindScene(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				ID string `json:"id"`
			} `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Variables.ID != sceneUUID {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": nil}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": map[string]any{
			"id": sceneUUID, "title": "Some Scene", "release_date": "2021-01-01",
			"studio": map[string]any{"name": "Some Studio", "parent": nil},
		}}})
	}
}

// TestAdultRenameWorkflow_ScanThenApply_EndToEnd is the full proof of the
// library-backed Adult rename slice (Whisparr eliminated, Stage 4): a real
// mode.Build-backed Scan walks Adult's own library root folder, identifies the
// scene by its embedded UUID, and emits a Pending proposal carrying the raw
// (box, scene_id) give-back identity. Applying it through the GENERIC
// /api/proposals/{id}/apply route relocates+renames the file to SAK's Adult
// naming scheme and records it in SAK's own library (library_scenes) — no
// Whisparr anywhere. No real network: stashdb and ollama are httptest fakes.
func TestAdultRenameWorkflow_ScanThenApply_EndToEnd(t *testing.T) {
	adultRoot := t.TempDir()
	// A real scene file on disk under a UUID-named folder — ScanRootFolder
	// walks the actual filesystem now (not a Whisparr unmapped-folder list),
	// and the UUID in the parent folder name drives identification.
	sceneFile := writeTestVideoFile(t, filepath.Join(adultRoot, "Some.Scene."+sceneUUID), "scene.mp4", 10)

	fakeStashDB := httptest.NewServer(fakeStashboxFindScene(t))
	defer fakeStashDB.Close()
	// Ollama is present (required for buildIdentifier to return a non-nil
	// identifier) but never called — the UUID path resolves before it.
	fakeOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Ollama must not be called when the UUID lookup path resolves")
	}))
	defer fakeOllama.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	for _, c := range []struct{ service, url string }{
		{"stashdb", fakeStashDB.URL},
		{"ollama", fakeOllama.URL},
	} {
		if err := connStore.Upsert(ctx, c.service, c.url, "test-key"); err != nil {
			t.Fatalf("seeding %s connection: %v", c.service, err)
		}
		overrideFixedURL(t, c.service, c.url)
	}
	// Without the model set, buildIdentifier returns nil and Scan fast-fails.
	if err := settingsStore.Set(ctx, mode.AIModelKey, "test-model"); err != nil {
		t.Fatalf("seeding ollama model: %v", err)
	}
	// Adult's own free-typed library root folder (replaces Whisparr's rootfolder).
	if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, adultRoot); err != nil {
		t.Fatalf("seeding adult root folder: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	// Scan → one Pending proposal carrying the scene identifiers.
	scanResp, err := http.Post(srv.URL+"/api/modes/adult/rename/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(scanResp.Body)
		t.Fatalf("expected 200 from scan, got %d: %s", scanResp.StatusCode, body)
	}
	var scanned []proposals.Proposal
	if err := json.NewDecoder(scanResp.Body).Decode(&scanned); err != nil {
		t.Fatalf("decoding scan response: %v", err)
	}
	if len(scanned) != 1 {
		t.Fatalf("expected exactly one proposal, got %+v", scanned)
	}
	p := scanned[0]
	if p.Status != proposals.Pending || p.ForeignID != sceneUUID || p.ItemType != "scene" {
		t.Fatalf("expected a Pending proposal carrying the scene id, got %+v", p)
	}
	if p.GiveBackBox != "stashdb" || p.GiveBackSceneID != sceneUUID {
		t.Fatalf("expected the raw (box, scene_id) give-back identity captured, got box=%q scene=%q", p.GiveBackBox, p.GiveBackSceneID)
	}
	if p.Title != "Some Scene" || p.Studio != "Some Studio" || p.Date != "2021-01-01" {
		t.Fatalf("unexpected identification fields: %+v", p)
	}
	if p.SourcePath != sceneFile {
		t.Fatalf("expected SourcePath to be the resolved video file %q, got %q", sceneFile, p.SourcePath)
	}

	// The queue reflects what scan just staged.
	listResp, err := http.Get(srv.URL + "/api/modes/adult/rename/proposals")
	if err != nil {
		t.Fatalf("list GET failed: %v", err)
	}
	defer listResp.Body.Close()
	var listed []proposals.Proposal
	json.NewDecoder(listResp.Body).Decode(&listed)
	if len(listed) != 1 || listed[0].ID != p.ID || listed[0].ForeignID != sceneUUID {
		t.Fatalf("expected the queue to reflect the staged proposal, got %+v", listed)
	}

	// Apply through the generic route → the scene lands in SAK's own library.
	applyResp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(p.ID, 10)+"/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("apply POST failed: %v", err)
	}
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(applyResp.Body)
		t.Fatalf("expected 200 from apply, got %d: %s", applyResp.StatusCode, body)
	}
	var applied proposals.Proposal
	if err := json.NewDecoder(applyResp.Body).Decode(&applied); err != nil {
		t.Fatalf("decoding apply response: %v", err)
	}
	if applied.Status != proposals.Applied || applied.TrackedID == 0 {
		t.Fatalf("expected the proposal Applied with a nonzero library scene id, got %+v", applied)
	}

	// The scene is recorded under its (box, scene_id) identity, renamed to
	// SAK's Adult scheme (phash embedded, since the hasher succeeded).
	scene, err := libStore.GetScene(ctx, "stashdb", sceneUUID)
	if err != nil {
		t.Fatalf("expected the scene to be recorded in the library, got: %v", err)
	}
	wantDest := filepath.Join(adultRoot, "Some Studio - Some Scene (2021-01-01) [phash-ffffffffffffffff].mp4")
	if scene.Title != "Some Scene" || scene.Studio != "Some Studio" || scene.Date != "2021-01-01" || scene.FilePath != wantDest {
		t.Fatalf("unexpected recorded scene: %+v (wanted FilePath %q)", scene, wantDest)
	}
}
