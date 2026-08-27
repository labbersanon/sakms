package library

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

func TestRemapPath_RewritesItemAndFileRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	oldDir := "/media/Movies/Old Title"
	oldFile := filepath.Join(oldDir, "movie.mkv")
	item, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Old Title", Year: 2020,
		FilePath: oldFile, RootFolderPath: "/media/Movies",
	})
	if err != nil {
		t.Fatal(err)
	}

	newDir := "/media/Movies/New Title"
	newFile := filepath.Join(newDir, "movie.mkv")
	ok, err := s.RemapPath(ctx, oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected remap to touch rows")
	}

	got, err := s.Get(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != newFile {
		t.Errorf("item path = %q, want %q", got.FilePath, newFile)
	}
	files, err := s.ListFiles(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].FilePath != newFile {
		t.Errorf("files = %+v, want %q", files, newFile)
	}

	sibling := "/media/Movies/Old Title 2/movie.mkv"
	if _, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 43, Title: "Other", FilePath: sibling, RootFolderPath: "/media/Movies",
	}); err != nil {
		t.Fatal(err)
	}
	// A second remap of a prefix that no longer exists is a no-op for the
	// already-moved title and must not swallow the sibling "Old Title 2".
	ok, err = s.RemapPath(ctx, oldDir, newDir)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.GetByTMDBID(ctx, mode.Movies, 43)
	if err != nil {
		t.Fatal(err)
	}
	if other.FilePath != sibling {
		t.Errorf("sibling remapped: %q", other.FilePath)
	}
	_ = ok
}

func TestForgetPath_DeletesMovieAndClearsEpisode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	movie, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 7, Title: "Gone",
		FilePath: "/media/Movies/Gone/gone.mkv", RootFolderPath: "/media/Movies",
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := s.ForgetPath(ctx, "/media/Movies/Gone")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected forget to touch the movie")
	}
	if _, err := s.Get(ctx, movie.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("movie still present: %v", err)
	}

	series, err := s.UpsertSeries(ctx, Series{TMDBID: 99, Title: "Show", RootFolderPath: "/media/TV"})
	if err != nil {
		t.Fatal(err)
	}
	ep, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1,
		Title: "Pilot", FilePath: "/media/TV/Show/Season 01/Show S01E01.mkv",
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err = s.ForgetPath(ctx, ep.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected forget to clear the episode file")
	}
	got, err := s.GetEpisodeByID(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FilePath != "" {
		t.Errorf("episode path = %q, want empty (missing)", got.FilePath)
	}
}

func TestEntryTracked(t *testing.T) {
	tracked := []string{"/media/Movies/Foo/movie.mkv", "/media/Movies/Bar/a.mkv"}
	if !EntryTracked("/media/Movies/Foo", true, tracked) {
		t.Error("dir Foo should be tracked")
	}
	if EntryTracked("/media/Movies/Other", true, tracked) {
		t.Error("dir Other should not be tracked")
	}
	if !EntryTracked("/media/Movies/Foo/movie.mkv", false, tracked) {
		t.Error("file should be tracked")
	}
	if EntryTracked("/media/Movies/Foo/other.mkv", false, tracked) {
		t.Error("other file should not be tracked")
	}
}

func TestTrackedChildNames_FooDoesNotSwallowFooTwo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 11, Title: "Foo", Year: 2021,
		FilePath: "/media/Movies/Foo/movie.mkv", RootFolderPath: "/media/Movies",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 12, Title: "Foo Two", Year: 2022,
		FilePath: "/media/Movies/Foo Two/movie.mkv", RootFolderPath: "/media/Movies",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 13, Title: "Loose", Year: 2023,
		FilePath: "/media/Movies/loose.mkv", RootFolderPath: "/media/Movies",
	}); err != nil {
		t.Fatal(err)
	}

	names, err := s.TrackedChildNames(ctx, "/media/Movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 || names[0] != "Foo" || names[1] != "Foo Two" || names[2] != "loose.mkv" {
		t.Fatalf("names = %v, want [Foo, Foo Two, loose.mkv]", names)
	}

	names, err = s.TrackedChildNames(ctx, "/media/Movies/Foo")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "movie.mkv" {
		t.Fatalf("Foo children = %v, want [movie.mkv] (must not include Foo Two)", names)
	}

	names, err = s.TrackedChildNames(ctx, "/media/Movies/Other")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("empty dir names = %v, want none", names)
	}
}

func TestLibraryHitsForPath_FileAndFolder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	moviePath := "/media/Movies/Foo/movie.mkv"
	if _, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 11, Title: "Foo", Year: 2021,
		FilePath: moviePath, RootFolderPath: "/media/Movies",
	}); err != nil {
		t.Fatal(err)
	}
	series, err := s.UpsertSeries(ctx, Series{
		TMDBID: 22, Title: "Show", Year: 2019, RootFolderPath: "/media/TV",
	})
	if err != nil {
		t.Fatal(err)
	}
	epPath := "/media/TV/Show/S01E01.mkv"
	if _, err := s.UpsertEpisode(ctx, Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1,
		Title: "Pilot", FilePath: epPath,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(ctx, Item{
		Mode: mode.Movies, TMDBID: 12, Title: "Foo Two", Year: 2022,
		FilePath: "/media/Movies/Foo Two/movie.mkv", RootFolderPath: "/media/Movies",
	}); err != nil {
		t.Fatal(err)
	}

	hits, total, err := s.LibraryHitsForPath(ctx, moviePath)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(hits) != 1 || hits[0].Title != "Foo" || hits[0].Kind != "movie" {
		t.Fatalf("file hits = %+v total=%d", hits, total)
	}

	hits, total, err = s.LibraryHitsForPath(ctx, "/media/Movies/Foo")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(hits) != 1 || hits[0].Title != "Foo" {
		t.Fatalf("folder must not swallow Foo Two: %+v total=%d", hits, total)
	}

	hits, total, err = s.LibraryHitsForPath(ctx, epPath)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(hits) != 1 || hits[0].Kind != "episode" || hits[0].Series != "Show" {
		t.Fatalf("episode hits = %+v total=%d", hits, total)
	}
	if hits[0].Title != "S01E01 Pilot" {
		t.Errorf("episode title = %q", hits[0].Title)
	}
}
