package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTagWorkflow_AddThenRemove_EndToEnd exercises the real path a tag chip
// click takes: assign a tag (creating it upstream since it doesn't exist
// yet), confirm the vocabulary reflects it, then remove it — hitting
// Tidyarr's real HTTP handlers and a fake Radarr throughout. Unlike Rename/
// Purge/Dedup, there's no proposals queue involved: see internal/tag's doc
// comment for why assigning a tag is an immediate action, not a staged one.
func TestTagWorkflow_AddThenRemove_EndToEnd(t *testing.T) {
	tags := []map[string]any{}
	itemTags := []int{}

	fakeRadarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(tags)
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodPost:
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			newTag := map[string]any{"id": 7, "label": body["label"]}
			tags = append(tags, newTag)
			json.NewEncoder(w).Encode(newTag)
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodGet:
			ids := make([]any, len(itemTags))
			for i, id := range itemTags {
				ids[i] = id
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 9, "title": "Some Movie", "tags": ids})
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodPut:
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			itemTags = itemTags[:0]
			for _, v := range body["tags"].([]any) {
				itemTags = append(itemTags, int(v.(float64)))
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer fakeRadarr.Close()

	connStore, propStore, allowStore, settingsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "radarr", fakeRadarr.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	// Assign a brand-new tag.
	addBody, _ := json.Marshal(addItemTagRequest{Label: "needs-review"})
	addResp, err := http.Post(srv.URL+"/api/modes/movies/items/9/tags", "application/json", bytes.NewReader(addBody))
	if err != nil {
		t.Fatalf("add tag POST failed: %v", err)
	}
	addResp.Body.Close()
	if addResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from add, got %d", addResp.StatusCode)
	}
	if len(itemTags) != 1 || itemTags[0] != 7 {
		t.Fatalf("expected the item to be tagged with the newly created tag (7), got %v", itemTags)
	}

	// The vocabulary now reflects the newly created tag.
	vocabResp, err := http.Get(srv.URL + "/api/modes/movies/tags")
	if err != nil {
		t.Fatalf("list tags GET failed: %v", err)
	}
	defer vocabResp.Body.Close()
	var vocab []map[string]any
	json.NewDecoder(vocabResp.Body).Decode(&vocab)
	if len(vocab) != 1 || vocab[0]["label"] != "needs-review" {
		t.Fatalf("expected the vocabulary to include the new tag, got %+v", vocab)
	}

	// Remove it again.
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/modes/movies/items/9/tags/7", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("remove tag DELETE failed: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from remove, got %d", delResp.StatusCode)
	}
	if len(itemTags) != 0 {
		t.Fatalf("expected the item to have no tags after removal, got %v", itemTags)
	}
}

// TestTagWorkflow_Adult_AddThenRemove_EndToEnd is the Adult-mode twin of the
// test above: it drives the same add->list->remove cycle through Tidyarr's
// real mux, but against a fake Whisparr V3. Because Whisparr's itemResource()
// maps to "movie" (a Radarr fork — client.go's default: branch), the wire
// paths here are byte-identical to the Radarr run above; this test therefore
// pins that Adult routes as a *movie-shaped* app (not Sonarr/series — the
// default: t.Fatalf below is the guard that proves it). Whisparr-specificity
// itself is pinned separately by the "whisparr" connStore key here plus
// AppType()==servarr.Whisparr in internal/mode's TestBuild_AdultUsesWhisparr-
// Connection — there is no way to distinguish Whisparr from Radarr on the wire.
func TestTagWorkflow_Adult_AddThenRemove_EndToEnd(t *testing.T) {
	tags := []map[string]any{}
	itemTags := []int{}

	fakeWhisparr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(tags)
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodPost:
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			newTag := map[string]any{"id": 7, "label": body["label"]}
			tags = append(tags, newTag)
			json.NewEncoder(w).Encode(newTag)
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodGet:
			ids := make([]any, len(itemTags))
			for i, id := range itemTags {
				ids[i] = id
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 9, "title": "Some Scene", "tags": ids})
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodPut:
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			itemTags = itemTags[:0]
			for _, v := range body["tags"].([]any) {
				itemTags = append(itemTags, int(v.(float64)))
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer fakeWhisparr.Close()

	connStore, propStore, allowStore, settingsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "whisparr", fakeWhisparr.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	// Assign a brand-new tag.
	addBody, _ := json.Marshal(addItemTagRequest{Label: "needs-review"})
	addResp, err := http.Post(srv.URL+"/api/modes/adult/items/9/tags", "application/json", bytes.NewReader(addBody))
	if err != nil {
		t.Fatalf("add tag POST failed: %v", err)
	}
	addResp.Body.Close()
	if addResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from add, got %d", addResp.StatusCode)
	}
	if len(itemTags) != 1 || itemTags[0] != 7 {
		t.Fatalf("expected the item to be tagged with the newly created tag (7), got %v", itemTags)
	}

	// The vocabulary now reflects the newly created tag.
	vocabResp, err := http.Get(srv.URL + "/api/modes/adult/tags")
	if err != nil {
		t.Fatalf("list tags GET failed: %v", err)
	}
	defer vocabResp.Body.Close()
	var vocab []map[string]any
	json.NewDecoder(vocabResp.Body).Decode(&vocab)
	if len(vocab) != 1 || vocab[0]["label"] != "needs-review" {
		t.Fatalf("expected the vocabulary to include the new tag, got %+v", vocab)
	}

	// Remove it again.
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/modes/adult/items/9/tags/7", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("remove tag DELETE failed: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from remove, got %d", delResp.StatusCode)
	}
	if len(itemTags) != 0 {
		t.Fatalf("expected the item to have no tags after removal, got %v", itemTags)
	}
}

func TestAddItemTagHandler_RequiresLabel(t *testing.T) {
	connStore, propStore, allowStore, settingsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	body, _ := json.Marshal(addItemTagRequest{})
	resp, err := http.Post(srv.URL+"/api/modes/movies/items/9/tags", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing label, got %d", resp.StatusCode)
	}
}

func TestListTagsHandler_ModeNotConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/tags")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when radarr isn't configured yet, got %d", resp.StatusCode)
	}
}
