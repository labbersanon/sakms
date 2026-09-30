package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

func fakeStashboxCatalogImages(urls []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				ID string `json:"id"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		images := make([]map[string]string, 0, len(urls))
		for _, u := range urls {
			images = append(images, map[string]string{"url": u})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": map[string]any{
			"id": req.Variables.ID, "title": "T", "images": images,
		}}})
	}
}

func newAdultPosterEditServer(t *testing.T, stashbox http.HandlerFunc) (*library.Store, *httptest.Server) {
	t.Helper()
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	fake := httptest.NewServer(stashbox)
	t.Cleanup(fake.Close)
	if err := connStore.Upsert(ctx, "stashdb", fake.URL, "test-key"); err != nil {
		t.Fatalf("stashdb: %v", err)
	}
	overrideFixedURL(t, "stashdb", fake.URL)
	retargetStashBoxDatabase(t, connStore.DB(), "stashdb", fake.URL)
	if err := settingsStore.Set(ctx, mode.AIModelKey, "test-model"); err != nil {
		t.Fatalf("ai model: %v", err)
	}
	mux := NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return libStore, srv
}

func TestAdultCatalogPosters_ListsSanitizedImages(t *testing.T) {
	urls := []string{"https://1.1.1.1/a.jpg", "https://1.1.1.1/b.jpg", "https://127.0.0.1/secret.jpg"}
	lib, srv := newAdultPosterEditServer(t, fakeStashboxCatalogImages(urls))
	scene, err := lib.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: "uuid-art", Title: "Art", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/modes/adult/scenes/" + strconv.FormatInt(scene.ID, 10) + "/catalog-posters")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET = %d (%s)", resp.StatusCode, b)
	}
	var out adultCatalogPostersResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.URLs) != 2 || out.URLs[0] != "https://1.1.1.1/a.jpg" || out.URLs[1] != "https://1.1.1.1/b.jpg" {
		t.Fatalf("urls = %v", out.URLs)
	}
}

func TestAdultCatalogPosters_LocalIsEmpty(t *testing.T) {
	lib, srv := newAdultPosterEditServer(t, fakeStashboxCatalogImages([]string{"https://1.1.1.1/a.jpg"}))
	scene, err := lib.UpsertScene(context.Background(), library.Scene{
		Box: library.LocalSceneBox, SceneID: library.LocalSceneID("abc"), Title: "Local", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	resp, err := http.Get(srv.URL + "/api/modes/adult/scenes/" + strconv.FormatInt(scene.ID, 10) + "/catalog-posters")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	var out adultCatalogPostersResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.URLs) != 0 {
		t.Fatalf("local catalog = %v", out.URLs)
	}
}

func TestPutAdultScenePoster_AcceptsCatalogURL(t *testing.T) {
	pick := "https://1.1.1.1/b.jpg"
	lib, srv := newAdultPosterEditServer(t, fakeStashboxCatalogImages([]string{"https://1.1.1.1/a.jpg", pick}))
	scene, err := lib.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: "uuid-put", Title: "Put", RootFolderPath: "/adult",
		PosterURL: "https://1.1.1.1/a.jpg",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"url": pick})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/adult/scenes/"+strconv.FormatInt(scene.ID, 10)+"/poster", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT = %d (%s)", resp.StatusCode, b)
	}
	got, err := lib.GetSceneByID(context.Background(), scene.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PosterURL != pick {
		t.Fatalf("stored = %q, want %q", got.PosterURL, pick)
	}
	again, err := lib.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: "uuid-put", Title: "Put 2", RootFolderPath: "/adult",
		PosterURL: "https://1.1.1.1/a.jpg",
	})
	if err != nil {
		t.Fatalf("upsert after pick: %v", err)
	}
	if again.PosterURL != pick {
		t.Fatalf("operator lock lost, got %q", again.PosterURL)
	}
}

func TestPutAdultScenePoster_RejectsNonCatalogURL(t *testing.T) {
	lib, srv := newAdultPosterEditServer(t, fakeStashboxCatalogImages([]string{"https://1.1.1.1/a.jpg"}))
	scene, err := lib.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: "uuid-bad", Title: "Bad", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"url": "https://1.1.1.1/other.jpg"})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/adult/scenes/"+strconv.FormatInt(scene.ID, 10)+"/poster", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-catalog PUT = %d, want 400", resp.StatusCode)
	}
}

func TestAdultScenePoster_MissingScene404(t *testing.T) {
	_, srv := newAdultPosterEditServer(t, fakeStashboxCatalogImages(nil))
	resp, err := http.Get(srv.URL + "/api/modes/adult/scenes/999/catalog-posters")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET missing = %d, want 404", resp.StatusCode)
	}
}
