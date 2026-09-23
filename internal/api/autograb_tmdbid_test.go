package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/tmdb"
)

func titleSearchTMDB(t *testing.T, path string, results []map[string]any) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			t.Errorf("path = %s, want %s", r.URL.Path, path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(srv.Close)
	return tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test", BypassCache: true}, srv.Client())
}

func TestResolveTitleTMDBID_UniqueExactTitle(t *testing.T) {
	client := titleSearchTMDB(t, "/search/tv", []map[string]any{
		{"id": 2071, "name": "Barnaby Jones", "first_air_date": "1973-01-28"},
		{"id": 99, "name": "Barnaby Jones Reunion", "first_air_date": "1993-01-01"},
	})
	sess := &mode.Session{TMDB: client}
	got := resolveTitleTMDBID(context.Background(), sess, mode.Series, "Barnaby Jones")
	if got != 2071 {
		t.Fatalf("tmdb id = %d, want 2071", got)
	}
}

func TestResolveTitleTMDBID_AmbiguousExactTitleStaysZero(t *testing.T) {
	client := titleSearchTMDB(t, "/search/tv", []map[string]any{
		{"id": 1665, "name": "Magnum, P.I.", "first_air_date": "1980-12-11"},
		{"id": 900, "name": "Magnum, P.I.", "first_air_date": "2018-09-24"},
	})
	sess := &mode.Session{TMDB: client}
	if got := resolveTitleTMDBID(context.Background(), sess, mode.Series, "Magnum, P.I."); got != 0 {
		t.Fatalf("tmdb id = %d, want 0 for two exact hits", got)
	}
}

func TestResolveTitleTMDBID_NoExactMatchStaysZero(t *testing.T) {
	client := titleSearchTMDB(t, "/search/movie", []map[string]any{
		{"id": 11, "title": "Star Wars", "release_date": "1977-05-25"},
	})
	sess := &mode.Session{TMDB: client}
	if got := resolveTitleTMDBID(context.Background(), sess, mode.Movies, "A New Hope"); got != 0 {
		t.Fatalf("tmdb id = %d, want 0", got)
	}
}

func TestFillMissingTMDBID_WritesResolvedIDOnExistingGrab(t *testing.T) {
	_, _, _, grabsStore, _, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	g, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Series, Title: "Barnaby Jones"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	client := titleSearchTMDB(t, "/search/tv", []map[string]any{
		{"id": 2071, "name": "Barnaby Jones", "first_air_date": "1973-01-28"},
	})
	req := AutoGrabRequest{Mode: mode.Series, Title: "Barnaby Jones", ExistingGrabID: g.ID}
	fillMissingTMDBID(ctx, &mode.Session{TMDB: client}, AutoGrabDeps{GrabsStore: grabsStore}, &req)
	if req.TMDBID != 2071 {
		t.Fatalf("req.TMDBID = %d, want 2071", req.TMDBID)
	}
	got, err := grabsStore.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TMDBID != 2071 {
		t.Fatalf("stored tmdb id = %d, want 2071", got.TMDBID)
	}
}
