package proposals

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/mode"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file —
// exercising the actual SQL, not a mock, matching every other store test in
// this repo.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
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
				{Label: "tracked", Path: "/media/Movies/Movie A/a.mkv", TrackedID: 9, PHash: "phash64/5f:aa11"},
				{Label: "Movie.A.1080p", Path: "/media/Movies/Movie.A.1080p/b.mkv", PHash: "phash64/5f:aa12", Winner: true},
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
	if len(got.Candidates) != 2 || got.Candidates[0].PHash != "phash64/5f:aa11" || got.Candidates[1].PHash != "phash64/5f:aa12" {
		t.Fatalf("expected candidate phashes to round-trip from candidates_json, got %+v", got.Candidates)
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
