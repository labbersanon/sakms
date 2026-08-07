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
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/bravesearch"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/ollama"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/tmdb"
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

func TestScanLibrarySeries_SkipsAlreadyTrackedWithFile(t *testing.T) {
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
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected the duplicate to surface as unmatched, got %+v", got)
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

// TestScanLibrarySeries_LooseParentDirParseCollisionMarksSecondClaimantUnmatched
// is the code-reviewer MEDIUM-finding regression test: ResolveEpisodeVideoFiles
// returns every video file in a directory with no filter, and
// ParseEpisodeNumbersLoose resolves EVERY file in a marker-named parent
// directory to the SAME season/episode (there is only one parent dir to fall
// back to). Two video files sharing a parent dir (a main file plus a
// sample/featurette, say) would both resolve to an identical
// (tmdbID, season, episode) and both land on Pending — the second Apply
// would silently overwrite the first's library.Episode row. The within-batch
// collision guard in ScanLibrarySeries must catch this: exactly one file
// (in scan order) reaches Pending, and the other is marked Unmatched with a
// clear reason instead.
func TestScanLibrarySeries_LooseParentDirParseCollisionMarksSecondClaimantUnmatched(t *testing.T) {
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
			if !strings.Contains(p.Reason, "already claims") {
				t.Errorf("expected a within-batch collision reason, got %q", p.Reason)
			}
		default:
			t.Errorf("unexpected status %v on proposal: %+v", p.Status, p)
		}
	}
	if pendingCount != 1 || unmatchedCount != 1 {
		t.Fatalf("expected exactly 1 Pending + 1 Unmatched (collision), got %d Pending, %d Unmatched: %+v", pendingCount, unmatchedCount, got)
	}
	// The first-resolved file (scan order) must be the one that wins Pending.
	if got[0].SourcePath != firstPath || got[0].Status != proposals.Pending {
		t.Errorf("expected the first-scanned file (%q) to win Pending, got %+v", firstPath, got[0])
	}
	if got[1].SourcePath != secondPath || got[1].Status != proposals.Unmatched {
		t.Errorf("expected the second-scanned file (%q) to be marked Unmatched, got %+v", secondPath, got[1])
	}
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
	epID, changes, err := ApplyLibrarySeries(ctx, libStore, nil, p, naming.Jellyfin, "")
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
	epID, changes, err := ApplyLibrarySeries(ctx, libStore, nil, p, naming.Jellyfin, "medium")
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
	epID, changes, err := ApplyLibrarySeries(ctx, libStore, nil, p, naming.Jellyfin, "")
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
	if _, _, err := ApplyLibrarySeries(ctx, libStore, nil, p, naming.Legacy, ""); err != nil {
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
	epID, _, err := ApplyLibrarySeries(ctx, libStore, tmdbClient, p, naming.Jellyfin, "")
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
		if _, _, err := ApplyLibrarySeries(context.Background(), libStore, nil, proposals.Proposal{Status: status}, naming.Jellyfin, ""); err == nil {
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

// TestScanLibrarySeries_EpisodeTitleMatch_AlreadyTrackedSlotStaysUnmatched is
// case F (plan §2.5): a title match that would land on a slot ALREADY
// tracked with a file must Unmatch, never Pending — otherwise Apply would
// silently orphan the existing tracked file via UpsertEpisodes' unique
// constraint.
func TestScanLibrarySeries_EpisodeTitleMatch_AlreadyTrackedSlotStaysUnmatched(t *testing.T) {
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
	if p.Status != proposals.Unmatched {
		t.Fatalf("expected Unmatched — never Pending — onto an already-tracked slot, got %+v", p)
	}
	wantPrefix := fmt.Sprintf("appears to already be in the library as %q S02E02", redSkeltonShowTitle)
	if !strings.HasPrefix(p.Reason, wantPrefix) {
		t.Errorf("expected the already-tracked reason (prefix %q), got %q", wantPrefix, p.Reason)
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
