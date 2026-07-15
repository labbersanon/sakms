package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/adultnewest"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
)

// TestBackfillAdultDurationsHandler_CorrectsZeroDurationTPDBScenes is the
// handler test for the one-off duration backfill (see
// adult_duration_backfill.go's package doc): a tpdb-sourced Scene row stuck
// at duration=0 gets corrected via a live GetSceneByID re-fetch, while a
// non-zero-duration row and a non-tpdb row are both left untouched.
func TestBackfillAdultDurationsHandler_CorrectsZeroDurationTPDBScenes(t *testing.T) {
	fakeTPDB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/scenes/scene-1" {
			t.Fatalf("expected only /scenes/scene-1 to be fetched (already-correct/non-tpdb rows must not trigger a TPDB call), got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"_id": "scene-1", "title": "Scene One", "duration": 1863,
		}})
	}))
	defer fakeTPDB.Close()

	fakeOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Ollama must not be called by the duration backfill — it only re-fetches durations by id")
	}))
	defer fakeOllama.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore := testStores(t)
	ctx := context.Background()
	for _, c := range []struct{ service, url string }{
		{"tpdb", fakeTPDB.URL},
		{"ollama", fakeOllama.URL},
	} {
		if err := connStore.Upsert(ctx, c.service, c.url, "test-key"); err != nil {
			t.Fatalf("seeding %s connection: %v", c.service, err)
		}
	}
	if err := settingsStore.Set(ctx, mode.AIModelKey, "test-model"); err != nil {
		t.Fatalf("seeding ai model: %v", err)
	}

	if err := adultNewestReleaseStore.Insert(ctx, adultnewest.MatchedRelease{
		RowType: adultnewest.RowScene, EntityID: "scene-1", EntitySource: "tpdb",
		EntityTitle: "Scene One", EntityDurationSeconds: 0,
	}); err != nil {
		t.Fatalf("seeding scene-1: %v", err)
	}
	if err := adultNewestReleaseStore.Insert(ctx, adultnewest.MatchedRelease{
		RowType: adultnewest.RowScene, EntityID: "scene-2", EntitySource: "tpdb",
		EntityTitle: "Scene Two", EntityDurationSeconds: 1800,
	}); err != nil {
		t.Fatalf("seeding scene-2: %v", err)
	}
	if err := adultNewestReleaseStore.Insert(ctx, adultnewest.MatchedRelease{
		RowType: adultnewest.RowScene, EntityID: "scene-3", EntitySource: "stashdb",
		EntityTitle: "Scene Three", EntityDurationSeconds: 0,
	}); err != nil {
		t.Fatalf("seeding scene-3: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/modes/adult/newest-rows/backfill-durations", "application/json", nil)
	if err != nil {
		t.Fatalf("backfill POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got backfillAdultDurationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Checked != 1 {
		t.Errorf("Checked = %d, want 1 (only the zero-duration tpdb-sourced scene)", got.Checked)
	}
	if got.Updated != 1 {
		t.Errorf("Updated = %d, want 1", got.Updated)
	}
	if len(got.Errors) != 0 {
		t.Errorf("Errors = %v, want none", got.Errors)
	}

	list, err := adultNewestReleaseStore.List(ctx, adultnewest.RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("listing scenes: %v", err)
	}
	byID := map[string]int{}
	for _, m := range list {
		byID[m.EntityID] = m.EntityDurationSeconds
	}
	if byID["scene-1"] != 1863 {
		t.Errorf("scene-1 duration = %d, want the corrected 1863", byID["scene-1"])
	}
	if byID["scene-2"] != 1800 {
		t.Errorf("scene-2 (already non-zero) duration = %d, want it left untouched at 1800", byID["scene-2"])
	}
	if byID["scene-3"] != 0 {
		t.Errorf("scene-3 (stashdb-sourced) duration = %d, want it left untouched at 0", byID["scene-3"])
	}
}
