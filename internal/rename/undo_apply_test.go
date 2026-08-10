package rename

// undo_apply_test.go — Rename Undo's reversal coverage
// (deep-interview-rename-undo, implementation plan §7).
//
// The three non-negotiable correctness rules each have their own named test,
// deliberately rather than as incidental assertions inside a round-trip test:
//   - size-match file gate  → TestUndoMovies_SizeMismatchRefusesSiblingsFile,
//                             TestUndoMovies_MissingFileStillRevertsRow
//   - shared rows never revert → TestUndoSeries_LeavesSharedSeriesRowUntouched,
//                                TestUndoMovies_LeavesSharedZeroTMDBRowUntouched
//   - consumed_at vs evicted_at → TestUndoEviction_KeepsOnlyDepthActive

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/stashbox"
)

type undoEnv struct {
	db        *sql.DB
	libStore  *library.Store
	propStore *proposals.Store
	undoStore *UndoStore
	root      string
	ctx       context.Context
}

// newUndoEnv builds one isolated database plus the three stores undo needs, and
// registers the archive store as the process-wide default so the real Apply
// functions archive through it. The default is reset on cleanup — leaving a
// closed test database registered would make a later test's Apply log an
// archive error.
func newUndoEnv(t *testing.T, depth DepthFunc) *undoEnv {
	t.Helper()
	db := dbtest.New(t)
	store := NewUndoStore(db, depth)
	SetDefaultUndoStore(store)
	t.Cleanup(func() { SetDefaultUndoStore(nil) })
	return &undoEnv{
		db: db, libStore: library.New(db), propStore: proposals.New(db),
		undoStore: store, root: t.TempDir(), ctx: context.Background(),
	}
}

// seedProposals writes real Pending rows so RestoreSnapshot has something to
// update. ReplacePending clears the live queue for (mode, workflow), so every
// proposal a test needs must be seeded in ONE call.
func (e *undoEnv) seedProposals(t *testing.T, m mode.Mode, in ...proposals.Proposal) []proposals.Proposal {
	t.Helper()
	for i := range in {
		in[i].Status = proposals.Pending
	}
	out, err := e.propStore.ReplacePending(e.ctx, m, proposals.Rename, in)
	if err != nil {
		t.Fatalf("seeding proposals: %v", err)
	}
	return out
}

// undoLatest reverses a proposal the way the HTTP handler does: look up its
// newest active archive entry, then run the reversal.
func (e *undoEnv) undoLatest(t *testing.T, proposalID int64) UndoResult {
	t.Helper()
	entry, err := e.undoStore.LatestActiveFor(e.ctx, proposalID)
	if err != nil {
		t.Fatalf("LatestActiveFor(%d): %v", proposalID, err)
	}
	res, err := UndoApply(e.ctx, e.undoStore, e.libStore, e.propStore, entry)
	if err != nil {
		t.Fatalf("UndoApply(%d): %v", proposalID, err)
	}
	return res
}

func writeFileAt(t *testing.T, path, contents string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func mustNotExist(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s: %q still exists (stat err = %v)", why, path, err)
	}
}

func mustHaveContents(t *testing.T, path, want, why string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: reading %q: %v", why, path, err)
	}
	if string(got) != want {
		t.Errorf("%s: %q holds %q, want %q", why, path, got, want)
	}
}

func mustStatus(t *testing.T, e *undoEnv, id int64, want proposals.Status) {
	t.Helper()
	p, err := e.propStore.Get(e.ctx, id)
	if err != nil {
		t.Fatalf("re-reading proposal %d: %v", id, err)
	}
	if p.Status != want {
		t.Errorf("proposal %d status = %q, want %q", id, p.Status, want)
	}
}

// ---------------------------------------------------------------- Movies ----

// TestUndoMovies_SimpleRoundTrip is the INSERT case: a first Apply for a TMDB
// id inserts the library row, so undo deletes it outright.
func TestUndoMovies_SimpleRoundTrip(t *testing.T) {
	e := newUndoEnv(t, nil)
	src := writeFileAt(t, filepath.Join(e.root, "inbox", "some.release.mkv"), "movie-bytes")

	seeded := e.seedProposals(t, mode.Movies, proposals.Proposal{
		SourceName: "some.release.mkv", SourcePath: src, RootFolderPath: e.root,
		Title: "Arrival", Year: 2016, TMDBID: 329865,
	})
	p := seeded[0]

	itemID, changes, err := ApplyLibrary(e.ctx, e.libStore, p, naming.Jellyfin, "high", nil)
	if err != nil {
		t.Fatalf("ApplyLibrary: %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, p.ID, int(itemID)); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	dest := changes[1].Path
	mustNotExist(t, src, "after Apply the source")

	res := e.undoLatest(t, p.ID)

	if !res.FileRestored {
		t.Fatalf("expected the file to be moved back, got %+v", res)
	}
	if res.RestoredPath != src {
		t.Errorf("restored to %q, want %q", res.RestoredPath, src)
	}
	mustHaveContents(t, src, "movie-bytes", "after undo the source path")
	mustNotExist(t, dest, "after undo the applied destination")

	if _, err := e.libStore.Get(e.ctx, itemID); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("expected the INSERTed library_items row to be deleted, Get returned %v", err)
	}
	mustStatus(t, e, p.ID, proposals.Pending)

	// The entry is consumed — terminal. A second undo finds nothing.
	if _, err := e.undoStore.LatestActiveFor(e.ctx, p.ID); !errors.Is(err, ErrNoUndoEntry) {
		t.Errorf("expected the archive entry to be consumed, LatestActiveFor returned %v", err)
	}
	// consumed_at is set; evicted_at is NOT — undoing must never look like eviction.
	var consumed, evicted sql.NullString
	if err := e.db.QueryRowContext(e.ctx,
		`SELECT consumed_at, evicted_at FROM rename_undo_archive WHERE proposal_id = $1`, p.ID).
		Scan(&consumed, &evicted); err != nil {
		t.Fatalf("reading lifecycle columns: %v", err)
	}
	if !consumed.Valid || consumed.String == "" {
		t.Error("expected consumed_at to be set after an undo")
	}
	if evicted.Valid && evicted.String != "" {
		t.Errorf("undoing an entry must never set evicted_at, got %q", evicted.String)
	}
}

// TestUndoMovies_MissingFileStillRevertsRow is the explicitly approved
// missing-file behaviour (spec Round 5): reverse everything still reversible
// and say plainly that the file was not restored.
func TestUndoMovies_MissingFileStillRevertsRow(t *testing.T) {
	e := newUndoEnv(t, nil)
	src := writeFileAt(t, filepath.Join(e.root, "inbox", "gone.mkv"), "movie-bytes")

	p := e.seedProposals(t, mode.Movies, proposals.Proposal{
		SourceName: "gone.mkv", SourcePath: src, RootFolderPath: e.root,
		Title: "Gone", Year: 2019, TMDBID: 777,
	})[0]

	itemID, changes, err := ApplyLibrary(e.ctx, e.libStore, p, naming.Jellyfin, "high", nil)
	if err != nil {
		t.Fatalf("ApplyLibrary: %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, p.ID, int(itemID)); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	if err := os.Remove(changes[1].Path); err != nil {
		t.Fatalf("removing the applied file: %v", err)
	}

	res := e.undoLatest(t, p.ID)

	if res.FileRestored {
		t.Error("a missing file cannot be restored")
	}
	if !strings.Contains(res.FileMessage, "no file is at") {
		t.Errorf("expected the MISSING-file message, got %q", res.FileMessage)
	}
	if strings.Contains(res.FileMessage, "different file") {
		t.Errorf("the missing-file and different-file messages must not be conflated, got %q", res.FileMessage)
	}
	if _, err := e.libStore.Get(e.ctx, itemID); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("the row must still revert when the file is gone, Get returned %v", err)
	}
	mustStatus(t, e, p.ID, proposals.Pending)
}

// TestUndoMovies_SizeMismatchRefusesSiblingsFile is the plan's §6-step-1 worked
// failure case, end to end and with real Applies.
//
// Proposal A applies simply (its own ViaAlternateFold is FALSE). Proposal B
// then applies for the same TMDB id at a higher probed tier, taking the promote
// branch: it demotes A's file to an alternate name and moves its OWN file into
// the canonical primary path A recorded. Undoing A must not move B's file.
//
// A flag-only gate would fail this test: A's entry says ViaAlternateFold=false,
// so a "warn but proceed" best-effort read would relocate B's live file.
func TestUndoMovies_SizeMismatchRefusesSiblingsFile(t *testing.T) {
	e := newUndoEnv(t, nil)
	srcA := writeFileAt(t, filepath.Join(e.root, "inbox", "a.mkv"), "aaaa")
	srcB := writeFileAt(t, filepath.Join(e.root, "inbox", "b.mkv"), "bbbbbbbbbbbbbbbb")

	seeded := e.seedProposals(t, mode.Movies,
		proposals.Proposal{SourceName: "a.mkv", SourcePath: srcA, RootFolderPath: e.root,
			Title: "Heat", Year: 1995, TMDBID: 949},
		proposals.Proposal{SourceName: "b.mkv", SourcePath: srcB, RootFolderPath: e.root,
			Title: "Heat", Year: 1995, TMDBID: 949},
	)
	pA, pB := seeded[0], seeded[1]

	// A applies simply.
	itemID, changesA, err := ApplyLibrary(e.ctx, e.libStore, pA, naming.Jellyfin, "low", nil)
	if err != nil {
		t.Fatalf("ApplyLibrary(A): %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, pA.ID, int(itemID)); err != nil {
		t.Fatalf("MarkApplied(A): %v", err)
	}
	primaryPath := changesA[1].Path

	// B applies as a strictly higher tier, so it promotes over A's file.
	prober := mapProber{
		primaryPath: {Height: 480, CodecName: "h264", BitRate: 800_000},
		srcB:        {Height: 2160, CodecName: "hevc", BitRate: 40_000_000},
	}
	if _, _, err := ApplyLibrary(e.ctx, e.libStore, pB, naming.Jellyfin, "low", prober); err != nil {
		t.Fatalf("ApplyLibrary(B): %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, pB.ID, int(itemID)); err != nil {
		t.Fatalf("MarkApplied(B): %v", err)
	}
	// B's file now occupies the path A recorded.
	mustHaveContents(t, primaryPath, "bbbbbbbbbbbbbbbb", "after B's promote the canonical primary")

	beforeFiles, err := e.libStore.ListFiles(e.ctx, itemID)
	if err != nil {
		t.Fatalf("listing files before undo: %v", err)
	}
	if len(beforeFiles) != 2 {
		t.Fatalf("expected B's promote to leave 2 file rows, got %d", len(beforeFiles))
	}

	res := e.undoLatest(t, pA.ID)

	if res.FileRestored {
		t.Fatal("undoing A must NOT move B's file")
	}
	if !strings.Contains(res.FileMessage, "a different file now occupies") {
		t.Errorf("expected the DIFFERENT-file message, got %q", res.FileMessage)
	}
	mustHaveContents(t, primaryPath, "bbbbbbbbbbbbbbbb", "after undoing A, B's promoted file")
	mustNotExist(t, srcA, "after undoing A, A's original source path")

	// B's library rows are completely untouched.
	if _, err := e.libStore.Get(e.ctx, itemID); err != nil {
		t.Errorf("undoing A must not delete the row B is now using: %v", err)
	}
	afterFiles, err := e.libStore.ListFiles(e.ctx, itemID)
	if err != nil {
		t.Fatalf("listing files after undo: %v", err)
	}
	if len(afterFiles) != len(beforeFiles) {
		t.Errorf("B's file layout changed: %d rows before, %d after", len(beforeFiles), len(afterFiles))
	}
	if !res.DriftDetected {
		t.Error("expected drift to be reported for the shared row that was left in place")
	}
}

// TestUndoMovies_AlternateFoldNeverMovesAFile covers the belt-and-braces
// short-circuit: an entry that itself went through the promote branch never
// stats anything, never moves anything, and still reverts its row.
func TestUndoMovies_AlternateFoldNeverMovesAFile(t *testing.T) {
	e := newUndoEnv(t, nil)
	libDir := filepath.Join(e.root, "Heat (1995) [tmdbid-949]")
	primary := writeFileAt(t, filepath.Join(libDir, "Heat (1995) [tmdbid-949].mkv"), "old-primary")
	src := writeFileAt(t, filepath.Join(e.root, "inbox", "better.mkv"), "better-bytes")

	existing, err := e.libStore.Upsert(e.ctx, library.Item{
		Mode: mode.Movies, TMDBID: 949, Title: "Heat", Year: 1995,
		FilePath: primary, RootFolderPath: e.root, QualityTier: "low",
	})
	if err != nil {
		t.Fatalf("seeding the existing item: %v", err)
	}

	p := e.seedProposals(t, mode.Movies, proposals.Proposal{
		SourceName: "better.mkv", SourcePath: src, RootFolderPath: e.root,
		Title: "Heat", Year: 1995, TMDBID: 949,
	})[0]

	prober := mapProber{
		primary: {Height: 480, CodecName: "h264", BitRate: 800_000},
		src:     {Height: 2160, CodecName: "hevc", BitRate: 40_000_000},
	}
	itemID, _, err := ApplyLibrary(e.ctx, e.libStore, p, naming.Jellyfin, "low", prober)
	if err != nil {
		t.Fatalf("ApplyLibrary: %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, p.ID, int(itemID)); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	promoted := filepath.Join(libDir, "Heat (1995) [tmdbid-949].mkv")
	mustHaveContents(t, promoted, "better-bytes", "after the promote the canonical primary")

	res := e.undoLatest(t, p.ID)

	if res.FileRestored {
		t.Fatal("an alternate-fold undo must never move a file")
	}
	if !strings.Contains(res.FileMessage, "quality alternate") {
		t.Errorf("expected the alternate-fold message, got %q", res.FileMessage)
	}
	mustHaveContents(t, promoted, "better-bytes", "after undo the promoted file")

	// The row still reverts (plan §7): file_path back to the pre-Apply primary.
	got, err := e.libStore.Get(e.ctx, existing.ID)
	if err != nil {
		t.Fatalf("re-reading the item: %v", err)
	}
	if got.FilePath != primary {
		t.Errorf("expected the row reverted to the pre-Apply primary %q, got %q", primary, got.FilePath)
	}
	if got.QualityTier != "low" {
		t.Errorf("expected the pre-Apply quality tier restored, got %q", got.QualityTier)
	}
	mustStatus(t, e, p.ID, proposals.Pending)
}

// TestUndoMovies_LoseTieAlternateStillMovesFileBack is the SIBLING of the
// promote-branch test directly above, and it exists because that test alone
// gave `viaAlternateFold` false confidence.
//
// applyLibraryAlternate has TWO branches. Only the promote branch renames
// another proposal's already-applied file; on the lose/tie branch the incoming
// file is simply placed under an alternate name and NOTHING else — no other
// file moves, and the shared row's primary path is not rewritten. So undo can
// and must move it back under the ordinary size-match gate.
//
// Hardcoding viaAlternateFold = true for every fold-in (which is what shipped
// before this test existed) made this case silently unrecoverable: the file
// stayed at its alternate name while the proposal was restored to Pending
// pointing at a source path with no file at it, and the result claimed another
// proposal's file had been renamed, which never happened.
func TestUndoMovies_LoseTieAlternateStillMovesFileBack(t *testing.T) {
	e := newUndoEnv(t, nil)
	libDir := filepath.Join(e.root, "Heat (1995) [tmdbid-949]")
	primary := writeFileAt(t, filepath.Join(libDir, "Heat (1995) [tmdbid-949].mkv"), "existing-primary")
	src := writeFileAt(t, filepath.Join(e.root, "inbox", "same-tier.mkv"), "incoming-bytes")

	existing, err := e.libStore.Upsert(e.ctx, library.Item{
		Mode: mode.Movies, TMDBID: 949, Title: "Heat", Year: 1995,
		FilePath: primary, RootFolderPath: e.root, QualityTier: "medium",
	})
	if err != nil {
		t.Fatalf("seeding the existing item: %v", err)
	}

	p := e.seedProposals(t, mode.Movies, proposals.Proposal{
		SourceName: "same-tier.mkv", SourcePath: src, RootFolderPath: e.root,
		Title: "Heat", Year: 1995, TMDBID: 949,
	})[0]

	// EQUAL tier on both sides — the incoming file loses the tie, so the
	// existing primary is kept and the incoming file becomes the alternate.
	same := &mediainfo.Probe{Height: 1080, CodecName: "h264", BitRate: 5_000_000}
	itemID, changes, err := ApplyLibrary(e.ctx, e.libStore, p, naming.Jellyfin, "medium", mapProber{primary: same, src: same})
	if err != nil {
		t.Fatalf("ApplyLibrary: %v", err)
	}
	if itemID != existing.ID {
		t.Fatalf("fixture premise broken: the fold-in must reuse item %d, got %d", existing.ID, itemID)
	}
	if err := e.propStore.MarkApplied(e.ctx, p.ID, int(itemID)); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	alt := lastCreatedPath(changes, "")
	if alt == "" || alt == primary {
		t.Fatalf("fixture premise broken: the incoming file should have landed at an alternate name, got %q", alt)
	}
	mustHaveContents(t, primary, "existing-primary", "the untouched existing primary")
	mustHaveContents(t, alt, "incoming-bytes", "the incoming file at its alternate name")

	res := e.undoLatest(t, p.ID)

	// THE ASSERTION THIS TEST EXISTS FOR.
	if !res.FileRestored {
		t.Fatalf("a lose/tie alternate Apply moved only its OWN file, so undo must move it back; got %q", res.FileMessage)
	}
	if strings.Contains(res.FileMessage, "quality alternate") {
		t.Errorf("the alternate-fold refusal message must not fire on the lose/tie branch, got %q", res.FileMessage)
	}
	mustHaveContents(t, src, "incoming-bytes", "after undo the source path")
	mustNotExist(t, alt, "after undo the alternate-named copy")

	// The other proposal's primary is untouched, as it was throughout.
	mustHaveContents(t, primary, "existing-primary", "after undo the existing primary")
	got, err := e.libStore.Get(e.ctx, existing.ID)
	if err != nil {
		t.Fatalf("re-reading the item: %v", err)
	}
	if got.FilePath != primary {
		t.Errorf("the shared row's primary path must be unchanged, got %q", got.FilePath)
	}
	mustStatus(t, e, p.ID, proposals.Pending)
}

// TestUndoMovies_LeavesSharedZeroTMDBRowUntouched — NON-NEGOTIABLE RULE 2, the
// Movies half. Every unidentified-movie Apply upserts through the one row keyed
// (mode='movies', tmdb_id=0), so undoing one must not touch it.
func TestUndoMovies_LeavesSharedZeroTMDBRowUntouched(t *testing.T) {
	e := newUndoEnv(t, nil)
	srcA := writeFileAt(t, filepath.Join(e.root, "inbox", "first.mkv"), "first-bytes")
	srcB := writeFileAt(t, filepath.Join(e.root, "inbox", "second.mkv"), "second-bytes")

	seeded := e.seedProposals(t, mode.Movies,
		proposals.Proposal{SourceName: "first.mkv", SourcePath: srcA, RootFolderPath: e.root, Title: "First Unknown"},
		proposals.Proposal{SourceName: "second.mkv", SourcePath: srcB, RootFolderPath: e.root, Title: "Second Unknown"},
	)
	pA, pB := seeded[0], seeded[1]
	if pA.TMDBID != 0 || pB.TMDBID != 0 {
		t.Fatalf("fixture must use zero TMDB ids, got %d and %d", pA.TMDBID, pB.TMDBID)
	}

	idA, changesA, err := ApplyLibrary(e.ctx, e.libStore, pA, naming.Jellyfin, "high", nil)
	if err != nil {
		t.Fatalf("ApplyLibrary(A): %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, pA.ID, int(idA)); err != nil {
		t.Fatalf("MarkApplied(A): %v", err)
	}
	idB, _, err := ApplyLibrary(e.ctx, e.libStore, pB, naming.Jellyfin, "high", nil)
	if err != nil {
		t.Fatalf("ApplyLibrary(B): %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, pB.ID, int(idB)); err != nil {
		t.Fatalf("MarkApplied(B): %v", err)
	}
	if idA != idB {
		t.Fatalf("fixture premise broken: both zero-TMDB Applies must land on ONE shared row, got %d and %d", idA, idB)
	}

	res := e.undoLatest(t, pA.ID)

	if !res.FileRestored {
		t.Errorf("A's own file should still be moved back: %q", res.FileMessage)
	}
	mustHaveContents(t, srcA, "first-bytes", "after undo A's source path")
	mustNotExist(t, changesA[1].Path, "after undo A's applied destination")

	if res.RowsReverted != 0 {
		t.Errorf("a zero-TMDBID undo must revert NO library row, got %d", res.RowsReverted)
	}
	shared, err := e.libStore.Get(e.ctx, idA)
	if err != nil {
		t.Fatalf("the shared (movies, tmdb_id=0) row must survive A's undo: %v", err)
	}
	if shared.Title != "Second Unknown" {
		t.Errorf("the shared row must still reflect B's Apply, got title %q", shared.Title)
	}
}

// ---------------------------------------------------------------- Series ----

func seedSeriesEpisodeFile(t *testing.T, root, name string) string {
	t.Helper()
	return writeFileAt(t, filepath.Join(root, "inbox", name), "episode-bytes")
}

// TestUndoSeries_SimpleRoundTrip is the Series INSERT case.
func TestUndoSeries_SimpleRoundTrip(t *testing.T) {
	e := newUndoEnv(t, nil)
	src := seedSeriesEpisodeFile(t, e.root, "Show.Name.S01E01.mkv")

	p := e.seedProposals(t, mode.Series, proposals.Proposal{
		SourceName: "Show.Name.S01E01.mkv", SourcePath: src, RootFolderPath: e.root,
		Title: "Show Name", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1,
	})[0]

	epID, changes, err := ApplyLibrarySeries(e.ctx, e.libStore, nil, nil, p, naming.Jellyfin, "medium", nil)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries: %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, p.ID, int(epID)); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	dest := changes[1].Path

	res := e.undoLatest(t, p.ID)

	if !res.FileRestored {
		t.Fatalf("expected the episode file moved back, got %+v", res)
	}
	mustHaveContents(t, src, "episode-bytes", "after undo the source path")
	mustNotExist(t, dest, "after undo the applied destination")

	if _, err := e.libStore.GetEpisodeByID(e.ctx, epID); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("expected the INSERTed library_episodes row deleted, got %v", err)
	}
	// The shared show row must survive — it is never in touched_rows_snapshot.
	if _, err := e.libStore.GetSeriesByTMDBID(e.ctx, 555); err != nil {
		t.Errorf("undo must never delete the shared library_series row: %v", err)
	}
	mustStatus(t, e, p.ID, proposals.Pending)
}

// TestUndoSeries_LeavesSharedSeriesRowUntouched — NON-NEGOTIABLE RULE 2, the
// Series half, and the exact regression this feature's plan exists to prevent.
//
// Two episodes of one show are applied with DIFFERENT show-level metadata, so
// the second Apply legitimately rewrites the shared library_series row. Undoing
// the FIRST episode must leave that row exactly as the second Apply left it.
func TestUndoSeries_LeavesSharedSeriesRowUntouched(t *testing.T) {
	e := newUndoEnv(t, nil)
	src1 := seedSeriesEpisodeFile(t, e.root, "S01E01.mkv")
	src2 := seedSeriesEpisodeFile(t, e.root, "S01E02.mkv")

	seeded := e.seedProposals(t, mode.Series,
		proposals.Proposal{SourceName: "S01E01.mkv", SourcePath: src1, RootFolderPath: e.root,
			Title: "Old Show Title", Year: 2001, TMDBID: 4242, SeasonNumber: 1, EpisodeNumber: 1},
		proposals.Proposal{SourceName: "S01E02.mkv", SourcePath: src2, RootFolderPath: e.root,
			Title: "Corrected Show Title", Year: 2002, TMDBID: 4242, SeasonNumber: 1, EpisodeNumber: 2},
	)
	p1, p2 := seeded[0], seeded[1]

	ep1, _, err := ApplyLibrarySeries(e.ctx, e.libStore, nil, nil, p1, naming.Jellyfin, "medium", nil)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries(ep1): %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, p1.ID, int(ep1)); err != nil {
		t.Fatalf("MarkApplied(ep1): %v", err)
	}
	ep2, _, err := ApplyLibrarySeries(e.ctx, e.libStore, nil, nil, p2, naming.Jellyfin, "medium", nil)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries(ep2): %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, p2.ID, int(ep2)); err != nil {
		t.Fatalf("MarkApplied(ep2): %v", err)
	}

	before, err := e.libStore.GetSeriesByTMDBID(e.ctx, 4242)
	if err != nil {
		t.Fatalf("reading the shared show row: %v", err)
	}
	if before.Title != "Corrected Show Title" || before.Year != 2002 {
		t.Fatalf("fixture premise broken: the second Apply should own the shared row, got %+v", before)
	}

	e.undoLatest(t, p1.ID)

	after, err := e.libStore.GetSeriesByTMDBID(e.ctx, 4242)
	if err != nil {
		t.Fatalf("the shared show row must survive: %v", err)
	}
	if after.Title != before.Title || after.Year != before.Year {
		t.Errorf("undoing episode 1 rolled back the SHARED show row: %q/%d -> %q/%d",
			before.Title, before.Year, after.Title, after.Year)
	}
	// Episode 2's own row is untouched too.
	if _, err := e.libStore.GetEpisodeByID(e.ctx, ep2); err != nil {
		t.Errorf("episode 2's row must be untouched by episode 1's undo: %v", err)
	}
	// Episode 1's row IS reverted (deleted — it was an INSERT).
	if _, err := e.libStore.GetEpisodeByID(e.ctx, ep1); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("episode 1's own row should have been deleted, got %v", err)
	}
}

// TestUndoSeries_AlternateFoldNeverMovesAFile is the Series counterpart of the
// Movies alternate-fold test.
func TestUndoSeries_AlternateFoldNeverMovesAFile(t *testing.T) {
	e := newUndoEnv(t, nil)
	src1 := seedSeriesEpisodeFile(t, e.root, "first.S01E01.mkv")
	src2 := writeFileAt(t, filepath.Join(e.root, "inbox", "better.S01E01.mkv"), "better-episode-bytes")

	seeded := e.seedProposals(t, mode.Series,
		proposals.Proposal{SourceName: "first.S01E01.mkv", SourcePath: src1, RootFolderPath: e.root,
			Title: "Show", TMDBID: 8080, SeasonNumber: 1, EpisodeNumber: 1},
		proposals.Proposal{SourceName: "better.S01E01.mkv", SourcePath: src2, RootFolderPath: e.root,
			Title: "Show", TMDBID: 8080, SeasonNumber: 1, EpisodeNumber: 1},
	)
	p1, p2 := seeded[0], seeded[1]

	ep1, changes1, err := ApplyLibrarySeries(e.ctx, e.libStore, nil, nil, p1, naming.Jellyfin, "low", nil)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries(first): %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, p1.ID, int(ep1)); err != nil {
		t.Fatalf("MarkApplied(first): %v", err)
	}
	firstDest := changes1[1].Path

	prober := mapProber{
		firstDest: {Height: 480, CodecName: "h264", BitRate: 800_000},
		src2:      {Height: 2160, CodecName: "hevc", BitRate: 40_000_000},
	}
	ep2, _, err := ApplyLibrarySeries(e.ctx, e.libStore, nil, nil, p2, naming.Jellyfin, "low", prober)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries(better): %v", err)
	}
	if ep2 != ep1 {
		t.Fatalf("fixture premise broken: the fold-in must reuse episode row %d, got %d", ep1, ep2)
	}
	if err := e.propStore.MarkApplied(e.ctx, p2.ID, int(ep2)); err != nil {
		t.Fatalf("MarkApplied(better): %v", err)
	}
	mustHaveContents(t, firstDest, "better-episode-bytes", "after the promote the canonical episode path")

	res := e.undoLatest(t, p2.ID)

	if res.FileRestored {
		t.Fatal("an alternate-fold undo must never move a file")
	}
	if !strings.Contains(res.FileMessage, "quality alternate") {
		t.Errorf("expected the alternate-fold message, got %q", res.FileMessage)
	}
	mustHaveContents(t, firstDest, "better-episode-bytes", "after undo the promoted episode file")
	if res.RowsReverted != 1 {
		t.Errorf("the episode row should still revert, RowsReverted = %d", res.RowsReverted)
	}
	mustStatus(t, e, p2.ID, proposals.Pending)
}

// ----------------------------------------------------------------- Adult ----

// TestUndoAdult_RoundTripRestoresPriorSceneRow is the UPDATE case: a scene row
// for (box, scene_id) already existed, so undo restores its pre-Apply snapshot
// rather than deleting it — phash cache-validity fields included.
func TestUndoAdult_RoundTripRestoresPriorSceneRow(t *testing.T) {
	e := newUndoEnv(t, nil)
	sess := adultTestSession(t, nil, map[string]*stashbox.Client{})
	oldPath := writeFileAt(t, filepath.Join(e.root, "old", "original.mp4"), "old-scene")
	src := writeFileAt(t, filepath.Join(e.root, "inbox", "raw-release.mp4"), "scene-bytes")

	prior, err := e.libStore.UpsertScene(e.ctx, library.Scene{
		Box: "stashdb", SceneID: "scene-1", Title: "Old Title", Studio: "Old Studio",
		Date: "2019-01-01", FilePath: oldPath, RootFolderPath: filepath.Join(e.root, "old"),
		Size: 9, QualityTier: "low", PHash: "oldhash", PHashFileSize: 9, PHashFileMTime: "2019-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("seeding the prior scene row: %v", err)
	}

	p := e.seedProposals(t, mode.Adult, proposals.Proposal{
		SourceName: "raw-release.mp4", SourcePath: src, RootFolderPath: e.root,
		Title: "New Title", Studio: "New Studio", Date: "2021-05-05",
		GiveBackBox: "stashdb", GiveBackSceneID: "scene-1", PHash: "newhash", DurationSeconds: 1800,
	})[0]

	sceneID, _, changes, err := ApplyLibraryAdult(e.ctx, sess, e.libStore, p, "high")
	if err != nil {
		t.Fatalf("ApplyLibraryAdult: %v", err)
	}
	if sceneID != prior.ID {
		t.Fatalf("fixture premise broken: the Apply must upsert the SAME (box, scene_id) row")
	}
	if err := e.propStore.MarkApplied(e.ctx, p.ID, int(sceneID)); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	dest := changes[1].Path

	res := e.undoLatest(t, p.ID)

	if !res.FileRestored {
		t.Fatalf("expected the scene file moved back, got %+v", res)
	}
	mustHaveContents(t, src, "scene-bytes", "after undo the source path")
	mustNotExist(t, dest, "after undo the applied destination")

	got, err := e.libStore.GetSceneByID(e.ctx, prior.ID)
	if err != nil {
		t.Fatalf("re-reading the scene: %v", err)
	}
	if got.Title != "Old Title" || got.Studio != "Old Studio" || got.FilePath != oldPath {
		t.Errorf("expected the pre-Apply scene row restored, got %+v", got)
	}
	// The phash cache-validity key must come back with the rest of the row.
	if got.PHash != "oldhash" || got.PHashFileSize != 9 || got.PHashFileMTime != "2019-01-01T00:00:00Z" {
		t.Errorf("expected the phash cache fields restored, got phash=%q size=%d mtime=%q",
			got.PHash, got.PHashFileSize, got.PHashFileMTime)
	}
	mustStatus(t, e, p.ID, proposals.Pending)
}

// TestUndoAdult_ReportsUnretractableGiveBack: a real give-back fired at Apply
// time, so undo must say plainly that it cannot be retracted — without making
// any outbound call of its own.
func TestUndoAdult_ReportsUnretractableGiveBack(t *testing.T) {
	e := newUndoEnv(t, nil)
	rec := &giveBackRecord{}
	box := newFakeAdultBox(t, nil, rec, nil)
	sess := adultTestSession(t, nil, map[string]*stashbox.Client{"stashdb": box})
	src := writeFileAt(t, filepath.Join(e.root, "inbox", "give-back.mp4"), "scene-bytes")

	p := e.seedProposals(t, mode.Adult, proposals.Proposal{
		SourceName: "give-back.mp4", SourcePath: src, RootFolderPath: e.root,
		Title: "Given Back", Studio: "Studio", Date: "2022-02-02",
		GiveBackBox: "stashdb", GiveBackSceneID: "scene-9", PHash: "hash9", DurationSeconds: 1200,
	})[0]

	sceneID, submitted, _, err := ApplyLibraryAdult(e.ctx, sess, e.libStore, p, "high")
	if err != nil {
		t.Fatalf("ApplyLibraryAdult: %v", err)
	}
	if !submitted || !rec.submitted {
		t.Fatalf("fixture premise broken: the give-back should have fired (submitted=%v rec=%v)", submitted, rec.submitted)
	}
	if err := e.propStore.MarkApplied(e.ctx, p.ID, int(sceneID)); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}

	res := e.undoLatest(t, p.ID)

	if !res.GiveBackNotRetractable {
		t.Error("expected undo to report the community-database submission as unretractable")
	}
	if _, err := e.libStore.GetSceneByID(e.ctx, sceneID); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("the INSERTed scene row should have been deleted, got %v", err)
	}
}

// TestUndoSeries_RangeAlternateStillMovesFileBack is the Series sibling of
// TestUndoMovies_LoseTieAlternateStillMovesFileBack, and it covers the case the
// hardcoded flag hit HARDEST.
//
// applyLibrarySeriesAlternate carries two Series-only refusals that Movies has
// no analogue for: a RANGE proposal never promotes, and promotion is refused
// when the existing primary is a shared/bundled multi-episode file. Both force
// the lose/tie branch. So EVERY range alternate Apply took the lose/tie branch,
// and hardcoding viaAlternateFold = true made every one of them permanently
// un-undoable — not an edge case, the whole category.
func TestUndoSeries_RangeAlternateStillMovesFileBack(t *testing.T) {
	e := newUndoEnv(t, nil)
	src1 := seedSeriesEpisodeFile(t, e.root, "Show.S01E01.mkv")
	// A range file covering E01-E02. Its primary slot (E01) is occupied by the
	// single-episode file applied first, so it folds in — and, being a range,
	// it can never promote.
	src2 := writeFileAt(t, filepath.Join(e.root, "inbox", "Show.S01E01-E02.mkv"), "range-bytes")

	seeded := e.seedProposals(t, mode.Series,
		proposals.Proposal{SourceName: "Show.S01E01.mkv", SourcePath: src1, RootFolderPath: e.root,
			Title: "Show", TMDBID: 9191, SeasonNumber: 1, EpisodeNumber: 1},
		proposals.Proposal{SourceName: "Show.S01E01-E02.mkv", SourcePath: src2, RootFolderPath: e.root,
			Title: "Show", TMDBID: 9191, SeasonNumber: 1, EpisodeNumber: 1, ExtraEpisodeNumbers: []int{2}},
	)
	p1, pRange := seeded[0], seeded[1]

	ep1, changes1, err := ApplyLibrarySeries(e.ctx, e.libStore, nil, nil, p1, naming.Jellyfin, "low", nil)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries(single): %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, p1.ID, int(ep1)); err != nil {
		t.Fatalf("MarkApplied(single): %v", err)
	}
	firstDest := changes1[1].Path

	// A strictly HIGHER probed tier on the incoming range file. Movies would
	// promote on this; Series must not, purely because it is a range — which is
	// exactly what makes this the right fixture.
	prober := mapProber{
		firstDest: {Height: 480, CodecName: "h264", BitRate: 800_000},
		src2:      {Height: 2160, CodecName: "hevc", BitRate: 40_000_000},
	}
	_, changesRange, err := ApplyLibrarySeries(e.ctx, e.libStore, nil, nil, pRange, naming.Jellyfin, "low", prober)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries(range): %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, pRange.ID, int(ep1)); err != nil {
		t.Fatalf("MarkApplied(range): %v", err)
	}
	// The range refusal held: the original single-episode file is still primary.
	mustHaveContents(t, firstDest, "episode-bytes", "after the range fold-in the original primary")
	rangeAlt := lastCreatedPath(changesRange, "")
	if rangeAlt == "" || rangeAlt == firstDest {
		t.Fatalf("fixture premise broken: the range file should have landed at an alternate name, got %q", rangeAlt)
	}
	mustHaveContents(t, rangeAlt, "range-bytes", "the range file at its alternate name")

	res := e.undoLatest(t, pRange.ID)

	// THE ASSERTION THIS TEST EXISTS FOR.
	if !res.FileRestored {
		t.Fatalf("a range alternate Apply moved only its OWN file, so undo must move it back; got %q", res.FileMessage)
	}
	if strings.Contains(res.FileMessage, "quality alternate") {
		t.Errorf("the alternate-fold refusal message must not fire for a range fold-in, got %q", res.FileMessage)
	}
	mustHaveContents(t, src2, "range-bytes", "after undo the range file's source path")
	mustNotExist(t, rangeAlt, "after undo the alternate-named range copy")
	// The first proposal's file and row are untouched by the second's undo.
	mustHaveContents(t, firstDest, "episode-bytes", "after undo the original primary")
	mustStatus(t, e, pRange.ID, proposals.Pending)
}

// ------------------------------------------------------------ drift/gate ----

// TestUndoSeries_RowDriftWarnsButStillMovesFile pins the best-effort policy:
// ROW drift warns and PROCEEDS — both the row revert and the file move-back
// still happen.
//
// The fixture uses a Series Apply onto a FILELESS catalog row, which is the one
// shape that produces a non-INSERT touched row on a NON-alternate-fold entry
// (the fold-in gate treats an empty FilePath as an unoccupied slot). That makes
// it the only place a genuine row-drift-plus-file-move case exists.
//
// DEVIATION from the plan's §7 "Drift" bullet, which asks for the FILE's size to
// be mutated and the move to happen anyway. That bullet contradicts the same
// plan's §6 step 1 and this feature's stated non-negotiable rule: a size
// mismatch is exactly the signal that the file is not the one this Apply put
// there, and moving it anyway is how a sibling proposal's data gets destroyed.
// §6 wins — TestUndoMovies_SizeMismatchRefusesSiblingsFile covers that case —
// and drift is exercised here through the row, which is what the best-effort
// policy was actually authorised for.
func TestUndoSeries_RowDriftWarnsButStillMovesFile(t *testing.T) {
	e := newUndoEnv(t, nil)
	src := seedSeriesEpisodeFile(t, e.root, "Drift.S02E03.mkv")

	series, err := e.libStore.UpsertSeries(e.ctx, library.Series{
		TMDBID: 31337, Title: "Drift Show", RootFolderPath: e.root,
	})
	if err != nil {
		t.Fatalf("seeding the series: %v", err)
	}
	// A fileless catalog row: TMDB knows the episode, nothing is on disk yet.
	// The Apply UPDATEs it, so the archived touched row is WasInsert=false and
	// ViaAlternateFold=false.
	catalog, err := e.libStore.UpsertEpisode(e.ctx, library.Episode{
		SeriesID: series.ID, SeasonNumber: 2, EpisodeNumber: 3,
		Title: "Catalogued", AirDate: "2015-03-03", QualityTier: "medium",
	})
	if err != nil {
		t.Fatalf("seeding the catalog episode row: %v", err)
	}

	p := e.seedProposals(t, mode.Series, proposals.Proposal{
		SourceName: "Drift.S02E03.mkv", SourcePath: src, RootFolderPath: e.root,
		Title: "Drift Show", TMDBID: 31337, SeasonNumber: 2, EpisodeNumber: 3,
	})[0]

	epID, changes, err := ApplyLibrarySeries(e.ctx, e.libStore, nil, nil, p, naming.Jellyfin, "medium", nil)
	if err != nil {
		t.Fatalf("ApplyLibrarySeries: %v", err)
	}
	if epID != catalog.ID {
		t.Fatalf("fixture premise broken: the Apply must UPDATE the catalog row %d, got %d", catalog.ID, epID)
	}
	if err := e.propStore.MarkApplied(e.ctx, p.ID, int(epID)); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	dest := changes[1].Path

	// Something else rewrote the row's path afterwards — pure row drift, with
	// the file itself untouched at the path this Apply left it.
	drifted := filepath.Join(filepath.Dir(dest), "moved-by-something-else.mkv")
	if err := e.libStore.UpdateEpisodePrimaryPath(e.ctx, epID, drifted, "medium", 7); err != nil {
		t.Fatalf("simulating row drift: %v", err)
	}

	res := e.undoLatest(t, p.ID)

	if !res.DriftDetected || len(res.DriftWarnings) == 0 {
		t.Errorf("expected row drift to be reported, got %+v", res)
	}
	if !res.FileRestored {
		t.Errorf("row drift must NOT block the file move-back, got %q", res.FileMessage)
	}
	mustHaveContents(t, src, "episode-bytes", "after undo the source path")
	mustNotExist(t, dest, "after undo the applied destination")

	if res.RowsReverted != 1 {
		t.Errorf("row drift must NOT block the row revert, RowsReverted = %d", res.RowsReverted)
	}
	got, err := e.libStore.GetEpisodeByID(e.ctx, catalog.ID)
	if err != nil {
		t.Fatalf("the catalog row must survive (it predates the Apply): %v", err)
	}
	if got.FilePath != "" {
		t.Errorf("expected the fileless pre-Apply state restored, got file_path %q", got.FilePath)
	}
	if got.Title != "Catalogued" || got.AirDate != "2015-03-03" {
		t.Errorf("expected the pre-Apply catalog metadata restored, got %+v", got)
	}
}

// -------------------------------------------------------------- eviction ----

// TestUndoEviction_KeepsOnlyDepthActive — NON-NEGOTIABLE RULE 3. The rolling
// depth pushes older entries out via evicted_at, NEVER consumed_at, and an
// evicted entry is no longer undoable.
func TestUndoEviction_KeepsOnlyDepthActive(t *testing.T) {
	const depth = 2
	e := newUndoEnv(t, func(context.Context, mode.Mode) int { return depth })

	var applied []proposals.Proposal
	seeds := make([]proposals.Proposal, 0, 4)
	for i := 0; i < 4; i++ {
		name := filepath.Base(writeFileAt(t, filepath.Join(e.root, "inbox", "m"+string(rune('a'+i))+".mkv"), "bytes"))
		seeds = append(seeds, proposals.Proposal{
			SourceName: name, SourcePath: filepath.Join(e.root, "inbox", name), RootFolderPath: e.root,
			Title: "Movie " + string(rune('A'+i)), Year: 2000 + i, TMDBID: 9000 + i,
		})
	}
	applied = e.seedProposals(t, mode.Movies, seeds...)
	for _, p := range applied {
		id, _, err := ApplyLibrary(e.ctx, e.libStore, p, naming.Jellyfin, "high", nil)
		if err != nil {
			t.Fatalf("ApplyLibrary(%s): %v", p.Title, err)
		}
		if err := e.propStore.MarkApplied(e.ctx, p.ID, int(id)); err != nil {
			t.Fatalf("MarkApplied: %v", err)
		}
	}

	active, err := e.undoStore.ListActive(e.ctx, mode.Movies)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != depth {
		t.Fatalf("expected exactly %d active entries after 4 Applies, got %d", depth, len(active))
	}
	for _, got := range active {
		if got.ProposalID != applied[2].ID && got.ProposalID != applied[3].ID {
			t.Errorf("expected only the two most recent Applies to stay active, found proposal %d", got.ProposalID)
		}
	}

	// The evicted entries carry evicted_at and NOT consumed_at.
	var evictedCount, consumedCount int
	if err := e.db.QueryRowContext(e.ctx, `
		SELECT count(*) FILTER (WHERE evicted_at IS NOT NULL),
		       count(*) FILTER (WHERE consumed_at IS NOT NULL)
		FROM rename_undo_archive WHERE mode = $1`, string(mode.Movies)).
		Scan(&evictedCount, &consumedCount); err != nil {
		t.Fatalf("counting lifecycle columns: %v", err)
	}
	if evictedCount != 2 {
		t.Errorf("expected 2 evicted entries, got %d", evictedCount)
	}
	if consumedCount != 0 {
		t.Errorf("eviction must NEVER set consumed_at, got %d consumed rows", consumedCount)
	}

	// An evicted entry is indistinguishable from "nothing to undo" — the
	// deterministic outcome the endpoint turns into a 404.
	if _, err := e.undoStore.LatestActiveFor(e.ctx, applied[0].ID); !errors.Is(err, ErrNoUndoEntry) {
		t.Errorf("expected an evicted entry to be unfindable, got %v", err)
	}

	// Undoing a still-active entry must not resurrect an eviction slot.
	e.undoLatest(t, applied[3].ID)
	if err := e.db.QueryRowContext(e.ctx, `
		SELECT count(*) FILTER (WHERE evicted_at IS NOT NULL)
		FROM rename_undo_archive WHERE mode = $1`, string(mode.Movies)).Scan(&evictedCount); err != nil {
		t.Fatalf("re-counting evicted rows: %v", err)
	}
	if evictedCount != 2 {
		t.Errorf("undoing an entry must not clear evicted_at anywhere, got %d evicted rows", evictedCount)
	}
}
