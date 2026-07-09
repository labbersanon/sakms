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

	"github.com/curtiswtaylorjr/tidyarr/internal/mediainfo"
	"github.com/curtiswtaylorjr/tidyarr/internal/mode"
	"github.com/curtiswtaylorjr/tidyarr/internal/proposals"
)

// TestAdultDedupWorkflow_ScanThenApply_EndToEnd is the full proof of the Adult
// Dedup slice against the real HTTP handlers, a real migrated SQLite database,
// a fake Whisparr + fake StashDB + present-but-unused fake Ollama, and real
// on-disk files. Two unmapped folders resolve (via the UUID path) to the same
// scene → one Dedup proposal carrying a ForeignID; applying it through the
// generic /api/proposals/{id}/apply route registers the surviving copy as a
// Whisparr scene (foreignId/itemType) and removes the loser.
func TestAdultDedupWorkflow_ScanThenApply_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	adultRoot := filepath.Join(dir, "Adult")
	nameSD := "Some.Scene.SD." + sceneUUID
	nameHD := "Some.Scene.HD." + sceneUUID
	dirSD := filepath.Join(adultRoot, nameSD)
	dirHD := filepath.Join(adultRoot, nameHD)
	fileSD := writeTestVideoFile(t, dirSD, "scene.mkv", 10)
	fileHD := writeTestVideoFile(t, dirHD, "scene.mkv", 10)

	var addBody map[string]any
	fakeWhisparr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/rootfolder":
			w.Write([]byte(`[{"id":1,"path":"` + adultRoot + `","accessible":true,"freeSpace":1,"unmappedFolders":[
				{"name":"` + nameSD + `","path":"` + dirSD + `","relativePath":"` + nameSD + `"},
				{"name":"` + nameHD + `","path":"` + dirHD + `","relativePath":"` + nameHD + `"}
			]}]`))
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodGet:
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&addBody)
			json.NewEncoder(w).Encode(map[string]any{"id": 88})
		case r.URL.Path == "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":4,"name":"HD"}]`))
		case r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected whisparr request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer fakeWhisparr.Close()
	fakeStashDB := httptest.NewServer(fakeStashboxFindScene(t))
	defer fakeStashDB.Close()
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
	if err := settingsStore.Set(ctx, mode.AIModelKey, "test-model"); err != nil {
		t.Fatalf("seeding ollama model: %v", err)
	}

	prober := &fakeDedupProber{byPath: map[string]*mediainfo.Probe{
		fileSD: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		fileHD: {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, prober, settingsStore))
	defer srv.Close()

	// Scan → one Dedup proposal carrying the scene identifier + 2 candidates.
	scanResp, err := http.Post(srv.URL+"/api/modes/adult/dedup/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(scanResp.Body)
		t.Fatalf("expected 200 from scan, got %d: %s", scanResp.StatusCode, body)
	}
	var scanned []proposals.Proposal
	json.NewDecoder(scanResp.Body).Decode(&scanned)
	if len(scanned) != 1 {
		t.Fatalf("expected exactly one dedup proposal, got %+v", scanned)
	}
	p := scanned[0]
	if p.Status != proposals.Pending || p.ForeignID != sceneUUID || p.ItemType != "scene" {
		t.Fatalf("expected a Pending proposal keyed by foreignID, got %+v", p)
	}
	if len(p.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %+v", p.Candidates)
	}

	// Apply with no body → auto-resolve by quality (the HD orphan wins, gets
	// registered as a scene; the SD orphan is removed).
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
	json.NewDecoder(applyResp.Body).Decode(&applied)
	if applied.Status != proposals.Applied || applied.TrackedID != 88 {
		t.Fatalf("expected the proposal Applied with trackedId=88, got %+v", applied)
	}
	if addBody["foreignId"] != sceneUUID || addBody["itemType"] != "scene" {
		t.Fatalf("expected Whisparr to receive the scene identifiers on register, got %+v", addBody)
	}
}
