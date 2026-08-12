package rename

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// Rename Undo — snapshot-at-Apply-time archiving (deep-interview-rename-undo).
//
// This is the ONLY new code path that touches ApplyLibrary/ApplyLibrarySeries/
// ApplyLibraryAdult, and each touch is deliberately a single call-out rather
// than inlined logic.
//
// It is a TWO-STEP process with two different timing requirements, and they are
// not collapsible into one call at one point in the Apply flow:
//
//	Step A (CAPTURE): read the pre-Apply row state. Must happen BEFORE the
//	  Apply's mutating call (Upsert/UpdateItemPrimaryPath/UpsertScene/...),
//	  because recording what the row looked like beforehand is the whole point.
//	  Every Apply path already performs the lookup Step A needs (Movies'
//	  GetByTMDBID, Series' resolved map, Adult's new GetScene), so Step A
//	  re-fetches nothing.
//	Step B (PERSIST): write the archive row. Happens AFTER the Apply's own
//	  mutations succeed, using the row IDs Apply returned — a failed Apply has
//	  nothing to archive, and the row id is not known until the upsert returns.
//
// Step A's captured data is threaded to Step B as plain values ([]touchedRow
// built from Step A's reads); Step B re-reads nothing.

// recordUndoArchive persists everything needed to reverse one Apply.
//
// DEVIATION from the plan's stated signature: it takes no *UndoStore. The store
// is the process-wide default (SetDefaultUndoStore), for the same reason
// organizeevents.Log resolves its own store — the alternative is growing three
// exported Apply signatures, all of their call sites, and ~6700 lines of
// existing tests, solely to thread one optional dependency. Archiving no-ops
// entirely when no store is registered.
//
// A failure here NEVER fails the Apply: the physical move and the library write
// have already committed, and refusing to return them would be strictly worse
// than losing the ability to undo. Logged and swallowed, matching
// organizeevents.Log's "the activity log must never fail an Apply" stance —
// the closest precedent in this codebase for a best-effort side record.
func recordUndoArchive(
	ctx context.Context,
	m mode.Mode,
	preApplyProposal proposals.Proposal,
	preApplySourcePath string,
	touched []touchedRow,
	postApplyPath string,
	postApplySize int64,
	viaAlternateFold bool,
	giveBackSubmitted bool,
) {
	store := DefaultUndoStore()
	if store == nil {
		return
	}
	proposalJSON, err := json.Marshal(preApplyProposal)
	if err != nil {
		log.Printf("rename: undo archive for proposal %d: encoding proposal snapshot: %v", preApplyProposal.ID, err)
		return
	}
	if touched == nil {
		touched = []touchedRow{}
	}
	touchedJSON, err := json.Marshal(touched)
	if err != nil {
		log.Printf("rename: undo archive for proposal %d: encoding touched rows: %v", preApplyProposal.ID, err)
		return
	}
	if _, err := store.Insert(ctx, UndoEntry{
		ProposalID:               preApplyProposal.ID,
		Mode:                     m,
		AppliedAt:                time.Now().UTC().Format(time.RFC3339Nano),
		PreApplySourcePath:       preApplySourcePath,
		PreApplyProposalSnapshot: string(proposalJSON),
		TouchedRowsJSON:          string(touchedJSON),
		PostApplyPath:            postApplyPath,
		PostApplySize:            postApplySize,
		ViaAlternateFold:         viaAlternateFold,
		GiveBackSubmitted:        giveBackSubmitted,
	}); err != nil {
		log.Printf("rename: undo archive for proposal %d: %v", preApplyProposal.ID, err)
	}
}

// snapshotRow JSON-encodes a pre-Apply library row for touchedRow.PreApplyRowJSON.
// An encoding failure yields "" — which undo reads as "nothing to restore", the
// same as an INSERT — rather than aborting the archive write, since a partially
// reversible entry beats no entry at all.
func snapshotRow(row any) string {
	b, err := json.Marshal(row)
	if err != nil {
		log.Printf("rename: undo archive: encoding row snapshot: %v", err)
		return ""
	}
	return string(b)
}

// Post-Apply size is captured with library.FileSize, which returns 0 when the
// path cannot be stat'd. 0 is the safe direction for undo's size-match gate: a
// real file at the recorded path will not match 0, so the move is skipped
// rather than performed blind.

// lastCreatedPath returns the final mode.Created path in changes, which for
// both alternate-fold paths is where the INCOMING file ended up (the demoted
// existing primary is always moved first, so its Created entry comes earlier).
// Falls back when the Apply committed no move at all.
func lastCreatedPath(changes []mode.PathChange, fallback string) string {
	for i := len(changes) - 1; i >= 0; i-- {
		if changes[i].Kind == mode.Created {
			return changes[i].Path
		}
	}
	return fallback
}

// movieTouchedRows builds the touched-row set for a Movies Apply.
//
// RETURNS AN EMPTY SET WHEN p.TMDBID == 0, and that is deliberate, not a gap.
// ApplyLibrary skips GetByTMDBID for a zero TMDB id and upserts straight into
// the row keyed (mode='movies', tmdb_id=0) — a SHARED row every unidentified-
// movie Apply writes through, structurally identical to library_series. Undoing
// one such Apply must not delete or restore it, or it would destroy whatever a
// sibling unidentified-movie Apply legitimately wrote. Undo of a zero-TMDBID
// Apply reverses the file (subject to the size-match gate) and the proposal only.
func movieTouchedRows(p proposals.Proposal, existing *library.Item, itemID int64) []touchedRow {
	if p.TMDBID == 0 {
		return []touchedRow{}
	}
	if existing == nil {
		return []touchedRow{{Table: tableLibraryItems, RowID: itemID, WasInsert: true}}
	}
	return []touchedRow{{
		Table:           tableLibraryItems,
		RowID:           existing.ID,
		WasInsert:       false,
		PreApplyRowJSON: snapshotRow(*existing),
		FilePath:        existing.FilePath,
	}}
}

// episodeTouchedRows builds the touched-row set for a Series Apply.
//
// library_episodes ONLY — never a library_series entry, unconditionally,
// regardless of how the show row looked pre-Apply. UpsertSeries writes
// ON CONFLICT(tmdb_id) against a row shared by every episode of the show, so
// restoring it from one episode's snapshot would clobber a sibling episode's
// later Apply (spec Constraints).
//
// resolved maps every episode number this Apply wrote to its pre-Apply row, or
// to nil where no row existed — exactly the map ApplyLibrarySeries' fold-in gate
// already builds, in one full pass, before any mutation.
func episodeTouchedRows(upserted []library.Episode, resolved map[int]*library.Episode) []touchedRow {
	out := make([]touchedRow, 0, len(upserted))
	for _, ep := range upserted {
		prior := resolved[ep.EpisodeNumber]
		if prior == nil {
			out = append(out, touchedRow{Table: tableLibraryEpisodes, RowID: ep.ID, WasInsert: true})
			continue
		}
		out = append(out, touchedRow{
			Table:           tableLibraryEpisodes,
			RowID:           prior.ID,
			WasInsert:       false,
			PreApplyRowJSON: snapshotRow(*prior),
			FilePath:        prior.FilePath,
		})
	}
	return out
}

// sceneTouchedRows builds the touched-row set for an Adult Apply. One entry
// against library_scenes, keyed (box, scene_id).
//
// ViaAlternateFold is NOT always false here: the alternate fold gate in
// ApplyLibraryAdult (added in C6) passes promoted directly to recordUndoArchive.
// Simple-path Applies (new scene, no existing file) still pass false. The
// promote branch passes true (undo must NOT move the file back). The lose/tie
// branch passes false (undo can and should move it back via the size-match gate).
//
// The pre-image is the WHOLE scene row, which matters beyond title/path:
// PHash/PHashFileSize/PHashFileMTime are the phash cache's file-identity key,
// and restoring the row without them would leave Stage-2 Dedup trusting a
// cached hash against the wrong file size/mtime.
func sceneTouchedRows(existing *library.Scene, sceneID int64) []touchedRow {
	if existing == nil {
		return []touchedRow{{Table: tableLibraryScenes, RowID: sceneID, WasInsert: true}}
	}
	return []touchedRow{{
		Table:           tableLibraryScenes,
		RowID:           existing.ID,
		WasInsert:       false,
		PreApplyRowJSON: snapshotRow(*existing),
		FilePath:        existing.FilePath,
	}}
}
