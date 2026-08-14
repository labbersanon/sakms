package library

import (
	"context"
	"errors"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

func TestSetItemRating_RoundTripAndSurvivesUpsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 100, Title: "Rated Movie",
		FilePath: "/movies/a.mkv", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := s.SetItemRating(ctx, item.ID, 4); err != nil {
		t.Fatalf("set rating: %v", err)
	}
	got, err := s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Rating != 4 {
		t.Fatalf("rating = %d, want 4", got.Rating)
	}

	if _, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 100, Title: "Rated Movie (regrab)",
		FilePath: "/movies/a.mkv", RootFolderPath: "/movies",
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, err = s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("get after upsert: %v", err)
	}
	if got.Rating != 4 {
		t.Fatalf("upsert wiped rating: got %d, want 4", got.Rating)
	}

	if err := s.SetItemRating(ctx, item.ID, 0); err != nil {
		t.Fatalf("clear rating: %v", err)
	}
	got, err = s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if got.Rating != 0 {
		t.Fatalf("cleared rating = %d, want 0", got.Rating)
	}
}

func TestSetItemRating_RejectsOutOfRangeAndMissing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 101, Title: "X", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetItemRating(ctx, item.ID, 6); !errors.Is(err, ErrInvalidRating) {
		t.Fatalf("rating 6: got %v, want ErrInvalidRating", err)
	}
	if err := s.SetItemRating(ctx, item.ID, -1); !errors.Is(err, ErrInvalidRating) {
		t.Fatalf("rating -1: got %v, want ErrInvalidRating", err)
	}
	if err := s.SetItemRating(ctx, 999999, 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id: got %v, want ErrNotFound", err)
	}
}

func TestSetSceneRating_RoundTripAndSurvivesUpsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	scene, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "uuid-rate", Title: "Scene", RootFolderPath: "/adult",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetSceneRating(ctx, scene.ID, 2); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := s.UpsertScene(ctx, Scene{
		Box: "stashdb", SceneID: "uuid-rate", Title: "Scene renamed", RootFolderPath: "/adult",
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, err := s.GetScene(ctx, "stashdb", "uuid-rate")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Rating != 2 {
		t.Fatalf("upsert wiped scene rating: got %d, want 2", got.Rating)
	}
}

func TestSetSeriesRating_RoundTripAndSurvivesUpsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	series, err := s.UpsertSeries(ctx, Series{TMDBID: 77, Title: "Show", RootFolderPath: "/tv"})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetSeriesRating(ctx, series.ID, 5); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := s.UpsertSeries(ctx, Series{TMDBID: 77, Title: "Show (renamed)", RootFolderPath: "/tv"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, err := s.GetSeries(ctx, series.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Rating != 5 {
		t.Fatalf("upsert wiped series rating: got %d, want 5", got.Rating)
	}
}
