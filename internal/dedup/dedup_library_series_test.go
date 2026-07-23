package dedup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// fakeTMDBSeriesSearch stands in for TMDB's /search/tv endpoint — results
// keyed by the exact query string expected, same convention
// fakeTMDBSearch (Movies) already uses for /search/movie.
func fakeTMDBSeriesSearch(t *testing.T, results map[string]string) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		term := r.URL.Query().Get("query")
		body, ok := results[term]
		if !ok {
			t.Fatalf("unexpected search term %q", term)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

func TestScanLibrarySeries_RequiresTMDBConfigured(t *testing.T) {
	sess := &mode.Session{Mode: mode.Series}
	if _, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), t.TempDir(), &fakeProber{}, &fakePHasher{}, 10); err == nil {
		t.Fatal("expected an error when TMDB isn't configured")
	}
}

func TestScanLibrarySeries_RequiresRootFolderPath(t *testing.T) {
	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesSearch(t, nil)}
	if _, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), "", &fakeProber{}, &fakePHasher{}, 10); err == nil {
		t.Fatal("expected an error when no root folder path is configured")
	}
}

func TestScanLibrarySeries_TrackedEpisodePlusOrphan_ProposesWithCorrectWinner(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01.mkv", 100)
	orphanFile := writeVideoFile(t, dir, "Show.Name.S01E01.1080p.BluRay.x264-GROUP.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesSearch(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	})}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}

	got, err := ScanLibrarySeries(ctx, sess, libStore, dir, prober, matchingPHasher(trackedFile, orphanFile), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 duplicate group, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.TMDBID != 555 || p.SeasonNumber != 1 || p.EpisodeNumber != 1 || len(p.Candidates) != 2 {
		t.Fatalf("unexpected proposal: %+v", p)
	}

	var winner, loser proposals.Candidate
	for _, c := range p.Candidates {
		if c.Winner {
			winner = c
		} else {
			loser = c
		}
	}
	if winner.Path != orphanFile {
		t.Errorf("expected the higher-resolution orphan to win, got winner=%+v", winner)
	}
	if loser.Path != trackedFile || loser.TrackedID != int(tracked.ID) {
		t.Errorf("expected the tracked episode to be the loser, got %+v", loser)
	}
}

// TestScanLibrarySeries_DiscoversDuplicateEpisodeAlongsideAlreadyTrackedOne
// proves ScanRootFolder's recursion: once a season folder has one
// already-tracked episode file inside it, the folder is no longer atomic —
// a duplicate episode file dropped in beside it surfaces individually and
// gets grouped as a duplicate, rather than being masked by the whole
// "Show Name/Season 01/" subtree having previously been marked known.
func TestScanLibrarySeries_DiscoversDuplicateEpisodeAlongsideAlreadyTrackedOne(t *testing.T) {
	dir := t.TempDir()
	seasonDir := filepath.Join(dir, "Show Name", "Season 01")
	trackedFile := writeVideoFile(t, seasonDir, "Show Name - S01E01.mkv", 100)
	orphanFile := writeVideoFile(t, seasonDir, "Show.Name.S01E01.1080p.BluRay.x264-GROUP.mkv", 100)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: trackedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesSearch(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	})}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}

	got, err := ScanLibrarySeries(ctx, sess, libStore, dir, prober, matchingPHasher(trackedFile, orphanFile), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected the sibling duplicate dropped alongside the tracked episode to be discovered, got %+v", got)
	}
	var loser proposals.Candidate
	for _, c := range got[0].Candidates {
		if !c.Winner {
			loser = c
		}
	}
	if loser.Path != trackedFile || loser.TrackedID != int(tracked.ID) {
		t.Errorf("expected the tracked episode to be the loser, got %+v", loser)
	}
}

// TestScanLibrarySeries_SeasonPackOrphanMatchesExistingSingleEpisodeDuplicate
// is the concrete proof of the grouping-key design: a season-pack orphan
// directory is broken into individual files, and the one matching an
// already-tracked episode's (show, season, episode) groups with it —
// exactly like a loose single-episode orphan would.
func TestScanLibrarySeries_SeasonPackOrphanMatchesExistingSingleEpisodeDuplicate(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, filepath.Join(dir, "Show Name", "Season 01"), "Show Name - S01E01.mkv", 100)
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

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesSearch(t, map[string]string{
		"Show Name": `{"results":[{"id":555,"name":"Show Name"}]}`,
	})}
	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		packEp1:     {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
		packEp2:     {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}

	got, err := ScanLibrarySeries(ctx, sess, libStore, dir, prober, matchingPHasher(trackedFile, packEp1, packEp2), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Episode 1 duplicates the tracked copy (2 candidates); episode 2 is a
	// lone new orphan from the pack (no duplicate, not reported).
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 duplicate group (episode 1 only), got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.SeasonNumber != 1 || p.EpisodeNumber != 1 || len(p.Candidates) != 2 {
		t.Fatalf("unexpected proposal: %+v", p)
	}
	var sawPackFile, sawTrackedFile bool
	for _, c := range p.Candidates {
		if c.Path == packEp1 {
			sawPackFile = true
		}
		if c.Path == trackedFile {
			sawTrackedFile = true
		}
	}
	if !sawPackFile || !sawTrackedFile {
		t.Fatalf("expected the season-pack's episode 1 file to group with the tracked episode 1, got %+v", p.Candidates)
	}
}

func TestScanLibrarySeries_SingleNewOrphanEpisodeIsNotADuplicate(t *testing.T) {
	dir := t.TempDir()
	writeVideoFile(t, dir, "New.Show.S01E01.mkv", 100)

	sess := &mode.Session{Mode: mode.Series, TMDB: fakeTMDBSeriesSearch(t, map[string]string{
		"New Show": `{"results":[{"id":777,"name":"New Show"}]}`,
	})}

	got, err := ScanLibrarySeries(context.Background(), sess, newTestLibraryStore(t), dir, &fakeProber{}, &fakePHasher{}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no duplicate groups for a single new episode, got %+v", got)
	}
}

func TestApplyLibrarySeries_KeepsWinnerByDefault_DeletesOrphanLoser(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeVideoFile(t, dir, "loser.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "X", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", FilePath: "/winner.mkv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "X", TMDBID: 1, SeasonNumber: 1, EpisodeNumber: 1,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: "/winner.mkv", TrackedID: int(tracked.ID), Winner: true},
			{Label: "loser", Path: loserPath},
		},
	}
	id, changes, err := ApplyLibrarySeries(ctx, libStore, p, nil, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != tracked.ID {
		t.Errorf("expected the already-tracked winner's episode id (%d), got %d", tracked.ID, id)
	}
	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Error("expected the losing orphan file to be deleted")
	}
	ep, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.FilePath != "/winner.mkv" || ep.Title != "Pilot" {
		t.Errorf("expected the episode row untouched, got %+v", ep)
	}
	// The winner didn't move, so only the loser's exact candidate path
	// shows up in changes.
	if len(changes) != 1 || changes[0].Path != loserPath || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for %q, got %+v", loserPath, changes)
	}
}

func TestApplyLibrarySeries_WinnerIsOrphan_DeletesTrackedLoserFile_UpsertsSameEpisodeRow(t *testing.T) {
	dir := t.TempDir()
	trackedFile := writeVideoFile(t, dir, "tracked.mkv", 10)
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", AirDate: "2020-01-01", FilePath: trackedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1,
		RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "tracked", Path: trackedFile, TrackedID: int(tracked.ID)},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	id, changes, err := ApplyLibrarySeries(ctx, libStore, p, nil, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == 0 {
		t.Error("expected a nonzero episode id for the newly registered winner")
	}
	// Same episode row (same id), not a fresh one — the slot's content was
	// overwritten, nothing was ever deleted.
	if id != tracked.ID {
		t.Errorf("expected the same episode row id to be reused (%d), got %d", tracked.ID, id)
	}
	if _, err := os.Stat(trackedFile); !os.IsNotExist(err) {
		t.Error("expected the losing tracked file to be deleted")
	}

	ep, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.FilePath != winnerPath {
		t.Errorf("expected the episode row's file path updated to the winner, got %+v", ep)
	}
	// Existing metadata (from a prior Sonarr import/Rename scan) is
	// preserved, not blanked, even though this Apply call only supplied a
	// file path.
	if ep.Title != "Pilot" || ep.AirDate != "2020-01-01" {
		t.Errorf("expected existing episode metadata preserved, got %+v", ep)
	}
	// Row 8 (player-rescan-notify plan): the removed loser's candidate path
	// (c.Path) is reported. The winner's slot was overwritten in place, not
	// moved, so it never appears in changes.
	if len(changes) != 1 || changes[0].Path != trackedFile || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for %q, got %+v", trackedFile, changes)
	}
}

// TestApplyLibrarySeries_SharedFileLosesItsOwnKey_NotDeleted_SiblingIntact is
// the critical regression test for the logical-episode-splitting
// correctness fix: episode 1 and episode 2 of the same series share ONE
// physical file (a "S01E01-E02" split). Dedup's episode-1 dedup group finds
// a better standalone copy of episode 1 elsewhere, so the shared file LOSES
// its own key's comparison. Before the fix, ApplyLibrarySeries would
// unconditionally os.Remove the loser — deleting the shared file while
// episode 2's row still pointed at it, a live "no drift" mission violation
// (see CLAUDE.md's Mission section and dedup.go's ApplyLibrarySeries doc
// comment). This test proves: the shared file survives on disk, episode 2's
// row is completely untouched, and episode 1's row is still correctly
// updated to the winner.
func TestApplyLibrarySeries_SharedFileLosesItsOwnKey_NotDeleted_SiblingIntact(t *testing.T) {
	dir := t.TempDir()
	sharedFile := writeVideoFile(t, dir, "Show.S01E01-E02.mkv", 10)
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Episode 1 AND episode 2 both point at the exact same file — the
	// logical-episode-split scenario library.CountEpisodesByFilePath exists
	// to detect.
	ep1, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Part One", FilePath: sharedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep2Before, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, Title: "Part Two", AirDate: "2020-01-08", FilePath: sharedFile,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Dedup proposal for EPISODE 1's key only — the shared file is the
	// tracked/losing candidate, a standalone orphan copy is the winner.
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1,
		RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "tracked", Path: sharedFile, TrackedID: int(ep1.ID)},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	_, changes, err := ApplyLibrarySeries(ctx, libStore, p, nil, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The critical assertion: the shared file must survive on disk, even
	// though it lost episode 1's dedup comparison, because episode 2's row
	// still needs it.
	if _, err := os.Stat(sharedFile); err != nil {
		t.Fatalf("expected the shared file to SURVIVE (still referenced by episode 2), but stat failed: %v", err)
	}
	// No Deleted PathChange should be reported for a file that was never
	// actually deleted.
	for _, c := range changes {
		if c.Path == sharedFile && c.Kind == mode.Deleted {
			t.Errorf("expected no Deleted PathChange for the still-referenced shared file, got %+v", changes)
		}
	}

	// Episode 2's row must be completely untouched — same file path, same
	// metadata, same row id.
	ep2After, err := libStore.GetEpisode(ctx, series.ID, 1, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep2After.ID != ep2Before.ID || ep2After.FilePath != sharedFile {
		t.Fatalf("expected episode 2's row to be completely untouched, got %+v (was %+v)", ep2After, ep2Before)
	}
	if ep2After.Title != "Part Two" || ep2After.AirDate != "2020-01-08" {
		t.Errorf("expected episode 2's metadata untouched, got %+v", ep2After)
	}

	// Episode 1's row is still correctly updated to the winner — the fix
	// doesn't mean episode 1's OWN dedup resolution stops working, only
	// that the shared file's physical deletion is what's guarded.
	ep1After, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep1After.FilePath != winnerPath {
		t.Errorf("expected episode 1's row updated to the winner path, got %+v", ep1After)
	}
}

// TestApplyLibrarySeries_SharedFileGuardIsPathBased_NotCandidateLabelBased
// closes a follow-up flagged during pre-merge review: the shared-file guard
// (CountEpisodesByFilePath) is a pure function of the candidate's PATH
// STRING, not of how that candidate was labeled/discovered. This proves it
// protects a shared file even when it arrives as a plain, non-"tracked"-
// labeled losing candidate (TrackedID=0) — not just the "tracked" shape the
// other regression test already covers. (Note: a shared file surfacing as
// an actual Dedup-scan-discovered ORPHAN is not reachable in practice —
// ScanLibrarySeries's `known` set masks every already-tracked FilePath from
// ever being reported as an unmapped/orphan entry in the first place,
// regardless of which key it would parse to — but ApplyLibrarySeries
// itself makes no such assumption about how its Candidates arrived, so this
// test exercises that generality directly.)
func TestApplyLibrarySeries_SharedFileGuardIsPathBased_NotCandidateLabelBased(t *testing.T) {
	dir := t.TempDir()
	sharedFile := writeVideoFile(t, dir, "Show.S01E01-E02.mkv", 10)
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 555, Title: "Show Name", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: sharedFile,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: sharedFile,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The shared path arrives as a PLAIN candidate (TrackedID=0, not labeled
	// "tracked") — a shape that wouldn't occur via today's real Scan, but
	// which ApplyLibrarySeries must still guard correctly, since its guard
	// keys purely on Path.
	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Show Name", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1,
		RootFolderPath: dir,
		Candidates: []proposals.Candidate{
			{Label: "some-orphan-name", Path: sharedFile},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	if _, changes, err := ApplyLibrarySeries(ctx, libStore, p, nil, nil, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	} else {
		for _, c := range changes {
			if c.Path == sharedFile && c.Kind == mode.Deleted {
				t.Errorf("expected no Deleted PathChange for the still-referenced shared file, got %+v", changes)
			}
		}
	}

	if _, err := os.Stat(sharedFile); err != nil {
		t.Fatalf("expected the shared file to survive regardless of candidate labeling, but stat failed: %v", err)
	}
	ep2, err := libStore.GetEpisode(ctx, series.ID, 1, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep2.FilePath != sharedFile {
		t.Errorf("expected episode 2's row untouched, got %+v", ep2)
	}
}

func TestApplyLibrarySeries_KeepAll_NoMutation(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "X", RootFolderPath: "/x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: "/a.mkv",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending,
		Candidates: []proposals.Candidate{
			{Label: "a", Path: "/a.mkv", TrackedID: int(tracked.ID)},
			{Label: "b", Path: "/b.mkv"},
		},
	}
	id, changes, err := ApplyLibrarySeries(ctx, libStore, p, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != tracked.ID {
		t.Errorf("expected keepAll to still report the existing tracked episode id, got %d", id)
	}
	if _, err := libStore.GetEpisode(ctx, series.ID, 1, 1); err != nil {
		t.Errorf("expected keepAll to leave the episode row untouched, got err=%v", err)
	}
	// Edge #3 (player-rescan-notify plan): keepAll removes nothing, so it
	// must report zero PathChanges.
	if len(changes) != 0 {
		t.Errorf("expected keepAll to report zero PathChanges, got %+v", changes)
	}
}

func TestApplyLibrarySeries_RejectsNonPendingProposal(t *testing.T) {
	libStore := newTestLibraryStore(t)
	p := proposals.Proposal{
		Status:     proposals.Applied,
		Candidates: []proposals.Candidate{{Path: "/a.mkv"}, {Path: "/b.mkv"}},
	}
	if _, _, err := ApplyLibrarySeries(context.Background(), libStore, p, nil, nil, false); err == nil {
		t.Fatal("expected ApplyLibrarySeries to refuse an already-applied proposal")
	}
}

func TestApplyLibrarySeries_RejectsFewerThanTwoCandidates(t *testing.T) {
	libStore := newTestLibraryStore(t)
	p := proposals.Proposal{Status: proposals.Pending, Candidates: []proposals.Candidate{{Path: "/a.mkv"}}}
	if _, _, err := ApplyLibrarySeries(context.Background(), libStore, p, nil, nil, false); err == nil {
		t.Fatal("expected ApplyLibrarySeries to refuse a proposal with fewer than 2 candidates")
	}
}
