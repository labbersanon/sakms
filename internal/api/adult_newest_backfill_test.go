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

// TestBackfillAdultImagesHandler_OnlyRefreshesTPDBSourcedRows is the handler
// test for the one-off poster backfill (see adult_newest_backfill.go's
// package doc): a tpdb-sourced Scene row's stale image gets corrected via a
// live GetSceneByID re-fetch, while a stashdb-sourced row is left untouched
// entirely — StashDB/FansDB image reliability is a separate, unconfirmed
// question, explicitly out of scope for this backfill.
func TestBackfillAdultImagesHandler_OnlyRefreshesTPDBSourcedRows(t *testing.T) {
	fakeTPDB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/scenes/scene-1" {
			t.Fatalf("expected only /scenes/scene-1 to be fetched (stashdb-sourced rows must not trigger a TPDB call), got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"_id": "scene-1", "title": "Scene One",
			"background": map[string]any{"large": "https://cdn.theporndb.net/scene/scene-1/large.jpg"},
		}})
	}))
	defer fakeTPDB.Close()

	fakeOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Ollama must not be called by the poster backfill — it only re-fetches images by id")
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
		EntityTitle: "Scene One", EntityImage: "https://old.example/broken.jpg",
	}); err != nil {
		t.Fatalf("seeding scene-1: %v", err)
	}
	if err := adultNewestReleaseStore.Insert(ctx, adultnewest.MatchedRelease{
		RowType: adultnewest.RowScene, EntityID: "scene-2", EntitySource: "stashdb",
		EntityTitle: "Scene Two", EntityImage: "https://old.example/broken2.jpg",
	}); err != nil {
		t.Fatalf("seeding scene-2: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/modes/adult/newest-rows/backfill-images", "application/json", nil)
	if err != nil {
		t.Fatalf("backfill POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var got backfillAdultImagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got.Checked != 1 {
		t.Errorf("Checked = %d, want 1 (only the tpdb-sourced row)", got.Checked)
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
	byID := map[string]string{}
	for _, m := range list {
		byID[m.EntityID] = m.EntityImage
	}
	if byID["scene-1"] != "https://cdn.theporndb.net/scene/scene-1/large.jpg" {
		t.Errorf("scene-1 image = %q, want the corrected TPDB CDN URL", byID["scene-1"])
	}
	if byID["scene-2"] != "https://old.example/broken2.jpg" {
		t.Errorf("scene-2 (stashdb-sourced) image = %q, want it left untouched", byID["scene-2"])
	}
}
