package rename

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/naming"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/tmdb"
)

// fakeTMDBSeriesServer stands in for TMDB's /search/tv and
// /tv/{id}/season/{n} endpoints — season lookups always succeed unless the
// season number is in failSeasons, letting tests exercise the "TMDB
// couldn't confirm this season" path.
func fakeTMDBSeriesServer(t *testing.T, searchResults map[string]string, failSeasons map[int]bool) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search/tv":
			term := r.URL.Query().Get("query")
			body, ok := searchResults[term]
			if !ok {
				t.Fatalf("unexpected search term %q", term)
			}
			w.Write([]byte(body))
		case strings.HasPrefix(r.URL.Path, "/tv/"):
			var tmdbID, season int
			if _, err := fmt.Sscanf(r.URL.Path, "/tv/%d/season/%d", &tmdbID, &season); err != nil {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			if failSeasons[season] {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Write([]byte(`{"episodes":[{"episode_number":1,"name":"Pilot","air_date":"2020-01-01"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

func TestScanLibrarySeries_ProducesPendingProposalForNewEpisode(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Show.Name.S01E01.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name","overview":"..."}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)

	got, err := ScanLibrarySeries(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.Title != "Show Name" || p.TMDBID != 555 || p.SeasonNumber != 1 || p.EpisodeNumber != 1 {
		t.Errorf("unexpected proposal: %+v", p)
	}
}

func TestScanLibrarySeries_SeasonPackProducesOneProposalPerEpisode(t *testing.T) {
	root := t.TempDir()
	packDir := filepath.Join(root, "Show.Name.Season.01")
	if err := os.Mkdir(packDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, name := range []string{"Show.Name.S01E01.mkv", "Show.Name.S01E02.mkv"} {
		if err := os.WriteFile(filepath.Join(packDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)

	got, err := ScanLibrarySeries(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected one proposal per episode file in the season pack, got %d: %+v", len(got), got)
	}
	for _, p := range got {
		if p.Status != proposals.Pending || p.TMDBID != 555 || p.SeasonNumber != 1 {
			t.Errorf("unexpected proposal: %+v", p)
		}
	}
}

// TestScanLibrarySeries_DiscoversNewEpisodeAlongsideAlreadyTrackedOne proves
// ScanRootFolder's recursion: once a season folder has one already-tracked
// episode file inside it, the folder is no longer atomic — a second, new
// episode file dropped in beside it surfaces individually, rather than the
// whole "Show Name/Season 01/" subtree staying masked forever just because
// one episode in it is already tracked.
func TestScanLibrarySeries_DiscoversNewEpisodeAlongsideAlreadyTrackedOne(t *testing.T) {
	root := t.TempDir()
	seasonDir := filepath.Join(root, "Show Name", "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tracked := filepath.Join(seasonDir, "Show Name - S01E01.mkv")
	if err := os.WriteFile(tracked, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	newFile := filepath.Join(seasonDir, "Show.Name.S01E02.mkv")
	if err := os.WriteFile(newFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: tracked,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].SourcePath != newFile || got[0].EpisodeNumber != 2 {
		t.Fatalf("expected only the new episode (not the already-tracked S01E01) to surface, got %+v", got)
	}
}

// TestScanLibrarySeries_SkipsAlreadyConformantEpisodeInMixedSeasonPack
// proves the schema-conformance filter applies per resolved file, not per
// directory: a "Season 01" folder with one already-Jellyfin-conformant
// episode and one non-conformant one only proposes the non-conformant file.
func TestScanLibrarySeries_SkipsAlreadyConformantEpisodeInMixedSeasonPack(t *testing.T) {
	root := t.TempDir()
	seasonDir := filepath.Join(root, "Show Name [tmdbid-555]", "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seasonDir, "Show Name S01E01.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	nonConformant := filepath.Join(seasonDir, "Show.Name.S01E02.mkv")
	if err := os.WriteFile(nonConformant, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)

	got, err := ScanLibrarySeries(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].SourcePath != nonConformant || got[0].EpisodeNumber != 2 {
		t.Fatalf("expected only the non-conformant episode proposed, got %+v", got)
	}
}

func TestScanLibrarySeries_SkipsAlreadyTrackedWithFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Show.Name.S01E01.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: "/elsewhere/ep.mkv",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected the duplicate to surface as unmatched, got %+v", got)
	}
}

func TestScanLibrarySeries_DoesNotSkipEpisodeKnownAsMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Show.Name.S01E02.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// TMDB knows episode 2 exists, but no file for it yet (file_path == "").
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Second",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending {
		t.Fatalf("expected finding a file for a known-missing episode to still propose it, got %+v", got)
	}
}

func TestScanLibrarySeries_UnmatchedWhenParseFails(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Not.An.Episode.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, nil, nil)}
	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected an unmatched proposal when season/episode can't be parsed, got %+v", got)
	}
}

func TestScanLibrarySeries_UnmatchedWhenSeasonDetailsFail(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Show.Name.S01E01.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	}, map[int]bool{1: true})}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected unmatched when TMDB can't confirm the season, got %+v", got)
	}
}

// TestScanLibrarySeries_MarksUnmatchedWhenTMDBResultIsWeakMatch is Series'
// counterpart to Movies' TestScanLibrary_MarksUnmatchedWhenTMDBResultIsWeakMatch
// — the confidence gate in proposeOneEpisodeLibrary is a separate call site
// from Movies' proposeOneLibrary and needs its own direct coverage, not just
// the shared matchConfidence unit tests.
func TestScanLibrarySeries_MarksUnmatchedWhenTMDBResultIsWeakMatch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "xyz123.S01E01.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"xyz123": `{"results":[{"id":555,"name":"Completely Unrelated Show"}]}`,
	}, nil)}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultConfidenceThreshold)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected the weak match to route to unmatched, got %+v", got)
	}
	if got[0].TMDBID != 0 {
		t.Errorf("expected no TMDB id to be assigned on a rejected weak match, got %d", got[0].TMDBID)
	}
}

// TestScanLibrarySeries_ThresholdZeroAcceptsAnyTMDBResult is Series'
// counterpart to Movies' TestScanLibrary_ThresholdZeroAcceptsAnyTMDBResult.
func TestScanLibrarySeries_ThresholdZeroAcceptsAnyTMDBResult(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "xyz123.S01E01.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"xyz123": `{"results":[{"id":555,"name":"Completely Unrelated Show"}]}`,
	}, nil)}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending || got[0].TMDBID != 555 {
		t.Fatalf("expected a threshold of 0 to accept the weak match, got %+v", got)
	}
}

func TestApplyLibrarySeries_RelocatesIntoSeasonFolderAndPreservesMetadata(t *testing.T) {
	base := t.TempDir()
	sourceRoot := filepath.Join(base, "incoming")
	destRoot := filepath.Join(base, "TV")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(sourceRoot, "Show.Name.S01E01.mkv")
	if err := os.WriteFile(sourcePath, []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	// A prior Sonarr import already recorded this episode's title as
	// missing — Apply must preserve that metadata, not blank it.
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: destRoot})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", AirDate: "2020-01-01",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555,
		SeasonNumber: 1, EpisodeNumber: 1, SourcePath: sourcePath, RootFolderPath: destRoot,
	}
	epID, changes, err := ApplyLibrarySeries(ctx, libStore, p, naming.Jellyfin)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if epID == 0 {
		t.Error("expected a nonzero episode id")
	}

	wantDest := filepath.Join(destRoot, "Show Name [tmdbid-555]", "Season 01", "Show Name S01E01.mkv")
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Errorf("expected the source file to be gone, stat returned: %v", err)
	}
	if data, err := os.ReadFile(wantDest); err != nil || string(data) != "fake video data" {
		t.Errorf("expected the file at %q, err=%v data=%q", wantDest, err, data)
	}

	ep, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.FilePath != wantDest {
		t.Errorf("expected file path recorded, got %q", ep.FilePath)
	}
	if ep.Title != "Pilot" || ep.AirDate != "2020-01-01" {
		t.Errorf("expected existing episode metadata to be preserved, got %+v", ep)
	}

	// Row 2 (player-rescan-notify plan): unlike Movies, the Deleted side is
	// p.SourcePath DIRECTLY (no ResolveVideoFile indirection) — intentional
	// asymmetry with row 1.
	want := []mode.PathChange{{Path: sourcePath, Kind: mode.Deleted}, {Path: wantDest, Kind: mode.Created}}
	if len(changes) != 2 || changes[0] != want[0] || changes[1] != want[1] {
		t.Errorf("expected changes %+v, got %+v", want, changes)
	}
}

// TestApplyLibrarySeries_NoMoveWhenAlreadyCorrectlyPlaced is the Series
// mirror of rename's TestApplyLibrary_NoMoveWhenAlreadyCorrectlyPlaced:
// RelocateEpisode's own self-collision guard means moved can equal
// p.SourcePath when the file already sits at the preset-computed
// destination — no os.Rename happens, so ApplyLibrarySeries must not report
// a bogus Deleted+Created pair for the same unchanged path.
func TestApplyLibrarySeries_NoMoveWhenAlreadyCorrectlyPlaced(t *testing.T) {
	base := t.TempDir()
	seasonDir := filepath.Join(base, "Show Name [tmdbid-555]", "Season 01")
	sourcePath := filepath.Join(seasonDir, "Show Name S01E01.mkv")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555,
		SeasonNumber: 1, EpisodeNumber: 1, SourcePath: sourcePath, RootFolderPath: base,
	}
	epID, changes, err := ApplyLibrarySeries(ctx, libStore, p, naming.Jellyfin)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Errorf("expected the file to stay in place (already correctly named), got: %v", err)
	}
	series, err := libStore.GetSeriesByTMDBID(ctx, 555)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.ID != epID || ep.FilePath != sourcePath {
		t.Errorf("expected the recorded file path to be the unchanged source path, got %q", ep.FilePath)
	}
	// No physical move happened, so no bogus Deleted+Created pair for the
	// same unchanged path should be reported.
	if len(changes) != 0 {
		t.Errorf("expected zero PathChanges when the file didn't move, got %+v", changes)
	}
}

// TestApplyLibrarySeries_LegacyPresetPreservesTodaysShape proves the Legacy
// preset keeps the exact dash-separated, no-tag shape this project used
// before Jellyfin/Emby alignment existed — an explicit opt-in so an
// already-renamed library's on-disk shape doesn't silently change after an
// upgrade.
func TestApplyLibrarySeries_LegacyPresetPreservesTodaysShape(t *testing.T) {
	base := t.TempDir()
	destRoot := filepath.Join(base, "TV")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(base, "Show.Name.S01E01.mkv")
	if err := os.WriteFile(sourcePath, []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555,
		SeasonNumber: 1, EpisodeNumber: 1, SourcePath: sourcePath, RootFolderPath: destRoot,
	}
	if _, _, err := ApplyLibrarySeries(ctx, libStore, p, naming.Legacy); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantDest := filepath.Join(destRoot, "Show Name", "Season 01", "Show Name - S01E01.mkv")
	if _, err := os.ReadFile(wantDest); err != nil {
		t.Errorf("expected the file at %q (legacy shape, no year/tag), err=%v", wantDest, err)
	}
}

func TestApplyLibrarySeries_RejectsNonPendingProposal(t *testing.T) {
	libStore := newTestLibraryStore(t)
	for _, status := range []proposals.Status{proposals.Applied, proposals.Dismissed, proposals.Unmatched} {
		if _, _, err := ApplyLibrarySeries(context.Background(), libStore, proposals.Proposal{Status: status}, naming.Jellyfin); err == nil {
			t.Errorf("expected ApplyLibrarySeries to refuse a %q proposal", status)
		}
	}
}
