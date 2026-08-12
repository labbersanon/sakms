package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
)

func TestImportGrabContent_MoviesUsesRelocateMovie(t *testing.T) {
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)

	staging := t.TempDir()
	root := t.TempDir()
	src := filepath.Join(staging, "Some.Movie.2020.mkv")
	if err := os.WriteFile(src, []byte("movie"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &grabs.Grab{
		Mode: mode.Movies, Title: "Some Movie", TMDBID: 42,
		RootFolderPath: root,
	}
	changes, err := importGrabContent(context.Background(), libStore, g, src, "bluray", settingsStore, nil, nil, nil)
	if err != nil {
		t.Fatalf("importGrabContent: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("expected path changes")
	}
	wantFolder := naming.MovieFolderName(naming.Jellyfin, "Some Movie", 0, 42)
	wantFile := naming.MovieFileName(naming.Jellyfin, "Some Movie", 0, 42, ".mkv")
	want := filepath.Join(root, wantFolder, wantFile)
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected organized movie at %q: %v (changes=%v)", want, err, changes)
	}
	items, err := libStore.List(context.Background(), mode.Movies)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].FilePath != want || items[0].TMDBID != 42 {
		t.Fatalf("library item = %+v", items)
	}
}

func TestImportGrabContent_SeriesUsesRelocateEpisode(t *testing.T) {
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)

	staging := t.TempDir()
	root := t.TempDir()
	src := filepath.Join(staging, "Show.Name.S01E02.mkv")
	if err := os.WriteFile(src, []byte("ep"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &grabs.Grab{
		Mode: mode.Series, Title: "Show Name", TMDBID: 99,
		SeasonNumber: 1, EpisodeNumber: 2, SeasonSpecified: true,
		RootFolderPath: root,
	}
	changes, err := importGrabContent(context.Background(), libStore, g, src, "web", settingsStore, nil, nil, nil)
	if err != nil {
		t.Fatalf("importGrabContent: %v", err)
	}
	found := false
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		base := filepath.Base(path)
		if strings.Contains(base, "S01E02") && strings.HasPrefix(base, "Show Name") {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("expected S01E02 episode under %q; changes=%v", root, changes)
	}
}

func TestImportGrabContent_AdultRelocatesWithoutOrganizeWhenNoSession(t *testing.T) {
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)

	staging := t.TempDir()
	root := t.TempDir()
	src := filepath.Join(staging, "[Site] Scene.mp4")
	if err := os.WriteFile(src, []byte("vid"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &grabs.Grab{Mode: mode.Adult, Title: "Scene", RootFolderPath: root}
	changes, err := importGrabContent(context.Background(), libStore, g, src, "web", settingsStore, nil, nil, nil)
	if err != nil {
		t.Fatalf("importGrabContent: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %v", changes)
	}
	dest := filepath.Join(root, "[Site] Scene.mp4")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("expected basename relocate to %q: %v", dest, err)
	}
	scenes, err := libStore.ListScenes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(scenes) != 0 {
		t.Errorf("organize skipped without session — expected 0 scenes, got %d", len(scenes))
	}
}
