package library

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

func TestMoviePosterArt_SetAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 99, Title: "Poster Movie", Year: 2020,
		FilePath: "/m.mkv", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	_ = item
	if err := s.SetMoviePosterArt(ctx, mode.Movies, 99, "https://img.example/p.jpg", PosterSourceTVDB); err != nil {
		t.Fatalf("SetMoviePosterArt: %v", err)
	}
	art, err := s.MoviePosterArt(ctx, mode.Movies, 99)
	if err != nil {
		t.Fatalf("MoviePosterArt: %v", err)
	}
	if art.URL != "https://img.example/p.jpg" || art.Source != PosterSourceTVDB || art.Title != "Poster Movie" {
		t.Fatalf("unexpected art: %+v", art)
	}
	m, err := s.MoviePosterURLMap(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("MoviePosterURLMap: %v", err)
	}
	if m[99] != "https://img.example/p.jpg" {
		t.Fatalf("map = %#v", m)
	}
}

func TestListMoviesNeedingPoster(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 1, Title: "Needs Art", Year: 2020,
		FilePath: "/a.mkv", RootFolderPath: "/movies",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 2, Title: "Has Art", Year: 2021,
		FilePath: "/b.mkv", RootFolderPath: "/movies",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMoviePosterArt(ctx, mode.Movies, 2, "https://img.example/b.jpg", PosterSourceTMDB); err != nil {
		t.Fatal(err)
	}
	need, err := s.ListMoviesNeedingPoster(ctx, mode.Movies)
	if err != nil {
		t.Fatal(err)
	}
	if len(need) != 1 || need[0].TMDBID != 1 {
		t.Fatalf("need = %+v", need)
	}
}

func TestListSeriesNeedingPoster_IncludesZeroTMDB(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ser, err := s.UpsertSeries(ctx, Series{TMDBID: 50, Title: "Zero Soon", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE library_series SET tmdb_id = 0, poster_url = '' WHERE id = ?`, ser.ID); err != nil {
		t.Fatal(err)
	}
	need, err := s.ListSeriesNeedingPoster(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range need {
		if n.ID == ser.ID && n.TMDBID == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected zero-tmdb series in need list: %+v", need)
	}
}
