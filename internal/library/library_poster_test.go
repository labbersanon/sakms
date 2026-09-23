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
