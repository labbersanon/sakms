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

func TestPutItemRating_MoviesRoundTrip(t *testing.T) {
	lib, mux := newTrackedMux(t)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	item, err := lib.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Rated", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	body, _ := json.Marshal(map[string]int{"rating": 3})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/items/"+strconv.FormatInt(item.ID, 10)+"/rating", bytes.NewReader(body))
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
		t.Fatalf("PUT rating = %d (%s), want 204", resp.StatusCode, b)
	}

	got, err := lib.Get(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Rating != 3 {
		t.Fatalf("stored rating = %d, want 3", got.Rating)
	}
}

func TestPutItemRating_AdultGenericRoute400(t *testing.T) {
	_, mux := newTrackedMux(t)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]int{"rating": 2})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/adult/items/1/rating", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("adult generic rating route = %d, want 400", resp.StatusCode)
	}
}

func TestPutSceneRating_RoundTrip(t *testing.T) {
	lib, mux := newTrackedMux(t)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	scene, err := lib.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: "uuid-r", Title: "S", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	body, _ := json.Marshal(map[string]int{"rating": 1})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/adult/scenes/"+strconv.FormatInt(scene.ID, 10)+"/rating", bytes.NewReader(body))
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
		t.Fatalf("PUT scene rating = %d (%s), want 204", resp.StatusCode, b)
	}
	got, err := lib.GetSceneByID(context.Background(), scene.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Rating != 1 {
		t.Fatalf("stored rating = %d, want 1", got.Rating)
	}
}

func TestPutItemRating_RejectsOutOfRange(t *testing.T) {
	lib, mux := newTrackedMux(t)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	item, err := lib.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 2, Title: "X", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	body, _ := json.Marshal(map[string]int{"rating": 9})
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/movies/items/"+strconv.FormatInt(item.ID, 10)+"/rating", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("rating 9 = %d, want 400", resp.StatusCode)
	}
}
