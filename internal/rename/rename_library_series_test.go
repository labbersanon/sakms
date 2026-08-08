package rename

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/labbersanon/sakms/internal/bravesearch"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/ollama"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/tvdb"
	"github.com/labbersanon/sakms/internal/websearch"
)

// fakeTMDBSeriesServer stands in for TMDB's /search/tv and
// /tv/{id}/season/{n} endpoints — season lookups always succeed unless the
// season number is in failSeasons (all show ids), letting tests exercise the
// "TMDB couldn't confirm this season" path.
func fakeTMDBSeriesServer(t *testing.T, searchResults map[string]string, failSeasons map[int]bool) *tmdb.Client {
	t.Helper()
	return fakeTMDBSeriesServerEx(t, searchResults, failSeasons, nil)
}

// fakeTMDBSeriesServerEx is fakeTMDBSeriesServer plus optional per-show season
// failures (failSeasonByID[tmdbID][season]). Used when an .nfo points at one
// id that lacks a season while filename search should hit a different id.
func fakeTMDBSeriesServerEx(
	t *testing.T,
	searchResults map[string]string,
	failSeasons map[int]bool,
	failSeasonByID map[int]map[int]bool,
) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search/tv":
			term := r.URL.Query().Get("query")
			body, ok := searchResults[term]
			if !ok {
				w.Write([]byte(`{"results":[]}`))
				return
			}
			w.Write([]byte(body))
		case strings.HasPrefix(r.URL.Path, "/tv/"):
			var tmdbID, season int
			if _, err := fmt.Sscanf(r.URL.Path, "/tv/%d/season/%d", &tmdbID, &season); err != nil {
				// enrichment calls (/tv/{id}, /tv/{id}/aggregate_credits) — soft-fail
				// Return empty details so Pending can still form with search title.
				if n, err2 := fmt.Sscanf(r.URL.Path, "/tv/%d", &tmdbID); err2 == nil && n == 1 && !strings.Contains(r.URL.Path, "/season/") {
					w.Write([]byte(fmt.Sprintf(`{"id":%d,"name":"Show Name","first_air_date":"2020-01-01","genres":[]}`, tmdbID)))
					return
				}
				http.NotFound(w, r)
				return
			}
			if failSeasons[season] {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			if byID := failSeasonByID[tmdbID]; byID != nil && byID[season] {
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
	if err := os.WriteFile(filepath.Join(root, "Show.Name.2020.S01E01.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name","overview":"...","first_air_date":"2020-01-01"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)

	got, err := ScanLibrarySeries(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
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
	for _, name := range []string{"Show.Name.2020.S01E01.mkv", "Show.Name.2020.S01E02.mkv"} {
		if err := os.WriteFile(filepath.Join(packDir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name","first_air_date":"2020-01-01"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)

	got, err := ScanLibrarySeries(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
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
// TestScanLibrarySeries_LogicalSplitProducesOneProposalWithExtraEpisodes
// proves a bundled multi-episode file ("S01E01-E02") produces exactly ONE
// proposal (not two, and not a truncated single-episode one) with
// EpisodeNumber set to the primary and ExtraEpisodeNumbers carrying the
// rest.
func TestScanLibrarySeries_LogicalSplitProducesOneProposalWithExtraEpisodes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Show.Name.2020.S01E01-E02.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name","first_air_date":"2020-01-01"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)

	got, err := ScanLibrarySeries(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 proposal for the bundled file, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.SeasonNumber != 1 || p.EpisodeNumber != 1 {
		t.Errorf("unexpected proposal: %+v", p)
	}
	if len(p.ExtraEpisodeNumbers) != 1 || p.ExtraEpisodeNumbers[0] != 2 {
		t.Errorf("expected ExtraEpisodeNumbers=[2], got %+v", p.ExtraEpisodeNumbers)
	}
}

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
	newFile := filepath.Join(seasonDir, "Show.Name.2020.S01E02.mkv")
	if err := os.WriteFile(newFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name","first_air_date":"2020-01-01"}]}`,
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

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
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
	nonConformant := filepath.Join(seasonDir, "Show.Name.2020.S01E02.mkv")
	if err := os.WriteFile(nonConformant, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name","first_air_date":"2020-01-01"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)

	got, err := ScanLibrarySeries(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].SourcePath != nonConformant || got[0].EpisodeNumber != 2 {
		t.Fatalf("expected only the non-conformant episode proposed, got %+v", got)
	}
}

// TestScanLibrarySeries_AlreadyTrackedWithFileIsPendingAlternate covers SITE 2
// (the acceptSeries path): an orphan resolving onto an episode slot already
// tracked WITH a file.
//
// REWRITE IN PLACE of the retired ..._SkipsAlreadyTrackedWithFile, which
// asserted Unmatched. Plan §9.7.1's enumeration ("9 line citations, 8 distinct
// test functions") MISSED this one, because §9.7.1(D)'s greps only find tests
// asserting on the reason STRING and this test asserted on Status alone with a
// prose failure message. A grep-clean audit is therefore not proof the
// reconciliation is complete.
//
// Deliberately kept as the narrow status+reason check, and deliberately NOT
// named ..._TrackedSlotDuplicateIsPendingAlternate: plan §9.5 reserves that
// name for the new Site 2 test that additionally asserts
// Title/TMDBID/Year/RootFolderPath are populated (the §5.1 helper-asymmetry
// check). The two are complements, not duplicates.
func TestScanLibrarySeries_AlreadyTrackedWithFileIsPendingAlternate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Show.Name.2020.S01E01.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name","first_air_date":"2020-01-01"}]}`,
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

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending {
		t.Fatalf("expected the duplicate to surface as a Pending alternate, got %+v", got)
	}
	if !strings.HasPrefix(got[0].Reason, `alternate: "Show Name" S01E01 already has a file in the library`) {
		t.Errorf("expected the softened alternate reason, got %q", got[0].Reason)
	}
}

// TestScanLibrarySeries_TrackedSlotDuplicateIsPendingAlternate is plan §9.5's
// SITE 2 test and the deliberate COMPLEMENT of
// ..._AlreadyTrackedWithFileIsPendingAlternate directly above — not a duplicate
// of it. That test pins Status + the reason prefix; this one pins the FIELD
// POPULATION the softened row must carry, which §9.5 reserves for this test by
// name.
//
// What it catches, stated precisely because a weaker assertion would not:
// acceptDuplicatePendingEpisode is deliberately ASYMMETRIC with Movies'
// acceptDuplicatePending — it writes Status and Reason ONLY, and must never
// touch Title/TMDBID/Year/RootFolderPath, because Series' seven call sites each
// populate those from a different source (§5.1). "Unifying" the two helpers
// would run the Movies version's clobbering writes here and silently zero this
// row's placement data.
//
// The assertions are on EXACT values, not on non-emptiness: the failure mode is
// a field being blanked or overwritten with the helper's own idea of the show,
// and a Year != 0 check would not catch a wrong-but-non-zero year.
func TestScanLibrarySeries_TrackedSlotDuplicateIsPendingAlternate(t *testing.T) {
	root := t.TempDir()
	orphan := filepath.Join(root, "Show.Name.2020.S01E01.mkv")
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The tracked slot's own file lives OUTSIDE the scan root, so the walk never
	// sees it and the only proposal produced is the duplicate.
	trackedPath := filepath.Join(t.TempDir(), "already-tracked.mkv")
	if err := os.WriteFile(trackedPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name","first_air_date":"2020-01-01"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedPath,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending-as-alternate", p.Status, p.Reason)
	}
	if !strings.HasPrefix(p.Reason, "alternate:") {
		t.Errorf("reason = %q, want the softened alternate reason", p.Reason)
	}
	if p.Title != "Show Name" {
		t.Errorf("Title = %q, want %q — the helper must not overwrite the site's own title", p.Title, "Show Name")
	}
	if p.TMDBID != 555 {
		t.Errorf("TMDBID = %d, want 555 — a softened duplicate still carries its placement data", p.TMDBID)
	}
	if p.Year != 2020 {
		t.Errorf("Year = %d, want 2020 (TMDB's first_air_date) — a blanked year is the folder-splitting corruption §5.1 guards against", p.Year)
	}
	if p.RootFolderPath != root {
		t.Errorf("RootFolderPath = %q, want %q", p.RootFolderPath, root)
	}
	if p.SeasonNumber != 1 || p.EpisodeNumber != 1 {
		t.Errorf("placement = S%02dE%02d, want S01E01", p.SeasonNumber, p.EpisodeNumber)
	}
	if p.SourcePath != orphan {
		t.Errorf("SourcePath = %q, want the orphan %q", p.SourcePath, orphan)
	}
}

func TestScanLibrarySeries_DoesNotSkipEpisodeKnownAsMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Show.Name.2020.S01E02.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name","first_air_date":"2020-01-01"}]}`,
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

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
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
	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected an unmatched proposal when season/episode can't be parsed, got %+v", got)
	}
}

func TestScanLibrarySeries_UnmatchedWhenSeasonDetailsFail(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Show.Name.2020.S01E01.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name","first_air_date":"2020-01-01"}]}`,
	}, map[int]bool{1: true})}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected unmatched when TMDB can't confirm the season, got %+v", got)
	}
}

// TestScanLibrarySeries_NFOSeasonMissFallsThroughToSearch proves a tvshow.nfo
// whose TMDB id lacks the filename's season no longer hard-unmatches — search
// (and downstream TVDB/web) can still recover a Pending proposal.
func TestScanLibrarySeries_NFOSeasonMissFallsThroughToSearch(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "Monster.2022.S02E01.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// NFO points at a show id that 404s on season 2; filename search hits a
	// different id that has season 2.
	if err := os.WriteFile(filepath.Join(root, "tvshow.nfo"), []byte(`<tvshow><tmdbid>128826</tmdbid></tvshow>`), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{
		Mode: mode.Series,
		TMDB: fakeTMDBSeriesServerEx(t,
			map[string]string{
				"Monster": `{"results":[{"id":999,"name":"Monster","first_air_date":"2022-01-01","overview":"..."}]}`,
			},
			nil,
			map[int]map[int]bool{128826: {2: true}},
		),
	}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("expected Pending after NFO season miss fallthrough, got %v reason=%q", p.Status, p.Reason)
	}
	if p.TMDBID != 999 {
		t.Errorf("expected recovered TMDB id 999, got %d", p.TMDBID)
	}
	if p.SeasonNumber != 2 || p.EpisodeNumber != 1 {
		t.Errorf("expected S02E01, got S%02dE%02d", p.SeasonNumber, p.EpisodeNumber)
	}
	if strings.Contains(p.Reason, "could not confirm season") {
		t.Errorf("fallthrough must not keep the hard NFO season-404 reason, got %q", p.Reason)
	}
}

// TestScanLibrarySeries_NFODuplicateIsPendingAlternate is plan §9.5's SITE 1
// test — the .nfo duplicate-slot branch (rename.go:1034-1036), the site the
// spec's acceptance criteria name explicitly (the Beverly Hillbillies .nfo
// case) and the only one of the seven softened sites no other test reaches.
//
// Site 1 is the STRUCTURALLY ODD one of the seven: isDuplicateSlot is captured
// ~38 lines above its use, and the guard firing does NOT return early — control
// falls through the whole TMDB-confirm block, which populates Title/TMDBID/Year/
// RootFolderPath, before acceptDuplicatePendingEpisode runs and rewrites Status
// and Reason ONLY. So the field assertions below are not incidental: they pin
// that the helper's deliberate asymmetry with Movies' acceptDuplicatePending
// (which DOES clobber those fields) still holds here. Unifying the two would
// blank this row's placement data, which is the folder-splitting corruption
// class §5.2.4 exists to prevent.
//
// Two properties of the setup are load-bearing and easy to undo by accident:
//
//   - The fake TMDB server must SUCCEED on SeasonDetails(nfo id, 1). The
//     sibling ..._NFOSeasonMissFallsThroughToSearch drives the MISS path and
//     therefore never reaches line 1034 at all.
//   - searchResults is deliberately nil, so the filename search returns nothing.
//     Any silent fall-through out of the .nfo branch lands Unmatched and fails
//     loudly here, instead of quietly reaching the search path's own duplicate
//     site (rename.go:1140) and passing for the wrong reason. The TMDBID
//     assertion is the positive half of the same discrimination: Site 1 sets it
//     from hint.TMDBID, the search site from match.ID.
func TestScanLibrarySeries_NFODuplicateIsPendingAlternate(t *testing.T) {
	root := t.TempDir()
	orphan := filepath.Join(root, "The.Beverly.Hillbillies.S01E01.mkv")
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// <year> is required: line 1022 populates p.Year from the SIDECAR hint, not
	// from TVDetails, so a year-less .nfo would leave Year 0 and the assertion
	// below would have no teeth.
	if err := os.WriteFile(filepath.Join(root, "tvshow.nfo"),
		[]byte(`<tvshow><tmdbid>1899</tmdbid><year>1962</year></tvshow>`), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The tracked slot's own file lives OUTSIDE the scan root, so the walk never
	// sees it: the collision is a genuine duplicate, not a self-collision.
	trackedPath := filepath.Join(t.TempDir(), "already-tracked.mkv")
	if err := os.WriteFile(trackedPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, nil, nil)}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1899, Title: "The Beverly Hillbillies", RootFolderPath: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A non-empty FilePath is what makes this an OCCUPIED slot — rename.go:721
	// skips fileless catalog rows, so a pathless row would never enter tracked
	// and Site 1's guard would not fire at all.
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedPath,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending-as-alternate", p.Status, p.Reason)
	}
	if !strings.HasPrefix(p.Reason, "alternate:") {
		t.Errorf("reason = %q, want the softened alternate reason", p.Reason)
	}
	// Distinguishes acceptDuplicatePendingEpisode's reason from the within-batch
	// `seen` guard's ("another file in this scan also claims"), which also
	// prefixes "alternate:" — it cannot fire on a one-proposal scan, but the
	// substring makes the test self-documenting about which writer it pins.
	if !strings.Contains(p.Reason, "already has a file in the library") {
		t.Errorf("reason = %q, want acceptDuplicatePendingEpisode's own wording", p.Reason)
	}
	if p.TMDBID != 1899 {
		t.Errorf("TMDBID = %d, want 1899 (the .nfo's id) — a different id means the filename-search site answered, not Site 1", p.TMDBID)
	}
	if p.Title != "Show Name" {
		// The shared fake's /tv/{id} stub returns this fixed name for EVERY id,
		// so the value proves the field survived the helper, not the lookup.
		t.Errorf("Title = %q, want %q — the helper must not overwrite the site's own title", p.Title, "Show Name")
	}
	if p.Year != 1962 {
		t.Errorf("Year = %d, want 1962 (the .nfo's year) — a blanked year is the folder-splitting corruption §5.2.4 guards against", p.Year)
	}
	if p.RootFolderPath != root {
		t.Errorf("RootFolderPath = %q, want %q", p.RootFolderPath, root)
	}
	if p.SeasonNumber != 1 || p.EpisodeNumber != 1 {
		t.Errorf("placement = S%02dE%02d, want S01E01", p.SeasonNumber, p.EpisodeNumber)
	}
	if p.SourcePath != orphan {
		t.Errorf("SourcePath = %q, want the orphan %q", p.SourcePath, orphan)
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

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultMatchConfig(), nil)
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

// TestScanLibrarySeries_ThresholdZeroAcceptsAnyTMDBResult is obsolete under
// drilldown — no year/actor/duration means Unmatched (same as Movies).
func TestScanLibrarySeries_NoSignalsUnmatchedEvenIfTMDBReturnsHit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "xyz123.S01E01.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"xyz123": `{"results":[{"id":555,"name":"Completely Unrelated Show"}]}`,
	}, nil)}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected Unmatched without corroborating signals, got %+v", got)
	}
}

// TestScanLibrarySeries_LooseParentDirParseRecoversOpaqueHashBasename is the
// §3.3 integration case for .omc/plans/autopilot-impl.md's "The Path" fix:
// an opaque hash basename with zero recognizable content, nested one level
// under a folder that IS a normal scene-release name
// ("The.Path.S01E02.2160p.WEB.h265-NiXON") — the real shape from
// .omc/artifacts/series-parse-failures-20260806.psv. Before the fix this
// produced Unmatched "could not determine season/episode from %q"
// immediately, without ever reaching TMDB. With ParseEpisodeNumbersLoose +
// StripEpisodeMarkerLoose it recovers season/episode AND a usable TMDB
// search term from the parent directory, landing on Pending.
func TestScanLibrarySeries_LooseParentDirParseRecoversOpaqueHashBasename(t *testing.T) {
	root := t.TempDir()
	epDir := filepath.Join(root, "The Path", "Season 1", "The.Path.S01E02.2160p.WEB.h265-NiXON")
	if err := os.MkdirAll(epDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	videoPath := filepath.Join(epDir, "2ea4ad06efe20501d944b90f3a291e6f.mp4")
	if err := os.WriteFile(videoPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search/tv":
			if r.URL.Query().Get("query") != "The Path" {
				w.Write([]byte(`{"results":[]}`))
				return
			}
			w.Write([]byte(`{"results":[{"id":777,"name":"The Path","first_air_date":"2016-03-30"}]}`))
		case r.URL.Path == "/tv/777/season/1":
			// runtime (minutes) matches the fake prober's 1800s duration
			// exactly, so signal corroboration accepts this candidate.
			w.Write([]byte(`{"episodes":[{"episode_number":2,"name":"Homecoming","air_date":"2016-04-06","runtime":30}]}`))
		case r.URL.Path == "/tv/777":
			w.Write([]byte(`{"id":777,"name":"The Path","first_air_date":"2016-03-30","genres":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	sess := &mode.Session{Mode: mode.Series, TMDB: tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())}
	prober := mapProber{videoPath: {Duration: 1800}}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultMatchConfig(), prober)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("expected Pending (hash basename resolved via the parent directory), got %v reason=%q", p.Status, p.Reason)
	}
	if p.SeasonNumber != 1 || p.EpisodeNumber != 2 {
		t.Errorf("expected S01E02 recovered from the parent directory, got S%02dE%02d", p.SeasonNumber, p.EpisodeNumber)
	}
	if p.TMDBID != 777 {
		t.Errorf("expected TMDB id 777 found by searching the parent dir's stripped title, got %d", p.TMDBID)
	}
}

// TestScanLibrarySeries_SameSlotWithinOneScanBothStayPending covers the `seen`
// guard (plan §5.3), reached via the loose parent-dir parse:
// ResolveEpisodeVideoFiles returns every video file in a directory with no
// filter, and ParseEpisodeNumbersLoose resolves EVERY file in a marker-named
// parent directory to the SAME season/episode (there is only one parent dir to
// fall back to). Two video files sharing a parent dir both resolve to an
// identical (tmdbID, season, episode).
//
// This is the REWRITE IN PLACE of the retired
// ..._LooseParentDirParseCollisionMarksSecondClaimantUnmatched (plan
// §9.7.1(A), A3): same fixture, opposite outcome. Both files now stay Pending
// (was: exactly 1 Pending + 1 Unmatched) — the second is a legitimate
// alternate, annotated rather than declined, and ApplyLibrarySeries resolves
// the collision against live DB state: whichever applies first becomes the
// tracked primary and the second folds in via applyLibrarySeriesAlternate,
// order-independently.
//
// Placement note for anyone diffing against plan §9.7.1(A): the plan says the
// retired collision-reason check "moves into the Pending branch". Taken
// literally that would assert the alternate prefix on BOTH rows and fail — the
// `seen` guard annotates only the SECOND claimant. So the check lives on
// got[1], alongside the scan-order assertions it belongs with.
func TestScanLibrarySeries_SameSlotWithinOneScanBothStayPending(t *testing.T) {
	root := t.TempDir()
	epDir := filepath.Join(root, "The Path", "Season 1", "The.Path.S01E02.2160p.WEB.h265-NiXON")
	if err := os.MkdirAll(epDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// os.ReadDir (which ResolveEpisodeVideoFiles uses) returns entries in
	// lexical order, so "aaa..." resolves first and "bbb..." second —
	// deterministic scan order for this test's assertions.
	firstPath := filepath.Join(epDir, "aaa2ea4ad06efe20501d944b90f3a291e6f.mp4")
	secondPath := filepath.Join(epDir, "bbb6c9038580261273f37fee109d678acbf.mp4")
	if err := os.WriteFile(firstPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(secondPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search/tv":
			if r.URL.Query().Get("query") != "The Path" {
				w.Write([]byte(`{"results":[]}`))
				return
			}
			w.Write([]byte(`{"results":[{"id":777,"name":"The Path","first_air_date":"2016-03-30"}]}`))
		case r.URL.Path == "/tv/777/season/1":
			w.Write([]byte(`{"episodes":[{"episode_number":2,"name":"Homecoming","air_date":"2016-04-06","runtime":30}]}`))
		case r.URL.Path == "/tv/777":
			w.Write([]byte(`{"id":777,"name":"The Path","first_air_date":"2016-03-30","genres":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	sess := &mode.Session{Mode: mode.Series, TMDB: tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())}
	// Both files probe to the same duration, so both independently pass
	// corroboration and both resolve to the identical S01E02/tmdb 777 —
	// exactly the collision this guard exists to catch.
	prober := mapProber{firstPath: {Duration: 1800}, secondPath: {Duration: 1800}}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultMatchConfig(), prober)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 proposals (one per file), got %d: %+v", len(got), got)
	}

	var pendingCount, unmatchedCount int
	for _, p := range got {
		switch p.Status {
		case proposals.Pending:
			pendingCount++
			if p.SeasonNumber != 1 || p.EpisodeNumber != 2 || p.TMDBID != 777 {
				t.Errorf("unexpected Pending proposal: %+v", p)
			}
		case proposals.Unmatched:
			unmatchedCount++
		default:
			t.Errorf("unexpected status %v on proposal: %+v", p.Status, p)
		}
	}
	if pendingCount != 2 || unmatchedCount != 0 {
		t.Fatalf("expected both files Pending (the second as an alternate), got %d Pending, %d Unmatched: %+v", pendingCount, unmatchedCount, got)
	}
	// Scan-order determinism is still the property being pinned: the
	// first-resolved file (scan order) is the slot's primary claimant.
	if got[0].SourcePath != firstPath || got[0].Status != proposals.Pending {
		t.Errorf("expected the first-scanned file (%q) to be the Pending primary claimant, got %+v", firstPath, got[0])
	}
	if got[1].SourcePath != secondPath || got[1].Status != proposals.Pending {
		t.Errorf("expected the second-scanned file (%q) to stay Pending as an alternate, got %+v", secondPath, got[1])
	}
	if !strings.HasPrefix(got[1].Reason, "alternate:") {
		t.Errorf("expected the second claimant to carry the `seen` guard's alternate annotation, got %q", got[1].Reason)
	}
}

// TestScanLibrarySeries_AppliedThenRescannedDuplicateIsPendingAlternate is plan
// §9.5's CROSS-SCAN test — the one that would have caught the CRITICAL finding,
// and the only test in the suite that distinguishes "the tracked guards were
// audited" from "the tracked guards were assumed to be three".
//
// WHY NO SINGLE-SCAN TEST CAN COVER THIS. Within one scan both copies are
// caught by the `seen` guard (§5.3), which is softened — so every single-scan
// duplicate test passes EVEN IF the tracked-slot sites were left hard-declining.
// The `seen` guard masks the whole defect. Only after one copy is APPLIED does
// the slot appear in `tracked`, routing the second copy to a tracked site
// (Site 2 here) instead.
//
// The three scans pin three different mechanisms — say which, because they look
// alike from the outside:
//
//	scan 1: the `seen` guard   — two same-slot copies in one scan, both Pending.
//	scan 2: SITE 2 (tracked)   — the surviving copy still Pending + "alternate:",
//	                             now because the slot is tracked, not because a
//	                             sibling was seen. THIS is the regression guard.
//	scan 3: §5.4's known feed  — neither applied path is re-proposed.
//
// HONEST LIMIT ON SCAN 3, so nobody reads more into it than it proves: an
// Apply-produced alternate lands in the season folder under a
// preset-conformant name, so naming.MatchesSeriesSchema ALSO suppresses it
// independently of the `known` feed. Scan 3 therefore pins the end-to-end
// outcome an operator sees, not the feed itself. The feed is pinned two ways
// that cannot be masked: directly, on AllEpisodeFilePaths (its data source),
// and by the non-conformant-path subtest at the end, which has its own control.
//
// DEVIATION from §9.5, recorded rather than left silent: the plan asks for this
// to be table-driven over the NFO / acceptSeries / episode-title-match /
// anthology fixtures. This covers the acceptSeries path only, which is US-008's
// acceptance criterion; the other three resolution paths remain uncovered
// cross-scan.
func TestScanLibrarySeries_AppliedThenRescannedDuplicateIsPendingAlternate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// Two copies of one episode, each in its own folder so ResolveEpisodeVideoFiles
	// hands back one file per entry. Identical basenames mean both derive the
	// same search term and resolve to the same (555, S01, E01).
	firstPath := filepath.Join(root, "copy-a", "Show.Name.2020.S01E01.mkv")
	secondPath := filepath.Join(root, "copy-b", "Show.Name.2020.S01E01.mkv")
	for _, p := range []string{firstPath, secondPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("seeding %q: %v", p, err)
		}
	}

	tmdbClient := fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name","first_air_date":"2020-01-01"}]}`,
	}, nil)
	sess := &mode.Session{Mode: mode.Series, TMDB: tmdbClient}
	libStore := newTestLibraryStore(t)
	prober := mapProber{}

	// --- Scan 1: the `seen` guard. Both copies stay Pending. ---
	first, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), prober)
	if err != nil {
		t.Fatalf("scan 1: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("scan 1: expected 2 proposals, got %d: %+v", len(first), first)
	}
	for _, p := range first {
		if p.Status != proposals.Pending {
			t.Fatalf("scan 1: %q is %v (%s), want Pending", p.SourcePath, p.Status, p.Reason)
		}
	}

	// Selected by SourcePath, never by index — scan order is a walk detail.
	apply := func(t *testing.T, got []proposals.Proposal, sourcePath string) int64 {
		t.Helper()
		for _, p := range got {
			if p.SourcePath != sourcePath {
				continue
			}
			id, _, err := ApplyLibrarySeries(ctx, libStore, tmdbClient, nil, p, naming.Jellyfin, "medium", prober)
			if err != nil {
				t.Fatalf("ApplyLibrarySeries(%q): %v", sourcePath, err)
			}
			return id
		}
		t.Fatalf("no proposal for %q in %+v", sourcePath, got)
		return 0
	}
	episodeID := apply(t, first, firstPath)

	// --- Scan 2: SITE 2. The surviving copy must NOT regress to Unmatched. ---
	second, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), prober)
	if err != nil {
		t.Fatalf("scan 2: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("scan 2: expected only the un-applied copy to surface, got %d: %+v", len(second), second)
	}
	p := second[0]
	if p.SourcePath != secondPath {
		t.Fatalf("scan 2: proposal is for %q, want the un-applied copy %q", p.SourcePath, secondPath)
	}
	if p.Status != proposals.Pending {
		t.Fatalf("scan 2: status = %v (%s) — an applied duplicate's twin regressed to a hard decline on the next scan",
			p.Status, p.Reason)
	}
	if !strings.HasPrefix(p.Reason, "alternate:") {
		t.Errorf("scan 2: reason = %q, want the softened tracked-slot alternate reason", p.Reason)
	}
	if p.TMDBID != 555 || p.SeasonNumber != 1 || p.EpisodeNumber != 1 {
		t.Errorf("scan 2: placement = tmdb %d S%02dE%02d, want tmdb 555 S01E01", p.TMDBID, p.SeasonNumber, p.EpisodeNumber)
	}

	// --- Apply the alternate, then scan 3: nothing is re-proposed. ---
	if gotID := apply(t, second, secondPath); gotID != episodeID {
		t.Errorf("the alternate folded into episode %d, want the existing slot %d", gotID, episodeID)
	}
	files, err := libStore.ListEpisodeFiles(ctx, episodeID)
	if err != nil {
		t.Fatalf("ListEpisodeFiles: %v", err)
	}
	var altPath string
	for _, f := range files {
		if !f.IsPrimary {
			altPath = f.FilePath
		}
	}
	if altPath == "" {
		t.Fatalf("no non-primary row after applying the alternate: %+v", files)
	}
	// §5.4's feed, asserted on its data source — this cannot be masked by
	// naming.MatchesSeriesSchema the way scan 3's outcome can.
	paths, err := libStore.AllEpisodeFilePaths(ctx)
	if err != nil {
		t.Fatalf("AllEpisodeFilePaths: %v", err)
	}
	found := false
	for _, path := range paths {
		if path == altPath {
			found = true
		}
	}
	if !found {
		t.Errorf("AllEpisodeFilePaths (the `known` feed) is missing the applied alternate %q, so a later Scan would re-propose it as an orphan", altPath)
	}

	third, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), prober)
	if err != nil {
		t.Fatalf("scan 3: %v", err)
	}
	if len(third) != 0 {
		t.Fatalf("scan 3: expected no proposals once both copies are applied, got %+v", third)
	}

	t.Run("known feed suppresses a NON-CONFORMANT applied alternate", func(t *testing.T) {
		// The scan-3 assertion above cannot fail while MatchesSeriesSchema
		// covers for the feed. This subtest removes that cover: the alternate
		// path is a loose, non-conformant file, so `known` is the ONLY thing
		// that can suppress it — and the control below proves it is otherwise
		// discovered.
		seed := func(t *testing.T, withAlternateRow bool) []proposals.Proposal {
			t.Helper()
			subRoot := t.TempDir()
			seasonDir := filepath.Join(subRoot, "Show Name (2020) [tmdbid-555]", "Season 01")
			if err := os.MkdirAll(seasonDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			primary := filepath.Join(seasonDir, "Show Name S01E01 Pilot.mkv")
			if err := os.WriteFile(primary, []byte("x"), 0o644); err != nil {
				t.Fatalf("seeding primary: %v", err)
			}
			loose := filepath.Join(subRoot, "loose-alt", "Show.Name.2020.S01E01.mkv")
			if err := os.MkdirAll(filepath.Dir(loose), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(loose, []byte("x"), 0o644); err != nil {
				t.Fatalf("seeding loose alternate: %v", err)
			}
			store := newTestLibraryStore(t)
			series, err := store.UpsertSeries(ctx, library.Series{
				TMDBID: 555, Title: "Show Name", Year: 2020, RootFolderPath: subRoot,
			})
			if err != nil {
				t.Fatalf("UpsertSeries: %v", err)
			}
			ep, err := store.UpsertEpisode(ctx, library.Episode{
				SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: primary,
			})
			if err != nil {
				t.Fatalf("UpsertEpisode: %v", err)
			}
			if withAlternateRow {
				if _, err := store.UpsertEpisodeFile(ctx, library.EpisodeFile{
					EpisodeID: ep.ID, FilePath: loose, IsPrimary: false,
				}); err != nil {
					t.Fatalf("UpsertEpisodeFile: %v", err)
				}
			}
			got, err := ScanLibrarySeries(ctx, sess, store, subRoot, naming.Jellyfin, DefaultMatchConfig(), prober)
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			return got
		}
		if got := seed(t, false); len(got) != 1 {
			t.Fatalf("control: expected the un-recorded loose file to be proposed, got %d: %+v", len(got), got)
		}
		if got := seed(t, true); len(got) != 0 {
			t.Fatalf("expected the recorded alternate to be `known` and skipped, got %+v", got)
		}
	})
}

// TestScanLibrarySeries_LooseParentDirParseStaysUnmatchedWhenCorroborationFails
// is the code-reviewer HIGH-finding regression test: once
// ParseEpisodeNumbersLoose lets an opaque hash basename past the parse gate,
// execution can reach identify.GuessTitle and bravePhase2Series. Unlike
// tryWebAuthoritySeries (gated on IsJunkRenameFilename/HasTitleTokenOverlap),
// bravePhase2Series has NO title-overlap guard — it accepts the first TMDB
// candidate whose season exists. Before this fix, the raw hash `name` was
// what got passed as the identity seed to both, which risked "AI hallucinates
// a title from 32 hex chars → bravePhase2Series accepts a plausible-looking
// but wrong match" landing on a confident Pending instead of correctly
// staying Unmatched — a direct hit on precision-over-recall.
//
// This proves two things: (1) containment — GuessTitle's AI prompt and
// bravePhase2Series' web-grounding query both carry the parent-dir-recovered
// title ("The Path"/"The.Path"), never the raw hash; (2) the end state is
// Unmatched when nothing genuinely corroborates, not a false-confidence
// Pending built from a hash-seeded guess.
func TestScanLibrarySeries_LooseParentDirParseStaysUnmatchedWhenCorroborationFails(t *testing.T) {
	root := t.TempDir()
	epDir := filepath.Join(root, "The Path", "Season 1", "The.Path.S01E02.2160p.WEB.h265-NiXON")
	if err := os.MkdirAll(epDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hexHash := "2ea4ad06efe20501d944b90f3a291e6f"
	videoPath := filepath.Join(epDir, hexHash+".mp4")
	if err := os.WriteFile(videoPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var aiPrompts []string
	var braveQueries []string

	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		prompt := ""
		if len(body.Messages) > 0 {
			prompt = body.Messages[len(body.Messages)-1].Content
		}
		aiPrompts = append(aiPrompts, prompt)
		// Always decline — isolates the test from any specific "confidently
		// guessed" outcome and forces bravePhase2Series's entryName fallback
		// (the second, more subtle half of the fix) to actually fire.
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": `{"title":null}`}})
	}))
	t.Cleanup(aiSrv.Close)

	braveSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		braveQueries = append(braveQueries, r.URL.Query().Get("q"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"web": map[string]any{"results": []map[string]any{}}})
	}))
	t.Cleanup(braveSrv.Close)

	tmdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// No TMDB match for anything — whatever gets queried, nothing
		// corroborates, so the file must end up Unmatched.
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(tmdbSrv.Close)

	sess := &mode.Session{
		Mode:         mode.Series,
		TMDB:         tmdb.New(tmdb.Config{BaseURL: tmdbSrv.URL, APIKey: "test-key"}, tmdbSrv.Client()),
		MainstreamAI: ollama.New(aiSrv.URL, "m", aiSrv.Client()),
		WebSearch:    websearch.Brave{Inner: bravesearch.New(braveSrv.URL, "k", braveSrv.Client())},
	}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Unmatched {
		t.Fatalf("expected Unmatched (nothing genuinely corroborates), got %v title=%q tmdb=%d reason=%q", p.Status, p.Title, p.TMDBID, p.Reason)
	}

	if len(aiPrompts) == 0 {
		t.Fatal("expected at least one GuessTitle call")
	}
	for _, prompt := range aiPrompts {
		if strings.Contains(prompt, hexHash) {
			t.Errorf("GuessTitle's AI prompt leaked the raw hash basename instead of the loose parent-dir title: %q", prompt)
		}
	}
	sawLooseTitle := false
	for _, prompt := range aiPrompts {
		if strings.Contains(prompt, "The Path") || strings.Contains(prompt, "The.Path") {
			sawLooseTitle = true
		}
	}
	if !sawLooseTitle {
		t.Errorf("expected at least one GuessTitle prompt to carry the parent-dir-recovered title, got prompts: %v", aiPrompts)
	}

	// bravePhase2Series runs before tryWebAuthoritySeries and issues exactly
	// one query, so braveQueries[0] is its query — the one this fix touches.
	// tryWebAuthoritySeries (braveQueries[1:], if any) deliberately still
	// seeds from the raw name — that call site was NOT part of this fix
	// (it has its own IsJunkRenameFilename/HasTitleTokenOverlap guard) and
	// is out of scope here.
	if len(braveQueries) == 0 {
		t.Fatal("expected at least one bravePhase2Series web-grounding query")
	}
	if strings.Contains(braveQueries[0], hexHash) {
		t.Errorf("bravePhase2Series's web-grounding query leaked the raw hash basename: %q", braveQueries[0])
	}
	if !strings.Contains(braveQueries[0], "The") {
		t.Errorf("expected bravePhase2Series's query to carry the parent-dir-recovered title, got %q", braveQueries[0])
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
	sourcePath := filepath.Join(sourceRoot, "Show.Name.2020.S01E01.mkv")
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
	// nil TMDB: title still comes from the existing library episode row
	// ("Pilot") so the Jellyfin destination includes the episode name.
	epID, changes, err := ApplyLibrarySeries(ctx, libStore, nil, nil, p, naming.Jellyfin, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if epID == 0 {
		t.Error("expected a nonzero episode id")
	}

	wantDest := filepath.Join(destRoot, "Show Name [tmdbid-555]", "Season 01", "Show Name S01E01 Pilot.mkv")
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

// TestApplyLibrarySeries_LogicalSplitCreatesOneEpisodeRowPerNumber proves
// the core of logical episode-splitting: a proposal carrying
// ExtraEpisodeNumbers relocates the source file exactly ONCE (a single
// range-shaped destination name), then creates one Episode row per bundled
// number — all pointing at that same relocated path — preserving each
// number's own pre-existing title/air-date metadata rather than blanking
// it (the fix the advisor flagged: the extra-episode loop must repeat the
// primary's metadata-preserve dance, not just upsert a bare FilePath).
func TestApplyLibrarySeries_LogicalSplitCreatesOneEpisodeRowPerNumber(t *testing.T) {
	base := t.TempDir()
	sourceRoot := filepath.Join(base, "incoming")
	destRoot := filepath.Join(base, "TV")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(sourceRoot, "Show.Name.2020.S01E01-E02.mkv")
	if err := os.WriteFile(sourcePath, []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: destRoot})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Episode 2 already has TMDB-seeded metadata (e.g. from a prior Scan
	// reporting it as missing) — this must survive, not get blanked.
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Part Two", AirDate: "2020-01-08",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555,
		SeasonNumber: 1, EpisodeNumber: 1, ExtraEpisodeNumbers: []int{2},
		SourcePath: sourcePath, RootFolderPath: destRoot,
	}
	epID, changes, err := ApplyLibrarySeries(ctx, libStore, nil, nil, p, naming.Jellyfin, "medium", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if epID == 0 {
		t.Error("expected a nonzero episode id for the primary episode")
	}

	// Primary has no library title → bare range name; ep2 metadata still preserved.
	wantDest := filepath.Join(destRoot, "Show Name [tmdbid-555]", "Season 01", "Show Name S01E01-E02.mkv")
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Errorf("expected the source file to be gone, stat returned: %v", err)
	}
	if data, err := os.ReadFile(wantDest); err != nil || string(data) != "fake video data" {
		t.Errorf("expected the file at %q, err=%v data=%q", wantDest, err, data)
	}

	ep1, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep1.FilePath != wantDest {
		t.Errorf("expected episode 1's file path to be the relocated path, got %q", ep1.FilePath)
	}

	ep2, err := libStore.GetEpisode(ctx, series.ID, 1, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep2.FilePath != wantDest {
		t.Errorf("expected episode 2 to point at the SAME relocated path as episode 1, got %q", ep2.FilePath)
	}
	if ep2.Title != "Part Two" || ep2.AirDate != "2020-01-08" {
		t.Errorf("expected episode 2's existing metadata to be preserved, not blanked, got %+v", ep2)
	}

	// Capture-at-write under logical episode-splitting: BOTH rows carry the
	// one physical file's FULL size, not a half share each. The storage
	// aggregation de-duplicates by file_path, so dividing here would
	// under-report the file; asserting both rows pins that contract.
	wantSize := int64(len("fake video data"))
	if ep1.Size != wantSize || ep2.Size != wantSize {
		t.Errorf("expected both split rows to carry the file's full size %d, got ep1=%d ep2=%d", wantSize, ep1.Size, ep2.Size)
	}
	if ep1.QualityTier != "medium" || ep2.QualityTier != "medium" {
		t.Errorf("expected both split rows to record the resolved tier, got ep1=%q ep2=%q", ep1.QualityTier, ep2.QualityTier)
	}

	// Only one relocate happened — exactly one Deleted+Created pair, not two.
	want := []mode.PathChange{{Path: sourcePath, Kind: mode.Deleted}, {Path: wantDest, Kind: mode.Created}}
	if len(changes) != 2 || changes[0] != want[0] || changes[1] != want[1] {
		t.Errorf("expected exactly one relocate's worth of changes %+v, got %+v", want, changes)
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
	epID, changes, err := ApplyLibrarySeries(ctx, libStore, nil, nil, p, naming.Jellyfin, "", nil)
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
	sourcePath := filepath.Join(base, "Show.Name.2020.S01E01.mkv")
	if err := os.WriteFile(sourcePath, []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555,
		SeasonNumber: 1, EpisodeNumber: 1, SourcePath: sourcePath, RootFolderPath: destRoot,
	}
	if _, _, err := ApplyLibrarySeries(ctx, libStore, nil, nil, p, naming.Legacy, "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantDest := filepath.Join(destRoot, "Show Name", "Season 01", "Show Name - S01E01.mkv")
	if _, err := os.ReadFile(wantDest); err != nil {
		t.Errorf("expected the file at %q (legacy shape, no year/tag), err=%v", wantDest, err)
	}
}

// TestApplyLibrarySeries_FetchesTMDBEpisodeTitleForJellyfinName proves Apply
// pulls SeasonDetails when the library has no episode title yet, so the
// destination is the full Jellyfin shape ("Show S01E01 Pilot.mkv") rather
// than a bare SxxExx name.
func TestApplyLibrarySeries_FetchesTMDBEpisodeTitleForJellyfinName(t *testing.T) {
	base := t.TempDir()
	destRoot := filepath.Join(base, "TV")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(base, "Show.Name.2020.S01E01.mkv")
	if err := os.WriteFile(sourcePath, []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555,
		SeasonNumber: 1, EpisodeNumber: 1, SourcePath: sourcePath, RootFolderPath: destRoot,
	}
	tmdbClient := fakeTMDBSeriesServer(t, nil, nil)
	epID, _, err := ApplyLibrarySeries(ctx, libStore, tmdbClient, nil, p, naming.Jellyfin, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if epID == 0 {
		t.Error("expected a nonzero episode id")
	}

	wantDest := filepath.Join(destRoot, "Show Name [tmdbid-555]", "Season 01", "Show Name S01E01 Pilot.mkv")
	if data, err := os.ReadFile(wantDest); err != nil || string(data) != "fake video data" {
		t.Errorf("expected the file at %q, err=%v data=%q", wantDest, err, data)
	}

	series, err := libStore.GetSeriesByTMDBID(ctx, 555)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.Title != "Pilot" || ep.AirDate != "2020-01-01" {
		t.Errorf("expected TMDB episode metadata recorded on the library row, got %+v", ep)
	}
}

// fakeTVDBEpisode is one item in a fake TheTVDB v4 episode-listing page. The
// JSON tags mirror internal/tvdb's own unexported episodeItem exactly — this
// is a different package, so the wire shape has to be restated rather than
// shared, the same way fakeTMDBSeriesServerEx above restates TMDB's.
type fakeTVDBEpisode struct {
	ID           int    `json:"id"`
	SeriesID     int    `json:"seriesId"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
	SeasonNumber int    `json:"seasonNumber"`
	Aired        string `json:"aired"`
	Runtime      int    `json:"runtime"`
}

// fakeTVDBEpisodesServer stands in for TheTVDB v4's POST /v4/login and
// GET /v4/series/{id}/episodes/{type}. The whole catalog is served on page 0
// and every later page answers empty, which is SeriesEpisodes' exhaustion
// signal — so a fixture spanning several seasons arrives in ONE call, exactly
// as the live API returns it, which is what makes the season-filter assertion
// in the main case below meaningful.
func fakeTVDBEpisodesServer(t *testing.T, catalog []fakeTVDBEpisode) *tvdb.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"token":"tok"}}`)
	})
	mux.HandleFunc("GET /v4/series/{id}/episodes/{type}", func(w http.ResponseWriter, r *http.Request) {
		items := []fakeTVDBEpisode{}
		if page, _ := strconv.Atoi(r.URL.Query().Get("page")); page == 0 {
			items = catalog
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"episodes": items},
		}); err != nil {
			t.Errorf("encoding fixture page: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return tvdb.New(tvdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

// failingTVDBEpisodesServer logs in successfully and then answers the episode
// listing with a 500. The successful login is the point: SeriesEpisodes calls
// ensureToken first, so a server with no login route would fail BEFORE the
// listing was ever requested and would prove nothing about a SeriesEpisodes
// failure specifically.
func failingTVDBEpisodesServer(t *testing.T) *tvdb.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"token":"tok"}}`)
	})
	mux.HandleFunc("GET /v4/series/{id}/episodes/{type}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return tvdb.New(tvdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

// countingTVDBServer answers NOTHING — deliberately, including no
// POST /v4/login. The gate short-circuits before SeriesEpisodes is called at
// all, so ensureToken never runs and no login route is needed; adding one
// "just in case" would make P3's zero-request assertion vacuous by letting a
// stray call succeed through the very handler meant to catch it. The counter
// is atomic and asserted from the test goroutine rather than t.Fatalf'd inside
// the handler, since FailNow from a non-test goroutine is not permitted.
func countingTVDBServer(t *testing.T) (*tvdb.Client, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "this server must never be reached", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	return tvdb.New(tvdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client()), &hits
}

// countingTMDBServer is countingTVDBServer's TMDB counterpart, used by the
// main case to prove the recovered title came from TheTVDB and not from the
// TMDB branch.
func countingTMDBServer(t *testing.T) (*tmdb.Client, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "this server must never be reached", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	return tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client()), &hits
}

// anthologyTVDBCatalog is the shared fixture for the cases below: Laurel &
// Hardy's S03E01 "Duck Soup", plus a DECOY at S08E01 carrying the SAME episode
// number in a different season. SeriesEpisodes returns the whole show in one
// call, so an implementation that indexed by Number alone would resolve the
// decoy's title here — that decoy is what turns "season-filtered" from an
// asserted property into a proven one.
//
// THE DECOY MUST STAY LAST, and this is the whole reason the ordering is
// pinned rather than incidental: the fill builds a map keyed on Number, so a
// season-blind implementation is last-write-wins. With the decoy listed FIRST
// the real S03E01 would overwrite it and the test would pass against the very
// bug it exists to catch — verified by mutation, not assumed. Listing it last
// makes a season-blind fill resolve "WRONG SEASON DECOY" and fail loudly.
// (This says nothing about the production code's own ordering-independence,
// which the season filter is what guarantees.)
func anthologyTVDBCatalog() []fakeTVDBEpisode {
	return []fakeTVDBEpisode{
		{ID: 1, SeriesID: 73910, Name: "Duck Soup", Number: 1, SeasonNumber: 3, Aired: "1927-03-13"},
		{ID: 2, SeriesID: 73910, Name: "The Music Box", Number: 3, SeasonNumber: 8, Aired: "1932-04-16"},
		{ID: 3, SeriesID: 73910, Name: "WRONG SEASON DECOY", Number: 1, SeasonNumber: 8, Aired: "1932-01-01"},
	}
}

// TestApplyLibrarySeries_FetchesTVDBEpisodeTitleForAnthologyMatch is the
// complementary twin of TestApplyLibrarySeries_FetchesTMDBEpisodeTitleForJellyfinName
// above: same shape, opposite branch. An anthology proposal carries a
// synthetic NEGATIVE tmdb id, which gates the TMDB fetch off entirely, so
// TheTVDB's catalog is the only place its episode title can come from. Both
// consumers are asserted — the destination file name AND library_episodes.title
// — because the second is the half that made the original gap invisible (the
// recovered title used to be readable only by querying the proposals table's
// reason string).
func TestApplyLibrarySeries_FetchesTVDBEpisodeTitleForAnthologyMatch(t *testing.T) {
	base := t.TempDir()
	destRoot := filepath.Join(base, "TV")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(base, "laurel.hardy.duck.soup.mkv")
	if err := os.WriteFile(sourcePath, []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	syntheticID := anthologyTMDBID(73910)
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Laurel & Hardy", Year: 1921,
		TMDBID: syntheticID, TVDBID: 73910,
		SeasonNumber: 3, EpisodeNumber: 1, SourcePath: sourcePath, RootFolderPath: destRoot,
	}
	tmdbClient, tmdbHits := countingTMDBServer(t)
	tvdbClient := fakeTVDBEpisodesServer(t, anthologyTVDBCatalog())

	epID, _, err := ApplyLibrarySeries(ctx, libStore, tmdbClient, tvdbClient, p, naming.Jellyfin, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if epID == 0 {
		t.Error("expected a nonzero episode id")
	}
	if got := tmdbHits.Load(); got != 0 {
		t.Errorf("expected zero TMDB requests for a synthetic negative id, got %d", got)
	}

	// No [tmdbid-N] tag: naming.SeriesFolderName omits it for a non-positive id.
	wantDest := filepath.Join(destRoot, "Laurel & Hardy (1921)", "Season 03", "Laurel & Hardy S03E01 Duck Soup.mkv")
	if data, err := os.ReadFile(wantDest); err != nil || string(data) != "fake video data" {
		t.Errorf("expected the file at %q, err=%v data=%q", wantDest, err, data)
	}

	series, err := libStore.GetSeriesByTMDBID(ctx, syntheticID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// §6.3a: the Apply path must persist TVDBID, or the anthology pass'
	// established-pin shortcut has nothing to key on for the next Scan.
	if series.TVDBID != 73910 {
		t.Errorf("expected tvdb id 73910 persisted on the series row, got %d", series.TVDBID)
	}
	ep, err := libStore.GetEpisode(ctx, series.ID, 3, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.Title != "Duck Soup" {
		t.Errorf("expected library_episodes.title %q, got %q", "Duck Soup", ep.Title)
	}
	if ep.AirDate != "1927-03-13" {
		t.Errorf("expected air date %q threaded from TheTVDB, got %q", "1927-03-13", ep.AirDate)
	}
}

// TestApplyLibrarySeries_NilTVDBClientDegradesToBareEpisodeName is P1: no
// TVDB connection is configured, so the branch is inert and Apply degrades to
// exactly today's behaviour — a bare SxxExx name and an empty library title —
// rather than erroring.
func TestApplyLibrarySeries_NilTVDBClientDegradesToBareEpisodeName(t *testing.T) {
	base := t.TempDir()
	destRoot := filepath.Join(base, "TV")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(base, "laurel.hardy.duck.soup.mkv")
	if err := os.WriteFile(sourcePath, []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	syntheticID := anthologyTMDBID(73910)
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Laurel & Hardy", Year: 1921,
		TMDBID: syntheticID, TVDBID: 73910,
		SeasonNumber: 3, EpisodeNumber: 1, SourcePath: sourcePath, RootFolderPath: destRoot,
	}
	if _, _, err := ApplyLibrarySeries(ctx, libStore, nil, nil, p, naming.Jellyfin, "", nil); err != nil {
		t.Fatalf("expected a nil tvdbClient to be inert, got error: %v", err)
	}

	wantDest := filepath.Join(destRoot, "Laurel & Hardy (1921)", "Season 03", "Laurel & Hardy S03E01.mkv")
	if _, err := os.Stat(wantDest); err != nil {
		t.Errorf("expected the file relocated to %q, got %v", wantDest, err)
	}
	series, err := libStore.GetSeriesByTMDBID(ctx, syntheticID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep, err := libStore.GetEpisode(ctx, series.ID, 3, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.Title != "" {
		t.Errorf("expected an empty library title with no TVDB client, got %q", ep.Title)
	}
}

// TestApplyLibrarySeries_TVDBFetchFailureStillApplies is P2, and it is the
// single most likely rule to be got wrong: internal/tvdb's SeriesEpisodes is
// deliberately fail-CLOSED, but this call site is deliberately fail-SOFT. The
// season and episode are already decided and persisted on the proposal by the
// time Apply runs, and the catalog is consulted only for a display title — so
// a TheTVDB blip must never fail the Apply of a file whose placement is
// already correct. An executor mirroring SeriesEpisodes' own contract here
// would return the error and break exactly this case.
func TestApplyLibrarySeries_TVDBFetchFailureStillApplies(t *testing.T) {
	base := t.TempDir()
	destRoot := filepath.Join(base, "TV")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(base, "laurel.hardy.duck.soup.mkv")
	if err := os.WriteFile(sourcePath, []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	syntheticID := anthologyTMDBID(73910)
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Laurel & Hardy", Year: 1921,
		TMDBID: syntheticID, TVDBID: 73910,
		SeasonNumber: 3, EpisodeNumber: 1, SourcePath: sourcePath, RootFolderPath: destRoot,
	}
	if _, _, err := ApplyLibrarySeries(ctx, libStore, nil, failingTVDBEpisodesServer(t), p, naming.Jellyfin, "", nil); err != nil {
		t.Fatalf("expected a TVDB 500 to be swallowed, got error: %v", err)
	}

	wantDest := filepath.Join(destRoot, "Laurel & Hardy (1921)", "Season 03", "Laurel & Hardy S03E01.mkv")
	if _, err := os.Stat(wantDest); err != nil {
		t.Errorf("expected the file relocated despite the TVDB failure, got %v", err)
	}
	series, err := libStore.GetSeriesByTMDBID(ctx, syntheticID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep, err := libStore.GetEpisode(ctx, series.ID, 3, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.Title != "" {
		t.Errorf("expected an empty library title after a failed fetch, got %q", ep.Title)
	}
}

// TestApplyLibrarySeries_OrdinaryProposalMakesNoTVDBRequest is P3: an ordinary
// Series proposal must never touch TheTVDB. The gate is the exact complement of
// the TMDB branch's precondition, so proving zero requests here proves no
// existing Apply path changed behaviour.
//
// Two sub-cases, because the plain (TMDBID > 0, TVDBID == 0) proposal the plan
// names pins only HALF the gate: with TVDBID == 0, SeriesEpisodes rejects the
// id locally and issues no HTTP request at all, so that case would still see
// zero requests even if the p.TMDBID <= 0 condition were deleted outright. The
// second sub-case carries BOTH a positive TMDB id and a positive TVDB id — a
// shape only the p.TMDBID <= 0 half can exclude — so together they pin the
// whole gate rather than one conjunct of it.
func TestApplyLibrarySeries_OrdinaryProposalMakesNoTVDBRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tvdbID int
	}{
		{"no tvdb id (the ordinary shape)", 0},
		{"positive tvdb id alongside a positive tmdb id", 999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			destRoot := filepath.Join(base, "TV")
			if err := os.MkdirAll(destRoot, 0o755); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			sourcePath := filepath.Join(base, "Show.Name.2020.S01E01.mkv")
			if err := os.WriteFile(sourcePath, []byte("fake video data"), 0o644); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			libStore := newTestLibraryStore(t)
			ctx := context.Background()
			p := proposals.Proposal{
				ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555, TVDBID: tc.tvdbID,
				SeasonNumber: 1, EpisodeNumber: 1, SourcePath: sourcePath, RootFolderPath: destRoot,
			}
			tvdbClient, tvdbHits := countingTVDBServer(t)
			if _, _, err := ApplyLibrarySeries(ctx, libStore, nil, tvdbClient, p, naming.Jellyfin, "", nil); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := tvdbHits.Load(); got != 0 {
				t.Errorf("expected zero TVDB requests for an ordinary Series proposal, got %d", got)
			}

			wantDest := filepath.Join(destRoot, "Show Name [tmdbid-555]", "Season 01", "Show Name S01E01.mkv")
			if _, err := os.Stat(wantDest); err != nil {
				t.Errorf("expected the file relocated to %q, got %v", wantDest, err)
			}
		})
	}
}

// TestApplyLibrarySeries_LibraryEpisodeTitleWinsOverTVDB pins the "only if
// still empty" shape against the never-blank invariant the toUpsert loop
// documents: an operator-edited (or previously seeded) library title must not
// be silently overwritten just because this Apply happened to fetch a catalog.
// Both consumers are asserted — the library row AND the file name, since
// resolveEpisodeMeta feeds fileTitle too.
func TestApplyLibrarySeries_LibraryEpisodeTitleWinsOverTVDB(t *testing.T) {
	base := t.TempDir()
	destRoot := filepath.Join(base, "TV")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(base, "laurel.hardy.duck.soup.mkv")
	if err := os.WriteFile(sourcePath, []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	syntheticID := anthologyTMDBID(73910)

	// Seed the slot the way Scan records a known-but-missing episode: a title
	// and no file path, so RelocateEpisodeRange's self-collision guard sees
	// exactly what it would in the un-seeded case.
	seeded, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: syntheticID, TVDBID: 73910, Title: "Laurel & Hardy", Year: 1921, RootFolderPath: destRoot,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: seeded.ID, SeasonNumber: 3, EpisodeNumber: 1, Title: "Operator Edited",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Laurel & Hardy", Year: 1921,
		TMDBID: syntheticID, TVDBID: 73910,
		SeasonNumber: 3, EpisodeNumber: 1, SourcePath: sourcePath, RootFolderPath: destRoot,
	}
	tvdbClient := fakeTVDBEpisodesServer(t, anthologyTVDBCatalog())
	if _, _, err := ApplyLibrarySeries(ctx, libStore, nil, tvdbClient, p, naming.Jellyfin, "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ep, err := libStore.GetEpisode(ctx, seeded.ID, 3, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.Title != "Operator Edited" {
		t.Errorf("expected the existing library title to win over TheTVDB's, got %q", ep.Title)
	}
	// The "only if still empty" rule is PER FIELD, so the same call that must
	// leave the seeded title alone must still fill the air date the seed left
	// blank. Asserting only the protected field would pass against a fill that
	// had been disabled outright.
	if ep.AirDate != "1927-03-13" {
		t.Errorf("expected the unseeded air date to still fill from TheTVDB, got %q", ep.AirDate)
	}
	wantDest := filepath.Join(destRoot, "Laurel & Hardy (1921)", "Season 03", "Laurel & Hardy S03E01 Operator Edited.mkv")
	if _, err := os.Stat(wantDest); err != nil {
		t.Errorf("expected the destination name to use the library title, %q missing: %v", wantDest, err)
	}
}

// TestScanLibrarySeries_RejectsTitleCollisionAndPicksCorrectShow is the
// headline test for autopilot-impl-title-collision-fix.md — proves the walk
// continues past a gate-rejected candidate instead of halting (§1.2): if the
// gate were placed inside acceptSeries, this would return Unmatched instead
// of finding TMDB 65227 at candidate position 2. Both candidates share the
// same first_air_date on purpose so the year signal can't be what
// discriminates them — only the folder-pinned TMDB id guard can.
func TestScanLibrarySeries_RejectsTitleCollisionAndPicksCorrectShow(t *testing.T) {
	root := t.TempDir()
	trackedDir := filepath.Join(root, "The Path (2016) [tmdbid-65227]", "Season 01")
	if err := os.MkdirAll(trackedDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	trackedPath := filepath.Join(trackedDir, "The Path S01E01.mkv")
	if err := os.WriteFile(trackedPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	newDir := filepath.Join(root, "The Path", "Season 1")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	newFile := filepath.Join(newDir, "The.Path.2016.S01E02.mkv")
	if err := os.WriteFile(newFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"The Path": `{"results":[{"id":900,"name":"The Oath","first_air_date":"2016-01-01"},{"id":65227,"name":"The Path","first_air_date":"2016-01-01"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 65227, Title: "The Path", RootFolderPath: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedPath,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 proposal (the already-tracked file must stay masked), got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.TMDBID != 65227 {
		t.Fatalf("expected Pending under the folder-pinned show TMDB 65227, got status=%v tmdbID=%d reason=%q", p.Status, p.TMDBID, p.Reason)
	}
}

// TestScanLibrarySeries_UnmatchedWhenOnlyCandidateIsWrongShowInPinnedFolder
// is the RejectsTitleCollision fixture's sibling: when the ONLY TMDB
// candidate is the wrong show, the file must go Unmatched with no TMDB id
// recorded — never fall back to accepting the wrong show (mirrors the
// existing MarksUnmatchedWhenTMDBResultIsWeakMatch assertion shape).
func TestScanLibrarySeries_UnmatchedWhenOnlyCandidateIsWrongShowInPinnedFolder(t *testing.T) {
	root := t.TempDir()
	trackedDir := filepath.Join(root, "The Path (2016) [tmdbid-65227]", "Season 01")
	if err := os.MkdirAll(trackedDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	trackedPath := filepath.Join(trackedDir, "The Path S01E01.mkv")
	if err := os.WriteFile(trackedPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	newDir := filepath.Join(root, "The Path", "Season 1")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	newFile := filepath.Join(newDir, "The.Path.2016.S01E02.mkv")
	if err := os.WriteFile(newFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"The Path": `{"results":[{"id":900,"name":"The Secret Path","first_air_date":"2016-01-01"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 65227, Title: "The Path", RootFolderPath: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedPath,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Unmatched {
		t.Fatalf("expected Unmatched when the only candidate is the wrong show, got %+v", p)
	}
	if p.TMDBID != 0 {
		t.Errorf("expected no TMDB id to be assigned on a gate-rejected candidate, got %d", p.TMDBID)
	}
	// §4.3: a gate rejection must report the DISTINCT diagnostic reason, not
	// unmatchedAfterWebAuthority's generic "no catalog/web title+year" —
	// without this assertion, a regression that silently drops the gate's
	// rejected-candidate count (or bypasses gateRejectedReason() at any of
	// this function's Unmatched exit points) would go undetected even
	// though Status/TMDBID still look correct.
	wantReasonPrefix := "rejected 1 candidate(s) that did not match this folder's tracked show"
	if !strings.HasPrefix(p.Reason, wantReasonPrefix) {
		t.Errorf("expected the distinct gate-rejection reason (prefix %q), got %q", wantReasonPrefix, p.Reason)
	}
}

// TestScanLibrarySeries_UnpinnedFolderStillMatches is a regression guard: a
// brand-new show folder with no tracked series at all (so folderIDs has no
// entry for it — pinnedTMDBID resolves to 0) must still produce its normal
// Pending proposal. Without this, a future over-tightening of the §3.4 rule
// 1 fail-open condition would go unnoticed.
func TestScanLibrarySeries_UnpinnedFolderStillMatches(t *testing.T) {
	root := t.TempDir()
	newDir := filepath.Join(root, "Show Name", "Season 1")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	newFile := filepath.Join(newDir, "Show.Name.2020.S01E01.mkv")
	if err := os.WriteFile(newFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name","first_air_date":"2020-01-01"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)

	got, err := ScanLibrarySeries(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending || got[0].TMDBID != 555 {
		t.Fatalf("expected an unpinned folder to match normally, got %+v", got)
	}
}

// TestScanLibrarySeries_AmbiguousFolderKeyImposesNoConstraint proves two
// tracked series normalizing to the same key (via the title feed alone —
// neither has a tracked episode file) leave that key's guard inert (§3.4
// rule 2): a brand-new third show at that same folder name must still match
// normally, not be rejected by the ambiguous pin.
func TestScanLibrarySeries_AmbiguousFolderKeyImposesNoConstraint(t *testing.T) {
	root := t.TempDir()
	newDir := filepath.Join(root, "Monster", "Season 1")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	newFile := filepath.Join(newDir, "Monster.2020.S01E01.mkv")
	if err := os.WriteFile(newFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, map[string]string{
		"Monster": `{"results":[{"id":555,"name":"Monster","first_air_date":"2020-01-01"}]}`,
	}, nil)}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	// Two DIFFERENT tracked shows both titled "Monster" — same normalized
	// key, different TMDB ids — makes folderIDs["monster"] sticky-ambiguous
	// (0) via the title feed alone, with zero tracked episode files.
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 111, Title: "Monster", RootFolderPath: root}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 222, Title: "Monster", RootFolderPath: root}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending || got[0].TMDBID != 555 {
		t.Fatalf("expected the ambiguous key to impose no constraint on an unrelated third show, got %+v", got)
	}
}

// TestScanLibrarySeries_LiveTMDBTitleCollisionGuard is
// autopilot-impl-title-collision-fix.md §7.1 Option C: the only test in this
// suite that exercises the fix against TMDB's REAL /search/tv response for
// "The Path", rather than a fake server whose fixture ASSUMES the
// wrong-show-ahead-of-right-show ordering that caused the original bug —
// nothing else here confirms live TMDB search for "The Path" actually
// returns a collision candidate ahead of or alongside TMDB 65227.
//
// Gated behind SAKMS_TEST_TMDB_API_KEY and testing.Short(), the same shape
// this package's SAKMS_TEST_DATABASE_URL-gated tests already use to skip
// when their dependency is absent, so `go test ./...` never depends on
// network access or a real TMDB key in an environment without one. No
// parser fix (this is base commit 57c5515, not e40bbab — see the plan's
// §7.1 Option B, deliberately not implemented here), no deploy, no live
// production DB write: libStore is the same ephemeral Postgres
// newTestLibraryStore(t) every other test in this file uses.
func TestScanLibrarySeries_LiveTMDBTitleCollisionGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live TMDB network test in short mode")
	}
	apiKey := os.Getenv("SAKMS_TEST_TMDB_API_KEY")
	if apiKey == "" {
		t.Skip("SAKMS_TEST_TMDB_API_KEY not set; skipping live TMDB verification (see autopilot-impl-title-collision-fix.md §7.1 Option C)")
	}

	root := t.TempDir()
	dir := filepath.Join(root, "The Path", "Season 1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "The.Path.2016.S01E02.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: tmdb.New(tmdb.Config{APIKey: apiKey}, http.DefaultClient)}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	// Title feed alone pins the folder — no tracked episode file needed,
	// exactly the plan's §7.1 Option C fixture ("a seeded
	// library.Series{TMDBID: 65227, Title: "The Path"}").
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 65227, Title: "The Path", RootFolderPath: root}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("expected Pending against live TMDB, got status=%v reason=%q", p.Status, p.Reason)
	}
	if p.TMDBID != 65227 {
		t.Errorf("expected the folder-pinned show TMDB 65227 against live TMDB search results, got tmdbID=%d title=%q", p.TMDBID, p.Title)
	}
}

func TestApplyLibrarySeries_RejectsNonPendingProposal(t *testing.T) {
	libStore := newTestLibraryStore(t)
	for _, status := range []proposals.Status{proposals.Applied, proposals.Dismissed, proposals.Unmatched} {
		if _, _, err := ApplyLibrarySeries(context.Background(), libStore, nil, nil, proposals.Proposal{Status: status}, naming.Jellyfin, "", nil); err == nil {
			t.Errorf("expected ApplyLibrarySeries to refuse a %q proposal", status)
		}
	}
}

// ---------------------------------------------------------------------------
// Episode-title matching (autopilot-impl-episode-title-matching.md)
// ---------------------------------------------------------------------------

const (
	redSkeltonShowID    = 10534
	redSkeltonShowTitle = "The Red Skelton Show"
)

// redSkeltonEpisodeFixture returns this suite's per-season episode fixture
// for TMDB id 10534 ("The Red Skelton Show").
//
// VERIFICATION STATUS, stated plainly per this project's honesty-about-
// unverified-assumptions convention (mirroring internal/tmdb/client.go's own
// SeasonDetails comment): no SAKMS_TEST_TMDB_API_KEY was available in this
// environment (verified: unset) and this repo carries no testdata/ fixture
// directory for TMDB 10534, so NONE of the episode titles below were
// transcribed from a live TMDB response — they are CONSTRUCTED placeholders
// for this test suite's shape only, chosen to plausibly resemble real
// 1950s-Red-Skelton-episode titling, and must not be cited as evidence about
// 10534's real catalogue. "More Funny Faces" is the one exception: it is not
// claimed as real TMDB data either, but it IS the same string
// web_authority_test.go already uses as its HasTitleTokenOverlap fixture
// (the spec's own "motivating shape" example), reused here for continuity
// rather than independently invented. Wade accepted this substitution (see
// the plan's §5.3) in lieu of a live capture.
// TestScanLibrarySeries_LiveTMDBEpisodeTitleMatch below is the one test in
// this file that verifies against TMDB's real data, when a key is available.
func redSkeltonEpisodeFixture() map[int][]tmdb.SeasonEpisode {
	return map[int][]tmdb.SeasonEpisode{
		1: {
			{EpisodeNumber: 1, Name: "Cousin Fanny's Visit", AirDate: "1951-09-30"},
			{EpisodeNumber: 2, Name: "The Bank Robbery", AirDate: "1951-10-07"},
		},
		2: {
			{EpisodeNumber: 1, Name: "San Fernando Red", AirDate: "1952-09-28"},
			{EpisodeNumber: 2, Name: "More Funny Faces", AirDate: "1952-10-05"},
		},
	}
}

// fakeTMDBEpisodeTitleServer serves /tv/{id} (with a real seasons[] array),
// /tv/{id}/season/{n} (with per-season episode lists) and
// /tv/{id}/aggregate_credits (the accept path's enrichment call) for the
// episode-title matching tests. Sibling of fakeTMDBSeriesServer/
// fakeTMDBSeriesServerEx above, deliberately not an extension of either —
// this feature's tests hit identical /tv/10534/season/N paths with
// deliberately DIFFERENT bodies across tests (unique-match vs. ambiguous vs.
// no-match), which those two helpers' fixed one-episode-"Pilot"-for-every-
// season body cannot express; see the plan's §5.1(b).
//
// failSeason, when >= 0, makes that ONE season 404, exercising the
// fail-closed rule (§1.2). -1 means "no season fails."
//
// reqs, when non-nil, is a caller-owned counter incremented on EVERY request
// this server receives — TestScanLibrarySeries_EpisodeTitleMatch_UnpinnedFolderInert
// asserts it stays 0, which is the only real proof that the pinnedShow
// precondition short-circuits before any network call; without it that
// assertion would be aspirational.
func fakeTMDBEpisodeTitleServer(
	t *testing.T,
	showID int, showName string,
	seasons map[int][]tmdb.SeasonEpisode,
	failSeason int,
	reqs *int,
) *tmdb.Client {
	t.Helper()
	tvPath := fmt.Sprintf("/tv/%d", showID)
	seasonPathPrefix := tvPath + "/season/"
	creditsPath := tvPath + "/aggregate_credits"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reqs != nil {
			*reqs++
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == tvPath:
			seasonNums := make([]int, 0, len(seasons))
			for n := range seasons {
				seasonNums = append(seasonNums, n)
			}
			sort.Ints(seasonNums)
			type seasonJSON struct {
				SeasonNumber int `json:"season_number"`
			}
			body := struct {
				ID      int          `json:"id"`
				Name    string       `json:"name"`
				Seasons []seasonJSON `json:"seasons"`
			}{ID: showID, Name: showName}
			for _, n := range seasonNums {
				body.Seasons = append(body.Seasons, seasonJSON{SeasonNumber: n})
			}
			_ = json.NewEncoder(w).Encode(body)
		case r.URL.Path == creditsPath:
			w.Write([]byte(`{"cast":[]}`))
		case strings.HasPrefix(r.URL.Path, seasonPathPrefix):
			var season int
			if _, err := fmt.Sscanf(r.URL.Path, seasonPathPrefix+"%d", &season); err != nil {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			if season == failSeason {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			type episodeJSON struct {
				EpisodeNumber int    `json:"episode_number"`
				Name          string `json:"name"`
				AirDate       string `json:"air_date"`
			}
			body := struct {
				Episodes []episodeJSON `json:"episodes"`
			}{}
			for _, ep := range seasons[season] {
				body.Episodes = append(body.Episodes, episodeJSON{EpisodeNumber: ep.EpisodeNumber, Name: ep.Name, AirDate: ep.AirDate})
			}
			_ = json.NewEncoder(w).Encode(body)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

// TestScanLibrarySeries_EpisodeTitleMatch_UniqueMatchAccepted is plan §5.2
// case A: a file with no SxxExx anywhere in its basename or parent directory,
// under a folder already pinned to a tracked show, whose recovered title
// matches exactly one episode across every season — must land Pending on
// that exact slot.
func TestScanLibrarySeries_EpisodeTitleMatch_UniqueMatchAccepted(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, redSkeltonShowTitle)
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "Red Skelton More Funny Faces.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	client := fakeTMDBEpisodeTitleServer(t, redSkeltonShowID, redSkeltonShowTitle, redSkeltonEpisodeFixture(), -1, nil)
	sess := &mode.Session{Mode: mode.Series, TMDB: client}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: redSkeltonShowID, Title: redSkeltonShowTitle, Year: 1951, RootFolderPath: root}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("expected Pending, got status=%v reason=%q", p.Status, p.Reason)
	}
	if p.TMDBID != redSkeltonShowID || p.SeasonNumber != 2 || p.EpisodeNumber != 2 {
		t.Errorf("expected TMDB %d S02E02, got tmdbID=%d S%02dE%02d", redSkeltonShowID, p.TMDBID, p.SeasonNumber, p.EpisodeNumber)
	}
	if !strings.HasPrefix(p.Reason, episodeTitleMatchReasonPrefix) {
		t.Errorf("expected reason to carry %q prefix, got %q", episodeTitleMatchReasonPrefix, p.Reason)
	}
}

// TestScanLibrarySeries_EpisodeTitleMatch_AmbiguousRejected is case B: the
// same episode title occupying a slot in TWO different seasons must
// Unmatch, naming both slots, with no TMDB id assigned. The duplicate is
// CONSTRUCTED for this test (see redSkeltonEpisodeFixture's VERIFICATION
// STATUS) — a claim about the matcher, not a claim that TMDB's real 10534
// catalogue contains this duplicate.
func TestScanLibrarySeries_EpisodeTitleMatch_AmbiguousRejected(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, redSkeltonShowTitle)
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "Red Skelton More Funny Faces.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	seasons := map[int][]tmdb.SeasonEpisode{
		1: {{EpisodeNumber: 3, Name: "More Funny Faces", AirDate: "1951-10-14"}},
		2: {{EpisodeNumber: 2, Name: "More Funny Faces", AirDate: "1952-10-05"}},
	}
	client := fakeTMDBEpisodeTitleServer(t, redSkeltonShowID, redSkeltonShowTitle, seasons, -1, nil)
	sess := &mode.Session{Mode: mode.Series, TMDB: client}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: redSkeltonShowID, Title: redSkeltonShowTitle, Year: 1951, RootFolderPath: root}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Unmatched {
		t.Fatalf("expected Unmatched on an ambiguous title match, got %+v", p)
	}
	if p.TMDBID != 0 {
		t.Errorf("expected no TMDB id on an ambiguous match, got %d", p.TMDBID)
	}
	if !strings.Contains(p.Reason, "S01E03") || !strings.Contains(p.Reason, "S02E02") {
		t.Errorf("expected the ambiguous reason to name both slots, got %q", p.Reason)
	}
}

// TestScanLibrarySeries_EpisodeTitleMatch_NoMatchFallsThroughUnchanged is
// case C: a recovered title that appears in no season must leave the file on
// the SAME parse-failure Unmatched reason the existing "could not determine
// season/episode" path already produces — continuity for anything already
// grepping that reason string.
func TestScanLibrarySeries_EpisodeTitleMatch_NoMatchFallsThroughUnchanged(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, redSkeltonShowTitle)
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	name := "Red Skelton Totally Unrelated Story.mp4"
	if err := os.WriteFile(filepath.Join(showDir, name), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	client := fakeTMDBEpisodeTitleServer(t, redSkeltonShowID, redSkeltonShowTitle, redSkeltonEpisodeFixture(), -1, nil)
	sess := &mode.Session{Mode: mode.Series, TMDB: client}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: redSkeltonShowID, Title: redSkeltonShowTitle, Year: 1951, RootFolderPath: root}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Unmatched {
		t.Fatalf("expected Unmatched, got %+v", p)
	}
	wantReason := fmt.Sprintf("could not determine season/episode from %q", name)
	if p.Reason != wantReason {
		t.Errorf("expected the unchanged parse-failure reason %q, got %q", wantReason, p.Reason)
	}
}

// TestScanLibrarySeries_EpisodeTitleMatch_UnpinnedFolderInert is case D: a
// folder with no tracked library.Series row at all must never invoke this
// feature — proven, not just asserted by outcome, via the reqs counter
// staying at 0 (zero TMDB calls of ANY kind for this file).
func TestScanLibrarySeries_EpisodeTitleMatch_UnpinnedFolderInert(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, redSkeltonShowTitle)
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "Red Skelton More Funny Faces.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reqs := 0
	client := fakeTMDBEpisodeTitleServer(t, redSkeltonShowID, redSkeltonShowTitle, redSkeltonEpisodeFixture(), -1, &reqs)
	sess := &mode.Session{Mode: mode.Series, TMDB: client}
	libStore := newTestLibraryStore(t) // no tracked library.Series row — folder is unpinned

	got, err := ScanLibrarySeries(context.Background(), sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected Unmatched on an unpinned folder, got %+v", got)
	}
	if reqs != 0 {
		t.Errorf("expected the pinnedShow precondition to short-circuit before any TMDB call, got %d request(s)", reqs)
	}
}

// TestScanLibrarySeries_EpisodeTitleMatch_GateCompositionAcceptsZeroTokenOverlap
// is case E, pinning §3.2/§3.3: the recovered file title shares NO token
// with the show title, which would make gate.allowsTitle (Check A) reject if
// it were ever consulted directly on this path instead of composed via
// gate.allows' pinned-ID short-circuit. A regression that swaps in
// allowsTitle here would fail this test loudly.
func TestScanLibrarySeries_EpisodeTitleMatch_GateCompositionAcceptsZeroTokenOverlap(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, redSkeltonShowTitle)
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Deliberately no "Red Skelton" prefix: titleTokens("More Funny Faces")
	// shares zero tokens with titleTokens("The Red Skelton Show").
	if err := os.WriteFile(filepath.Join(showDir, "More Funny Faces.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	client := fakeTMDBEpisodeTitleServer(t, redSkeltonShowID, redSkeltonShowTitle, redSkeltonEpisodeFixture(), -1, nil)
	sess := &mode.Session{Mode: mode.Series, TMDB: client}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: redSkeltonShowID, Title: redSkeltonShowTitle, Year: 1951, RootFolderPath: root}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending || got[0].TMDBID != redSkeltonShowID {
		t.Fatalf("expected Pending despite zero title/show token overlap, got %+v", got)
	}
}

// TestScanLibrarySeries_EpisodeTitleMatchDuplicateIsPendingAlternate covers
// SITE 5 (tryEpisodeTitleMatchSeries) reached DIRECTLY from the Scan — not
// through aiEpisodeMode1, which wraps the reason (see
// TestAIRecovery_Mode1TrackedSlotDeclines for that shape).
//
// This is the REWRITE IN PLACE of the retired
// ..._EpisodeTitleMatch_AlreadyTrackedSlotStaysUnmatched (plan §9.7.1(A), A1):
// same fixture, opposite outcome. A title match landing on a slot that is
// already tracked with a file is now a legitimate ALTERNATE, not a conflict —
// it stays Pending carrying an "alternate:" reason, and Apply folds it in as
// primary or alternate by probed quality against live DB state
// (applyLibrarySeriesAlternate) instead of overwriting the tracked row.
//
// Title/Year are asserted to still come from the PINNED TRACKED ROW: a
// Year == 0 here is the folder-splitting corruption
// autopilot-impl-episode-title-matching.md §0.3 exists to prevent, and
// softening this guard is exactly the edit that could reintroduce it.
func TestScanLibrarySeries_EpisodeTitleMatchDuplicateIsPendingAlternate(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, redSkeltonShowTitle)
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "Red Skelton More Funny Faces.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	client := fakeTMDBEpisodeTitleServer(t, redSkeltonShowID, redSkeltonShowTitle, redSkeltonEpisodeFixture(), -1, nil)
	sess := &mode.Session{Mode: mode.Series, TMDB: client}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: redSkeltonShowID, Title: redSkeltonShowTitle, Year: 1951, RootFolderPath: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A DIFFERENT file (outside root, so the scan itself never touches it)
	// already occupies the S02E02 slot this file's title would resolve to.
	existingPath := filepath.Join(t.TempDir(), "already-tracked.mkv")
	if err := os.WriteFile(existingPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 2, EpisodeNumber: 2, FilePath: existingPath,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("expected Pending-as-alternate onto an already-tracked slot, got %+v", p)
	}
	wantPrefix := fmt.Sprintf("alternate: %q S02E02 already has a file in the library", redSkeltonShowTitle)
	if !strings.HasPrefix(p.Reason, wantPrefix) {
		t.Errorf("expected the softened alternate reason (prefix %q), got %q", wantPrefix, p.Reason)
	}
	// Title/Year must still come from the pinned tracked row, NOT from
	// TVDetails (which carries no year at all).
	if p.Title != redSkeltonShowTitle {
		t.Errorf("Title = %q, want the pinned tracked row's %q", p.Title, redSkeltonShowTitle)
	}
	if p.Year != 1951 {
		t.Errorf("Year = %d, want the pinned tracked row's 1951 — a zero year here splits the show folder at Apply", p.Year)
	}
}

// TestScanLibrarySeries_EpisodeTitleMatch_FailClosedOnSeasonError is case G
// (plan §1.2): a SeasonDetails failure on a season OTHER than the one
// holding the real match still aborts the ENTIRE search — proving the
// fail-closed rule protects against exactly the case where the unreadable
// season might have held a second, ambiguity-proving match, not just the
// case where the failed season happens to hold the answer.
func TestScanLibrarySeries_EpisodeTitleMatch_FailClosedOnSeasonError(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, redSkeltonShowTitle)
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "Red Skelton More Funny Faces.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Season 1 404s — NOT the season the real match (S02E02) lives in.
	client := fakeTMDBEpisodeTitleServer(t, redSkeltonShowID, redSkeltonShowTitle, redSkeltonEpisodeFixture(), 1, nil)
	sess := &mode.Session{Mode: mode.Series, TMDB: client}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: redSkeltonShowID, Title: redSkeltonShowTitle, Year: 1951, RootFolderPath: root}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected Unmatched (fail-closed) when any season fails to load, got %+v", got)
	}
}

// TestScanLibrarySeries_EpisodeTitleMatch_TrackedRowWinsOverTMDBName is case
// H (plan §0.3): the REGRESSION TEST for the folder-split defect this whole
// feature was gated on. /tv/10534 reports a DIFFERENT name than the tracked
// row; the accepted proposal's Title/Year must come from the tracked row,
// never from TVDetails — a TVDetails-sourced Year would be 0 (TVDetails
// carries no year at all), which builds a second, differently-named folder
// at Apply and blanks the tracked row's year on the next Apply too.
func TestScanLibrarySeries_EpisodeTitleMatch_TrackedRowWinsOverTMDBName(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, redSkeltonShowTitle)
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(showDir, "Red Skelton More Funny Faces.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	client := fakeTMDBEpisodeTitleServer(t, redSkeltonShowID, "Some Other Show Entirely", redSkeltonEpisodeFixture(), -1, nil)
	sess := &mode.Session{Mode: mode.Series, TMDB: client}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: redSkeltonShowID, Title: redSkeltonShowTitle, Year: 1951, RootFolderPath: root}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Pending {
		t.Fatalf("expected Pending, got %+v", got)
	}
	p := got[0]
	if p.Title != redSkeltonShowTitle {
		t.Errorf("expected the tracked row's Title %q to win over TMDB's, got %q", redSkeltonShowTitle, p.Title)
	}
	if p.Year != 1951 {
		t.Errorf("expected the tracked row's Year 1951 to win over TMDB's (which has none), got %d", p.Year)
	}
}

// TestScanLibrarySeries_LiveTMDBEpisodeTitleMatch is this feature's opt-in
// live-verification test, mirroring
// TestScanLibrarySeries_LiveTMDBTitleCollisionGuard above — gated behind
// testing.Short() and SAKMS_TEST_TMDB_API_KEY, and the one test in this file
// that proves the feature against genuinely real TMDB data with no
// transcription step in between (plan §5.3).
func TestScanLibrarySeries_LiveTMDBEpisodeTitleMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live TMDB network test in short mode")
	}
	apiKey := os.Getenv("SAKMS_TEST_TMDB_API_KEY")
	if apiKey == "" {
		t.Skip("SAKMS_TEST_TMDB_API_KEY not set; skipping live TMDB verification (see autopilot-impl-episode-title-matching.md §5.3)")
	}
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, redSkeltonShowTitle)
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Reused from the constructed fixture above (see redSkeltonEpisodeFixture's
	// VERIFICATION STATUS) — NOT confirmed against TMDB 10534's real catalog;
	// substitute a verified title before relying on this test's result. No
	// SxxExx anywhere in the basename or parent directory either way.
	if err := os.WriteFile(filepath.Join(showDir, "Red Skelton More Funny Faces.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: tmdb.New(tmdb.Config{APIKey: apiKey}, http.DefaultClient)}
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: redSkeltonShowID, Title: redSkeltonShowTitle, Year: 1951, RootFolderPath: root}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("expected Pending against live TMDB, got status=%v reason=%q", p.Status, p.Reason)
	}
	if p.TMDBID != redSkeltonShowID {
		t.Errorf("expected TMDB id %d against live TMDB search results, got tmdbID=%d", redSkeltonShowID, p.TMDBID)
	}
}

// ---------------------------------------------------------------------------
// autopilot-impl.md §8.3 — end-to-end ScanLibrarySeries coverage of the
// TheTVDB anthology pass (cases A-O).
//
// Every case below drives the REAL ScanLibrarySeries, not tvdbAnthologyPass
// directly. That is the whole point of this block: the pass only ever sees a
// file whose season/episode PARSE FAILED (proposeOneEpisodeLibrary's second
// return value), so a test that hand-builds proposals and calls the pass
// directly cannot tell you whether a real filename reaches it at all. Case A
// is the positive control for every "rows unchanged" case here — and each of
// those additionally asserts the VERBATIM parse-failure reason, which is what
// proves the row was a candidate the pass DECLINED rather than a row the pass
// never saw.
//
// Case N ("Movies/Adult untouched") has no test function by design — §8.3
// specifies it as "assert by omission", i.e. the suite passing with ZERO edits
// to rename_library_test.go, rename_test.go and rename_adult_*_test.go. The
// evidence is `git diff --stat` over those paths plus a full-package run, not
// a static test that would misfire on an unrelated future change.
// ---------------------------------------------------------------------------

// The four filenames are REAL rows from .omc/artifacts/series-after-20260806.psv,
// verbatim — these cases assert against the actual library population, not a
// convenient synthetic shape.
const (
	anthologyDuckSoupFile    = "Laurel & Hardy - Duck Soup(B&W)-DVDRip.XviD-DIE-DVD12.mp4"
	anthologyMusicBoxFile    = "Laurel & Hardy - The Music Box(Colour)-DVDRip.XviD-DIE-DVD14.mkv"
	anthologyBerthMarksFile  = "Laurel & Hardy - Berth Marks(B&W)-DVDRip.XviD-DIE-DVD03.mp4"
	anthologyBigBusinessFile = "Laurel & Hardy - Big Business(B&W)-DVDRip.XviD-DIE-DVD05.mp4"

	// anthologyShowFolder is the ON-DISK folder name, deliberately spelled
	// "and" while TheTVDB spells the show "&". normalizeShowKey strips
	// punctuation but expands nothing, so the two produce DIFFERENT keys
	// ("laurelandhardy" vs "laurelhardy") — which is exactly what keeps cases
	// I and K unpinned, and what the established-pin shortcut exists for.
	anthologyShowFolder   = "Laurel and Hardy"
	anthologyTVDBSeriesID = 73910
)

// anthologyScanCatalog is the shared TheTVDB catalog for the cases below: four
// distinct titles in four distinct slots, one per fixture filename.
//
// Duck Soup S03E01 and The Music Box S08E03 are the LIVE-CONFIRMED values
// (2026-08-07, series 73910, seasonType "official" — see tvdb.SeasonTypeOfficial
// and §8.4). Berth Marks and Big Business carry plausible-but-unmeasured slots:
// nothing here asserts anything about TheTVDB's real numbering for them, only
// that whatever slot the catalog reports is the slot the proposal lands on.
func anthologyScanCatalog() []fakeTVDBEpisode {
	return []fakeTVDBEpisode{
		{ID: 1, SeriesID: anthologyTVDBSeriesID, Name: "Duck Soup", Number: 1, SeasonNumber: 3, Aired: "1927-03-13"},
		{ID: 2, SeriesID: anthologyTVDBSeriesID, Name: "Berth Marks", Number: 5, SeasonNumber: 2, Aired: "1929-06-01"},
		{ID: 3, SeriesID: anthologyTVDBSeriesID, Name: "Big Business", Number: 12, SeasonNumber: 2, Aired: "1929-04-20"},
		{ID: 4, SeriesID: anthologyTVDBSeriesID, Name: "The Music Box", Number: 3, SeasonNumber: 8, Aired: "1932-04-16"},
	}
}

// fakeTVDBAnthologyShow is one show fakeTVDBAnthologyServer returns from
// GET /v4/search, plus the catalog it serves for that show's episode listing.
type fakeTVDBAnthologyShow struct {
	ID           int
	Name         string
	Year         string // "YYYY"; TheTVDB v4 returns year as a string
	Catalog      []fakeTVDBEpisode
	EpisodesFail bool // answer GET .../episodes/{type} with a 500 (case J)
}

// fakeTVDBAnthologyCounts carries the two request counters §8.3 needs. They
// are DISTINCT on purpose: cases C and D assert that no TVDB request of any
// kind was made (the folder is skipped before the search), while case F needs
// the search to SUCCEED and asserts only that the catalog was never fetched —
// a single total counter cannot express both.
// Episodes counts HTTP requests, NOT logical catalog fetches, and the
// difference is load-bearing wherever a non-zero value is asserted: ONE
// logical fetch costs TWO requests, page 0 carrying the whole catalog plus the
// terminating empty page 1 that is SeriesEpisodes' exhaustion signal. So the
// expected value is 2 PER EVALUATED CANDIDATE — which incidentally also proves
// the fetch is hoisted out of the per-file loop (four files sharing one
// catalog, not four fetches).
type fakeTVDBAnthologyCounts struct {
	Total    atomic.Int64 // every request, login included
	Episodes atomic.Int64 // GET /v4/series/{id}/episodes/{type} only
}

// fakeTVDBAnthologyServer stands in for TheTVDB v4's POST /v4/login,
// GET /v4/search and GET /v4/series/{id}/episodes/{type}.
//
// It is a SIBLING of fakeTVDBEpisodesServer above, not an extension of it, and
// of fakeTMDBSeriesServerEx — this repo's no-premature-abstraction convention,
// and it keeps those helpers' existing call sites provably untouched.
// fakeTVDBEpisodesServer answers no search route at all, so the anthology pass
// (which resolves the show BEFORE fetching any catalog) cannot use it.
//
// Counters are atomic and read from the TEST goroutine: t.Fatalf from a
// handler goroutine is not permitted, the same constraint countingTVDBServer
// documents.
func fakeTVDBAnthologyServer(t *testing.T, shows []fakeTVDBAnthologyShow) (*tvdb.Client, *fakeTVDBAnthologyCounts) {
	t.Helper()
	counts := &fakeTVDBAnthologyCounts{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/login", func(w http.ResponseWriter, r *http.Request) {
		counts.Total.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"token":"tok"}}`)
	})
	mux.HandleFunc("GET /v4/search", func(w http.ResponseWriter, r *http.Request) {
		counts.Total.Add(1)
		items := make([]map[string]string, 0, len(shows))
		for _, s := range shows {
			items = append(items, map[string]string{
				"tvdb_id": strconv.Itoa(s.ID), "name": s.Name, "year": s.Year,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": items}); err != nil {
			t.Errorf("encoding search fixture: %v", err)
		}
	})
	mux.HandleFunc("GET /v4/series/{id}/episodes/{type}", func(w http.ResponseWriter, r *http.Request) {
		counts.Total.Add(1)
		counts.Episodes.Add(1)
		var show *fakeTVDBAnthologyShow
		for i := range shows {
			if strconv.Itoa(shows[i].ID) == r.PathValue("id") {
				show = &shows[i]
			}
		}
		if show == nil {
			http.Error(w, "unknown series", http.StatusNotFound)
			return
		}
		if show.EpisodesFail {
			http.Error(w, "upstream exploded", http.StatusInternalServerError)
			return
		}
		// The whole catalog arrives on page 0 and every later page is empty,
		// which is SeriesEpisodes' exhaustion signal — exactly how the live
		// API answered the real 158-episode catalog on 2026-08-07.
		items := []fakeTVDBEpisode{}
		if page, _ := strconv.Atoi(r.URL.Query().Get("page")); page == 0 {
			items = show.Catalog
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"episodes": items},
		}); err != nil {
			t.Errorf("encoding episodes fixture: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return tvdb.New(tvdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client()), counts
}

// fatalTMDBSeriesServer fails the test on ANY request. ScanLibrarySeries
// refuses to run with a nil sess.TMDB (rename.go), so this is both the
// isolation mechanism and the assertion that the anthology path never touches
// TMDB. Every §8.3 case uses it EXCEPT case C, whose positive tracked TMDB id
// correctly routes its files through tryEpisodeTitleMatchSeries first.
//
// t.Errorf, not t.Fatalf: FailNow from a non-test goroutine is not permitted.
// The test still fails, which is the whole point.
func fatalTMDBSeriesServer(t *testing.T) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("TMDB must never be called on the anthology path, got %s %s", r.Method, r.URL.Path)
		http.Error(w, "this server must never be reached", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	return tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

// seedAnthologyFiles writes each name as a real file under root/sub.
func seedAnthologyFiles(t *testing.T, root, sub string, names ...string) {
	t.Helper()
	dir := filepath.Join(root, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

// proposalByName returns the one proposal whose SourceName is name. Lookup is
// by name rather than by slice index everywhere below: the scan's ordering is
// a filesystem-walk detail, and asserting on it would couple these tests to
// something none of them is about.
func proposalByName(t *testing.T, got []proposals.Proposal, name string) proposals.Proposal {
	t.Helper()
	for _, p := range got {
		if p.SourceName == name {
			return p
		}
	}
	t.Fatalf("no proposal for %q in %d rows", name, len(got))
	return proposals.Proposal{}
}

// anthologyWant is one expected placement for assertAnthologyPending.
type anthologyWant struct {
	file     string
	season   int
	episode  int
	epTitle  string
	tvdbID   int
	title    string
	year     int
	rootPath string
}

// assertAnthologyPending is the SHARED assertion cases A and O both run
// against one expected table, so a future edit cannot silently update the flat
// case without updating the nested one (§8.3 case O's explicit requirement).
func assertAnthologyPending(t *testing.T, got []proposals.Proposal, wants []anthologyWant) {
	t.Helper()
	if len(got) != len(wants) {
		t.Fatalf("expected %d proposals, got %d: %+v", len(wants), len(got), got)
	}
	for _, w := range wants {
		p := proposalByName(t, got, w.file)
		if p.Status != proposals.Pending {
			t.Errorf("%q: status = %v (%s), want Pending", w.file, p.Status, p.Reason)
			continue
		}
		if p.SeasonNumber != w.season || p.EpisodeNumber != w.episode {
			t.Errorf("%q: got S%02dE%02d, want S%02dE%02d", w.file, p.SeasonNumber, p.EpisodeNumber, w.season, w.episode)
		}
		if p.TVDBID != w.tvdbID || p.TMDBID != anthologyTMDBID(w.tvdbID) {
			t.Errorf("%q: ids = tvdb %d / tmdb %d, want %d / %d", w.file, p.TVDBID, p.TMDBID, w.tvdbID, anthologyTMDBID(w.tvdbID))
		}
		if p.Title != w.title || p.Year != w.year || p.RootFolderPath != w.rootPath {
			t.Errorf("%q: title/year/root = %q/%d/%q, want %q/%d/%q",
				w.file, p.Title, p.Year, p.RootFolderPath, w.title, w.year, w.rootPath)
		}
		if len(p.ExtraEpisodeNumbers) != 0 {
			t.Errorf("%q: ExtraEpisodeNumbers = %v, want none (a title match resolves exactly one slot)", w.file, p.ExtraEpisodeNumbers)
		}
		wantReason := fmt.Sprintf("%s %q -> S%02dE%02d (tvdb %d)",
			tvdbAnthologyReasonPrefix, w.epTitle, w.season, w.episode, w.tvdbID)
		if p.Reason != wantReason {
			t.Errorf("%q: reason = %q, want %q", w.file, p.Reason, wantReason)
		}
	}
}

// parseFailureReason is proposeOneEpisodeLibrary's VERBATIM parse-failure
// string (rename.go). Asserting it — rather than merely asserting Unmatched —
// is what makes every "rows unchanged" case below non-vacuous: only that
// branch sets parseFailed, so a row carrying this exact reason is provably a
// row the anthology pass was HANDED and declined, not one it never saw.
func parseFailureReason(name string) string {
	return fmt.Sprintf("could not determine season/episode from %q", name)
}

// assertUntouchedParseFailures asserts every named file is still the untouched
// Unmatched parse failure ScanLibrarySeries produced before the pass ran.
func assertUntouchedParseFailures(t *testing.T, got []proposals.Proposal, names ...string) {
	t.Helper()
	if len(got) != len(names) {
		t.Fatalf("expected %d proposals, got %d: %+v", len(names), len(got), got)
	}
	for _, name := range names {
		p := proposalByName(t, got, name)
		if p.Status != proposals.Unmatched {
			t.Errorf("%q: status = %v, want untouched Unmatched", name, p.Status)
		}
		if p.Reason != parseFailureReason(name) {
			t.Errorf("%q: reason = %q, want the verbatim parse-failure reason %q", name, p.Reason, parseFailureReason(name))
		}
		if p.TMDBID != 0 || p.TVDBID != 0 || p.SeasonNumber != 0 || p.EpisodeNumber != 0 {
			t.Errorf("%q: row was modified: tmdb=%d tvdb=%d S%02dE%02d", name, p.TMDBID, p.TVDBID, p.SeasonNumber, p.EpisodeNumber)
		}
	}
}

// TestScanLibrarySeries_TVDBAnthology_CorroboratedFolderPinsAndPlaces is
// §8.3's cases A and O in ONE table-driven test, deliberately: case O is the
// dedicated C1 regression test and asserts a byte-for-byte identical OUTCOME
// to case A from a folder one level deeper, so sharing the expectation table
// is what stops a future edit updating one and not the other.
//
// With the wrong show-folder derivation (filepath.Base(filepath.Dir(path)))
// the nested subtest's gate would be seeded with "Uncompressed" instead of
// "Laurel and Hardy", Check A would veto the only candidate, and every row
// would stay Unmatched. The shape is real library data, not invented:
// Series/The Red Skelton Show/Uncompressed/ (10 of 13 files) has it today.
func TestScanLibrarySeries_TVDBAnthology_CorroboratedFolderPinsAndPlaces(t *testing.T) {
	for _, tc := range []struct {
		name string
		sub  string
	}{
		{name: "flat show folder (case A)", sub: anthologyShowFolder},
		{name: "nested show folder (case O)", sub: filepath.Join(anthologyShowFolder, "Uncompressed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			seedAnthologyFiles(t, root, tc.sub,
				anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)

			tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
				ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog(),
			}})
			sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

			got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root,
				naming.Jellyfin, DefaultMatchConfig(), nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			assertAnthologyPending(t, got, []anthologyWant{
				{file: anthologyDuckSoupFile, season: 3, episode: 1, epTitle: "Duck Soup",
					tvdbID: anthologyTVDBSeriesID, title: "Laurel & Hardy", year: 1921, rootPath: root},
				{file: anthologyBerthMarksFile, season: 2, episode: 5, epTitle: "Berth Marks",
					tvdbID: anthologyTVDBSeriesID, title: "Laurel & Hardy", year: 1921, rootPath: root},
				{file: anthologyBigBusinessFile, season: 2, episode: 12, epTitle: "Big Business",
					tvdbID: anthologyTVDBSeriesID, title: "Laurel & Hardy", year: 1921, rootPath: root},
				{file: anthologyMusicBoxFile, season: 8, episode: 3, epTitle: "The Music Box",
					tvdbID: anthologyTVDBSeriesID, title: "Laurel & Hardy", year: 1921, rootPath: root},
			})
		})
	}
}

// TestScanLibrarySeries_TVDBAnthology_BelowThresholdIsInert is §8.3 case B:
// the SAME four-file folder as case A, but the catalog carries only two of the
// four titles, so only two distinct slots are claimed — below
// minCorroboratingFiles. The candidate does not qualify and the whole folder
// is left alone.
//
// This exercises the REAL Step-4 threshold, not Step 3's cheap pre-filter (a
// too-small group is already covered by the unit-level pre-filter case). The
// catalog IS fetched here, deliberately: the fetch precedes the threshold
// check, so this case asserts zero TMDB calls (via the fatal server) rather
// than zero TVDB calls.
func TestScanLibrarySeries_TVDBAnthology_BelowThresholdIsInert(t *testing.T) {
	root := t.TempDir()
	seedAnthologyFiles(t, root, anthologyShowFolder,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)

	twoTitles := []fakeTVDBEpisode{
		{ID: 1, SeriesID: anthologyTVDBSeriesID, Name: "Duck Soup", Number: 1, SeasonNumber: 3, Aired: "1927-03-13"},
		{ID: 2, SeriesID: anthologyTVDBSeriesID, Name: "Berth Marks", Number: 5, SeasonNumber: 2, Aired: "1929-06-01"},
	}
	tvdbClient, counts := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: twoTitles,
	}})
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root,
		naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertUntouchedParseFailures(t, got,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)
	// Exactly one catalog fetch: the candidate cleared Check A and WAS
	// evaluated, then failed the slot threshold. Without this the case cannot
	// distinguish "declined on 2 slots < 3" from "Check A vetoed it before any
	// catalog was fetched", which is case F's outcome, not case B's.
	// 2 == ONE catalog fetch for the ONE candidate (page 0 plus the
	// terminating empty page 1) shared across all four files.
	if n := counts.Episodes.Load(); n != 2 {
		t.Errorf("SeriesEpisodes requests = %d, want 2 — the candidate must be fetched once and then rejected on the threshold", n)
	}
}

// TestScanLibrarySeries_TVDBAnthology_AlreadyPinnedFolderIsSkipped is §8.3
// case C: a tracked series whose title normalizes to this folder's key puts a
// POSITIVE id in folderIDs, and Step 2's presence test skips the folder before
// any TVDB network call.
//
// This is the ONE case that does not use the fatal TMDB server, and the reason
// is the whole point of the case: a positive tracked TMDBID is precisely the
// condition that routes these files through the existing TMDB-backed
// tryEpisodeTitleMatchSeries FIRST, so real TMDB calls are expected and
// correct here. The assertion is therefore narrowed to ZERO TVDB REQUESTS —
// not zero TMDB requests.
func TestScanLibrarySeries_TVDBAnthology_AlreadyPinnedFolderIsSkipped(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	seedAnthologyFiles(t, root, anthologyShowFolder,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)

	tvdbClient, counts := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog(),
	}})
	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesServer(t, nil, nil), TVDB: tvdbClient}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	// Title "Laurel and Hardy" normalizes to the SAME key as the on-disk
	// folder, which is what pins it.
	if _, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: 12345, Title: anthologyShowFolder, Year: 1921, RootFolderPath: root,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertUntouchedParseFailures(t, got,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)
	if n := counts.Total.Load(); n != 0 {
		t.Errorf("TVDB requests = %d, want 0 — a pinned folder must short-circuit before any TVDB call", n)
	}
}

// TestScanLibrarySeries_TVDBAnthology_AmbiguousFolderKeyIsSkipped is §8.3
// case D: two DIFFERENT tracked shows whose titles normalize to this folder's
// key drive pinFolderID to its sticky-ambiguous 0 sentinel. Step 2 is a
// PRESENCE test, so the 0 skips the folder just as a positive id does — the
// strongest possible "do not guess" signal in this package.
func TestScanLibrarySeries_TVDBAnthology_AmbiguousFolderKeyIsSkipped(t *testing.T) {
	root := t.TempDir()
	seedAnthologyFiles(t, root, anthologyShowFolder,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)

	tvdbClient, counts := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog(),
	}})
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	// Both titles normalize to "laurelandhardy" (normalizeShowKey drops every
	// non-alphanumeric rune), under two DIFFERENT TMDB ids.
	for _, s := range []library.Series{
		{TMDBID: 111, Title: "Laurel and Hardy", Year: 1921, RootFolderPath: root},
		{TMDBID: 222, Title: "Laurel-and-Hardy", Year: 1930, RootFolderPath: root},
	} {
		if _, err := libStore.UpsertSeries(ctx, s); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertUntouchedParseFailures(t, got,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)
	if n := counts.Total.Load(); n != 0 {
		t.Errorf("TVDB requests = %d, want 0 — the 0 sentinel must skip the folder before any TVDB call", n)
	}
}

// TestScanLibrarySeries_TVDBAnthology_GateIsSeededWithTheFolderName is §8.3
// case E, the regression test for §0.4: every filename here is the BARE short
// title with no show name in it at all, so the only place the TheTVDB query
// and the gate's titleSeed can come from is the SHOW FOLDER's name.
//
// This test fails loudly the moment anyone re-seeds the gate with the
// filename: allowsTitle("Laurel & Hardy") with seed "Duck Soup.mkv" is false,
// so Check A would veto the only candidate and every row would stay Unmatched.
func TestScanLibrarySeries_TVDBAnthology_GateIsSeededWithTheFolderName(t *testing.T) {
	root := t.TempDir()
	seedAnthologyFiles(t, root, anthologyShowFolder,
		"Duck Soup.mkv", "The Music Box.mkv", "Berth Marks.mkv")

	tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog(),
	}})
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root,
		naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertAnthologyPending(t, got, []anthologyWant{
		{file: "Duck Soup.mkv", season: 3, episode: 1, epTitle: "Duck Soup",
			tvdbID: anthologyTVDBSeriesID, title: "Laurel & Hardy", year: 1921, rootPath: root},
		{file: "Berth Marks.mkv", season: 2, episode: 5, epTitle: "Berth Marks",
			tvdbID: anthologyTVDBSeriesID, title: "Laurel & Hardy", year: 1921, rootPath: root},
		{file: "The Music Box.mkv", season: 8, episode: 3, epTitle: "The Music Box",
			tvdbID: anthologyTVDBSeriesID, title: "Laurel & Hardy", year: 1921, rootPath: root},
	})
}

// TestScanLibrarySeries_TVDBAnthology_WrongCandidateRejectedBeforeCatalogFetch
// is §8.3 case F: TheTVDB's search for "Laurel and Hardy" answers with a show
// named "Breaking Bad" whose catalog WOULD corroborate all four files. Check A
// must veto it on the NAME alone.
//
// The SeriesEpisodes counter is not optional and is the entire point: without
// it, "Check A vetoes before the catalog is ever fetched" would be aspirational
// rather than asserted — the rows would look identical either way.
func TestScanLibrarySeries_TVDBAnthology_WrongCandidateRejectedBeforeCatalogFetch(t *testing.T) {
	root := t.TempDir()
	seedAnthologyFiles(t, root, anthologyShowFolder,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)

	tvdbClient, counts := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: 81189, Name: "Breaking Bad", Year: "2008", Catalog: anthologyScanCatalog(),
	}})
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root,
		naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertUntouchedParseFailures(t, got,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)
	if n := counts.Episodes.Load(); n != 0 {
		t.Errorf("SeriesEpisodes requests = %d, want 0 — Check A must veto before the catalog is fetched", n)
	}
	if n := counts.Total.Load(); n == 0 {
		t.Errorf("TVDB requests = 0 — the search itself must still have happened, or this case proves nothing")
	}
}

// TestScanLibrarySeries_TVDBAnthology_ShowLevelAmbiguityDeclines is §8.3
// case G: TheTVDB returns TWO shows that both clear Check A and both
// corroborate. The whole folder declines. Never "first qualifying candidate
// wins" — that reintroduces exactly the ordering-dependent title-collision
// class of bug series_folder_guard.go was written to fix.
func TestScanLibrarySeries_TVDBAnthology_ShowLevelAmbiguityDeclines(t *testing.T) {
	root := t.TempDir()
	seedAnthologyFiles(t, root, anthologyShowFolder,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)

	tvdbClient, counts := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{
		{ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog()},
		{ID: 99999, Name: "Laurel and Hardy Collection", Year: "1930", Catalog: anthologyScanCatalog()},
	})
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root,
		naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertUntouchedParseFailures(t, got,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)
	// BOTH catalogs were fetched, so both candidates were genuinely evaluated
	// and both qualified. Without this the case cannot distinguish "declined
	// because two candidates qualified" from "declined because zero did".
	// 4 == TWO catalog fetches, one per candidate, at 2 requests each.
	if n := counts.Episodes.Load(); n != 4 {
		t.Errorf("SeriesEpisodes requests = %d, want 4 (2 candidates x 1 paginated fetch) — both candidates must be evaluated for the decline to be a UNIQUENESS decline", n)
	}
}

// TestScanLibrarySeries_TVDBAnthology_EpisodeLevelAmbiguityUnmatchesOneFile is
// §8.3 case H: a folder that corroborates fine overall, with ONE file whose
// title matches two slots.
//
// The fixture needs FOUR files, not three, and this is a real trap US-003 hit:
// an ambiguous file contributes NO slot, so three files (one ambiguous plus
// two clean) would only claim two slots — below the threshold of three — and
// the folder would decline for the wrong reason, proving nothing about
// episode-level ambiguity.
func TestScanLibrarySeries_TVDBAnthology_EpisodeLevelAmbiguityUnmatchesOneFile(t *testing.T) {
	root := t.TempDir()
	seedAnthologyFiles(t, root, anthologyShowFolder,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)

	// A SECOND "Duck Soup" slot makes that one file ambiguous; the other three
	// still claim three distinct slots and clear the threshold.
	catalog := append(anthologyScanCatalog(),
		fakeTVDBEpisode{ID: 5, SeriesID: anthologyTVDBSeriesID, Name: "Duck Soup", Number: 9, SeasonNumber: 5, Aired: "1930-01-01"})
	tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: catalog,
	}})
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root,
		naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 proposals, got %d: %+v", len(got), got)
	}

	ambiguous := proposalByName(t, got, anthologyDuckSoupFile)
	if ambiguous.Status != proposals.Unmatched {
		t.Fatalf("ambiguous file: status = %v (%s), want Unmatched", ambiguous.Status, ambiguous.Reason)
	}
	// The reason must name BOTH slots — an operator has to be able to tell
	// "SAK found two plausible slots and refused to guess" from "SAK found
	// nothing" without re-running the scan.
	for _, slot := range []string{"S03E01", "S05E09"} {
		if !strings.Contains(ambiguous.Reason, slot) {
			t.Errorf("ambiguity reason must name %s, got: %s", slot, ambiguous.Reason)
		}
	}
	if ambiguous.Reason == parseFailureReason(anthologyDuckSoupFile) {
		t.Errorf("ambiguous file kept the parse-failure reason — the pass never saw it")
	}

	// Its neighbours still pin normally.
	for _, w := range []anthologyWant{
		{file: anthologyBerthMarksFile, season: 2, episode: 5, epTitle: "Berth Marks"},
		{file: anthologyBigBusinessFile, season: 2, episode: 12, epTitle: "Big Business"},
		{file: anthologyMusicBoxFile, season: 8, episode: 3, epTitle: "The Music Box"},
	} {
		p := proposalByName(t, got, w.file)
		if p.Status != proposals.Pending || p.SeasonNumber != w.season || p.EpisodeNumber != w.episode {
			t.Errorf("%q: got %v S%02dE%02d (%s), want Pending S%02dE%02d",
				w.file, p.Status, p.SeasonNumber, p.EpisodeNumber, p.Reason, w.season, w.episode)
		}
	}
}

// TestScanLibrarySeries_AnthologyDuplicateIsPendingAlternate covers SITE 6
// (tvdbAnthologyPass) — a corroborated folder in which one matched slot is
// ALREADY tracked with a file under the synthetic id.
//
// This is the REWRITE IN PLACE of the retired
// ..._TVDBAnthology_AlreadyTrackedSlotStaysUnmatched (plan §9.7.1(A), A2):
// same fixture, opposite outcome. The claimed file now stays Pending with an
// "alternate:" reason; Apply resolves the collision against live DB state and
// folds the file in as primary or alternate by probed quality instead of
// overwriting the slot's library_episodes row.
//
// The tracked row deliberately carries TVDBID 0, so seriesByTVDBID stays empty
// and the established-pin shortcut does NOT fire: this case is about the
// tracked-slot guard, not about the threshold. Note also that its Title
// ("Laurel & Hardy") normalizes to a DIFFERENT folder key than the on-disk
// folder ("Laurel and Hardy"), and its synthetic TMDBID is negative — which
// pinFolderID ignores outright — so the folder stays unpinned either way.
//
// The session wires a MainstreamAI so aiEpisodeRecoveryPass genuinely RUNS,
// and the final reason is asserted to still be the softened one, uncarrying
// aiEpisodeMatchReasonPrefix. Read that assertion for exactly what it is: a
// REASON-NOT-CLOBBERED OUTCOME CHECK. It does NOT prove handled[i] is set and
// must not be cited as if it did (plan §5.2.5) — the softened row is Pending
// with a non-zero synthetic TMDBID, so series_ai_episode_match.go:160's gate
// excludes it on Status and TMDBID alone; delete the handled[i] write and this
// still passes. The genuine handled[i] coverage is the unit-level
// TestAIRecovery_HandledRowIsSkipped "branch 2" case.
func TestScanLibrarySeries_AnthologyDuplicateIsPendingAlternate(t *testing.T) {
	root := t.TempDir()
	seedAnthologyFiles(t, root, anthologyShowFolder,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)

	tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog(),
	}})
	ai := &countingAI{resp: aiGuess("Laurel & Hardy", aiChickensEpisode, 0, 0)}
	sess := &mode.Session{Mode: mode.Series, MainstreamAI: ai,
		TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: anthologyTMDBID(anthologyTVDBSeriesID), Title: "Laurel & Hardy", Year: 1921, RootFolderPath: root,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A DIFFERENT file, outside root so the scan itself never touches it,
	// already occupies S03E01.
	existingPath := filepath.Join(t.TempDir(), "already-tracked.mkv")
	if err := os.WriteFile(existingPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 3, EpisodeNumber: 1, FilePath: existingPath,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 proposals, got %d: %+v", len(got), got)
	}
	claimed := proposalByName(t, got, anthologyDuckSoupFile)
	if claimed.Status != proposals.Pending {
		t.Fatalf("tracked slot: status = %v (%s), want Pending-as-alternate", claimed.Status, claimed.Reason)
	}
	wantPrefix := fmt.Sprintf("alternate: %q S03E01 already has a file in the library", "Laurel & Hardy")
	if !strings.HasPrefix(claimed.Reason, wantPrefix) {
		t.Errorf("expected the softened alternate reason (prefix %q), got %q", wantPrefix, claimed.Reason)
	}
	// Outcome check only — see this test's doc comment. aiEpisodeRecoveryPass
	// really did run (the AI is wired), and it left the reason alone.
	if n := seriesPromptCount(ai); n != 0 {
		t.Errorf("seriesPromptCount = %d, want 0 — every row in this folder is anthology-resolved", n)
	}
	if strings.Contains(claimed.Reason, aiEpisodeMatchReasonPrefix) {
		t.Errorf("reason carries %q — the AI pass clobbered the anthology alternate reason: %q",
			aiEpisodeMatchReasonPrefix, claimed.Reason)
	}
	for _, name := range []string{anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile} {
		if p := proposalByName(t, got, name); p.Status != proposals.Pending {
			t.Errorf("%q: status = %v (%s), want Pending", name, p.Status, p.Reason)
		}
	}
}

// TestScanLibrarySeries_TVDBAnthology_CatalogFailureDeclinesWholeFolder is
// §8.3 case J: SeriesEpisodes 500s for the only candidate. EVERY row in the
// folder is left untouched — fail-closed at FOLDER granularity, which is what
// lets tvdbEpisodeSearch have no Incomplete field at all.
func TestScanLibrarySeries_TVDBAnthology_CatalogFailureDeclinesWholeFolder(t *testing.T) {
	root := t.TempDir()
	seedAnthologyFiles(t, root, anthologyShowFolder,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)

	tvdbClient, counts := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921",
		Catalog: anthologyScanCatalog(), EpisodesFail: true,
	}})
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root,
		naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertUntouchedParseFailures(t, got,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)
	if n := counts.Episodes.Load(); n == 0 {
		t.Errorf("SeriesEpisodes requests = 0 — the catalog fetch must actually have been attempted and failed")
	}
}

// TestScanLibrarySeries_TVDBAnthology_EstablishedPinShortcutPlacesOneFile is
// §8.3 case K: an ALREADY-established anthology show plus exactly ONE new
// matching file in an unpinned folder.
//
// This is the case that proves Step 3 is a PRE-filter and not a size gate:
// len(group) == 1 < minCorroboratingFiles, so the group survives Step 3 only
// because the tracked row makes len(seriesByTVDBID) != 0 and the conjunction
// is false. The real threshold (1, from the established-pin shortcut) is then
// applied per candidate in Step 4. A Step 3 that skipped on group size alone
// would fail this test.
//
// TheTVDB's search deliberately answers with a DIFFERENT name and year than
// the tracked row carries — the assertion is that Title/Year come from the
// TRACKED ROW, because sourcing them from TheTVDB would rename an already
// committed folder the moment TheTVDB's series name changed.
func TestScanLibrarySeries_TVDBAnthology_EstablishedPinShortcutPlacesOneFile(t *testing.T) {
	root := t.TempDir()
	seedAnthologyFiles(t, root, anthologyShowFolder, anthologyDuckSoupFile)

	tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel and Hardy Shorts", Year: "1999", Catalog: anthologyScanCatalog(),
	}})
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.UpsertSeries(ctx, library.Series{
		TMDBID: anthologyTMDBID(anthologyTVDBSeriesID), TVDBID: anthologyTVDBSeriesID,
		Title: "Laurel & Hardy", Year: 1921, RootFolderPath: root,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertAnthologyPending(t, got, []anthologyWant{
		{file: anthologyDuckSoupFile, season: 3, episode: 1, epTitle: "Duck Soup",
			tvdbID: anthologyTVDBSeriesID, title: "Laurel & Hardy", year: 1921, rootPath: root},
	})
}

// anthologyAmbiguousMusicBoxCatalog is anthologyScanCatalog plus a SECOND
// "The Music Box" in a different season, so anthologyMusicBoxFile yields
// Found == 2 and contributes nothing to `slots`.
//
// The duplicated title is deliberately DISJOINT from every tracked
// corroboration title seeded below. Duplicating "Duck Soup" instead — and then
// using it as the corroboration source — would make establishedPinCorroborated
// see Found == 2, abstain, and decline the folder: the test would fail against
// a CORRECT implementation, for a fixture reason.
//
// Kept local rather than reusing anthologyHandledCatalog(), which duplicates
// "Below Zero" for a different case's purposes.
//
// The APPEND POSITION is load-bearing for the expected ambiguity reason below:
// searchTVDBEpisodeByTitle fills First/Second in catalog ITERATION order, so
// the S08E03 entry (from anthologyScanCatalog) is First and the S05E07 entry
// appended here is Second. Move the append and the expected reason string
// swaps its two slots. That ordering carries no "preferred slot" meaning — it
// is purely the reason string's rendering order.
func anthologyAmbiguousMusicBoxCatalog() []fakeTVDBEpisode {
	return append(anthologyScanCatalog(),
		fakeTVDBEpisode{ID: 7, SeriesID: anthologyTVDBSeriesID, Name: "The Music Box", Number: 7, SeasonNumber: 5, Aired: "1932-04-16"},
	)
}

// TestScanLibrarySeries_TVDBAnthology_EstablishedPinCorroboratesFromTrackedEpisodes
// is the plan's §6.5 — the end-to-end reproduction of the production defect,
// and the ONLY test that exercises rename.go's trackedEpisodesByTVDBID map
// builder. A wiring mistake there (wrong key, wrong branch, populated only for
// TMDBID > 0) is invisible to every unit-level case, which passes the map in
// by hand.
//
// The fixture is the shrinking pool, literally: two episodes have already been
// applied and their rows are what the pin now corroborates from, while the ONE
// file left in the un-applied folder is ambiguous in the catalog and can
// corroborate nothing. Before the fix that folder declined whole and the row
// reverted to the raw parse-failure reason — exactly what production regressed
// to (Laurel & Hardy, tvdb 73910, 77/96 applied, 6 files reverting).
//
// DOCUMENTED DEVIATION from the plan's §6.5 fixture note, which says to "name
// the applied file in the active preset's shape so naming.MatchesSeriesSchema
// excludes it from the candidate pool — that IS the shrinking-pool mechanism
// under test." That rationale does not hold for an ANTHOLOGY show and the
// fixture must not pretend it does: naming.SeriesFolderName omits the
// [tmdbid-N] tag whenever tmdbID <= 0 (naming.MovieFolderName), an anthology
// show's synthetic id is strictly negative, and MatchesSeriesSchema's
// seriesFolderJellyfin pattern REQUIRES that tag — so it returns false for
// every applied anthology file, forever. The mechanism that actually removes
// an applied file from the scan is rename.go's `known[ep.FilePath] = true`
// feeding library.ScanRootFolder, which is what this fixture relies on. The
// applied files are still named in the preset's shape because that is what
// ApplyLibrarySeries genuinely writes; they are just not excluded BY that.
func TestScanLibrarySeries_TVDBAnthology_EstablishedPinCorroboratesFromTrackedEpisodes(t *testing.T) {
	// applied seeds one already-applied episode file on disk under root and
	// returns its path, in the shape ApplyLibrarySeries writes.
	applied := func(t *testing.T, root string, season, episode int, epTitle, ext string) string {
		t.Helper()
		dir := filepath.Join(root,
			naming.SeriesFolderName(naming.Jellyfin, "Laurel & Hardy", 1921, anthologyTMDBID(anthologyTVDBSeriesID)),
			naming.SeasonDirName(season))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		path := filepath.Join(dir, naming.EpisodeFileName(naming.Jellyfin, "Laurel & Hardy", season, episode, epTitle, ext))
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return path
	}

	// setup builds the whole fixture and runs the scan. trackedTitles are the
	// library_episodes.title values the two applied rows carry — the ONLY thing
	// that differs between the two sub-cases.
	setup := func(t *testing.T, trackedTitles [2]string) []proposals.Proposal {
		t.Helper()
		root := t.TempDir()
		seedAnthologyFiles(t, root, anthologyShowFolder, anthologyMusicBoxFile)

		tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
			ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921",
			Catalog: anthologyAmbiguousMusicBoxCatalog(),
		}})
		// MainstreamAI left nil so aiEpisodeRecoveryPass returns immediately and
		// this test measures the anthology pass alone.
		sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

		libStore := newTestLibraryStore(t)
		ctx := context.Background()
		// BOTH ids are required: rename.go's allSeries loop gates
		// trackedEpisodesByTVDBID on series.TVDBID > 0, and step 4's deviation
		// guard requires TMDBID == anthologyTMDBID(TVDBID). Drop either and the
		// folder declines for a WIRING reason while looking like a real decline.
		series, err := libStore.UpsertSeries(ctx, library.Series{
			TMDBID: anthologyTMDBID(anthologyTVDBSeriesID), TVDBID: anthologyTVDBSeriesID,
			Title: "Laurel & Hardy", Year: 1921, RootFolderPath: root,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, ep := range []library.Episode{
			{SeriesID: series.ID, SeasonNumber: 3, EpisodeNumber: 1, Title: trackedTitles[0],
				FilePath: applied(t, root, 3, 1, "Duck Soup", ".mp4")},
			{SeriesID: series.ID, SeasonNumber: 2, EpisodeNumber: 5, Title: trackedTitles[1],
				FilePath: applied(t, root, 2, 5, "Berth Marks", ".mp4")},
		} {
			if _, err := libStore.UpsertEpisode(ctx, ep); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return got
	}

	t.Run("tracked episode titles corroborate the established pin", func(t *testing.T) {
		got := setup(t, [2]string{"Duck Soup", "Berth Marks"})

		// One proposal only: the two applied files are in `known`, so
		// ScanRootFolder never reports them.
		if len(got) != 1 {
			t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
		}
		p := proposalByName(t, got, anthologyMusicBoxFile)
		if p.Status != proposals.Unmatched {
			t.Fatalf("status = %v (%s), want Unmatched", p.Status, p.Reason)
		}
		// THE ASSERTION: the AMBIGUITY reason, not parseFailureReason. Reaching
		// it at all requires the folder to have RESOLVED, which with len(slots)
		// == 0 is only possible through the tracked-episode channel. Before the
		// fix this row carried the raw parse-failure reason instead.
		wantReason := fmt.Sprintf(
			"%q matches more than one episode of %q on TheTVDB (S%02dE%02d %q and S%02dE%02d %q) — ambiguous, leaving in place for manual review",
			anthologyMusicBoxFile, "Laurel & Hardy", 8, 3, "The Music Box", 5, 7, "The Music Box")
		if p.Reason != wantReason {
			t.Errorf("reason = %q,\nwant %q", p.Reason, wantReason)
		}
		if p.Reason == parseFailureReason(anthologyMusicBoxFile) {
			t.Errorf("reason is still the raw parse failure — the folder declined whole, which is the production symptom this test reproduces")
		}
	})

	t.Run("tracked episode titles absent from the catalog decline the folder", func(t *testing.T) {
		// Real Laurel & Hardy shorts deliberately absent from the served
		// catalog: neither channel corroborates, so the row is back to the
		// untouched parse failure.
		got := setup(t, [2]string{"A Chump at Oxford", "Night Owls"})
		assertUntouchedParseFailures(t, got, anthologyMusicBoxFile)
	})
}

// TestScanLibrarySeries_TVDBAnthology_RunsBeforeTheSeenGuard is §8.3 case L,
// the ORDERING test (§3.2). Two files in a corroborated folder resolve to the
// SAME slot; the within-batch `seen` guard annotates the second claimant.
//
// That only happens if the anthology pass runs BEFORE the guard. Backwards,
// neither row would carry the guard's annotation at all. The ORDERING is the
// property here and it is unchanged — only the guard's own outcome softened
// (plan §9.7.1(B), B1): the second claimant stays Pending carrying the
// "alternate:" annotation instead of being turned Unmatched.
//
// Which of the two is annotated is a filesystem-walk-ordering detail, so the
// assertion is "both Pending, exactly one carrying the seen guard's alternate
// annotation" rather than a fixed index.
func TestScanLibrarySeries_TVDBAnthology_RunsBeforeTheSeenGuard(t *testing.T) {
	const secondBigBusiness = "Laurel & Hardy - Big Business(Colour)-DVDRip.XviD-DIE-DVD06.mkv"

	root := t.TempDir()
	seedAnthologyFiles(t, root, anthologyShowFolder,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile,
		anthologyBigBusinessFile, secondBigBusiness)

	tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{{
		ID: anthologyTVDBSeriesID, Name: "Laurel & Hardy", Year: "1921", Catalog: anthologyScanCatalog(),
	}})
	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t), TVDB: tvdbClient}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root,
		naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 proposals, got %d: %+v", len(got), got)
	}

	wantSeenPrefix := fmt.Sprintf(
		"alternate: another file in this scan also claims %q S02E12 — apply will fold as primary or alternate by quality.",
		"Laurel & Hardy")
	var pending, collided int
	for _, name := range []string{anthologyBigBusinessFile, secondBigBusiness} {
		p := proposalByName(t, got, name)
		// The annotated case MUST be tested first: an annotated row is also a
		// plain Pending S02E12 row, so a status-only case listed first would
		// swallow it and report 2 Pending / 0 annotated.
		switch {
		case p.Status == proposals.Pending && p.SeasonNumber == 2 && p.EpisodeNumber == 12 &&
			strings.HasPrefix(p.Reason, wantSeenPrefix):
			collided++
		case p.Status == proposals.Pending && p.SeasonNumber == 2 && p.EpisodeNumber == 12:
			pending++
		default:
			t.Errorf("%q: unexpected outcome status=%v S%02dE%02d reason=%q",
				name, p.Status, p.SeasonNumber, p.EpisodeNumber, p.Reason)
		}
	}
	if pending != 1 || collided != 1 {
		t.Errorf("same-slot pair = %d plain Pending / %d seen-guard-annotated Pending, want exactly 1 and 1 — "+
			"the anthology pass must run BEFORE the seen guard", pending, collided)
	}
	// The three unambiguous neighbours are unaffected by the collision.
	for _, name := range []string{anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile} {
		if p := proposalByName(t, got, name); p.Status != proposals.Pending {
			t.Errorf("%q: status = %v (%s), want Pending", name, p.Status, p.Reason)
		}
	}
}

// TestScanLibrarySeries_TVDBAnthology_NilTVDBClientIsInert is §8.3 case M: an
// otherwise-corroborating setup with NO TVDB client. TheTVDB is an OPTIONAL
// connection, so a nil client is the normal case on most installs — the pass
// must no-op rather than panic.
func TestScanLibrarySeries_TVDBAnthology_NilTVDBClientIsInert(t *testing.T) {
	root := t.TempDir()
	seedAnthologyFiles(t, root, anthologyShowFolder,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)

	sess := &mode.Session{Mode: mode.Series, TMDB: fatalTMDBSeriesServer(t)} // TVDB deliberately nil

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root,
		naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertUntouchedParseFailures(t, got,
		anthologyDuckSoupFile, anthologyMusicBoxFile, anthologyBerthMarksFile, anthologyBigBusinessFile)
}

// TestScanLibrarySeries_LiveTVDBAnthologyMatch is the ONE test that verifies
// this feature against TheTVDB's REAL "Laurel & Hardy" catalog, using the
// REAL filenames from .omc/artifacts/series-after-20260806.psv.
//
// The plan's §0.2 page-indexing question is RESOLVED (confirmed live
// 2026-08-07: page=0 returns the full catalog), so this test is no longer the
// first proof of that. It is now the DRIFT GUARD, and the EPISODE-COUNT
// ASSERTION BELOW IS STILL NOT DECORATION: pagination CONTINUATION remains
// unverified (the real catalog is 158 episodes against a 500 page size, so
// the loop's second iteration has never run live), and no fake server can
// speak to upstream field-name, envelope, or season-type-ordering drift. Do
// not delete it.
//
// Gated behind testing.Short() and SAKMS_TEST_TVDB_API_KEY, the same shape as
// TestScanLibrarySeries_LiveTMDBTitleCollisionGuard and
// TestScanLibrarySeries_LiveTMDBEpisodeTitleMatch above, so `go test ./...`
// never depends on network access or a real TVDB key.
//
// Deliberately client-only: it drives searchTVDBEpisodeByTitle — the real
// acceptance predicate, called exactly as tvdbAnthologyPass calls it — against
// the real catalog, with no library store, no temp dir and no
// ScanLibrarySeries. Cases A–O above already cover the end-to-end wiring
// against a fake server; what only a live run can establish is that TheTVDB's
// actual data still resolves these filenames to exactly one slot each.
func TestScanLibrarySeries_LiveTVDBAnthologyMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live TVDB network test in short mode")
	}
	apiKey := os.Getenv("SAKMS_TEST_TVDB_API_KEY")
	if apiKey == "" {
		t.Skip("SAKMS_TEST_TVDB_API_KEY not set; skipping live TVDB verification (see autopilot-impl.md §8.4)")
	}
	// BaseURL is set EXPLICITLY: tvdb.New does not default it, so an omitted
	// BaseURL would send every request to a hostless URL and fail this test
	// for a reason that has nothing to do with TheTVDB.
	client := tvdb.New(tvdb.Config{BaseURL: tvdb.DefaultBaseURL, APIKey: apiKey}, http.DefaultClient)
	ctx := context.Background()

	// Resolve the series id LIVE. Never hardcode it: an id typed from memory
	// would make this test's "verifies against real data" claim false, and
	// TheTVDB has more than one Laurel & Hardy entry (the spec's own process
	// note records that TMDB's "Laurel & Hardy" TV id 117523 is an unrelated
	// 1966 cartoon — the same trap exists on TheTVDB).
	results, err := client.SearchSeries(ctx, "Laurel & Hardy")
	if err != nil {
		t.Fatalf("live TVDB SearchSeries failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("live TVDB SearchSeries returned no results for %q", "Laurel & Hardy")
	}
	for _, r := range results {
		t.Logf("live TVDB search result: id=%d name=%q year=%d", r.TVDBID, r.Name, r.Year)
	}
	// Select by id rather than taking results[0]: search RANKING is TheTVDB's
	// own database and may reorder benignly, which would make a
	// results[0].TVDBID == 73910 assertion brittle for no gain. The id itself
	// is the measured fact (2026-08-07, slug "laurel-and-hardy").
	var show *tvdb.Result
	for i := range results {
		if results[i].TVDBID == anthologyTVDBSeriesID {
			show = &results[i]
			break
		}
	}
	if show == nil {
		t.Fatalf("live TVDB search for %q no longer returns series id %d; got %+v",
			"Laurel & Hardy", anthologyTVDBSeriesID, results)
	}

	catalog, err := client.SeriesEpisodes(ctx, show.TVDBID, tvdb.SeasonTypeOfficial)
	if err != nil {
		t.Fatalf("live TVDB SeriesEpisodes(%d, %q) failed: %v", show.TVDBID, tvdb.SeasonTypeOfficial, err)
	}
	t.Logf("live TVDB catalog: series %d seasonType %q -> %d episodes",
		show.TVDBID, tvdb.SeasonTypeOfficial, len(catalog))
	// MEASURED 2026-08-07 against the real API: series id 73910 (slug
	// "laurel-and-hardy") returns exactly 158 episodes under seasonType
	// "official", in ONE page (links.total_items=158, links.page_size=500).
	// The threshold stays at > 100 rather than == 158 deliberately —
	// TheTVDB's catalog is community-edited and will legitimately grow or be
	// re-seasoned, so an exact count would be a brittle assertion about
	// someone else's database. > 100 still catches the two failures that
	// matter: 0 (an empty catalog, i.e. the request or the decode is broken)
	// and a small number (pagination stopped early).
	if len(catalog) <= 100 {
		t.Fatalf("expected > 100 episodes for series %d under %q (158 measured 2026-08-07), got %d",
			show.TVDBID, tvdb.SeasonTypeOfficial, len(catalog))
	}

	// Also fetch the OTHER season-type once, asserting only that it is
	// non-empty — never asserting numbers against it. Rationale: this feature
	// ships SeasonTypeOfficial because that is the ordering the asserted
	// (season, episode) pairs below were measured under; "default" was NOT
	// separately verified (§0.2, §1.1). A non-empty check is enough to notice
	// if the two ever diverge structurally, without pinning episode numbers
	// nobody has measured. The literal "default" is deliberately not promoted
	// to a const in internal/tvdb — nothing in production uses it.
	defaultCatalog, err := client.SeriesEpisodes(ctx, show.TVDBID, "default")
	if err != nil {
		t.Fatalf("live TVDB SeriesEpisodes(%d, \"default\") failed: %v", show.TVDBID, err)
	}
	t.Logf("live TVDB catalog: series %d seasonType %q -> %d episodes", show.TVDBID, "default", len(defaultCatalog))
	if len(defaultCatalog) == 0 {
		t.Errorf("expected a non-empty %q catalog for series %d, got 0", "default", show.TVDBID)
	}

	// Drive the REAL predicate against the REAL catalog, with show.Name as the
	// show-title argument — exactly what tvdbAnthologyPass passes (cand.Name),
	// which is what episodeTitleMatches subtracts from each episode name.
	//
	// CONFIRMED LIVE 2026-08-07 under seasonType "official": Duck Soup is
	// S03E01 (aired 1927-03-13) and The Music Box is S08E03 (aired
	// 1932-04-16) — exactly matching the spec's own citations. These are
	// measured values, not predictions. "official" is the CONFIRMED-WORKING
	// season-type, not a fallback to try if something disagrees.
	//
	// If a future run disagrees with these numbers, that is upstream drift in
	// TheTVDB's community-edited catalog, not a bug in this code — LOG the new
	// values and update this comment. Do NOT add a settings key for seasonType
	// (§9); it is one const in one place.
	for _, tc := range []struct {
		filename string
		season   int
		episode  int
		name     string
		aired    string
	}{
		{anthologyDuckSoupFile, 3, 1, "Duck Soup", "1927-03-13"},
		{anthologyMusicBoxFile, 8, 3, "The Music Box", "1932-04-16"},
	} {
		res := searchTVDBEpisodeByTitle(catalog, show.Name, tc.filename)
		if res.Found != 1 || res.Match == nil {
			// Found >= 2 here is a genuine finding about the real catalog, not
			// a test bug: production would decline these files as Unmatched.
			// Log both slots so the evidence is legible without a re-run.
			if res.Found >= 2 {
				t.Errorf("%q matched %d slots in the live catalog of %q (expected exactly 1): S%02dE%02d %q and S%02dE%02d %q",
					tc.filename, res.Found, show.Name,
					res.First.season, res.First.episode, res.First.name,
					res.Second.season, res.Second.episode, res.Second.name)
				continue
			}
			t.Errorf("%q matched %d slots in the live catalog of %q (expected exactly 1)",
				tc.filename, res.Found, show.Name)
			continue
		}
		m := res.Match
		t.Logf("live TVDB match: %q -> S%02dE%02d %q (aired %s)", tc.filename, m.season, m.episode, m.name, m.airDate)
		if m.season != tc.season || m.episode != tc.episode {
			t.Errorf("%q: expected S%02dE%02d (measured 2026-08-07), got S%02dE%02d",
				tc.filename, tc.season, tc.episode, m.season, m.episode)
		}
		if m.name != tc.name {
			t.Errorf("%q: expected episode name %q, got %q", tc.filename, tc.name, m.name)
		}
		if m.airDate != tc.aired {
			t.Errorf("%q: expected air date %q (measured 2026-08-07), got %q", tc.filename, tc.aired, m.airDate)
		}
	}
}

// seriesGroundingAIServer stands in for the Ollama endpoint on the two prompts a
// Series scan reaches here. GroundTitleViaSearch's prompt is the only one that
// embeds the web-search snippets, so that substring is the discriminator;
// GuessTitle always declines, which forces the web-authority path to build its
// own queries from the entry name (the shape production actually takes for this
// population).
func seriesGroundingAIServer(t *testing.T, groundedTitle string, groundedYear int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		prompt := ""
		if len(body.Messages) > 0 {
			prompt = body.Messages[len(body.Messages)-1].Content
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(prompt, "Web search results:") {
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{
				"content": fmt.Sprintf(`{"title":%q,"year":%d}`, groundedTitle, groundedYear),
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": `{"title":null}`}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seriesBraveServer returns one canned web result so GroundTitleViaSearch gets
// past its "no results" early return and actually calls the AI.
func seriesBraveServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"web": map[string]any{"results": []map[string]any{
			{"title": "The Red Skelton Show (TV Series 1951-1971)", "description": "American variety show.", "url": "https://example.invalid/red-skelton"},
		}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestScanLibrarySeries_CompactCodeBlockedFromWebAuthority is the safety-critical
// regression test for the §1.3a guard.
//
// The compact eNNNN parser newly makes this population reachable by
// tryWebAuthoritySeries — a path that makes NO SeasonDetails existence check and
// mints a SYNTHETIC NEGATIVE TMDB id. "The Red Skelton Show" has a real TMDB
// entry (10534), so a web-authority Pending here would create a SECOND
// library_series row for an already-tracked show. HasTitleTokenOverlap does not
// stop it: "skelton" is a >= 4-rune shared token, so the overlap gate passes on
// its strong branch. The guard is what keeps e2516 (season 25 — the show's
// highest is 20) Unmatched instead of confidently wrong.
//
// The SxxExx file in the same folder is a POSITIVE CONTROL, and it is what stops
// this test being vacuous: it proves the fake grounding chain really does drive
// tryWebAuthoritySeries to a synthetic-negative-id Pending, so the compact-code
// file's Unmatched is the guard firing rather than the chain simply not working.
// It also pins the flag's meaning — the block keys on HOW the season/episode was
// parsed, not on whether the filename happens to contain an eNNNN token.
func TestScanLibrarySeries_CompactCodeBlockedFromWebAuthority(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, "The Red Skelton Show")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	compactPath := filepath.Join(showDir, "RED SKELTON e2516.mp4")
	markerPath := filepath.Join(showDir, "RED SKELTON S05E03.mp4")
	for _, p := range []string{compactPath, markerPath} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	aiSrv := seriesGroundingAIServer(t, "The Red Skelton Show", 1951)
	braveSrv := seriesBraveServer(t)

	sess := &mode.Session{
		Mode: mode.Series,
		// No TMDB search result for anything: the catalog paths all decline, so
		// web authority is the ONLY thing that could produce a Pending here.
		TMDB:         fakeTMDBSeriesServer(t, nil, nil),
		MainstreamAI: ollama.New(aiSrv.URL, "m", aiSrv.Client()),
		WebSearch:    websearch.Brave{Inner: bravesearch.New(braveSrv.URL, "k", braveSrv.Client())},
	}
	// A real duration makes sig.HasAny() true, which is what puts both files on
	// the MAIN path (rename.go's post-search block) rather than the defensive
	// !sig.HasAny() branch — matching production, where a compact-code file is
	// always a real, probeable .mp4.
	prober := &fakeProber{durations: map[string]float64{compactPath: 1500, markerPath: 1500}}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultMatchConfig(), prober)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byName := map[string]proposals.Proposal{}
	for _, p := range got {
		byName[p.SourceName] = p
	}
	if len(byName) != 2 {
		t.Fatalf("expected 2 proposals, got %d: %+v", len(got), got)
	}

	compact := byName["RED SKELTON e2516.mp4"]
	if compact.Status != proposals.Unmatched {
		t.Errorf("compact-code file must stay Unmatched, got %v title=%q tmdb=%d reason=%q",
			compact.Status, compact.Title, compact.TMDBID, compact.Reason)
	}
	if compact.Reason != compactCodeWebAuthorityRefusedReason {
		t.Errorf("compact-code file must carry the distinct web-authority-refused reason for positive attribution;\n got  %q\n want %q", compact.Reason, compactCodeWebAuthorityRefusedReason)
	}
	// A NEGATIVE TMDBID on this row means the guard did not fire — that is a
	// blocker (a duplicate library_series row under a synthetic show identity),
	// not a bonus Pending.
	if compact.TMDBID < 0 {
		t.Errorf("compact-code file landed a SYNTHETIC NEGATIVE TMDB id (%d) — the §1.3a guard is not firing", compact.TMDBID)
	}
	// The season/episode must still have PARSED — the guard blocks a matching
	// route, it does not undo the parse.
	if compact.SeasonNumber != 0 && compact.SeasonNumber != 25 {
		t.Errorf("unexpected season on the compact-code row: %d", compact.SeasonNumber)
	}
	if strings.Contains(compact.Reason, "could not determine season/episode") {
		t.Errorf("compact-code file must parse its season/episode, got reason %q", compact.Reason)
	}

	marker := byName["RED SKELTON S05E03.mp4"]
	if marker.Status != proposals.Pending {
		t.Fatalf("POSITIVE CONTROL failed: the SxxExx file should reach tryWebAuthoritySeries and land Pending, got %v reason=%q — without this the compact-code assertion above is vacuous",
			marker.Status, marker.Reason)
	}
	if marker.TMDBID >= 0 {
		t.Errorf("POSITIVE CONTROL failed: expected a synthetic NEGATIVE web-authority TMDB id, got %d", marker.TMDBID)
	}
	if marker.Title != "The Red Skelton Show" {
		t.Errorf("POSITIVE CONTROL: expected the grounded title, got %q", marker.Title)
	}
}

// TestScanLibrarySeries_CompactCodeGateRejectionWinsOverWebAuthorityRefusal pins
// the one direction in which the terminal-reason ordering is load-bearing: when
// BOTH the gate rejected candidates and the web-authority guard fired, the gate's
// reason must win, because "rejected N candidates that did not share a title
// token" is strictly more diagnostic than "web authority was skipped."
func TestScanLibrarySeries_CompactCodeGateRejectionWinsOverWebAuthorityRefusal(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, "The Red Skelton Show")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	compactPath := filepath.Join(showDir, "RED SKELTON e1524.mp4")
	if err := os.WriteFile(compactPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Every TMDB search returns one candidate whose title shares no token with
	// either the file-derived seed or the grounded title, so the seriesGate
	// rejects it and bumps its shared counter.
	tmdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search/tv":
			_, _ = w.Write([]byte(`{"results":[{"id":777,"name":"Completely Unrelated Program","first_air_date":"1999-01-01"}]}`))
		case strings.Contains(r.URL.Path, "/season/"):
			_, _ = w.Write([]byte(`{"episodes":[{"episode_number":24,"name":"Ep","air_date":"1999-01-01","runtime":25}]}`))
		default:
			_, _ = w.Write([]byte(`{"id":777,"name":"Completely Unrelated Program","first_air_date":"1999-01-01","genres":[]}`))
		}
	}))
	t.Cleanup(tmdbSrv.Close)

	aiSrv := seriesGroundingAIServer(t, "Grounded Alpha Beta", 1951)
	braveSrv := seriesBraveServer(t)

	sess := &mode.Session{
		Mode:         mode.Series,
		TMDB:         tmdb.New(tmdb.Config{BaseURL: tmdbSrv.URL, APIKey: "test-key"}, tmdbSrv.Client()),
		MainstreamAI: ollama.New(aiSrv.URL, "m", aiSrv.Client()),
		WebSearch:    websearch.Brave{Inner: bravesearch.New(braveSrv.URL, "k", braveSrv.Client())},
	}
	prober := &fakeProber{durations: map[string]float64{compactPath: 1500}}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultMatchConfig(), prober)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Unmatched {
		t.Fatalf("expected Unmatched, got %v title=%q tmdb=%d reason=%q", p.Status, p.Title, p.TMDBID, p.Reason)
	}
	if !strings.Contains(p.Reason, "did not match this folder's tracked show or share a title token") {
		t.Errorf("the gate rejection reason must WIN over the web-authority-refused reason, got %q", p.Reason)
	}
	if p.Reason == compactCodeWebAuthorityRefusedReason {
		t.Errorf("the web-authority-refused reason swallowed the more specific gate rejection")
	}
	if p.TMDBID < 0 {
		t.Errorf("compact-code file landed a SYNTHETIC NEGATIVE TMDB id (%d) — the §1.3a guard is not firing", p.TMDBID)
	}
}

// TestScanLibrarySeries_CompactCodeSeedFallsBackToShowFolder is the only
// behavioral coverage of STEP 3 of the compact-code title-seed cascade, and it
// is the only thing that proves the new `roots` parameter is threaded with a
// usable value: an empty slice makes showFolderName return "" and silently
// leaves the seed as "_720x480.mp4", which compiles and passes every other test
// in this file.
//
// "e614_720x480.mp4" is the real file the fallback exists for (plan §0.4 —
// three such files sit directly in the show folder). Its cascade is:
// StripEpisodeMarkerLoose no-ops (no SxxExx in the file OR its parent), step 2
// strips the code to "_720x480.mp4", hasWordToken is false on the surviving
// {720x480} token, and step 3 substitutes the SHOW FOLDER's own name. Without
// step 3 the seed shares ZERO tokens with any real show title, seriesGate
// Check A rejects every candidate, and the file can never match.
//
// The seed is observed where production actually consumes it — the
// bravePhase2Series web query and the GuessTitle prompt both receive looseTitle
// verbatim.
func TestScanLibrarySeries_CompactCodeSeedFallsBackToShowFolder(t *testing.T) {
	tmdb.ResetDefaultCache()
	t.Cleanup(tmdb.ResetDefaultCache)

	root := t.TempDir()
	showDir := filepath.Join(root, "The Red Skelton Show")
	if err := os.MkdirAll(showDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	videoPath := filepath.Join(showDir, "e614_720x480.mp4")
	if err := os.WriteFile(videoPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var braveQueries []string
	braveSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		braveQueries = append(braveQueries, r.URL.Query().Get("q"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"web": map[string]any{"results": []map[string]any{}}})
	}))
	t.Cleanup(braveSrv.Close)

	var aiPrompts []string
	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) > 0 {
			aiPrompts = append(aiPrompts, body.Messages[len(body.Messages)-1].Content)
		}
		w.Header().Set("Content-Type", "application/json")
		// Always decline, so nothing can accidentally match and the assertions
		// below are about the SEED alone.
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": `{"title":null}`}})
	}))
	t.Cleanup(aiSrv.Close)

	sess := &mode.Session{
		Mode:         mode.Series,
		TMDB:         fakeTMDBSeriesServer(t, nil, nil),
		MainstreamAI: ollama.New(aiSrv.URL, "m", aiSrv.Client()),
		WebSearch:    websearch.Brave{Inner: bravesearch.New(braveSrv.URL, "k", braveSrv.Client())},
	}
	prober := &fakeProber{durations: map[string]float64{videoPath: 1500}}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), root, naming.Jellyfin, DefaultMatchConfig(), prober)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	// The season/episode must have parsed via the compact code (S06E14) — the
	// seed fallback is downstream of that, not a substitute for it.
	if strings.Contains(got[0].Reason, "could not determine season/episode") {
		t.Fatalf("expected the compact code to parse, got reason %q", got[0].Reason)
	}

	if len(braveQueries) == 0 {
		t.Fatal("expected at least one bravePhase2Series web query")
	}
	sawFolderSeed := false
	for _, q := range braveQueries {
		if strings.Contains(strings.ToLower(q), "red skelton") {
			sawFolderSeed = true
		}
		if strings.Contains(q, "720x480") {
			t.Errorf("the resolution noise reached the web query — step 3 did not fire (check that roots is threaded into proposeOneEpisodeLibrary): %q", q)
		}
	}
	if !sawFolderSeed {
		t.Errorf("expected the show folder's own name to become the title seed, got brave queries: %v", braveQueries)
	}
	for _, prompt := range aiPrompts {
		if strings.Contains(prompt, "720x480") {
			t.Errorf("the resolution noise reached an AI prompt instead of the show-folder seed: %q", prompt)
		}
	}
}

// --- Plan §9.5's two TVDB-fallback tests: Sites 3 and 4 -----------------------
//
// Both sites had ZERO coverage before this. Site 4 is the more important of the
// two: §5.2.3 records it as a MORE common path than Site 3 on a low-signal
// library, which is precisely the target population.
//
// TEST-INFRASTRUCTURE DEVIATION, recorded because §9.5 names two helpers by
// name and neither is usable as written:
//   - fakeTVDBEpisodesServer answers no GET /v4/search route at all, and
//     tvdbFallbackSeries resolves the show through SearchSeries. The TVDB side
//     therefore reuses fakeTVDBAnthologyServer, which does serve that route —
//     genuine reuse, and its own doc comment already explains why it is a
//     sibling of fakeTVDBEpisodesServer rather than an extension.
//   - fakeTMDBSeriesServerEx serves no /find/{tvdb_id} route (the very call
//     that turns a TheTVDB hit into a TMDB id), serves no per-candidate title
//     or episode runtime, and t.Fatalf's on any unrecognised path — which would
//     explode on the TVAggregateCredits call both sites make. Hence the sibling
//     below, following the same convention.

// tvdbFallbackCandidate is one TheTVDB search hit as TMDB answers for it.
// TMDBName is deliberately DIFFERENT from the TheTVDB name in every fixture:
// that difference is what makes deviation D3 (§5.2.1) falsifiable — the emitted
// row and its reason must name TMDB's title, never TheTVDB's bestName.
type tvdbFallbackCandidate struct {
	TVDBID            int
	TMDBID            int
	TMDBName          string
	EpisodeRuntimeMin int // season 1 / episode 1 runtime, the duration-corroboration knob
}

// fakeTMDBTVDBFallbackServer serves the four TMDB endpoints tvdbFallbackSeries
// touches, plus /search/tv answering EMPTY so the caller falls through to the
// TVDB fallback in the first place.
//
// The returned counter map is keyed by TVDBID and counts /find/{tvdb_id}
// requests — the observable that proves how far the candidate loop walked. It
// is what Site 4's test uses to prove it reached the weak branch rather than
// the strong one.
//
// Unknown paths answer 404 (the mux default), never t.Fatalf: TVAggregateCredits
// is called on both sites and is expected to soft-fail here, and FailNow from a
// handler goroutine is not permitted anyway.
func fakeTMDBTVDBFallbackServer(t *testing.T, cands []tvdbFallbackCandidate) (*tmdb.Client, map[int]*atomic.Int64) {
	t.Helper()
	finds := map[int]*atomic.Int64{}
	byTMDBID := map[int]tvdbFallbackCandidate{}
	byTVDBID := map[int]tvdbFallbackCandidate{}
	for _, c := range cands {
		finds[c.TVDBID] = &atomic.Int64{}
		byTMDBID[c.TMDBID] = c
		byTVDBID[c.TVDBID] = c
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search/tv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[]}`)
	})
	mux.HandleFunc("GET /find/{tvdbid}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("tvdbid"))
		c, ok := byTVDBID[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		finds[id].Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"tv_results":[{"id":%d}]}`, c.TMDBID)
	})
	mux.HandleFunc("GET /tv/{id}/season/{season}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		c, ok := byTMDBID[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"episodes":[{"episode_number":1,"name":"Pilot","air_date":"2020-01-01","runtime":%d}]}`,
			c.EpisodeRuntimeMin)
	})
	mux.HandleFunc("GET /tv/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		c, ok := byTMDBID[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%d,"name":%q,"genres":[]}`, c.TMDBID, c.TMDBName)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client()), finds
}

// seedTVDBFallbackTrackedSlot seeds a series carrying tmdbID plus an OCCUPIED
// S01E01 slot whose file lives outside the scan root, so the walk never sees it
// and the only proposal produced is the incoming duplicate.
func seedTVDBFallbackTrackedSlot(t *testing.T, libStore *library.Store, root, title string, tmdbID int) {
	t.Helper()
	ctx := context.Background()
	trackedPath := filepath.Join(t.TempDir(), "already-tracked.mkv")
	if err := os.WriteFile(trackedPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("seeding tracked file: %v", err)
	}
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: tmdbID, Title: title, RootFolderPath: root})
	if err != nil {
		t.Fatalf("UpsertSeries: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedPath,
	}); err != nil {
		t.Fatalf("UpsertEpisode: %v", err)
	}
}

// TestScanLibrarySeries_TVDBFallbackStrongDuplicateIsPendingAlternate is plan
// §9.5's SITE 3 test (rename.go's tvdbFallbackSeries `accept` closure, reached
// on CorroborationStrong).
//
// Corroboration is forced STRONG the cheapest honest way: the filename's year
// (2020) equals the candidate year TheTVDB supplies, so SignalsCorroborate's
// yearMatched branch fires with no duration signal in play at all (the prober
// is nil, so sig.DurationSec is 0 — which also makes the weak branch, whose
// only route is yearMismatch + duration override, structurally unreachable
// here). That is this test's own proof that it is at Site 3 and not Site 4.
//
// The D3 assertion (§5.2.1) is the point of the title checks: the RETIRED
// Unmatched string named TheTVDB's bestName, while the emitted row carries
// TMDB's det.Title. A row showing two different show names is the specific
// regression these assertions exist to catch — so the fixture gives TheTVDB and
// TMDB deliberately different names and the reason is checked for both the
// presence of one and the absence of the other.
func TestScanLibrarySeries_TVDBFallbackStrongDuplicateIsPendingAlternate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	orphan := filepath.Join(root, "Cosmic Drift 2020 S01E01.mkv")
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatalf("seeding orphan: %v", err)
	}

	const (
		tvdbName = "Cosmic Drift: The TVDB Listing"
		tmdbName = "Cosmic Drift Chronicles"
	)
	tmdbClient, _ := fakeTMDBTVDBFallbackServer(t, []tvdbFallbackCandidate{
		{TVDBID: 900, TMDBID: 4200, TMDBName: tmdbName, EpisodeRuntimeMin: 30},
	})
	tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{
		{ID: 900, Name: tvdbName, Year: "2020"},
	})
	sess := &mode.Session{Mode: mode.Series, TMDB: tmdbClient, TVDB: tvdbClient}
	libStore := newTestLibraryStore(t)
	seedTVDBFallbackTrackedSlot(t, libStore, root, tmdbName, 4200)

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending-as-alternate — Site 3 must not hard-decline a tracked slot",
			p.Status, p.Reason)
	}
	if p.TMDBID != 4200 {
		t.Errorf("TMDBID = %d, want 4200 (resolved from TheTVDB id 900)", p.TMDBID)
	}
	if p.Title != tmdbName {
		t.Errorf("Title = %q, want TMDB's %q (deviation D3 — never TheTVDB's bestName)", p.Title, tmdbName)
	}
	wantPrefix := fmt.Sprintf("alternate: %q S01E01 already has a file in the library", tmdbName)
	if !strings.HasPrefix(p.Reason, wantPrefix) {
		t.Errorf("reason = %q, want prefix %q", p.Reason, wantPrefix)
	}
	if strings.Contains(p.Reason, "TVDB Listing") {
		t.Errorf("reason names TheTVDB's series name as well as TMDB's — that is the two-show-names-on-one-row regression D3 forbids: %q", p.Reason)
	}
}

// TestScanLibrarySeries_TVDBFallbackWeakDuplicateIsPendingAlternate is plan
// §9.5's SITE 4 test — tvdbFallbackSeries' POST-LOOP `weak != nil` branch,
// which fires only when NO candidate reached CorroborationStrong.
//
// HOW THE WEAK RANK IS FORCED: SignalsCorroborate returns Weak on exactly one
// combination — the filename year DISAGREES with the candidate year and the
// probed duration overrides it by matching the candidate's episode runtime. So
// candidate #1 is published with year 1999 against a 2020 filename, and the
// prober reports 1800s against a 30-minute episode.
//
// HOW THIS TEST PROVES IT REACHED SITE 4 AND NOT SITE 3 — the two-step argument
// §9.5 demands, stated in full because it is exactly what a future reader will
// "simplify" away:
//
//	(a) /find/{911} was requested, so the candidate loop walked PAST candidate
//	    #1. The strong branch returns from inside the loop, so had #1 corroborated
//	    strongly, #2 would never have been looked up at all.
//	(b) the emitted row carries candidate #1's TMDB title. The strong branch can
//	    only ever emit the title of the candidate it returned on — and by (a)
//	    that could only have been #2, whose title is different.
//
// Together those leave the post-loop weak branch as the only path that can
// produce this row. Candidate #2 is arranged to corroborate NONE (its runtime
// disagrees with the probe as well as its year), so under the Site-3
// counterfactual the scan would emit no TVDB-fallback row at all rather than a
// merely-different one — a strictly louder failure.
func TestScanLibrarySeries_TVDBFallbackWeakDuplicateIsPendingAlternate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	orphan := filepath.Join(root, "Cosmic Drift 2020 S01E01.mkv")
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatalf("seeding orphan: %v", err)
	}

	const (
		firstTVDBName  = "Cosmic Drift: The TVDB Listing"
		firstTMDBName  = "Cosmic Drift Origins"
		secondTVDBName = "Cosmic Drift: A Second TVDB Listing"
		secondTMDBName = "Cosmic Drift Reborn"
	)
	tmdbClient, finds := fakeTMDBTVDBFallbackServer(t, []tvdbFallbackCandidate{
		// #1 — year disagrees (1999 vs the filename's 2020), runtime matches the
		// probe: CorroborationWeak, stashed as the fallback.
		{TVDBID: 910, TMDBID: 4201, TMDBName: firstTMDBName, EpisodeRuntimeMin: 30},
		// #2 — year disagrees AND runtime disagrees: CorroborationNone, skipped.
		{TVDBID: 911, TMDBID: 4202, TMDBName: secondTMDBName, EpisodeRuntimeMin: 45},
	})
	tvdbClient, _ := fakeTVDBAnthologyServer(t, []fakeTVDBAnthologyShow{
		{ID: 910, Name: firstTVDBName, Year: "1999"},
		{ID: 911, Name: secondTVDBName, Year: "1998"},
	})
	sess := &mode.Session{Mode: mode.Series, TMDB: tmdbClient, TVDB: tvdbClient}
	libStore := newTestLibraryStore(t)
	seedTVDBFallbackTrackedSlot(t, libStore, root, firstTMDBName, 4201)

	// 1800s against candidate #1's 30-minute episode is an exact match, so the
	// tolerance percentage plays no part in whether this test passes.
	prober := mapProber{orphan: {Duration: 1800, Height: 720, CodecName: "h264", BitRate: 2_000_000}}

	got, err := ScanLibrarySeries(ctx, sess, libStore, root, naming.Jellyfin, DefaultMatchConfig(), prober)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]

	// (a) the loop walked past candidate #1 — so #1 did not return via the
	// strong branch.
	if n := finds[911].Load(); n != 1 {
		t.Fatalf("/find/911 was requested %d times, want 1 — the candidate loop returned early, so this row came from the STRONG branch (Site 3), not the weak one (Site 4)", n)
	}
	// (b) the row carries candidate #1's title, which the strong branch could
	// not have produced given (a).
	if p.Title != firstTMDBName {
		t.Fatalf("Title = %q, want candidate #1's %q — combined with (a) this row can only have come from the post-loop weak branch", p.Title, firstTMDBName)
	}
	if p.TMDBID != 4201 {
		t.Errorf("TMDBID = %d, want 4201 (the weak candidate)", p.TMDBID)
	}

	if p.Status != proposals.Pending {
		t.Fatalf("status = %v (%s), want Pending-as-alternate — Site 4 must not hard-decline a tracked slot",
			p.Status, p.Reason)
	}
	wantPrefix := fmt.Sprintf("alternate: %q S01E01 already has a file in the library", firstTMDBName)
	if !strings.HasPrefix(p.Reason, wantPrefix) {
		t.Errorf("reason = %q, want prefix %q (deviation D3 — weak.det.Title, never bestName)", p.Reason, wantPrefix)
	}
	if strings.Contains(p.Reason, "TVDB Listing") {
		t.Errorf("reason names TheTVDB's series name as well as TMDB's — the two-show-names-on-one-row regression D3 forbids: %q", p.Reason)
	}
}
