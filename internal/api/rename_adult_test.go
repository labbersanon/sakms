package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/curtiswtaylorjr/sak/internal/mode"
	"github.com/curtiswtaylorjr/sak/internal/proposals"
)

// sceneUUID is embedded in the unmapped folder name so identification resolves
// via the direct UUID lookup path (Identify.tryUUIDLookup) — skipping Ollama,
// the similarity gate, and all-but-the-first throttle wait, which keeps this
// one test (the only one running the real mode.Build with its 1s production
// throttle) fast and non-flaky.
const sceneUUID = "a29768db-b3cd-4a71-a75e-4294373207bb"

// fakeWhisparrHandler serves just enough of Whisparr V3's API for an Adult
// Scan followed by an Apply, capturing the Add body so the test can assert the
// scene identifiers made it onto the wire.
func fakeWhisparrHandler(t *testing.T, addedID int, addBody *map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/rootfolder":
			w.Write([]byte(`[{"id":1,"path":"/media/Adult","accessible":true,"freeSpace":1,"unmappedFolders":[
				{"name":"Some.Scene.` + sceneUUID + `","path":"/media/Adult/Some.Scene.` + sceneUUID + `","relativePath":"Some.Scene.` + sceneUUID + `"}
			]}]`))
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodGet:
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(addBody)
			json.NewEncoder(w).Encode(map[string]any{"id": addedID})
		case r.URL.Path == "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":4,"name":"HD"}]`))
		case r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected whisparr request: %s %s", r.Method, r.URL.Path)
		}
	}
}

// fakeStashboxFindScene serves the StashDB GraphQL findScene-by-id query for
// sceneUUID (mirrors identify_test.go's stashboxWithFindScene shape, which is
// unexported cross-package).
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
// slice: a real mode.Build-backed Scan emits a Pending Adult proposal carrying
// a ForeignID, and applying it through the GENERIC /api/proposals/{id}/apply
// route — the exact route that was a foot-gun before this slice — now
// correctly registers a Whisparr scene with foreignId/itemType. No real
// network: whisparr, stashdb, and ollama are all httptest fakes.
func TestAdultRenameWorkflow_ScanThenApply_EndToEnd(t *testing.T) {
	var addBody map[string]any
	fakeWhisparr := httptest.NewServer(fakeWhisparrHandler(t, 88, &addBody))
	defer fakeWhisparr.Close()
	fakeStashDB := httptest.NewServer(fakeStashboxFindScene(t))
	defer fakeStashDB.Close()
	// Ollama is present (required for buildIdentifier to return a non-nil
	// identifier) but never called — the UUID path resolves before it.
	fakeOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Ollama must not be called when the UUID lookup path resolves")
	}))
	defer fakeOllama.Close()

	connStore, propStore, allowStore, settingsStore := testStores(t)
	ctx := context.Background()
	for _, c := range []struct{ service, url string }{
		{"whisparr", fakeWhisparr.URL},
		{"stashdb", fakeStashDB.URL},
		{"ollama", fakeOllama.URL},
	} {
		if err := connStore.Upsert(ctx, c.service, c.url, "test-key"); err != nil {
			t.Fatalf("seeding %s connection: %v", c.service, err)
		}
	}
	// Without the model set, buildIdentifier returns nil and Scan fast-fails.
	if err := settingsStore.Set(ctx, mode.AIModelKey, "test-model"); err != nil {
		t.Fatalf("seeding ollama model: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	// Scan → one Pending proposal carrying the scene identifiers.
	scanResp, err := http.Post(srv.URL+"/api/modes/adult/rename/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from scan, got %d", scanResp.StatusCode)
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
	if p.Title != "Some Scene" || p.RootFolderPath != "/media/Adult" || p.SourcePath != "/media/Adult/Some.Scene."+sceneUUID {
		t.Fatalf("unexpected proposal fields: %+v", p)
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

	// Apply through the generic route → Whisparr receives the identifiers.
	applyResp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(p.ID, 10)+"/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("apply POST failed: %v", err)
	}
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from apply, got %d", applyResp.StatusCode)
	}
	var applied proposals.Proposal
	if err := json.NewDecoder(applyResp.Body).Decode(&applied); err != nil {
		t.Fatalf("decoding apply response: %v", err)
	}
	if applied.Status != proposals.Applied || applied.TrackedID != 88 {
		t.Fatalf("expected the proposal to come back Applied with trackedId=88, got %+v", applied)
	}
	if addBody["foreignId"] != sceneUUID || addBody["itemType"] != "scene" {
		t.Fatalf("expected Whisparr to receive the scene identifiers, got %+v", addBody)
	}
}
