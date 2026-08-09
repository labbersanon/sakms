package proposals

// Store-level tests for MoveMode — the W2a slice of the cross-mode
// "move to another section" feature (.omc/plans/autopilot-impl.md §4.2,
// §4.2a, §4.2b). These cover the store-level slice of the plan's §7 test
// table rows for AC1/AC3/AC4/AC6 and the "Additional tests" table's
// TrackedID/retirement rows — everything reachable without the HTTP
// handler, which is a different wave's scope.

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

// TestMoveMode_WritesModeAndMetadataInOneUpdate is the store-level slice of
// AC1: a single MoveMode call writes the target mode plus its metadata, and
// nothing else about the row (its id, created_at) changes underneath it.
func TestMoveMode_WritesModeAndMetadataInOneUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{
			Status: Pending, SourceName: "Movie A", SourcePath: "/media/Movies/Movie A",
			RootFolderPath: "/media/Movies", Title: "Movie A", TMDBID: 1, Year: 2020,
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}
	id := saved[0].ID

	season, episode := 2, 7
	target := MoveTarget{
		Mode: mode.Series, RootFolderPath: "/media/Series",
		Title: "A Show", TMDBID: 42, Year: 2021,
		SeasonNumber: season, EpisodeNumber: episode,
	}
	if err := s.MoveMode(ctx, id, target, nil); err != nil {
		t.Fatalf("MoveMode: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after move: %v", err)
	}
	if got.ID != id {
		t.Errorf("expected id to stay %d, got %d", id, got.ID)
	}
	if got.Mode != mode.Series {
		t.Errorf("expected mode=series, got %q", got.Mode)
	}
	if got.RootFolderPath != "/media/Series" {
		t.Errorf("expected root_folder_path to move with the mode, got %q", got.RootFolderPath)
	}
	if got.Title != "A Show" || got.TMDBID != 42 || got.Year != 2021 {
		t.Errorf("expected target metadata to be written, got title=%q tmdbId=%d year=%d", got.Title, got.TMDBID, got.Year)
	}
	if got.SeasonNumber != season || got.EpisodeNumber != episode {
		t.Errorf("expected season=%d episode=%d, got season=%d episode=%d", season, episode, got.SeasonNumber, got.EpisodeNumber)
	}
	if got.Status != Pending {
		t.Errorf("expected status to stay pending, got %q", got.Status)
	}
	// source_name/source_path are deliberately preserved by the UPDATE (the
	// file has not moved on disk yet) — see the MoveMode doc comment and
	// D-1a in the plan.
	if got.SourceName != "Movie A" || got.SourcePath != "/media/Movies/Movie A" {
		t.Errorf("expected source_name/source_path to be preserved, got %q / %q", got.SourceName, got.SourcePath)
	}
}

// TestMoveMode_ClearsSourceCatalogFields proves the other half of AC1/AC2:
// every field belonging to the mode being LEFT is cleared, not just the
// target fields being written. Uses an Adult proposal moved to Movies so the
// Adult-only fields (studio/scene_date/give_back_*) have real non-zero values
// to prove got cleared, and gives it a draft/fingerprint stamp to prove the
// give-back provenance stamps are cleared too (§4.2's "the one place that
// deviates from ReplacePending's preservation" note).
func TestMoveMode_ClearsSourceCatalogFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Adult, Rename, []Proposal{
		{
			Status: Pending, SourceName: "scene.mp4", SourcePath: "/media/Adult/scene.mp4",
			RootFolderPath: "/media/Adult", Title: "A Scene", Studio: "Studio X", Date: "2022-01-01",
			GiveBackBox: "stashdb", GiveBackSceneID: "abc-123",
			ForeignID: "abc-123", ItemType: "scene",
			PHash: "phash:deadbeef", DurationSeconds: 1800,
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}
	id := saved[0].ID

	if err := s.MarkDraftSubmitted(ctx, id, "draft-1"); err != nil {
		t.Fatalf("MarkDraftSubmitted: %v", err)
	}
	if err := s.MarkFingerprintSubmitted(ctx, id); err != nil {
		t.Fatalf("MarkFingerprintSubmitted: %v", err)
	}

	target := MoveTarget{Mode: mode.Movies, RootFolderPath: "/media/Movies", Title: "Movie A", TMDBID: 9, Year: 2019}
	if err := s.MoveMode(ctx, id, target, nil); err != nil {
		t.Fatalf("MoveMode: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after move: %v", err)
	}
	if got.Mode != mode.Movies {
		t.Fatalf("expected mode=movies, got %q", got.Mode)
	}
	if got.Studio != "" || got.Date != "" {
		t.Errorf("expected studio/date cleared, got studio=%q date=%q", got.Studio, got.Date)
	}
	if got.GiveBackBox != "" || got.GiveBackSceneID != "" {
		t.Errorf("expected give_back_box/give_back_scene_id cleared, got %q / %q", got.GiveBackBox, got.GiveBackSceneID)
	}
	if got.PHash != "" || got.DurationSeconds != 0 {
		t.Errorf("expected phash/duration_seconds cleared, got phash=%q duration=%d", got.PHash, got.DurationSeconds)
	}
	if got.DraftID != "" || got.DraftSubmittedAt != "" {
		t.Errorf("expected draft_id/draft_submitted_at cleared (moved-away give-back provenance), got draftId=%q draftSubmittedAt=%q", got.DraftID, got.DraftSubmittedAt)
	}
	if got.FingerprintSubmittedAt != "" {
		t.Errorf("expected fingerprint_submitted_at cleared, got %q", got.FingerprintSubmittedAt)
	}
	if got.TVDBID != 0 {
		t.Errorf("expected tvdb_id cleared, got %d", got.TVDBID)
	}
}

// TestMoveMode_ZeroesCandidateTrackedIDs is the §4.2a CRITICAL regression
// test: every candidate's TrackedID must become 0 across a move (or Apply can
// os.Remove an unrelated file — see the plan). Group membership, ordering,
// and every OTHER candidate field must stay byte-identical.
func TestMoveMode_ZeroesCandidateTrackedIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Adult, Dedup, []Proposal{
		{
			Status: Pending, SourceName: "Dup Group", Title: "Dup Group",
			TrackedID: 42, // top-level tracked_id — must ALSO be zeroed (§3/§4.2a)
			Candidates: []Candidate{
				{Label: "tracked", Path: "/media/Adult/a.mp4", TrackedID: 42, Resolution: 1080, Codec: "h264", BitRate: 5000, PHash: "p1", Winner: true},
				{Label: "orphan", Path: "/media/Adult/b.mp4", TrackedID: 0, Resolution: 720, Codec: "av1", BitRate: 2500, PHash: "p2", Winner: false},
				{Label: "another-tracked-elsewhere", Path: "/media/Adult/c.mp4", TrackedID: 99, Resolution: 480, Codec: "hevc", BitRate: 1200, PHash: "p3", Winner: false},
			},
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}
	id := saved[0].ID
	original := saved[0].Candidates

	// The caller (the handler, in this feature's real wiring) is responsible
	// for zeroing TrackedID before calling MoveMode — §4.2a. Build that copy
	// here the same way the handler will.
	zeroed := make([]Candidate, len(original))
	for i, c := range original {
		c.TrackedID = 0
		zeroed[i] = c
	}

	target := MoveTarget{
		Mode: mode.Movies, RootFolderPath: "/media/Movies", Title: "Dup Group Movie", TMDBID: 5,
		Candidates: zeroed,
	}
	if err := s.MoveMode(ctx, id, target, nil); err != nil {
		t.Fatalf("MoveMode: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after move: %v", err)
	}
	if got.TrackedID != 0 {
		t.Errorf("expected top-level tracked_id to be zeroed, got %d", got.TrackedID)
	}
	if len(got.Candidates) != len(original) {
		t.Fatalf("expected %d candidates to survive the move, got %d: %+v", len(original), len(got.Candidates), got.Candidates)
	}
	for i, c := range got.Candidates {
		want := original[i]
		if c.TrackedID != 0 {
			t.Errorf("candidate %d (%s): expected TrackedID zeroed, got %d", i, c.Label, c.TrackedID)
		}
		if c.Label != want.Label || c.Path != want.Path || c.Resolution != want.Resolution ||
			c.Codec != want.Codec || c.BitRate != want.BitRate || c.PHash != want.PHash || c.Winner != want.Winner {
			t.Errorf("candidate %d: file metadata not byte-identical after move — got %+v, want (same but TrackedID=0) %+v", i, c, want)
		}
	}
}

// TestMoveMode_RetireCallbackRunsInSameTransaction is the §4.2b atomicity
// proof: force ErrModeMoveConflict on the UPDATE and assert the retire
// callback's effects did NOT persist — proving retirement and the mode
// UPDATE share one transaction, not merely "run in order". A separate-step
// design would have already committed the retirement by the time the UPDATE
// fails, which is the exact silent-data-loss bug §4.2b documents.
func TestMoveMode_RetireCallbackRunsInSameTransaction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// The proposal being moved: a live Rename row at a given source_path.
	moving, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{
			Status: Pending, SourceName: "Movie A", SourcePath: "/media/shared/movie-a.mkv",
			RootFolderPath: "/media/Movies", Title: "Movie A", TMDBID: 1,
		},
	})
	if err != nil {
		t.Fatalf("seeding moving proposal: %v", err)
	}
	id := moving[0].ID

	// A pre-existing LIVE proposal already occupying the target mode+workflow
	// at the SAME source_path — this is what proposals_live_source_path_uidx
	// (0004_proposals_live_source_path.sql) will collide on when MoveMode
	// tries to write mode=series onto the row above.
	if _, err := s.ReplacePending(ctx, mode.Series, Rename, []Proposal{
		{
			Status: Pending, SourceName: "Movie A", SourcePath: "/media/shared/movie-a.mkv",
			RootFolderPath: "/media/Series", Title: "Occupying Row", TMDBID: 2,
		},
	}); err != nil {
		t.Fatalf("seeding conflicting live proposal: %v", err)
	}

	// A marker table the retire callback writes to, so the test can observe
	// whether its write survived a rollback — a real *sql.Tx write standing
	// in for §4.2b's DeleteItemTx/DeleteSceneTx/DeleteEpisodeTx (owned by a
	// different wave), scoped to the one property this test needs: does a
	// write made by retire(ctx, tx) roll back with the UPDATE it shares a
	// transaction with.
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS movemode_retire_marker (id bigint PRIMARY KEY)`); err != nil {
		t.Fatalf("creating marker table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.db.ExecContext(context.Background(), `DROP TABLE IF EXISTS movemode_retire_marker`)
	})

	retireRan := false
	retire := RetireFunc(func(ctx context.Context, tx *sql.Tx) error {
		retireRan = true
		_, err := tx.ExecContext(ctx, `INSERT INTO movemode_retire_marker (id) VALUES (1)`)
		return err
	})

	target := MoveTarget{Mode: mode.Series, RootFolderPath: "/media/Series", Title: "Movie A", TMDBID: 1}
	err = s.MoveMode(ctx, id, target, retire)
	if !errors.Is(err, ErrModeMoveConflict) {
		t.Fatalf("expected ErrModeMoveConflict, got %v", err)
	}
	if !retireRan {
		t.Fatal("expected the retire callback to have run before the conflicting UPDATE — MoveMode must call retire() inside the same transaction, before the write that can fail")
	}

	var markerCount int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM movemode_retire_marker`).Scan(&markerCount); err != nil {
		t.Fatalf("counting marker rows: %v", err)
	}
	if markerCount != 0 {
		t.Fatalf("expected the retire callback's write to have been rolled back with the failed UPDATE, but %d marker row(s) survived — retirement and the mode UPDATE are not sharing one transaction", markerCount)
	}

	// And the row itself must be provably untouched: mode unchanged, the
	// moving proposal is still findable by its original identity.
	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after failed move: %v", err)
	}
	if got.Mode != mode.Movies {
		t.Errorf("expected mode to remain movies after a rolled-back move, got %q", got.Mode)
	}
	if got.Title != "Movie A" || got.TMDBID != 1 {
		t.Errorf("expected metadata to remain unchanged after a rolled-back move, got title=%q tmdbId=%d", got.Title, got.TMDBID)
	}
}

// TestMoveMode_RetireCallbackCommitsOnSuccess is the mirror-image case: when
// the UPDATE succeeds, the retire callback's write commits WITH it — proving
// the shared transaction commits together, not just rolls back together.
func TestMoveMode_RetireCallbackCommitsOnSuccess(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Adult, Rename, []Proposal{
		{
			Status: Pending, SourceName: "scene.mp4", SourcePath: "/media/Adult/scene.mp4",
			RootFolderPath: "/media/Adult", Title: "A Scene", TrackedID: 7,
		},
	})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}
	id := saved[0].ID

	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS movemode_retire_marker_ok (id bigint PRIMARY KEY)`); err != nil {
		t.Fatalf("creating marker table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.db.ExecContext(context.Background(), `DROP TABLE IF EXISTS movemode_retire_marker_ok`)
	})

	retire := RetireFunc(func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO movemode_retire_marker_ok (id) VALUES (1)`)
		return err
	})

	target := MoveTarget{Mode: mode.Movies, RootFolderPath: "/media/Movies", Title: "Movie A", TMDBID: 3}
	if err := s.MoveMode(ctx, id, target, retire); err != nil {
		t.Fatalf("MoveMode: %v", err)
	}

	var markerCount int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM movemode_retire_marker_ok`).Scan(&markerCount); err != nil {
		t.Fatalf("counting marker rows: %v", err)
	}
	if markerCount != 1 {
		t.Fatalf("expected the retire callback's write to have committed alongside the successful UPDATE, got %d marker row(s)", markerCount)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after move: %v", err)
	}
	if got.Mode != mode.Movies || got.TrackedID != 0 {
		t.Errorf("expected the move to have committed (mode=movies, tracked_id=0), got mode=%q trackedId=%d", got.Mode, got.TrackedID)
	}
}

// TestMoveMode_NotFound mirrors Repick/RepickEpisode's own not-found
// behaviour (TestRepick_NotFound, TestRepickEpisode_NotFound) for the new
// method.
func TestMoveMode_NotFound(t *testing.T) {
	s := newTestStore(t)
	target := MoveTarget{Mode: mode.Movies, RootFolderPath: "/media/Movies", Title: "Nope", TMDBID: 1}
	if err := s.MoveMode(context.Background(), 999, target, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestMoveMode_ConflictWithLiveTargetProposal is the C-6 store-level
// coverage: moving into a mode+workflow that already has a live proposal at
// the same source_path returns ErrModeMoveConflict, and the moving row's own
// mode is left unchanged (no partial write).
func TestMoveMode_ConflictWithLiveTargetProposal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	moving, err := s.ReplacePending(ctx, mode.Movies, Rename, []Proposal{
		{
			Status: Pending, SourceName: "shared.mkv", SourcePath: "/media/shared.mkv",
			RootFolderPath: "/media/Movies", Title: "Movie A", TMDBID: 1,
		},
	})
	if err != nil {
		t.Fatalf("seeding moving proposal: %v", err)
	}
	if _, err := s.ReplacePending(ctx, mode.Series, Rename, []Proposal{
		{
			Status: Pending, SourceName: "shared.mkv", SourcePath: "/media/shared.mkv",
			RootFolderPath: "/media/Series", Title: "A Show", TMDBID: 2,
		},
	}); err != nil {
		t.Fatalf("seeding conflicting proposal: %v", err)
	}

	target := MoveTarget{Mode: mode.Series, RootFolderPath: "/media/Series", Title: "Movie A", TMDBID: 1}
	err = s.MoveMode(ctx, moving[0].ID, target, nil)
	if !errors.Is(err, ErrModeMoveConflict) {
		t.Fatalf("expected ErrModeMoveConflict, got %v", err)
	}

	got, err := s.Get(ctx, moving[0].ID)
	if err != nil {
		t.Fatalf("Get after conflict: %v", err)
	}
	if got.Mode != mode.Movies {
		t.Errorf("expected mode to remain movies after a conflicting move, got %q", got.Mode)
	}
}

// --- Repick/RepickEpisode regression re-run ---
//
// AC3(b): the existing repick-related store tests in this package must keep
// passing unmodified alongside MoveMode. They already live in
// proposals_test.go (TestRepick_*, TestRepickEpisode_*) and `go test
// ./internal/proposals/...` runs them in the same invocation as this file's
// tests — nothing here duplicates them. This comment exists so a reader of
// movemode_test.go can find where that coverage actually lives without
// searching the whole package.
