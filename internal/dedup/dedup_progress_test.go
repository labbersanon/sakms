package dedup

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
)

// collectProgress returns a ProgressFunc that records every event, plus the
// slice it fills — a test asserts against it after the scan returns.
func collectProgress() (ProgressFunc, *[]ProgressEvent) {
	var events []ProgressEvent
	return func(ev ProgressEvent) { events = append(events, ev) }, &events
}

// assertNeverExceeds fails if any event's Current exceeds its Total — the
// invariant that guarantees the displayed percentage can never read over 100%.
func assertNeverExceeds(t *testing.T, events []ProgressEvent) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("expected at least one progress event, got none")
	}
	for i, ev := range events {
		if ev.Current > ev.Total {
			t.Errorf("event %d reads over 100%%: Current=%d > Total=%d (%+v)", i, ev.Current, ev.Total, ev)
		}
		if ev.Current < 1 {
			t.Errorf("event %d has a non-positive Current=%d (%+v)", i, ev.Current, ev)
		}
	}
}

// TestScanLibrarySeriesPHash_SeasonPackProgressNeverExceedsTotal is the
// load-bearing regression for the >100% bug the plan's review caught. A single
// season-pack orphan entry expands to MULTIPLE video files via
// ResolveEpisodeVideoFiles; if Total counted entries (1) instead of the flat
// video-file list (2), Current — which counts video files — would climb to 3
// against a Total of 2 (150%). With the flat-list denominator, Total is
// len(trackedEpisodes)+len(flatVideoPaths)=1+2=3 and Current reaches it exactly.
func TestScanLibrarySeriesPHash_SeasonPackProgressNeverExceedsTotal(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01.mkv", 100)
	// One season-pack orphan directory — a SINGLE ScanRootFolder entry that
	// ResolveEpisodeVideoFiles expands into two episode files.
	packDir := filepath.Join(dir, "Show.Name.Season.01.1080p.WEB-DL-GROUP")
	packEp1 := writeVideoFile(t, packDir, "Show.Name.S01E01.mkv", 100)
	packEp2 := writeVideoFile(t, packDir, "Show.Name.S01E02.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedFile,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series}
	prober := &fakeProber{}
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, packEp1: refHash, packEp2: refHash}}

	onProgress, events := collectProgress()
	if _, err := ScanLibrarySeriesPHash(ctx, sess, libStore, dir, prober, hasher, 2, onProgress); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertNeverExceeds(t, *events)

	// 1 tracked episode + 2 flat season-pack video files = 3 analyze emissions.
	// Under the old len(entries) denominator this would be Total=2 with Current
	// climbing to 3 — the exact >100% the flat-list restructure prevents.
	const wantTotal = 3
	last := (*events)[len(*events)-1]
	if last.Total != wantTotal {
		t.Errorf("expected the flat-videoFile denominator Total=%d, got %d", wantTotal, last.Total)
	}
	if last.Current != wantTotal {
		t.Errorf("expected Current to reach Total (%d) on the final emission, got Current=%d", wantTotal, last.Current)
	}
	for _, ev := range *events {
		if ev.Total != wantTotal {
			t.Errorf("expected a stable Total=%d across the scan, got %d (%+v)", wantTotal, ev.Total, ev)
		}
	}

	// The reported names are the tracked episode plus the two expanded pack files.
	var names []string
	for _, ev := range *events {
		names = append(names, ev.Name)
	}
	joined := strings.Join(names, "|")
	for _, want := range []string{"Show Name - S01E01.mkv", "Show.Name.S01E01.mkv", "Show.Name.S01E02.mkv"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected the progress names to include %q, got %v", want, names)
		}
	}
}

// TestScanLibraryPHash_ReportsProgressWithFileNames covers Movies: a non-nil
// ProgressFunc is invoked, never exceeds Total, and reports recognizable file
// names (the tracked basename and the orphan entry name).
func TestScanLibraryPHash_ReportsProgressWithFileNames(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Some Movie (2020)"), "movie.mkv", 100)
	orphanDir := "Some.Movie.2020.1080p.BluRay.x264-GROUP"
	orphanFile := writeVideoFile(t, filepath.Join(dir, orphanDir), "movie.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.Upsert(ctx, library.Item{
		Mode: mode.Movies, TMDBID: 42, Title: "Some Movie", FilePath: trackedFile, RootFolderPath: dir,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Movies}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}
	hasher := &fakePHasher{byPath: map[string]string{trackedFile: refHash, orphanFile: refHash}}

	onProgress, events := collectProgress()
	if _, err := ScanLibraryPHash(ctx, sess, libStore, dir, prober, hasher, 2, onProgress); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertNeverExceeds(t, *events)

	var names []string
	for _, ev := range *events {
		if ev.Phase != "hashing" {
			t.Errorf("expected Movies to report the hashing phase, got %q", ev.Phase)
		}
		names = append(names, ev.Name)
	}
	joined := strings.Join(names, "|")
	if !strings.Contains(joined, "movie.mkv") {
		t.Errorf("expected the tracked basename %q in the reported names, got %v", "movie.mkv", names)
	}
	if !strings.Contains(joined, "Some.Movie.2020") {
		t.Errorf("expected the orphan entry name in the reported names, got %v", names)
	}
}

// TestScanLibraryAdult_ReportsIdentifyProgress covers Adult: progress is emitted
// once per non-sidecar entry with the identifying phase, reaches Total exactly,
// and reports the file name being identified.
func TestScanLibraryAdult_ReportsIdentifyProgress(t *testing.T) {
	dir := t.TempDir()
	trackedDir := filepath.Join(dir, "Studio", "Some Scene")
	orphanName := "Some.Scene." + sceneUUIDA
	orphanDir := filepath.Join(dir, "Studio", orphanName)
	trackedFile := writeVideoFile(t, trackedDir, "scene.mkv", 100)
	orphanFile := writeVideoFile(t, orphanDir, "scene.mkv", 100)

	libStore := newTestLibraryStore(t)
	if _, err := libStore.UpsertScene(context.Background(), library.Scene{
		Box: "stashdb", SceneID: sceneUUIDA, Title: "Some Scene", Studio: "Some Studio",
		FilePath: trackedFile, RootFolderPath: dir,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := newAdultLibraryScanSession(t, fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}

	onProgress, events := collectProgress()
	if _, err := ScanLibraryAdult(context.Background(), sess, libStore, dir, prober, matchingPHasher(trackedFile, orphanFile), 10, onProgress); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertNeverExceeds(t, *events)

	last := (*events)[len(*events)-1]
	if last.Current != last.Total {
		t.Errorf("expected Adult Current to reach Total on the final emission, got Current=%d Total=%d", last.Current, last.Total)
	}
	var names []string
	for _, ev := range *events {
		if ev.Phase != "identifying" {
			t.Errorf("expected Adult to report the identifying phase, got %q", ev.Phase)
		}
		if ev.Name == "" {
			t.Errorf("expected a non-empty file name on every Adult progress event, got %+v", ev)
		}
		names = append(names, ev.Name)
	}
	if !strings.Contains(strings.Join(names, "|"), "Some.Scene") {
		t.Errorf("expected the identified orphan name in the reported names, got %v", names)
	}
}
