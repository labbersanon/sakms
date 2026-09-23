package rename

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

func TestCatalogMovieAtPath_NFO(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Inside Out (2015).mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	nfoPath := filepath.Join(dir, "movie.nfo")
	if err := os.WriteFile(nfoPath, []byte(`<movie><tmdbid>150540</tmdbid><title>Inside Out</title><year>2015</year></movie>`), 0o644); err != nil {
		t.Fatal(err)
	}
	libStore := newTestLibraryStore(t)
	ok, err := catalogMovieAtPath(context.Background(), libStore, video, dir)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if !ok {
		t.Fatal("expected catalog")
	}
	got, err := libStore.GetByTMDBID(context.Background(), mode.Movies, 150540)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Inside Out" || got.FilePath != video {
		t.Fatalf("item = %+v", got)
	}
}

func TestCatalogMovieAtPath_DoesNotOverwriteExistingPrimary(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "copy.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.nfo"), []byte(`<movie><tmdbid>11</tmdbid><title>Star Wars</title></movie>`), 0o644); err != nil {
		t.Fatal(err)
	}
	libStore := newTestLibraryStore(t)
	primary := filepath.Join(dir, "primary.mkv")
	if _, err := libStore.Upsert(context.Background(), library.Item{
		Mode: mode.Movies, TMDBID: 11, Title: "Star Wars", FilePath: primary,
	}); err != nil {
		t.Fatal(err)
	}
	ok, err := catalogMovieAtPath(context.Background(), libStore, video, dir)
	if err != nil || !ok {
		t.Fatalf("catalog ok=%v err=%v", ok, err)
	}
	got, err := libStore.GetByTMDBID(context.Background(), mode.Movies, 11)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != primary {
		t.Fatalf("primary overwritten: %q", got.FilePath)
	}
}

func TestCatalogEpisodeAtPath_TVShowNFO(t *testing.T) {
	root := t.TempDir()
	season := filepath.Join(root, "Animaniacs (1993)", "Season 01")
	if err := os.MkdirAll(season, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Animaniacs (1993)", "tvshow.nfo"), []byte(`<tvshow><tmdbid>1566</tmdbid><title>Animaniacs</title><year>1993</year></tvshow>`), 0o644); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(season, "Animaniacs S01E01 Test.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	libStore := newTestLibraryStore(t)
	ok, err := catalogEpisodeAtPath(context.Background(), libStore, video, root)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if !ok {
		t.Fatal("expected catalog")
	}
	series, err := libStore.GetSeriesByTMDBID(context.Background(), 1566)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if series.Title != "Animaniacs" {
		t.Fatalf("title = %q", series.Title)
	}
	ep, err := libStore.GetEpisode(context.Background(), series.ID, 1, 1)
	if err != nil {
		t.Fatalf("episode: %v", err)
	}
	if ep.FilePath != video {
		t.Fatalf("path = %q", ep.FilePath)
	}
}
