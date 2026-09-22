package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
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

// TestImportGrabContent_SeriesUpgradeDeletesOldEpisode pins the quality-upgrade
// cleanup: re-importing an episode that already has a library file must remove
// the prior file once no episode row still references it.
func TestImportGrabContent_SeriesUpgradeDeletesOldEpisode(t *testing.T) {
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	root := t.TempDir()
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 77, Title: "Upgrade Show", Year: 2020, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	seasonDir := filepath.Join(root, "Upgrade Show", "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(seasonDir, "Upgrade Show - S01E01 - old.mkv")
	if err := os.WriteFile(oldPath, []byte("old-sd"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1,
		FilePath: oldPath, Size: 6, QualityTier: "medium",
	}); err != nil {
		t.Fatal(err)
	}

	staging := t.TempDir()
	src := filepath.Join(staging, "Upgrade.Show.S01E01.1080p.mkv")
	if err := os.WriteFile(src, []byte("new-hd-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &grabs.Grab{
		Mode: mode.Series, Title: "Upgrade Show", TMDBID: 77,
		SeasonNumber: 1, EpisodeNumber: 1, SeasonSpecified: true,
		RootFolderPath: root,
	}
	changes, err := importGrabContent(ctx, libStore, g, src, "high", settingsStore, nil, nil, nil)
	if err != nil {
		t.Fatalf("importGrabContent: %v", err)
	}

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old episode file still present at %q (err=%v); changes=%v", oldPath, err, changes)
	}
	ep, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ep.FilePath == "" || ep.FilePath == oldPath {
		t.Fatalf("episode path = %q, want new path != old", ep.FilePath)
	}
	if _, err := os.Stat(ep.FilePath); err != nil {
		t.Fatalf("new episode missing at %q: %v", ep.FilePath, err)
	}
	deleted := false
	for _, c := range changes {
		if c.Kind == mode.Deleted && c.Path == oldPath {
			deleted = true
		}
	}
	if !deleted {
		t.Fatalf("expected Deleted PathChange for %q in %v", oldPath, changes)
	}
}

// TestImportGrabContent_SeriesUpgradeKeepsSharedOldFile: when two episodes
// share one file and only one is re-imported, the shared file must stay.
func TestImportGrabContent_SeriesUpgradeKeepsSharedOldFile(t *testing.T) {
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()

	root := t.TempDir()
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 78, Title: "Shared Show", Year: 2020, RootFolderPath: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	seasonDir := filepath.Join(root, "Shared Show", "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(seasonDir, "Shared Show - S01E01-E02.mkv")
	if err := os.WriteFile(shared, []byte("bundle"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2} {
		if _, err := libStore.UpsertEpisode(ctx, library.Episode{
			SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: n,
			FilePath: shared, Size: 6, QualityTier: "medium",
		}); err != nil {
			t.Fatal(err)
		}
	}

	staging := t.TempDir()
	src := filepath.Join(staging, "Shared.Show.S01E01.1080p.mkv")
	if err := os.WriteFile(src, []byte("only-e01"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &grabs.Grab{
		Mode: mode.Series, Title: "Shared Show", TMDBID: 78,
		SeasonNumber: 1, EpisodeNumber: 1, SeasonSpecified: true,
		RootFolderPath: root,
	}
	if _, err := importGrabContent(ctx, libStore, g, src, "high", settingsStore, nil, nil, nil); err != nil {
		t.Fatalf("importGrabContent: %v", err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("shared file for E02 must remain: %v", err)
	}
	ep2, err := libStore.GetEpisode(ctx, series.ID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ep2.FilePath != shared {
		t.Fatalf("E02 path = %q, want shared %q", ep2.FilePath, shared)
	}
}
