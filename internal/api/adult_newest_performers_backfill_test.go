package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/adultnewest"
)

// TestBackfillAdultPerformersHandler_UpdatesTPDBRowsOnly proves the temporary
// performers backfill only touches entity_source=="tpdb" Scene/Movie rows —
// a stashdb-sourced row is skipped (not even checked), and a Studio/Performer
// row never enters the walked set at all (see ListTPDBSceneAndMovie).
func TestBackfillAdultPerformersHandler_UpdatesTPDBRowsOnly(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/scenes/tpdb-1":
			w.Write([]byte(`{"data":{"_id":"tpdb-1","title":"Scene One","performers":[{"name":"Jane Doe"}]}}`))
		case "/scenes/tpdb-2":
			w.Write([]byte(`{"data":{"_id":"tpdb-2","title":"Scene Two","performers":[]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	seed := []adultnewest.MatchedRelease{
		{RowType: adultnewest.RowScene, EntityID: "tpdb-1", EntitySource: "tpdb", EntityTitle: "Scene One"},
		{RowType: adultnewest.RowScene, EntityID: "tpdb-2", EntitySource: "tpdb", EntityTitle: "Scene Two"},
		{RowType: adultnewest.RowScene, EntityID: "sb-1", EntitySource: "stashdb", EntityTitle: "StashDB Scene"},
	}
	for _, m := range seed {
		if err := adultNewestReleaseStore.Insert(ctx, m); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/modes/adult/newest-rows/backfill-performers", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result struct {
		Checked int      `json:"checked"`
		Updated int      `json:"updated"`
		Errors  []string `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.Checked != 2 {
		t.Errorf("expected 2 tpdb rows checked (stashdb row excluded), got %d", result.Checked)
	}
	if result.Updated != 1 {
		t.Errorf("expected 1 row updated (Scene Two has no performers to backfill), got %d", result.Updated)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
	}

	list, err := adultNewestReleaseStore.List(ctx, adultnewest.RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	byID := map[string]adultnewest.MatchedRelease{}
	for _, m := range list {
		byID[m.EntityID] = m
	}
	if got := byID["tpdb-1"].Performers; len(got) != 1 || got[0] != "Jane Doe" {
		t.Errorf("expected tpdb-1 to be backfilled with Jane Doe, got %v", got)
	}
	if got := byID["sb-1"].Performers; len(got) != 0 {
		t.Errorf("expected the stashdb row to be left untouched, got %v", got)
	}
}
