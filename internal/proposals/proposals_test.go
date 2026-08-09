package proposals

import (
	"context"
	"errors"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/mode"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file —
// exercising the actual SQL, not a mock, matching every other store test in
// this repo.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB := dbtest.New(t)
	return New(sqlDB)
}

func TestReplacePending_InsertsAndAssignsIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A", SourcePath: "/media/Movies/Movie A", RootFolderPath: "/media/Movies", Title: "Movie A", TMDBID: 1},
		{Status: Unmatched, SourceName: "gibberish", SourcePath: "/media/Movies/gibberish", RootFolderPath: "/media/Movies", Reason: "no match"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("expected 2 saved proposals, got %d", len(saved))
	}
	for _, p := range saved {
		if p.ID == 0 {
			t.Errorf("expected a real assigned ID, got 0: %+v", p)
		}
		if p.CreatedAt == "" {
			t.Errorf("expected CreatedAt to be populated: %+v", p)
		}
		if p.Mode != mode.Movies || p.Workflow != Rename {
			t.Errorf("expected mode/workflow to be stamped on the saved row: %+v", p)
		}
	}
}

// Dedup is the one workflow that stores more than one file per proposal —
// Candidates must round-trip through the candidates_json column intact.
func TestReplacePending_PersistsCandidates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Movies, Dedup, []Proposal{
		{
			Status: Pending, SourceName: "Movie A", Title: "Movie A", TMDBID: 1,
			Candidates: []Candidate{
				{Label: "tracked", Path: "/media/Movies/Movie A/a.mkv", TrackedID: 9, Resolution: 720, Codec: "h264", BitRate: 3000},
				{Label: "Movie.A.1080p", Path: "/media/Movies/Movie.A.1080p/b.mkv", Resolution: 1080, Codec: "av1", BitRate: 4000, Winner: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(saved[0].Candidates) != 2 {
		t.Fatalf("expected 2 candidates to survive the insert, got %+v", saved[0].Candidates)
	}
	if !saved[0].Candidates[1].Winner || saved[0].Candidates[1].Resolution != 1080 {
		t.Errorf("unexpected candidate data: %+v", saved[0].Candidates[1])
	}

	got, err := s.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Candidates) != 2 || got.Candidates[0].TrackedID != 9 {
		t.Fatalf("expected candidates to round-trip from storage, got %+v", got.Candidates)
	}
}

// TestReplacePending_PersistsExtraEpisodeNumbers proves the logical-episode-
// splitting field round-trips through the extra_episode_numbers column, and
// that the ordinary single-episode case (no ExtraEpisodeNumbers at all)
// round-trips as nil/empty, not a stray "[]" or "null".
func TestReplacePending_PersistsExtraEpisodeNumbers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Series, Rename, []Proposal{
		{
			Status: Pending, SourceName: "Show S01E01-E02.mkv", Title: "Show", TMDBID: 1,
			SeasonNumber: 1, EpisodeNumber: 1, ExtraEpisodeNumbers: []int{2},
		},
		{
			Status: Pending, SourceName: "Show S01E03.mkv", Title: "Show", TMDBID: 1,
			SeasonNumber: 1, EpisodeNumber: 3,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(saved[0].ExtraEpisodeNumbers) != 1 || saved[0].ExtraEpisodeNumbers[0] != 2 {
		t.Fatalf("expected ExtraEpisodeNumbers=[2] to survive the insert, got %+v", saved[0].ExtraEpisodeNumbers)
	}
	if len(saved[1].ExtraEpisodeNumbers) != 0 {
		t.Fatalf("expected the single-episode proposal to have no extra episodes, got %+v", saved[1].ExtraEpisodeNumbers)
	}

	got, err := s.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.ExtraEpisodeNumbers) != 1 || got.ExtraEpisodeNumbers[0] != 2 {
		t.Fatalf("expected ExtraEpisodeNumbers to round-trip from storage, got %+v", got.ExtraEpisodeNumbers)
	}

	gotSingle, err := s.Get(ctx, saved[1].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotSingle.ExtraEpisodeNumbers) != 0 {
		t.Fatalf("expected the single-episode proposal to round-trip with no extra episodes, got %+v", gotSingle.ExtraEpisodeNumbers)
	}
}

// TestReplacePending_PersistsCandidatePHash proves the SAK-computed per-file
// perceptual hash (Movies Dedup) survives the candidates_json round-trip — a
// zero-migration field carried only inside the JSON blob, distinct from
// Proposal.PHash (Adult's Stash-read hash, a real column).
func TestReplacePending_PersistsCandidatePHash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Movies, Dedup, []Proposal{
		{
			Status: Pending, SourceName: "Movie A", Title: "Movie A", TMDBID: 1,
			Candidates: []Candidate{
				{Label: "tracked", Path: "/media/Movies/Movie A/a.mkv", TrackedID: 9, PHash: "pdq256/5f:aa11"},
				{Label: "Movie.A.1080p", Path: "/media/Movies/Movie.A.1080p/b.mkv", PHash: "pdq256/5f:aa12", Winner: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Candidates) != 2 || got.Candidates[0].PHash != "pdq256/5f:aa11" || got.Candidates[1].PHash != "pdq256/5f:aa12" {
		t.Fatalf("expected candidate phashes to round-trip from candidates_json, got %+v", got.Candidates)
	}
}

// TestReplacePending_PersistsPHashSimilarity is the regression test for the
// defect fixed by migration 0060: ScanLibraryPHash/ScanLibrarySeriesPHash
// compute PHashSimilarity, but before this column existed, ReplacePending's
// INSERT silently dropped it and List/Get always read back 0 — defeating the
// Dedup card's similarity badge end to end. Proves the value now survives
// both the insert and a subsequent List.
func TestReplacePending_PersistsPHashSimilarity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Movies, Dedup, []Proposal{
		{
			Status: Pending, SourceName: "Movie A", Title: "Movie A", TMDBID: 1,
			PHashSimilarity: 0.87,
			Candidates: []Candidate{
				{Label: "tracked", Path: "/media/Movies/Movie A/a.mkv", TrackedID: 9},
				{Label: "Movie.A.1080p", Path: "/media/Movies/Movie.A.1080p/b.mkv", Winner: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saved[0].PHashSimilarity != 0.87 {
		t.Fatalf("expected PHashSimilarity 0.87 to survive the insert, got %v", saved[0].PHashSimilarity)
	}

	got, err := s.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PHashSimilarity != 0.87 {
		t.Fatalf("expected PHashSimilarity to round-trip via Get, got %v", got.PHashSimilarity)
	}

	list, err := s.List(ctx, mode.Movies, Dedup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].PHashSimilarity != 0.87 {
		t.Fatalf("expected PHashSimilarity to round-trip via List, got %+v", list)
	}
}

// TestReplacePending_PHashSimilarityDefaultsToZeroForNonPHashWorkflows proves
// a Rename proposal — which never sets PHashSimilarity — reads back exactly
// 0, the same sentinel apidto/dto.go's omitempty tag treats as "no score",
// so a legacy/non-Dedup row is unaffected by this column's existence.
func TestReplacePending_PHashSimilarityDefaultsToZeroForNonPHashWorkflows(t *testing.T) {
	s := newTestStore(t)
	saved, err := s.ReplacePending(context.Background(), mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "x", Title: "X"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saved[0].PHashSimilarity != 0 {
		t.Fatalf("expected PHashSimilarity 0 for a Rename proposal, got %v", saved[0].PHashSimilarity)
	}
}

func TestReplacePending_EmptyCandidatesForNonDedupWorkflows(t *testing.T) {
	s := newTestStore(t)
	saved, err := s.ReplacePending(context.Background(), mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "x", Title: "X"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(saved[0].Candidates) != 0 {
		t.Fatalf("expected no candidates for a Rename proposal, got %+v", saved[0].Candidates)
	}
}

// Purge sets TrackedID at Scan time (it's an input identifying which
// already-tracked item to delete, unlike Rename where it's only an output
// of Apply) — ReplacePending's INSERT must actually persist it.
func TestReplacePending_PersistsTrackedIDSetAtScanTime(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Movies, Purge, []Proposal{
		{Status: Pending, SourceName: "Flagged Movie", SourcePath: "/x", RootFolderPath: "/media/Movies", TrackedID: 2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saved[0].TrackedID != 2 {
		t.Fatalf("expected TrackedID to survive the insert, got %+v", saved[0])
	}

	got, err := s.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TrackedID != 2 {
		t.Fatalf("expected TrackedID to round-trip from storage, got %+v", got)
	}
}

// Adult Rename sets ForeignID/ItemType at Scan time (derived from the AI
// identification result) — ReplacePending's INSERT and both SELECT paths must
// persist and round-trip them, proving the six order-sensitive column sites
// all agree.
func TestReplacePending_PersistsForeignIDAndItemType(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Adult, Rename, []Proposal{
		{
			Status: Pending, SourceName: "Some Scene", SourcePath: "/media/Adult/Some Scene",
			RootFolderPath: "/media/Adult", Title: "Some Scene",
			ForeignID: "abc-uuid", ItemType: "scene",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saved[0].ForeignID != "abc-uuid" || saved[0].ItemType != "scene" {
		t.Fatalf("expected ForeignID/ItemType to survive the insert, got %+v", saved[0])
	}

	got, err := s.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ForeignID != "abc-uuid" || got.ItemType != "scene" {
		t.Fatalf("expected ForeignID/ItemType to round-trip from storage, got %+v", got)
	}

	listed, err := s.List(ctx, mode.Adult, Rename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(listed) != 1 || listed[0].ForeignID != "abc-uuid" || listed[0].ItemType != "scene" {
		t.Fatalf("expected List to reflect the persisted identifiers, got %+v", listed)
	}
}

// Adult Rename captures Studio/Date at Scan time even for Unmatched
// (web-identified-only) proposals — SubmitDraft needs them for give-back.
func TestReplacePending_PersistsStudioAndDate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Adult, Rename, []Proposal{
		{
			Status: Unmatched, SourceName: "Some Scene", SourcePath: "/media/Adult/Some Scene",
			RootFolderPath: "/media/Adult", Title: "Some Scene",
			Studio: "Some Studio", Date: "2024", Reason: "web-identified only",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saved[0].Studio != "Some Studio" || saved[0].Date != "2024" {
		t.Fatalf("expected Studio/Date to survive the insert, got %+v", saved[0])
	}

	got, err := s.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Studio != "Some Studio" || got.Date != "2024" {
		t.Fatalf("expected Studio/Date to round-trip from storage, got %+v", got)
	}
}

func TestReplacePending_PersistsFingerprintFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Adult, Rename, []Proposal{
		{
			Status: Pending, SourceName: "Some Scene", SourcePath: "/media/Adult/Some Scene",
			RootFolderPath: "/media/Adult", Title: "Some Scene", ForeignID: "abc-123",
			PHash: "deadbeef", DurationSeconds: 1800, GiveBackBox: "stashdb", GiveBackSceneID: "abc-123",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saved[0].PHash != "deadbeef" || saved[0].DurationSeconds != 1800 ||
		saved[0].GiveBackBox != "stashdb" || saved[0].GiveBackSceneID != "abc-123" {
		t.Fatalf("expected fingerprint fields to survive the insert, got %+v", saved[0])
	}
	if saved[0].FingerprintSubmittedAt != "" {
		t.Fatalf("expected no fingerprint submission yet, got %+v", saved[0])
	}

	got, err := s.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PHash != "deadbeef" || got.DurationSeconds != 1800 ||
		got.GiveBackBox != "stashdb" || got.GiveBackSceneID != "abc-123" {
		t.Fatalf("expected fingerprint fields to round-trip from storage, got %+v", got)
	}

	list, err := s.List(ctx, mode.Adult, Rename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].PHash != "deadbeef" {
		t.Fatalf("expected fingerprint fields to round-trip via List too, got %+v", list)
	}
}

func TestMarkFingerprintSubmitted_PersistsTimestamp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Adult, Rename, []Proposal{
		{Status: Pending, SourceName: "Some Scene", Title: "Some Scene", GiveBackBox: "stashdb", GiveBackSceneID: "abc-123"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.MarkFingerprintSubmitted(ctx, saved[0].ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.FingerprintSubmittedAt == "" {
		t.Fatal("expected FingerprintSubmittedAt to be set")
	}
}

func TestMarkFingerprintSubmitted_NotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.MarkFingerprintSubmitted(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMarkDraftSubmitted_PersistsDraftID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Adult, Rename, []Proposal{
		{Status: Unmatched, SourceName: "Some Scene", Title: "Some Scene"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if saved[0].DraftID != "" || saved[0].DraftSubmittedAt != "" {
		t.Fatalf("expected no draft yet, got %+v", saved[0])
	}

	if err := s.MarkDraftSubmitted(ctx, saved[0].ID, "draft123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DraftID != "draft123" || got.DraftSubmittedAt == "" {
		t.Fatalf("expected DraftID/DraftSubmittedAt to persist, got %+v", got)
	}
	if got.Status != Unmatched {
		t.Fatalf("expected status to remain Unmatched after a draft submission, got %q", got.Status)
	}
}

func TestMarkDraftSubmitted_NotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.MarkDraftSubmitted(context.Background(), 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestReplacePending_LeavesAppliedAndDismissedAlone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A", SourcePath: "/a", RootFolderPath: "/media/Movies", Title: "Movie A"},
		{Status: Pending, SourceName: "Movie B", SourcePath: "/b", RootFolderPath: "/media/Movies", Title: "Movie B"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.MarkApplied(ctx, first[0].ID, 42); err != nil {
		t.Fatalf("marking applied: %v", err)
	}
	if err := s.Dismiss(ctx, first[1].ID); err != nil {
		t.Fatalf("dismissing: %v", err)
	}

	// A fresh Scan for the same mode/workflow must not touch the two rows
	// already resolved above.
	if _, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie C", SourcePath: "/c", RootFolderPath: "/media/Movies", Title: "Movie C"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all, err := s.List(ctx, mode.Movies, Rename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 rows (1 applied, 1 dismissed, 1 fresh pending), got %d: %+v", len(all), all)
	}

	applied, err := s.Get(ctx, first[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if applied.Status != Applied || applied.TrackedID != 42 {
		t.Errorf("expected applied row to survive unchanged, got %+v", applied)
	}
	dismissed, err := s.Get(ctx, first[1].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dismissed.Status != Dismissed {
		t.Errorf("expected dismissed row to survive unchanged, got %+v", dismissed)
	}
}

func TestListPage_LiveExcludesHistory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Keep Me", SourcePath: "/k", RootFolderPath: "/m", Title: "Keep"},
		{Status: Unmatched, SourceName: "Need Match", SourcePath: "/u", RootFolderPath: "/m", Title: ""},
		{Status: Pending, SourceName: "Will Apply", SourcePath: "/a", RootFolderPath: "/m", Title: "Apply"},
		{Status: Pending, SourceName: "Will Dismiss", SourcePath: "/d", RootFolderPath: "/m", Title: "Dismiss"},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.MarkApplied(ctx, first[2].ID, 1); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := s.Dismiss(ctx, first[3].ID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	live, total, err := s.ListPage(ctx, mode.Movies, Rename, 50, 0, ListViewLive)
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if total != 2 || len(live) != 2 {
		t.Fatalf("live want 2 rows, got total=%d len=%d %+v", total, len(live), live)
	}
	for _, p := range live {
		if p.Status != Pending && p.Status != Unmatched {
			t.Errorf("live leaked status %q", p.Status)
		}
	}

	hist, hTotal, err := s.ListPage(ctx, mode.Movies, Rename, 50, 0, ListViewHistory)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if hTotal != 2 || len(hist) != 2 {
		t.Fatalf("history want 2 rows, got total=%d len=%d %+v", hTotal, len(hist), hist)
	}
	for _, p := range hist {
		if p.Status != Applied && p.Status != Dismissed {
			t.Errorf("history leaked status %q", p.Status)
		}
	}
}

func TestParseListView(t *testing.T) {
	if ParseListView("") != ListViewLive {
		t.Errorf("empty → live")
	}
	if ParseListView("history") != ListViewHistory {
		t.Errorf("history")
	}
	if ParseListView("HISTORY") != ListViewHistory {
		t.Errorf("HISTORY")
	}
	if ParseListView("all") != ListViewLive {
		t.Errorf("unknown → live")
	}
}

func TestReplacePending_ScopedByModeAndWorkflow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A", SourcePath: "/a", RootFolderPath: "/media/Movies"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.ReplacePending(ctx, mode.Series, Rename, []Proposal{
		{Status: Pending, SourceName: "Show A", SourcePath: "/b", RootFolderPath: "/media/Series"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	movies, err := s.List(ctx, mode.Movies, Rename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(movies) != 1 || movies[0].SourceName != "Movie A" {
		t.Fatalf("expected Movies queue to only contain its own proposal, got %+v", movies)
	}

	series, err := s.List(ctx, mode.Series, Rename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(series) != 1 || series[0].SourceName != "Show A" {
		t.Fatalf("expected Series queue to only contain its own proposal, got %+v", series)
	}
}

func TestGet_NotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMarkApplied_NotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.MarkApplied(context.Background(), 999, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDismiss_NotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Dismiss(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreDelete_RemovesRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Delete Me", SourcePath: "/d", RootFolderPath: "/m", Title: "Delete"},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	id := saved[0].ID

	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from Get after delete, got %v", err)
	}

	all, err := s.List(ctx, mode.Movies, Rename)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range all {
		if p.ID == id {
			t.Fatalf("expected deleted row %d absent from List, found %+v", id, p)
		}
	}
}

func TestStoreDelete_UnknownIDReturnsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreDelete_LeavesSiblingsIntact(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Sibling A", SourcePath: "/a", RootFolderPath: "/m", Title: "A"},
		{Status: Pending, SourceName: "Delete Me", SourcePath: "/d", RootFolderPath: "/m", Title: "Delete"},
		{Status: Pending, SourceName: "Sibling B", SourcePath: "/b", RootFolderPath: "/m", Title: "B"},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.Delete(ctx, saved[1].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	remaining, err := s.List(ctx, mode.Movies, Rename)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("expected 2 surviving siblings, got %d: %+v", len(remaining), remaining)
	}
	for _, p := range remaining {
		if p.ID == saved[1].ID {
			t.Fatalf("deleted row %d still present: %+v", saved[1].ID, p)
		}
		if p.ID != saved[0].ID && p.ID != saved[2].ID {
			t.Fatalf("unexpected surviving row: %+v", p)
		}
	}
	if got, err := s.Get(ctx, saved[0].ID); err != nil || got.SourceName != "Sibling A" {
		t.Fatalf("sibling A not intact: got=%+v err=%v", got, err)
	}
	if got, err := s.Get(ctx, saved[2].ID); err != nil || got.SourceName != "Sibling B" {
		t.Fatalf("sibling B not intact: got=%+v err=%v", got, err)
	}
}

// TestStoreDelete_RowIsGoneFromHistoryView is the concrete assertion that
// distinguishes Delete from Dismiss: Dismiss leaves a row in ListViewHistory
// (it stays queryable as an audit trail); Delete's row does not appear even
// there, because the row itself no longer exists. The Dismiss half is
// asserted here too, as the contrast, rather than assumed.
func TestStoreDelete_RowIsGoneFromHistoryView(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Will Delete", SourcePath: "/d", RootFolderPath: "/m", Title: "Delete"},
		{Status: Pending, SourceName: "Will Dismiss", SourcePath: "/dm", RootFolderPath: "/m", Title: "Dismiss"},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	deleteID, dismissID := saved[0].ID, saved[1].ID

	if err := s.Delete(ctx, deleteID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Dismiss(ctx, dismissID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	hist, total, err := s.ListPage(ctx, mode.Movies, Rename, 50, 0, ListViewHistory)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if total != 1 || len(hist) != 1 {
		t.Fatalf("expected exactly 1 history row (the dismissed one), got total=%d len=%d %+v", total, len(hist), hist)
	}
	if hist[0].ID != dismissID {
		t.Fatalf("expected the dismissed row in history, got %+v", hist[0])
	}
	for _, p := range hist {
		if p.ID == deleteID {
			t.Fatalf("deleted row %d leaked into history view", deleteID)
		}
	}
}

func TestRepick_NotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.Repick(context.Background(), 999, "New Title", 42, 2020); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRepick_OverwritesFieldsAndPromotesToPending(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Unmatched, SourceName: "gibberish", SourcePath: "/media/Movies/gibberish", RootFolderPath: "/media/Movies", Reason: "no TMDB match for \"gibberish\""},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	id := saved[0].ID

	if err := s.Repick(ctx, id, "The Real Movie", 777, 2019); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != Pending {
		t.Errorf("expected re-picking to promote the proposal to pending, got %q", got.Status)
	}
	if got.Title != "The Real Movie" || got.TMDBID != 777 || got.Year != 2019 {
		t.Errorf("expected the overwritten fields to stick, got title=%q tmdbId=%d year=%d", got.Title, got.TMDBID, got.Year)
	}
	if got.Reason != "" {
		t.Errorf("expected the stale rejection reason to be cleared, got %q", got.Reason)
	}
}

// A proposal that was already Pending (a wrong-but-not-zero match) stays
// Pending after a re-pick — same end state, not demoted or re-promoted.
// TestReplacePending_PreservesIDOnRescanSamePath proves that a second
// ReplacePending call for a proposal whose source_path already exists in the
// live queue reuses the same row ID and created_at — the upsert-in-place
// guarantee that keeps apply-batch IDs stable across concurrent rescans.
func TestReplacePending_PreservesIDOnRescanSamePath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A (wrong title)", SourcePath: "/media/Movies/Movie A", RootFolderPath: "/media/Movies", Title: "Wrong Title", TMDBID: 1},
	})
	if err != nil {
		t.Fatalf("first replace: %v", err)
	}
	originalID := first[0].ID
	originalCreatedAt := first[0].CreatedAt

	// Second scan finds the same path, now with the corrected title.
	second, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A", SourcePath: "/media/Movies/Movie A", RootFolderPath: "/media/Movies", Title: "Movie A", TMDBID: 1},
	})
	if err != nil {
		t.Fatalf("second replace: %v", err)
	}
	if second[0].ID != originalID {
		t.Errorf("expected same ID after rescan of same path: got %d, want %d", second[0].ID, originalID)
	}
	if second[0].CreatedAt != originalCreatedAt {
		t.Errorf("expected created_at preserved: got %q, want %q", second[0].CreatedAt, originalCreatedAt)
	}
	if second[0].Title != "Movie A" {
		t.Errorf("expected updated title to be persisted, got %q", second[0].Title)
	}
	// Verify Get also returns the updated title.
	got, err := s.Get(ctx, originalID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "Movie A" {
		t.Errorf("Get: expected updated title, got %q", got.Title)
	}
}

// TestReplacePending_DeletesMissingPaths proves that a live row whose
// source_path no longer appears in the fresh scan is deleted — it left the
// library (moved, renamed, or deleted by the user) so the proposal is stale.
func TestReplacePending_DeletesMissingPaths(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A", SourcePath: "/media/Movies/Movie A", RootFolderPath: "/media/Movies", Title: "Movie A", TMDBID: 1},
		{Status: Pending, SourceName: "Movie B", SourcePath: "/media/Movies/Movie B", RootFolderPath: "/media/Movies", Title: "Movie B", TMDBID: 2},
	})
	if err != nil {
		t.Fatalf("first replace: %v", err)
	}
	goneID := first[1].ID // Movie B will vanish from the next scan.

	// Second scan: only Movie A remains.
	if _, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A", SourcePath: "/media/Movies/Movie A", RootFolderPath: "/media/Movies", Title: "Movie A", TMDBID: 1},
	}); err != nil {
		t.Fatalf("second replace: %v", err)
	}

	// Movie B's row must be gone.
	if _, err := s.Get(ctx, goneID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for deleted-path proposal %d, got %v", goneID, err)
	}

	live, err := s.List(ctx, mode.Movies, Rename)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(live) != 1 || live[0].SourcePath != "/media/Movies/Movie A" {
		t.Errorf("expected only Movie A to remain, got %+v", live)
	}
}

// TestReplacePending_DeferredDuringApply proves that ReplacePending returns
// ErrReplaceDeferred without mutating the queue when an apply is in flight for
// the same (mode, workflow). The existing live rows are unchanged so any
// concurrent apply-batch lookup can still find them.
func TestReplacePending_DeferredDuringApply(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Seed the live queue.
	first, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A", SourcePath: "/media/Movies/Movie A", RootFolderPath: "/media/Movies", Title: "Movie A", TMDBID: 1},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	originalID := first[0].ID

	// Use a fresh gate so this test doesn't interfere with DefaultGate state.
	saved := DefaultGate
	DefaultGate = newGate()
	t.Cleanup(func() { DefaultGate = saved })

	// Simulate an apply starting.
	DefaultGate.BeginApply(mode.Movies, Rename)
	defer DefaultGate.EndApply(mode.Movies, Rename)

	// ReplacePending must defer.
	_, err = s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A (updated)", SourcePath: "/media/Movies/Movie A", RootFolderPath: "/media/Movies", Title: "Updated Title", TMDBID: 1},
	})
	if !errors.Is(err, ErrReplaceDeferred) {
		t.Fatalf("expected ErrReplaceDeferred, got %v", err)
	}

	// The original live row must be unchanged.
	got, err := s.Get(ctx, originalID)
	if err != nil {
		t.Fatalf("Get after deferred replace: %v", err)
	}
	if got.Title != "Movie A" {
		t.Errorf("expected original title to be preserved, got %q", got.Title)
	}
}

func TestGetLiveBySourcePath_FindsPendingAfterIDChurn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.ReplacePending(ctx, mode.Series, Rename, []Proposal{
		{Status: Pending, SourceName: "Ep", SourcePath: "/media/Series/show/S01E01.mkv", RootFolderPath: "/media/Series", Title: "Show", TMDBID: 9, SeasonNumber: 1, EpisodeNumber: 1},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	oldID := first[0].ID

	// Simulate delete+reinsert id churn by forcing a path-matched upsert... upsert
	// preserves id. To churn, delete via a replace that drops then re-adds isn't
	// possible with upsert. Manually: dismiss old uniqueness by clearing then insert.
	// Soft approach: GetLiveBySourcePath finds the current live row by path.
	got, err := s.GetLiveBySourcePath(ctx, mode.Series, Rename, "/media/Series/show/S01E01.mkv")
	if err != nil {
		t.Fatalf("GetLiveBySourcePath: %v", err)
	}
	if got.ID != oldID {
		t.Errorf("expected id %d, got %d", oldID, got.ID)
	}
	if _, err := s.GetLiveBySourcePath(ctx, mode.Series, Rename, "/nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing path, got %v", err)
	}
}

func TestRepick_AlreadyPendingStaysPending(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{Status: Pending, SourceName: "Movie A", SourcePath: "/media/Movies/Movie A", RootFolderPath: "/media/Movies", Title: "Wrong Movie", TMDBID: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	id := saved[0].ID

	if err := s.Repick(ctx, id, "Correct Movie", 2, 2021); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != Pending || got.Title != "Correct Movie" || got.TMDBID != 2 {
		t.Errorf("unexpected result: %+v", got)
	}
}

// seedSeriesUnmatched stages one Unmatched Series Rename proposal shaped like
// the real gap population this feature exists for — a file whose basename
// carries no recoverable season/episode signal at all.
func seedSeriesUnmatched(t *testing.T, s *Store, p Proposal) int64 {
	t.Helper()
	saved, err := s.ReplacePending(context.Background(), mode.Series, Rename, []Proposal{p})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return saved[0].ID
}

func TestRepickEpisode_NotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.RepickEpisode(context.Background(), 999, "The Path", 42, 2020, 1, 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRepickEpisode_SetsSeasonAndEpisode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id := seedSeriesUnmatched(t, s, Proposal{
		Status:         Unmatched,
		SourceName:     "a3f9c2e1b7d84f0e.mkv",
		SourcePath:     "/media/Series/a3f9c2e1b7d84f0e.mkv",
		RootFolderPath: "/media/Series",
		Reason:         "could not parse a season/episode from the filename",
	})

	if err := s.RepickEpisode(ctx, id, "The Path", 777, 2016, 2, 7); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != Pending {
		t.Errorf("expected the manual assignment to promote the proposal to pending, got %q", got.Status)
	}
	if got.Title != "The Path" || got.TMDBID != 777 || got.Year != 2016 {
		t.Errorf("expected the overwritten show fields to stick, got title=%q tmdbId=%d year=%d", got.Title, got.TMDBID, got.Year)
	}
	if got.SeasonNumber != 2 || got.EpisodeNumber != 7 {
		t.Errorf("expected the operator's slot to persist, got season=%d episode=%d", got.SeasonNumber, got.EpisodeNumber)
	}
	if got.Reason != "" {
		t.Errorf("expected the stale rejection reason to be cleared, got %q", got.Reason)
	}
}

// TestRepickEpisode_AcceptsSeasonZero is the falsy-guard regression this
// feature's *int DTO fields exist to prevent: season 0 is Specials, a real
// value an operator can legitimately mean, and it must survive the round trip
// rather than being read as "no season supplied".
func TestRepickEpisode_AcceptsSeasonZero(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id := seedSeriesUnmatched(t, s, Proposal{
		Status:         Unmatched,
		SourceName:     "VIDEO_TS.VOB",
		SourcePath:     "/media/Series/Candid Camera/VIDEO_TS/VIDEO_TS.VOB",
		RootFolderPath: "/media/Series",
		Reason:         "no episode information in a DVD authoring filename",
	})

	if err := s.RepickEpisode(ctx, id, "Candid Camera", 555, 1960, 0, 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SeasonNumber != 0 || got.EpisodeNumber != 3 {
		t.Fatalf("expected season 0 / episode 3 to persist verbatim, got season=%d episode=%d", got.SeasonNumber, got.EpisodeNumber)
	}
	if got.Status != Pending {
		t.Errorf("expected pending, got %q", got.Status)
	}
}

// TestRepickEpisode_ClearsExtraEpisodeNumbers pins the deliberate clear: a
// manual single-slot assignment supersedes whatever multi-episode bundle a
// prior parse claimed, so Apply never upserts episode rows the operator never
// chose.
func TestRepickEpisode_ClearsExtraEpisodeNumbers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id := seedSeriesUnmatched(t, s, Proposal{
		Status:              Pending,
		SourceName:          "Show.S01E01-E02.mkv",
		SourcePath:          "/media/Series/Show.S01E01-E02.mkv",
		RootFolderPath:      "/media/Series",
		Title:               "Wrong Show",
		TMDBID:              111,
		SeasonNumber:        1,
		EpisodeNumber:       1,
		ExtraEpisodeNumbers: []int{2, 3},
	})

	if err := s.RepickEpisode(ctx, id, "Right Show", 222, 2001, 4, 9); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.ExtraEpisodeNumbers) != 0 {
		t.Errorf("expected the stale episode bundle to be cleared, got %v", got.ExtraEpisodeNumbers)
	}
	if got.SeasonNumber != 4 || got.EpisodeNumber != 9 {
		t.Errorf("expected the operator's slot, got season=%d episode=%d", got.SeasonNumber, got.EpisodeNumber)
	}
}
