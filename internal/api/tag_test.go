package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
)

// TestTagWorkflow_Series_AddThenRemove_EndToEnd is Series' own libStore-
// backed counterpart — no Sonarr connection at all, proving the tag
// workflow works entirely locally now. Adult's generic *arr-backed Tag
// path is covered separately by TestTagWorkflow_Adult_AddThenRemove_EndToEnd.
func TestTagWorkflow_Series_AddThenRemove_EndToEnd(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	series, err := libStore.UpsertSeries(context.Background(), library.Series{
		TMDBID: 1, Title: "Some Show", RootFolderPath: "/tv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	itemPath := "/api/modes/series/items/" + strconv.FormatInt(series.ID, 10) + "/tags"

	addBody, _ := json.Marshal(addItemTagRequest{Label: "needs-review"})
	addResp, err := http.Post(srv.URL+itemPath, "application/json", bytes.NewReader(addBody))
	if err != nil {
		t.Fatalf("add tag POST failed: %v", err)
	}
	addResp.Body.Close()
	if addResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from add, got %d", addResp.StatusCode)
	}

	vocabResp, err := http.Get(srv.URL + "/api/modes/series/tags")
	if err != nil {
		t.Fatalf("list tags GET failed: %v", err)
	}
	defer vocabResp.Body.Close()
	var vocab []libraryTagEntry
	json.NewDecoder(vocabResp.Body).Decode(&vocab)
	if len(vocab) != 1 || vocab[0].Label != "needs-review" || vocab[0].ID != "needs-review" {
		t.Fatalf("expected the vocabulary to include the new tag, got %+v", vocab)
	}

	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+itemPath+"/needs-review", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("remove tag DELETE failed: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from remove, got %d", delResp.StatusCode)
	}

	tags, err := libStore.SeriesTags(context.Background(), series.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("expected no tags after removal, got %v", tags)
	}
}

// TestTagWorkflow_Movies_AddThenRemove_EndToEnd is Movies' own libStore-
// backed counterpart — no Radarr connection configured at all, proving the
// tag workflow works entirely locally now.
func TestTagWorkflow_Movies_AddThenRemove_EndToEnd(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	item, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Some Movie", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	itemPath := "/api/modes/movies/items/" + strconv.FormatInt(item.ID, 10) + "/tags"

	addBody, _ := json.Marshal(addItemTagRequest{Label: "needs-review"})
	addResp, err := http.Post(srv.URL+itemPath, "application/json", bytes.NewReader(addBody))
	if err != nil {
		t.Fatalf("add tag POST failed: %v", err)
	}
	addResp.Body.Close()
	if addResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from add, got %d", addResp.StatusCode)
	}

	vocabResp, err := http.Get(srv.URL + "/api/modes/movies/tags")
	if err != nil {
		t.Fatalf("list tags GET failed: %v", err)
	}
	defer vocabResp.Body.Close()
	var vocab []libraryTagEntry
	json.NewDecoder(vocabResp.Body).Decode(&vocab)
	if len(vocab) != 1 || vocab[0].Label != "needs-review" || vocab[0].ID != "needs-review" {
		t.Fatalf("expected the vocabulary to include the new tag, got %+v", vocab)
	}

	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+itemPath+"/needs-review", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("remove tag DELETE failed: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from remove, got %d", delResp.StatusCode)
	}

	tags, err := libStore.Tags(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("expected no tags after removal, got %v", tags)
	}
}

// TestTagWorkflow_Adult_AddThenRemove_EndToEnd is the Adult-mode twin of the
// test above: it drives the same add->list->remove cycle through SAK's
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

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "whisparr", fakeWhisparr.URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
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

// TestSceneTagWorkflow_Adult_AddThenRemove_EndToEnd is Adult's own library-
// backed scene-tag counterpart to the Movies/Series tests above — no Whisparr
// connection at all, driving the full add -> list-scene-tags -> vocabulary ->
// remove cycle through SAK's real mux against libStore's scene-tag methods.
// These /scenes/... routes are deliberately separate from Adult's still-
// Whisparr-backed /items and /tags routes (covered by
// TestTagWorkflow_Adult_AddThenRemove_EndToEnd), which stay untouched.
func TestSceneTagWorkflow_Adult_AddThenRemove_EndToEnd(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	scene, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: "abc-123", Title: "Some Scene", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	scenePath := "/api/modes/adult/scenes/" + strconv.FormatInt(scene.ID, 10) + "/tags"

	addBody, _ := json.Marshal(addItemTagRequest{Label: "needs-review"})
	addResp, err := http.Post(srv.URL+scenePath, "application/json", bytes.NewReader(addBody))
	if err != nil {
		t.Fatalf("add tag POST failed: %v", err)
	}
	addResp.Body.Close()
	if addResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from add, got %d", addResp.StatusCode)
	}

	// The scene's own tag list reflects the new tag.
	sceneTagsResp, err := http.Get(srv.URL + scenePath)
	if err != nil {
		t.Fatalf("list scene tags GET failed: %v", err)
	}
	defer sceneTagsResp.Body.Close()
	var sceneTags []string
	json.NewDecoder(sceneTagsResp.Body).Decode(&sceneTags)
	if len(sceneTags) != 1 || sceneTags[0] != "needs-review" {
		t.Fatalf("expected the scene to carry the new tag, got %+v", sceneTags)
	}

	// The vocabulary now reflects the new tag.
	vocabResp, err := http.Get(srv.URL + "/api/modes/adult/scenes/tags")
	if err != nil {
		t.Fatalf("list vocabulary GET failed: %v", err)
	}
	defer vocabResp.Body.Close()
	var vocab []libraryTagEntry
	json.NewDecoder(vocabResp.Body).Decode(&vocab)
	if len(vocab) != 1 || vocab[0].Label != "needs-review" || vocab[0].ID != "needs-review" {
		t.Fatalf("expected the vocabulary to include the new tag, got %+v", vocab)
	}

	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+scenePath+"/needs-review", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("remove tag DELETE failed: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from remove, got %d", delResp.StatusCode)
	}

	tags, err := libStore.SceneTags(context.Background(), scene.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("expected no tags after removal, got %v", tags)
	}
}

func TestAddItemTagHandler_RequiresLabel(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
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

// TestListTagsHandler_ModeNotConfigured proves Adult's tag vocabulary still
// requires a Whisparr connection (unchanged, *arr-backed) — Movies/Series
// no longer need any connection at all (see
// TestListTagsHandler_NoConnectionNeeded below).
func TestListTagsHandler_ModeNotConfigured(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/tags")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when whisparr isn't configured yet, got %d", resp.StatusCode)
	}
}

// TestListTagsHandler_Series_NoConnectionNeeded confirms Series' vocabulary
// works with ZERO connections configured too — Series is now off Sonarr
// the same way Movies is off Radarr.
func TestListTagsHandler_Series_NoConnectionNeeded(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/tags")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with no sonarr connection at all, got %d", resp.StatusCode)
	}
	var vocab []libraryTagEntry
	json.NewDecoder(resp.Body).Decode(&vocab)
	if len(vocab) != 0 {
		t.Fatalf("expected an empty vocabulary on a fresh install, got %+v", vocab)
	}
}

// TestListTagsHandler_Movies_NoConnectionNeeded confirms Movies' vocabulary
// works with ZERO connections configured — the whole point of eliminating
// Radarr for this mode.
func TestListTagsHandler_Movies_NoConnectionNeeded(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/tags")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with no radarr connection at all, got %d", resp.StatusCode)
	}
	var vocab []libraryTagEntry
	json.NewDecoder(resp.Body).Decode(&vocab)
	if len(vocab) != 0 {
		t.Fatalf("expected an empty vocabulary on a fresh install, got %+v", vocab)
	}
}
