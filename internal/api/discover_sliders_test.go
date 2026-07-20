package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// TestSliderCRUD_EndToEnd exercises create/list/update/delete against the
// real HTTP handlers backed by a real migrated SQLite file.
func TestSliderCRUD_EndToEnd(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	createBody, _ := json.Marshal(apidto.SliderUpsertRequest{
		Title: "Marvel Movies", FilterType: "studio", FilterValue: "420", Target: "movie", Enabled: true,
	})
	createResp, err := http.Post(srv.URL+"/api/discover/sliders", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from create, got %d", createResp.StatusCode)
	}
	var created apidto.Slider
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decoding create response: %v", err)
	}
	if created.ID == 0 || created.Title != "Marvel Movies" || created.FilterType != "studio" {
		t.Fatalf("unexpected created slider: %+v", created)
	}

	// It shows up in the list.
	listResp, err := http.Get(srv.URL + "/api/discover/sliders")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer listResp.Body.Close()
	var list []apidto.Slider
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("decoding list: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("unexpected list: %+v", list)
	}

	// Update it.
	updateBody, _ := json.Marshal(apidto.SliderUpsertRequest{
		Title: "Marvel Studios", FilterType: "studio", FilterValue: "420", Target: "movie", Enabled: false,
	})
	updateReq, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/discover/sliders/"+strconv.Itoa(created.ID), bytes.NewReader(updateBody))
	updateResp, err := http.DefaultClient.Do(updateReq)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer updateResp.Body.Close()
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from update, got %d", updateResp.StatusCode)
	}
	var updated apidto.Slider
	if err := json.NewDecoder(updateResp.Body).Decode(&updated); err != nil {
		t.Fatalf("decoding update response: %v", err)
	}
	if updated.Title != "Marvel Studios" || updated.Enabled {
		t.Fatalf("unexpected updated slider: %+v", updated)
	}

	// Delete it.
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/discover/sliders/"+strconv.Itoa(created.ID), nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from delete, got %d", delResp.StatusCode)
	}

	finalListResp, err := http.Get(srv.URL + "/api/discover/sliders")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer finalListResp.Body.Close()
	var finalList []apidto.Slider
	if err := json.NewDecoder(finalListResp.Body).Decode(&finalList); err != nil {
		t.Fatalf("decoding final list: %v", err)
	}
	if len(finalList) != 0 {
		t.Errorf("expected empty list after delete, got %+v", finalList)
	}
}

// TestCreateSliderHandler_RejectsInvalidFilterType proves
// discoversliders.Store's validation errors surface as 400s, not 500s.
func TestCreateSliderHandler_RejectsInvalidFilterType(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(apidto.SliderUpsertRequest{Title: "Bad", FilterType: "not-a-real-type", Target: "movie"})
	resp, err := http.Post(srv.URL+"/api/discover/sliders", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid filter type, got %d", resp.StatusCode)
	}
}

// TestReorderSlidersHandler_Reorders proves POST .../reorder actually
// changes SortOrder in the persisted list.
func TestReorderSlidersHandler_Reorders(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()
	first, err := slidersStore.Create(ctx, "First", "trending", "", "mixed", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	second, err := slidersStore.Create(ctx, "Second", "popular", "", "mixed", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	body, _ := json.Marshal(apidto.SliderReorderRequest{IDs: []int{second.ID, first.ID}})
	resp, err := http.Post(srv.URL+"/api/discover/sliders/reorder", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	list, err := slidersStore.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("unexpected order after reorder: %+v", list)
	}
}

// TestResolveSliderHandler_GenreDispatchesToDiscoverMovieGenre proves a
// stored genre/movie slider resolves via /discover/movie?with_genres=.
func TestResolveSliderHandler_GenreDispatchesToDiscoverMovieGenre(t *testing.T) {
	var gotPath, gotGenre string
	fake := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotGenre = r.URL.Query().Get("with_genres")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"title":"Some Movie"}]}`))
	})

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sl, err := slidersStore.Create(context.Background(), "Action", "genre", "28", "movie", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/sliders/" + strconv.Itoa(sl.ID) + "/resolve")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotPath != "/discover/movie" || gotGenre != "28" {
		t.Errorf("expected /discover/movie?with_genres=28, got path=%s with_genres=%s", gotPath, gotGenre)
	}
	var items []tmdb.Item
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Some Movie" {
		t.Errorf("unexpected items: %+v", items)
	}
}

// TestResolveSliderHandler_MixedConcatenatesMovieAndTV proves a Target ==
// mixed slider combines both catalogs' results.
func TestResolveSliderHandler_MixedConcatenatesMovieAndTV(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/discover/movie", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":1,"title":"A Movie"}]}`))
	})
	mux.HandleFunc("/discover/tv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":2,"name":"A Show"}]}`))
	})
	fake := httptest.NewServer(mux)
	defer fake.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	overrideFixedURL(t, "tmdb", fake.URL)
	if err := connStore.Upsert(context.Background(), "tmdb", fake.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sl, err := slidersStore.Create(context.Background(), "Action Everywhere", "genre", "28", "mixed", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/sliders/" + strconv.Itoa(sl.ID) + "/resolve")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	var items []tmdb.Item
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 combined items, got %+v", items)
	}
	if items[0].Title != "A Movie" || items[0].MediaType != tmdb.Movie {
		t.Errorf("expected movie item first, got %+v", items[0])
	}
	if items[1].Title != "A Show" || items[1].MediaType != tmdb.TV {
		t.Errorf("expected tv item second, got %+v", items[1])
	}
}

// TestResolveSliderHandler_StudioRejectsTVTarget proves a misconfigured
// studio+tv slider is reported as a 400 (a permanent per-slider config
// problem the admin fixes by editing the slider), not a 502 (which would
// wrongly suggest a transient TMDB outage worth retrying) — see
// errSliderMisconfigured's doc comment.
func TestResolveSliderHandler_StudioRejectsTVTarget(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(context.Background(), "tmdb", "http://tmdb.local", "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sl, err := slidersStore.Create(context.Background(), "Bad Studio Slider", "studio", "420", "tv", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/sliders/" + strconv.Itoa(sl.ID) + "/resolve")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a studio+tv misconfiguration, got %d", resp.StatusCode)
	}
}

// TestResolveSliderHandler_UnknownIDReturns404 proves resolving a
// nonexistent slider id 404s instead of panicking or 500ing.
func TestResolveSliderHandler_UnknownIDReturns404(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/discover/sliders/999/resolve")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown slider id, got %d", resp.StatusCode)
	}
}
