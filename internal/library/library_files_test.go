package library

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

func TestUpsert_SyncsPrimaryFileRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 10, Title: "Film", Year: 2020,
		FilePath: "/movies/Film/Film.mkv", RootFolderPath: "/movies", QualityTier: "high", Size: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	files, err := s.ListFiles(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !files[0].IsPrimary || files[0].FilePath != item.FilePath {
		t.Fatalf("expected one primary synced file, got %+v", files)
	}
}

func TestUpsertFile_TwoFilesOnePrimary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 11, Title: "Film", FilePath: "/a.mkv", RootFolderPath: "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertFile(ctx, ItemFile{
		ItemID: item.ID, FilePath: "/b.mkv", IsPrimary: false, QualityTier: "low",
	}); err != nil {
		t.Fatal(err)
	}
	files, err := s.ListFiles(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files", len(files))
	}
	primaries := 0
	for _, f := range files {
		if f.IsPrimary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("expected exactly one primary, got %d", primaries)
	}
	paths, err := s.AllFilePaths(ctx, mode.Movies)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 2 {
		t.Fatalf("AllFilePaths should include primary+alt, got %v", paths)
	}
}
