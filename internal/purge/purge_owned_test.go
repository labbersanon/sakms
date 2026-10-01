package purge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveOwned_MovieDeletesFilesAndRow(t *testing.T) {
	root := t.TempDir()
	movieDir := filepath.Join(root, "Inception (2010)")
	primary := filepath.Join(movieDir, "Inception.mkv")
	alt := filepath.Join(movieDir, "Inception - 1080p.mp4")
	writeFile(t, primary)
	writeFile(t, alt)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	item, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 27205, Title: "Inception",
		FilePath: primary, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertFile(ctx, library.ItemFile{
		ItemID: item.ID, FilePath: alt, IsPrimary: false,
	}); err != nil {
		t.Fatal(err)
	}

	changes, gone, err := RemoveOwned(ctx, libStore, mode.Movies, item.ID, RemoveSpec{})
	if err != nil {
		t.Fatalf("RemoveOwned: %v", err)
	}
	if !gone {
		t.Fatal("expected gone")
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %+v", changes)
	}
	if _, err := os.Stat(movieDir); !os.IsNotExist(err) {
		t.Errorf("movie folder should be gone, stat: %v", err)
	}
	if _, err := libStore.Get(ctx, item.ID); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("item still present: %v", err)
	}
}

func TestRemoveOwned_AdultDeletesScene(t *testing.T) {
	root := t.TempDir()
	sceneDir := filepath.Join(root, "Studio")
	file := filepath.Join(sceneDir, "scene.mp4")
	writeFile(t, file)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	scene, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "tpdb", SceneID: "abc", Title: "A Scene",
		FilePath: file, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, gone, err := RemoveOwned(ctx, libStore, mode.Adult, scene.ID, RemoveSpec{})
	if err != nil {
		t.Fatalf("RemoveOwned: %v", err)
	}
	if !gone {
		t.Fatal("expected gone")
	}
	if _, err := os.Stat(sceneDir); !os.IsNotExist(err) {
		t.Errorf("scene folder should be gone, stat: %v", err)
	}
	if _, err := libStore.GetSceneByID(ctx, scene.ID); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("scene still present: %v", err)
	}
}

func TestRemoveOwned_SeriesEntireDeletesShow(t *testing.T) {
	root := t.TempDir()
	s1 := filepath.Join(root, "Show", "Season 01", "e1.mkv")
	s2 := filepath.Join(root, "Show", "Season 02", "e1.mkv")
	writeFile(t, s1)
	writeFile(t, s2)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "Show", RootFolderPath: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: s1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 2, EpisodeNumber: 1, FilePath: s2,
	}); err != nil {
		t.Fatal(err)
	}

	_, gone, err := RemoveOwned(ctx, libStore, mode.Series, series.ID, RemoveSpec{EntireSeries: true})
	if err != nil {
		t.Fatalf("RemoveOwned: %v", err)
	}
	if !gone {
		t.Fatal("expected gone")
	}
	if _, err := libStore.GetSeries(ctx, series.ID); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("series still present: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(s1)); !os.IsNotExist(err) {
		t.Errorf("season 1 folder should be gone, stat: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(s2)); !os.IsNotExist(err) {
		t.Errorf("season 2 folder should be gone, stat: %v", err)
	}
}

func TestRemoveOwned_SeriesOneSeasonKeepsTheRest(t *testing.T) {
	root := t.TempDir()
	s1 := filepath.Join(root, "Show", "Season 01", "e1.mkv")
	s2 := filepath.Join(root, "Show", "Season 02", "e1.mkv")
	writeFile(t, s1)
	writeFile(t, s2)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "Show", RootFolderPath: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: s1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 2, EpisodeNumber: 1, FilePath: s2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := libStore.SetSeasonMonitored(ctx, series.ID, 1, true); err != nil {
		t.Fatal(err)
	}

	changes, gone, err := RemoveOwned(ctx, libStore, mode.Series, series.ID, RemoveSpec{Seasons: []int{1}})
	if err != nil {
		t.Fatalf("RemoveOwned: %v", err)
	}
	if gone {
		t.Fatal("series should remain")
	}
	if len(changes) != 1 || changes[0].Path != s1 {
		t.Fatalf("changes = %+v", changes)
	}
	if _, err := os.Stat(s1); !os.IsNotExist(err) {
		t.Errorf("season 1 file should be gone")
	}
	if _, err := os.Stat(s2); err != nil {
		t.Errorf("season 2 file must survive: %v", err)
	}
	if _, err := libStore.GetSeries(ctx, series.ID); err != nil {
		t.Fatalf("series should remain: %v", err)
	}
	eps, err := libStore.ListEpisodes(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || eps[0].SeasonNumber != 2 {
		t.Fatalf("remaining episodes = %+v", eps)
	}
	flags, err := libStore.SeasonMonitorFlags(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if flags[1] {
		t.Fatal("season 1 monitor flag should be cleared")
	}
}

func TestRemoveOwned_SeriesLastSeasonDeletesSeries(t *testing.T) {
	root := t.TempDir()
	s1 := filepath.Join(root, "Show", "Season 01", "e1.mkv")
	writeFile(t, s1)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "Show", RootFolderPath: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: s1,
	}); err != nil {
		t.Fatal(err)
	}

	_, gone, err := RemoveOwned(ctx, libStore, mode.Series, series.ID, RemoveSpec{Seasons: []int{1}})
	if err != nil {
		t.Fatalf("RemoveOwned: %v", err)
	}
	if !gone {
		t.Fatal("expected gone after last season")
	}
	if _, err := libStore.GetSeries(ctx, series.ID); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("series still present: %v", err)
	}
}

func TestRemoveOwned_SeriesSharedFolderKeepsOtherSeason(t *testing.T) {
	root := t.TempDir()
	showDir := filepath.Join(root, "Show")
	s1 := filepath.Join(showDir, "Show - S01E01.mkv")
	s2 := filepath.Join(showDir, "Show - S02E01.mkv")
	writeFile(t, s1)
	writeFile(t, s2)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "Show", RootFolderPath: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: s1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 2, EpisodeNumber: 1, FilePath: s2,
	}); err != nil {
		t.Fatal(err)
	}

	_, gone, err := RemoveOwned(ctx, libStore, mode.Series, series.ID, RemoveSpec{Seasons: []int{1}})
	if err != nil {
		t.Fatalf("RemoveOwned: %v", err)
	}
	if gone {
		t.Fatal("series should remain")
	}
	if _, err := os.Stat(s1); !os.IsNotExist(err) {
		t.Errorf("s1 should be gone")
	}
	if _, err := os.Stat(s2); err != nil {
		t.Errorf("s2 must survive: %v", err)
	}
	if _, err := os.Stat(showDir); err != nil {
		t.Errorf("show folder must survive: %v", err)
	}
}

func TestRemoveOwned_SeriesRejectsEmptySpec(t *testing.T) {
	libStore := newTestLibraryStore(t)
	_, _, err := RemoveOwned(context.Background(), libStore, mode.Series, 1, RemoveSpec{})
	if !errors.Is(err, ErrRemoveSpec) {
		t.Fatalf("err = %v, want ErrRemoveSpec", err)
	}
}

func TestRemoveTrackedMediaKeeping_SameFolderKeepsSibling(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Show")
	drop := filepath.Join(dir, "a.mkv")
	keep := filepath.Join(dir, "b.mkv")
	writeFile(t, drop)
	writeFile(t, keep)

	changes, err := removeTrackedMediaKeeping([]string{drop}, root, []string{keep})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v", changes)
	}
	if _, err := os.Stat(drop); !os.IsNotExist(err) {
		t.Errorf("drop should be gone")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("keep must survive: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("shared folder must survive: %v", err)
	}
}
