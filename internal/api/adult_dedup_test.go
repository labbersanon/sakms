package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// TestAdultDedupWorkflow_ScanThenApply_EndToEnd is the full proof of the
// library-backed Adult Dedup slice (Whisparr eliminated, Stage 4) against the
// real HTTP handlers, a real migrated SQLite database, a fake StashDB +
// present-but-unused fake Ollama, and real on-disk files. Two scene folders
// resolve (via the UUID path) to the same (box, scene_id) → one Dedup proposal
// carrying that identity and 2 candidates; applying it through the generic
// /api/proposals/{id}/apply route records the surviving copy in SAK's own
// library (library_scenes) and removes the loser's file — no Whisparr.
func TestAdultDedupWorkflow_ScanThenApply_EndToEnd(t *testing.T) {
	adultRoot := t.TempDir()
	nameSD := "Some.Scene.SD." + sceneUUID
	nameHD := "Some.Scene.HD." + sceneUUID
	dirSD := filepath.Join(adultRoot, nameSD)
	dirHD := filepath.Join(adultRoot, nameHD)
	fileSD := writeTestVideoFile(t, dirSD, "scene.mkv", 10)
	fileHD := writeTestVideoFile(t, dirHD, "scene.mkv", 10)

	fakeStashDB := httptest.NewServer(fakeStashboxFindScene(t))
	defer fakeStashDB.Close()
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
	if err := settingsStore.Set(ctx, mode.AIModelKey, "test-model"); err != nil {
		t.Fatalf("seeding ollama model: %v", err)
	}
	if err := settingsStore.Set(ctx, adultLibraryRootFolderKey, adultRoot); err != nil {
		t.Fatalf("seeding adult root folder: %v", err)
	}

	prober := &fakeDedupProber{byPath: map[string]*mediainfo.Probe{
		fileSD: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		fileHD: {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, prober, testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil))
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
	// A dedup proposal keys by the raw (box, scene_id) give-back identity, not
	// ForeignID (which stays empty on the dedup path — the group identity lives
	// in GiveBackBox/GiveBackSceneID, the same separate-column pair
	// library_scenes is keyed on).
	if p.Status != proposals.Pending || p.ItemType != "scene" {
		t.Fatalf("expected a Pending scene proposal, got %+v", p)
	}
	if p.GiveBackBox != "stashdb" || p.GiveBackSceneID != sceneUUID {
		t.Fatalf("expected the raw (box, scene_id) give-back identity captured, got box=%q scene=%q", p.GiveBackBox, p.GiveBackSceneID)
	}
	if len(p.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %+v", p.Candidates)
	}

	// Apply with no body → auto-resolve by quality (the HD copy wins and is
	// recorded as the tracked scene; the SD copy's file is removed).
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
	if applied.Status != proposals.Applied || applied.TrackedID == 0 {
		t.Fatalf("expected the proposal Applied with a nonzero library scene id, got %+v", applied)
	}

	// The winning copy is now the one tracked scene for this (box, scene_id).
	scene, err := libStore.GetScene(ctx, "stashdb", sceneUUID)
	if err != nil {
		t.Fatalf("expected the surviving scene to be recorded, got: %v", err)
	}
	if scene.FilePath != fileHD {
		t.Errorf("expected the HD copy %q to be the tracked survivor, got %q", fileHD, scene.FilePath)
	}
	// The loser's file is gone.
	if _, err := os.Stat(fileSD); !os.IsNotExist(err) {
		t.Errorf("expected the SD loser file to be deleted, stat returned: %v", err)
	}
}
