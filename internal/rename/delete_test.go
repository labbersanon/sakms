package rename

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

func newTestProposalStore(t *testing.T) *proposals.Store {
	t.Helper()
	return proposals.New(dbtest.New(t))
}

// seedRenameProposal writes a real file under a fresh temp dir and inserts a
// live Rename proposal pointing at it, returning the stored row (ID populated).
func seedRenameProposal(t *testing.T, store *proposals.Store, status proposals.Status) proposals.Proposal {
	t.Helper()
	src := writeDeletableFile(t, "doomed.mkv")
	saved, err := store.ReplacePending(context.Background(), mode.Movies, proposals.Rename, []proposals.Proposal{{
		Status: status, SourceName: filepath.Base(src), SourcePath: src, RootFolderPath: filepath.Dir(src),
	}})
	if err != nil {
		t.Fatalf("seeding proposal: %v", err)
	}
	if len(saved) != 1 || saved[0].ID == 0 {
		t.Fatalf("expected one seeded proposal with an id, got %+v", saved)
	}
	return saved[0]
}

// writeDeletableFile creates a real file and asserts it exists before the test
// touches DeleteSource. Without this pre-assertion every "the file is gone
// afterwards" check in this file would pass vacuously against a file that was
// never created, since DeleteSource tolerates ENOENT.
func writeDeletableFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("video bytes"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("precondition: %s should exist before DeleteSource runs: %v", path, err)
	}
	return path
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be gone from disk, stat err = %v", path, err)
	}
}

func assertStillOnDisk(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to still be on disk, stat err = %v", path, err)
	}
}

func assertOneDeletedChange(t *testing.T, changes []mode.PathChange, path string) {
	t.Helper()
	if len(changes) != 1 {
		t.Fatalf("expected exactly one PathChange, got %+v", changes)
	}
	if changes[0].Path != path || changes[0].Kind != mode.Deleted {
		t.Fatalf("expected {%q, Deleted}, got %+v", path, changes[0])
	}
}

// recordingStore is the guard-test seam: it fails the test if Delete is ever
// reached. "The row is still there" is proven more directly by "the row delete
// was never attempted" than by re-reading a database.
type recordingStore struct {
	t      *testing.T
	called bool
}

func (r *recordingStore) Delete(context.Context, int64) error {
	r.called = true
	r.t.Errorf("propStore.Delete must not be called for a rejected proposal")
	return nil
}

// failingStore forces the §5.1 partial failure: os.Remove commits, the row
// delete does not. A real store cannot be made to produce this.
type failingStore struct {
	err    error
	called bool
}

func (f *failingStore) Delete(context.Context, int64) error {
	f.called = true
	return f.err
}

func TestDeleteSource_RemovesFileAndRow(t *testing.T) {
	ctx := context.Background()
	store := newTestProposalStore(t)
	p := seedRenameProposal(t, store, proposals.Pending)
	assertStillOnDisk(t, p.SourcePath)

	changes, err := DeleteSource(ctx, store, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertGone(t, p.SourcePath)
	assertOneDeletedChange(t, changes, p.SourcePath)
	if _, err := store.Get(ctx, p.ID); !errors.Is(err, proposals.ErrNotFound) {
		t.Fatalf("expected the proposal row to be gone (ErrNotFound), got %v", err)
	}
}

func TestDeleteSource_ENOENTTolerated(t *testing.T) {
	ctx := context.Background()
	store := newTestProposalStore(t)
	p := seedRenameProposal(t, store, proposals.Pending)

	// Out-of-band removal between Scan and Delete — the operator's goal already
	// achieved, not an error.
	if err := os.Remove(p.SourcePath); err != nil {
		t.Fatalf("out-of-band remove: %v", err)
	}

	changes, err := DeleteSource(ctx, store, p)
	if err != nil {
		t.Fatalf("expected ENOENT to be tolerated as success, got %v", err)
	}
	assertOneDeletedChange(t, changes, p.SourcePath)
	if _, err := store.Get(ctx, p.ID); !errors.Is(err, proposals.ErrNotFound) {
		t.Fatalf("expected the proposal row to still be deleted, got %v", err)
	}
}

func TestDeleteSource_RejectsApplied(t *testing.T) {
	src := writeDeletableFile(t, "already-organized.mkv")
	store := &recordingStore{t: t}

	// Rename workflow and a real non-empty path, so the ONLY thing that can
	// reject this is the status guard.
	_, err := DeleteSource(context.Background(), store, proposals.Proposal{
		ID: 7, Workflow: proposals.Rename, Status: proposals.Applied, SourcePath: src,
	})
	if err == nil {
		t.Fatal("expected an applied proposal to be refused")
	}
	if !strings.Contains(err.Error(), string(proposals.Applied)) {
		t.Errorf("expected the error to name the actual status %q, got %q", proposals.Applied, err)
	}
	assertStillOnDisk(t, src)
	if store.called {
		t.Error("expected the proposal row to survive a refused delete")
	}
}

func TestDeleteSource_RejectsDismissed(t *testing.T) {
	src := writeDeletableFile(t, "dismissed.mkv")
	store := &recordingStore{t: t}

	_, err := DeleteSource(context.Background(), store, proposals.Proposal{
		ID: 8, Workflow: proposals.Rename, Status: proposals.Dismissed, SourcePath: src,
	})
	if err == nil {
		t.Fatal("expected a dismissed proposal to be refused")
	}
	if !strings.Contains(err.Error(), string(proposals.Dismissed)) {
		t.Errorf("expected the error to name the actual status %q, got %q", proposals.Dismissed, err)
	}
	assertStillOnDisk(t, src)
	if store.called {
		t.Error("expected the proposal row to survive a refused delete")
	}
}

// TestDeleteSource_AcceptsUnmatched is the POSITIVE half of the status gate.
// Writing the guard as `Status != proposals.Pending` instead of the correct
// two-status check compiles, passes every rejection test above, and fails only
// here.
func TestDeleteSource_AcceptsUnmatched(t *testing.T) {
	ctx := context.Background()
	store := newTestProposalStore(t)
	p := seedRenameProposal(t, store, proposals.Unmatched)
	if p.Status != proposals.Unmatched {
		t.Fatalf("precondition: expected a seeded Unmatched row, got %q", p.Status)
	}
	assertStillOnDisk(t, p.SourcePath)

	changes, err := DeleteSource(ctx, store, p)
	if err != nil {
		t.Fatalf("unmatched proposals must be deletable, got %v", err)
	}
	assertGone(t, p.SourcePath)
	assertOneDeletedChange(t, changes, p.SourcePath)
	if _, err := store.Get(ctx, p.ID); !errors.Is(err, proposals.ErrNotFound) {
		t.Fatalf("expected the proposal row to be gone (ErrNotFound), got %v", err)
	}
}

// TestDeleteSource_RejectsNonRenameWorkflow guards §5.4 property 2: without the
// workflow gate this endpoint becomes a generic "delete any proposal's source
// file" primitive, reachable for Dedup and Purge rows that carry their own
// libStore bookkeeping this function performs none of.
func TestDeleteSource_RejectsNonRenameWorkflow(t *testing.T) {
	for _, wf := range []proposals.Workflow{proposals.Purge, proposals.Dedup} {
		t.Run(string(wf), func(t *testing.T) {
			src := writeDeletableFile(t, "tracked-by-another-workflow.mkv")
			store := &recordingStore{t: t}

			// Pending + a real path, so the workflow guard is the only thing
			// that can reject this.
			_, err := DeleteSource(context.Background(), store, proposals.Proposal{
				ID: 9, Workflow: wf, Status: proposals.Pending, SourcePath: src,
			})
			if err == nil {
				t.Fatalf("expected a %q proposal to be refused", wf)
			}
			if !strings.Contains(err.Error(), string(wf)) {
				t.Errorf("expected the error to name the actual workflow %q, got %q", wf, err)
			}
			assertStillOnDisk(t, src)
			if store.called {
				t.Error("expected the proposal row to survive a refused delete")
			}
		})
	}
}

// TestDeleteSource_RowDeleteFailsAfterFileRemoved is §5.1's contract: the
// os.Remove commits, the row delete fails, and the caller must still learn
// about the committed deletion so NotifyPlayers fires for it. The file is a
// real file on a real temp dir (asserted present immediately before the call),
// so the os.Remove genuinely runs rather than short-circuiting through the
// ENOENT branch.
func TestDeleteSource_RowDeleteFailsAfterFileRemoved(t *testing.T) {
	src := writeDeletableFile(t, "partial-failure.mkv")
	assertStillOnDisk(t, src)

	boom := errors.New("row delete exploded")
	store := &failingStore{err: boom}

	changes, err := DeleteSource(context.Background(), store, proposals.Proposal{
		ID: 42, Workflow: proposals.Rename, Status: proposals.Pending, SourcePath: src,
	})

	if !store.called {
		t.Fatal("expected the row delete to have been attempted after the file removal")
	}
	if err == nil {
		t.Fatal("expected the row-delete failure to be returned")
	}
	if !errors.Is(err, boom) {
		t.Errorf("expected the store's own error to surface, got %v", err)
	}
	if changes == nil {
		t.Fatal("changes must be non-nil on a partial failure — the file really is gone and the players must be told")
	}
	assertOneDeletedChange(t, changes, src)
	// The whole point: the file removal committed even though the row delete did not.
	assertGone(t, src)
}

// TestDeleteSource_EmptySourcePathRejected matters more than it looks:
// os.Remove("") returns ENOENT, which this function tolerates as success, so
// without the guard an empty path would destroy the proposal row while
// deleting nothing at all. The recordingStore proves os.Remove was never
// reached — Delete is unconditionally the next statement after it.
func TestDeleteSource_EmptySourcePathRejected(t *testing.T) {
	store := &recordingStore{t: t}
	changes, err := DeleteSource(context.Background(), store, proposals.Proposal{
		ID: 11, Workflow: proposals.Rename, Status: proposals.Pending, SourcePath: "",
	})
	if err == nil {
		t.Fatal("expected an empty source path to be refused")
	}
	if changes != nil {
		t.Errorf("expected no PathChange for a refused delete, got %+v", changes)
	}
	if store.called {
		t.Error("the empty-path guard must short-circuit before os.Remove and the row delete")
	}
}
