package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/autograb"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/tvdb"
)

func clearAnimationGenreCache(t *testing.T) {
	t.Helper()
	animationGenreCache.Clear()
	t.Cleanup(animationGenreCache.Clear)
}

func fakeTVDBGenreServer(t *testing.T, seriesID int, genres []map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v4/login", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]string{"token": "tok"},
		})
	})
	path := "/v4/series/" + strconv.Itoa(seriesID)
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"genres": genres},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGenresIncludeAnimation(t *testing.T) {
	if !genresIncludeAnimation([]string{"Comedy", "Animation"}) {
		t.Fatal("Animation among genres")
	}
	if !genresIncludeAnimation([]string{"animation"}) {
		t.Fatal("case-insensitive")
	}
	if genresIncludeAnimation([]string{"Kids", "Family"}) {
		t.Fatal("Kids is not Animation")
	}
	if genresIncludeAnimation(nil) {
		t.Fatal("empty is not Animation")
	}
}

func TestTitleIsAnimationFromStoredGenres(t *testing.T) {
	clearAnimationGenreCache(t)
	ctx := context.Background()
	_, _, _, _, libStore, _, _, _, _, _ := testStores(t)
	if _, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 333432, Title: "Looney Tunes", Genres: []string{"Animation", "Comedy"},
	}); err != nil {
		t.Fatal(err)
	}
	if !titleIsAnimation(ctx, libStore, nil, mode.Series, 333432, 0) {
		t.Fatal("stored Animation genre must count without TMDB")
	}
	if titleIsAnimation(ctx, libStore, nil, mode.Series, 1, 0) {
		t.Fatal("unknown title fail-closed")
	}
}

func TestTitleIsAnimationFromTMDB(t *testing.T) {
	clearAnimationGenreCache(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": 99, "name": "Show",
			"genres": []map[string]any{{"id": 16, "name": "Animation"}},
		})
	}))
	t.Cleanup(srv.Close)
	sess := &mode.Session{Mode: mode.Series, TMDB: tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())}
	if !titleIsAnimation(context.Background(), nil, sess, mode.Series, 99, 0) {
		t.Fatal("TMDB Animation genre")
	}
}

func TestTitleIsAnimationFromTVDB(t *testing.T) {
	clearAnimationGenreCache(t)
	srv := fakeTVDBGenreServer(t, 7266, []map[string]any{
		{"id": 16, "name": "Animation"},
		{"id": 4, "name": "Comedy"},
	})
	sess := &mode.Session{Mode: mode.Series, TVDB: tvdb.New(tvdb.Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())}
	synth := -1498833576
	if !titleIsAnimation(context.Background(), nil, sess, mode.Series, synth, 7266) {
		t.Fatal("TVDB Animation must count when TMDB id is synthetic")
	}
}

func TestTitleIsAnimationTVDBFallbackWhenTMDBMisses(t *testing.T) {
	clearAnimationGenreCache(t)
	tmdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"status_code": 34})
	}))
	t.Cleanup(tmdbSrv.Close)
	tvdbSrv := fakeTVDBGenreServer(t, 7266, []map[string]any{{"name": "Animation"}})
	sess := &mode.Session{
		Mode: mode.Series,
		TMDB: tmdb.New(tmdb.Config{BaseURL: tmdbSrv.URL, APIKey: "k"}, tmdbSrv.Client()),
		TVDB: tvdb.New(tvdb.Config{BaseURL: tvdbSrv.URL, APIKey: "k"}, tvdbSrv.Client()),
	}
	if !titleIsAnimation(context.Background(), nil, sess, mode.Series, 333432, 7266) {
		t.Fatal("TVDB must fill in when TMDB lookup fails")
	}
}

func TestTitleIsAnimationAnthologyLibraryResolvesTVDB(t *testing.T) {
	clearAnimationGenreCache(t)
	ctx := context.Background()
	_, _, _, _, libStore, _, _, _, _, _ := testStores(t)
	synth := -42
	if _, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: synth, TVDBID: 7266, Title: "Looney Tunes",
	}); err != nil {
		t.Fatal(err)
	}
	srv := fakeTVDBGenreServer(t, 7266, []map[string]any{{"name": "Animation"}})
	sess := &mode.Session{Mode: mode.Series, TVDB: tvdb.New(tvdb.Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())}
	if !titleIsAnimation(ctx, libStore, sess, mode.Series, synth, 0) {
		t.Fatal("library tvdb_id must resolve SeriesGenres when stored genres are empty")
	}
}

func TestTitleIsAnimationTVDBNotAnimation(t *testing.T) {
	clearAnimationGenreCache(t)
	srv := fakeTVDBGenreServer(t, 81189, []map[string]any{{"name": "Drama"}})
	sess := &mode.Session{Mode: mode.Series, TVDB: tvdb.New(tvdb.Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())}
	if titleIsAnimation(context.Background(), nil, sess, mode.Series, 0, 81189) {
		t.Fatal("Drama on TVDB is not Animation")
	}
}

func TestTitleIsAnimationNegativeTMDBWithoutTVDBFailClosed(t *testing.T) {
	clearAnimationGenreCache(t)
	if titleIsAnimation(context.Background(), nil, nil, mode.Series, -42, 0) {
		t.Fatal("synthetic TMDB without TVDB must fail closed")
	}
}

func TestApplyTitleAnimationMarksCandidates(t *testing.T) {
	clearAnimationGenreCache(t)
	ctx := context.Background()
	_, _, _, _, libStore, _, _, _, _, _ := testStores(t)
	if _, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 7, Title: "Toon", Genres: []string{"Animation"},
	}); err != nil {
		t.Fatal(err)
	}
	cands := []autograb.Candidate{{Title: "a"}, {Title: "b"}}
	applyTitleAnimation(ctx, libStore, nil, mode.Series, 7, 0, cands)
	if !cands[0].Animation || !cands[1].Animation {
		t.Fatalf("both candidates should be marked, got %+v", cands)
	}
}
