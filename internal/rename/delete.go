package rename

import (
	"context"
	"fmt"
	"os"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// Package-level placement note: DeleteSource sits alongside ApplyLibrary
// (rename.go:547) and ApplyLibrarySeries (rename.go:1863) as a third terminal
// operation on a Rename proposal — but it is deliberately NOT a sibling in
// shape. Those two RELOCATE a file and TRACK the result in libStore.
// DeleteSource destroys the file and destroys the row, and touches libStore
// not at all: a Pending/Unmatched Rename proposal is by definition a file
// that is NOT yet tracked (tracking is what Apply does), so there is no
// library row to clean up. Do not "helpfully" add a libStore.Delete here.
//
// That "by definition" is NOT self-evident — it holds only because of an
// invariant enforced in three other functions. Every Rename scan builds a
// `known` set of already-tracked file paths and feeds it into
// library.ScanRootFolder, which skips any path in the set outright
// (internal/library/library.go:484 — `if known[path] { ... }`, returning
// filepath.SkipDir for a tracked directory and nil for a tracked file). A
// tracked file therefore never becomes an UnmappedEntry, and so never becomes
// a Rename proposal in the first place. The three call sites:
//
//	Movies  rename.ScanLibrary            (rename.go:155)               known built :176/:179/:183, fed to ScanRootFolder :194
//	Series  rename.ScanLibrarySeries      (rename.go:628)               known built :654/:663/:749, fed to ScanRootFolder :764
//	Adult   rename.ScanLibraryAdult       (rename_adult_library.go:37)  known built :49/:53,        fed to ScanRootFolder :56
//
// If that exclusion ever stopped holding, DeleteSource would os.Remove a
// tracked file and leave its library row intact — a row pointing at nothing,
// with no libStore.Delete to clean it up (contrast purge.ApplyLibrary, which
// deletes the library row precisely because Purge operates ON tracked items).
// That is invisible library corruption. The behavioral guard is
// TestScanLibrary_TrackedPathsNeverBecomeRenameProposals in
// rename_library_test.go.
//
// Review if: rename.ScanLibrary / ScanLibrarySeries / ScanLibraryAdult stop
//	feeding a `known` tracked-path set into library.ScanRootFolder — that
//	exclusion (library.go:484) is the ONLY reason a Rename proposal's
//	SourcePath can never be a tracked library file, and it is why this
//	function deletes the file + proposal row and touches libStore not at all.

// DeleteSource permanently removes p's source file from disk and then deletes
// p's proposal row entirely. It is Rename's Delete action's whole backend.
//
// Status guard: Pending and Unmatched only, mirroring Dismiss's eligibility.
// This guard is the SERVER-SIDE enforcement of the frontend's dropdown
// gating — a hand-crafted request naming an Applied proposal must not be
// able to delete an already-organized library file. The workflow guard
// matters just as much: without it this is a generic "delete any proposal's
// source file" primitive reachable for Dedup and Purge rows, with none of
// those workflows' own libStore bookkeeping.
//
// ENOENT tolerance: os.IsNotExist is treated as success, mirroring
// dedup.removeLibraryCandidate (internal/dedup/dedup.go:279-282) and
// purge.ApplyLibrary (internal/purge/purge.go:152-154). A file moved or
// removed out-of-band between Scan and Delete is the operator's goal
// already achieved, not an error. It is also what makes a retry of the
// partial failure below idempotent.
//
// changes is a NAMED RETURN for the same reason purge.ApplyLibrary's is: if
// the os.Remove commits but the subsequent row delete fails, the file really
// is gone and the players must still be told. Returning (changes, err)
// preserves that. The resulting degraded state — file gone, row surviving
// with a dangling source_path — is self-reconciling: the next Scan drops the
// stale row via ReplacePending's orphan delete, or the operator retries and
// the ENOENT tolerance above carries it through.
//
// The empty-SourcePath guard is load-bearing rather than defensive: os.Remove("")
// returns ENOENT, which this function tolerates as success, so without the
// guard an empty path would silently destroy the proposal row while deleting
// nothing.
func DeleteSource(
	ctx context.Context,
	propStore proposalDeleteRowStore,
	p proposals.Proposal,
) (changes []mode.PathChange, err error) {
	if p.Workflow != proposals.Rename {
		return nil, fmt.Errorf("proposal %d is a %q proposal, not rename — delete is a Rename-only action", p.ID, p.Workflow)
	}
	if p.Status != proposals.Pending && p.Status != proposals.Unmatched {
		return nil, fmt.Errorf("proposal %d is %q — delete is only available for pending or unmatched proposals", p.ID, p.Status)
	}
	if p.SourcePath == "" {
		return nil, fmt.Errorf("proposal %d has no source path to delete", p.ID)
	}

	if rerr := os.Remove(p.SourcePath); rerr != nil && !os.IsNotExist(rerr) {
		return nil, fmt.Errorf("deleting %q: %w", p.SourcePath, rerr)
	}
	// Appended even on the ENOENT branch: "the file is not there" is the state
	// the players need to converge on either way, and NotifyPlayers is
	// best-effort. This matches dedup.removeLibraryCandidate, which returns
	// c.Path (a real PathChange for the caller) on its ENOENT branch too.
	changes = append(changes, mode.PathChange{Path: p.SourcePath, Kind: mode.Deleted})

	if derr := propStore.Delete(ctx, p.ID); derr != nil {
		return changes, derr // file gone, row survives
	}
	return changes, nil
}

// proposalDeleteRowStore is the one-method seam DeleteSource needs. Declared
// here rather than taking *proposals.Store so a test can make the row delete
// fail while os.Remove already succeeded — the partial-failure case a real
// store cannot be made to produce.
type proposalDeleteRowStore interface {
	Delete(ctx context.Context, id int64) error
}
