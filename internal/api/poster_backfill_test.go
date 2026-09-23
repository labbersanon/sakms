package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/websearch"
)

// scriptedIdentityAI declines GuessTitle, then returns ground once the prompt
// includes web snippets.
type scriptedIdentityAI struct {
	ground map[string]any
	calls  int
}

func (s *scriptedIdentityAI) ChatJSON(_ context.Context, prompt string) (map[string]any, error) {
	s.calls++
	if strings.Contains(prompt, "Web search results") {
		return s.ground, nil
	}
	return map[string]any{"title": nil, "year": nil}, nil
}

type scriptedSearch struct {
	query string
	res   []websearch.Result
}

func (s *scriptedSearch) Search(_ context.Context, query string, _ int) ([]websearch.Result, error) {
	s.query = query
	return s.res, nil
}

func (s *scriptedSearch) Ping(context.Context) error { return nil }

func TestRepairMovieIdentity_WebSearchAfterGuessDecline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/movie"):
			if !strings.Contains(r.URL.Query().Get("query"), "Jo Jo Dancer") {
				_, _ = w.Write([]byte(`{"results":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"results":[{"id":11449,"title":"Jo Jo Dancer, Your Life Is Calling","release_date":"1986-05-02","poster_path":"/p.jpg"}]}`))
		case strings.HasPrefix(r.URL.Path, "/movie/11449"):
			_, _ = w.Write([]byte(`{"id":11449,"title":"Jo Jo Dancer, Your Life Is Calling","release_date":"1986-05-02","poster_path":"/p.jpg"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	store := library.New(dbtest.New(t))
	ctx := context.Background()
	item, err := store.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: -7, Title: "jojo.dancer.1986.mkv",
		FilePath: "/movies/jojo.dancer.1986.mkv", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatal(err)
	}

	search := &scriptedSearch{res: []websearch.Result{{
		Title:       "Jo Jo Dancer, Your Life Is Calling (1986)",
		Description: "Film starring Richard Pryor, released 1986.",
		URL:         "https://example.test/jojo",
	}}}
	ai := &scriptedIdentityAI{ground: map[string]any{
		"title": "Jo Jo Dancer, Your Life Is Calling",
		"year":  float64(1986),
	}}
	sess := &mode.Session{
		TMDB:         tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test", BypassCache: true}, srv.Client()),
		MainstreamAI: ai,
		WebSearch:    search,
	}

	if !repairMovieIdentity(ctx, store, sess, nil, item) {
		t.Fatal("expected web grounding to repair the movie")
	}
	got, err := store.GetByTMDBID(ctx, mode.Movies, 11449)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != item.ID || got.Year != 1986 {
		t.Fatalf("repaired row: %+v", got)
	}
	if search.query != "jojo.dancer.1986.mkv" {
		t.Fatalf("search query = %q", search.query)
	}
	if ai.calls < 2 {
		t.Fatalf("expected GuessTitle then grounded extract, calls=%d", ai.calls)
	}
}

func TestRepairMovieIdentity_NoWebSearchStaysUnrepaired(t *testing.T) {
	store := library.New(dbtest.New(t))
	ctx := context.Background()
	item, err := store.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: -8, Title: "opaque.name.mkv",
		FilePath: "/movies/opaque.name.mkv", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatal(err)
	}
	sess := &mode.Session{
		TMDB:         tmdb.New(tmdb.Config{BaseURL: "http://127.0.0.1:1", APIKey: "test", BypassCache: true}, http.DefaultClient),
		MainstreamAI: &scriptedIdentityAI{},
	}
	if repairMovieIdentity(ctx, store, sess, nil, item) {
		t.Fatal("expected no repair without web search")
	}
}

func TestRepairSeriesIdentity_WebSearchAfterGuessDecline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/tv"):
			if !strings.Contains(r.URL.Query().Get("query"), "Laurel") {
				_, _ = w.Write([]byte(`{"results":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"results":[{"id":5555,"name":"Laurel & Hardy","first_air_date":"1919-01-01"}]}`))
		case r.URL.Path == "/tv/5555":
			_, _ = w.Write([]byte(`{"id":5555,"name":"Laurel & Hardy","first_air_date":"1919-01-01","poster_path":"/p.jpg"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	store := library.New(dbtest.New(t))
	ctx := context.Background()
	ser, err := store.UpsertSeries(ctx, library.Series{
		TMDBID: -42, TVDBID: 73910, Title: "Laurel & Hardy", Year: 1919, RootFolderPath: "/tv",
	})
	if err != nil {
		t.Fatal(err)
	}
	search := &scriptedSearch{res: []websearch.Result{{
		Title:       "Laurel & Hardy (TV series)",
		Description: "Shorts catalogued as a series, first aired 1919.",
		URL:         "https://example.test/lh",
	}}}
	ai := &scriptedIdentityAI{ground: map[string]any{
		"title": "Laurel & Hardy",
		"year":  float64(1919),
	}}
	sess := &mode.Session{
		TMDB:         tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test", BypassCache: true}, srv.Client()),
		MainstreamAI: ai,
		WebSearch:    search,
	}
	if !repairSeriesIdentity(ctx, store, sess, ser) {
		t.Fatal("expected web grounding to repair the series")
	}
	got, err := store.GetSeriesByTMDBID(ctx, 5555)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != ser.ID || got.TVDBID != 73910 {
		t.Fatalf("repaired series: %+v", got)
	}
	if search.query != "Laurel & Hardy" {
		t.Fatalf("search query = %q", search.query)
	}
}
