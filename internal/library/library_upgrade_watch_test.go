package library

import (
	"context"
	"errors"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

func TestSetItemUpgradeWatch_RoundTripAndSurvivesUpsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 200, Title: "Watched Movie",
		FilePath: "/movies/a.mkv", RootFolderPath: "/movies",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UpgradeWatch {
		t.Fatal("upgrade watch must default off")
	}

	if err := s.SetItemUpgradeWatch(ctx, item.ID, true); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err = s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if !got.UpgradeWatch {
		t.Fatal("expected upgrade watch on")
	}

	if _, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 200, Title: "Watched Movie (regrab)",
		FilePath: "/movies/b.mkv", RootFolderPath: "/movies",
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, err = s.Get(ctx, item.ID)
	if err != nil {
		t.Fatalf("get after upsert: %v", err)
	}
	if !got.UpgradeWatch {
		t.Fatal("upsert wiped upgrade watch")
	}

	listed, err := s.ListUpgradeWatchMovies(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 || listed[0].TMDBID != 200 {
		t.Fatalf("list = %+v, want the watched movie", listed)
	}

	if err := s.SetItemUpgradeWatch(ctx, item.ID, false); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err = s.GetByTMDBID(ctx, mode.Movies, 200)
	if err != nil {
		t.Fatalf("get by tmdb: %v", err)
	}
	if got.UpgradeWatch {
		t.Fatal("expected upgrade watch off after clear")
	}
	listed, err = s.ListUpgradeWatchMovies(ctx)
	if err != nil {
		t.Fatalf("list after clear: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("list after clear = %+v, want empty", listed)
	}
}

func TestSetItemUpgradeWatch_Missing(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetItemUpgradeWatch(context.Background(), 999999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id: got %v, want ErrNotFound", err)
	}
}
