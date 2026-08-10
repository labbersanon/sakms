package rename

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/organizeevents"
	"github.com/labbersanon/sakms/internal/proposals"
)

// Rename Undo — reversal (deep-interview-rename-undo, backend pass).
//
// One dispatcher, three mode-specific reversal functions mirroring the three
// Apply functions. All three run the SAME ordered steps — deliberately, so they
// cannot drift into different shapes:
//
//	1. file-move gate (size match; see restoreFileForUndo)
//	3. revert the touched library row(s)
//	4. restore the proposal wholesale from its snapshot
//	5. Adult only: report an unretractable give-back submission
//	6. mark the archive entry consumed
//	7. append an organize_events row
//
// (Step numbering follows the implementation plan's §6; there is no step 2.)

// Operator-facing file-status messages. "missing" and "a different file is
// there" are deliberately DIFFERENT strings: both end in "not restored", but an
// operator debugging this needs to know which happened, and conflating them
// hides an active cross-proposal collision behind a benign-looking message.
const (
	undoFileMovedFmt         = "moved back to %q"
	undoFileMissingFmt       = "no file is at %q any more — nothing was moved back"
	undoFileDifferentFileFmt = "a different file now occupies %q (%d bytes on disk, %d bytes when this Apply ran) — refusing to move it, because it is not the file this Apply put there"
	undoFileAlternateFold    = "this Apply folded the file in as a quality alternate, which physically renamed another proposal's already-applied file — no file is moved back for these, by design"
	undoFileNoRecordedPath   = "no post-Apply file path was recorded for this Apply — nothing was moved back"
	undoFileNoSourcePath     = "no pre-Apply source path was recorded for this Apply — nothing was moved back"
	undoFileMoveFailedFmt    = "moving %q back to %q failed: %v"
)

// UndoResult is one undo attempt's operator-facing outcome.
type UndoResult struct {
	ProposalID int64     `json:"proposalId"`
	Mode       mode.Mode `json:"mode"`
	// FileRestored is false whenever no file was moved back, for ANY reason —
	// missing, size-mismatched, alternate-fold, or a failed move. FileMessage
	// says which.
	FileRestored bool   `json:"fileRestored"`
	FileMessage  string `json:"fileMessage"`
	// RestoredPath is where the file actually landed, which is not necessarily
	// PreApplySourcePath: moveUnique routes through place.UniquePath, so a
	// collision at the original location yields a suffixed name.
	RestoredPath string `json:"restoredPath,omitempty"`
	// DriftDetected/DriftWarnings surface state that changed since the Apply.
	// Drift NEVER blocks the undo (best-effort policy) — with one exception
	// that is not drift at all: a file-identity mismatch, which is handled by
	// the gate in restoreFileForUndo rather than warned about.
	DriftDetected bool     `json:"driftDetected"`
	DriftWarnings []string `json:"driftWarnings,omitempty"`
	RowsReverted  int      `json:"rowsReverted"`
	// GiveBackNotRetractable is Adult-only: the Apply fired a fingerprint/draft
	// submission to a community stash-box, which is a one-way external side
	// effect outside this application's control. Purely informational — undo
	// never contacts StashDB/FansDB/TPDB.
	GiveBackNotRetractable bool `json:"giveBackNotRetractable"`
	// Changes is the inverse of the Apply's own []mode.PathChange, for
	// Session.NotifyPlayers — without it a player's index stays pointed at a
	// path that no longer exists after the move-back.
	Changes []mode.PathChange `json:"-"`
}

// UndoApply reverses one archived Apply, dispatching by the entry's mode.
//
// DEVIATION from the plan's stated signature: undoStore is an explicit
// parameter rather than resolved from the package default. Step 6 (mark
// consumed) genuinely needs it, and passing it keeps the reversal path
// testable without mutating process-wide state.
func UndoApply(ctx context.Context, undoStore *UndoStore, libStore *library.Store, propStore *proposals.Store, entry UndoEntry) (UndoResult, error) {
	switch entry.Mode {
	case mode.Movies:
		return undoApplyLibrary(ctx, undoStore, libStore, propStore, entry)
	case mode.Series:
		return undoApplyLibrarySeries(ctx, undoStore, libStore, propStore, entry)
	case mode.Adult:
		return undoApplyLibraryAdult(ctx, undoStore, libStore, propStore, entry)
	default:
		return UndoResult{}, fmt.Errorf("undo for unknown mode %q", entry.Mode)
	}
}

// undoApplyLibrary reverses one Movies Apply.
func undoApplyLibrary(ctx context.Context, undoStore *UndoStore, libStore *library.Store, propStore *proposals.Store, entry UndoEntry) (UndoResult, error) {
	res := UndoResult{ProposalID: entry.ProposalID, Mode: mode.Movies}
	restoreFileForUndo(entry, &res)
	if err := revertTouchedRows(ctx, libStore, entry, &res); err != nil {
		return res, err
	}
	if err := restoreProposalSnapshot(ctx, propStore, entry); err != nil {
		return res, err
	}
	if err := undoStore.MarkConsumed(ctx, entry.ID); err != nil {
		return res, err
	}
	logUndoEvent(ctx, entry, res)
	return res, nil
}

// undoApplyLibrarySeries reverses one Series Apply. Identical structure to
// undoApplyLibrary — the only Series-specific behaviour lives in
// revertTouchedRows' library_episodes branch, and in what capture put (and did
// NOT put) in touched_rows_snapshot: never a library_series entry.
func undoApplyLibrarySeries(ctx context.Context, undoStore *UndoStore, libStore *library.Store, propStore *proposals.Store, entry UndoEntry) (UndoResult, error) {
	res := UndoResult{ProposalID: entry.ProposalID, Mode: mode.Series}
	restoreFileForUndo(entry, &res)
	if err := revertTouchedRows(ctx, libStore, entry, &res); err != nil {
		return res, err
	}
	if err := restoreProposalSnapshot(ctx, propStore, entry); err != nil {
		return res, err
	}
	if err := undoStore.MarkConsumed(ctx, entry.ID); err != nil {
		return res, err
	}
	logUndoEvent(ctx, entry, res)
	return res, nil
}

// undoApplyLibraryAdult reverses one Adult Apply. Same structure as the other
// two plus step 5, the give-back report.
func undoApplyLibraryAdult(ctx context.Context, undoStore *UndoStore, libStore *library.Store, propStore *proposals.Store, entry UndoEntry) (UndoResult, error) {
	res := UndoResult{ProposalID: entry.ProposalID, Mode: mode.Adult}
	restoreFileForUndo(entry, &res)
	if err := revertTouchedRows(ctx, libStore, entry, &res); err != nil {
		return res, err
	}
	if err := restoreProposalSnapshot(ctx, propStore, entry); err != nil {
		return res, err
	}
	// Step 5, Adult only. Set unconditionally from what was true at Apply time;
	// this must never attempt to contact StashDB/FansDB/TPDB.
	res.GiveBackNotRetractable = entry.GiveBackSubmitted
	if err := undoStore.MarkConsumed(ctx, entry.ID); err != nil {
		return res, err
	}
	logUndoEvent(ctx, entry, res)
	return res, nil
}

// restoreFileForUndo is step 1 — the gate that prevents cross-proposal file
// corruption. READ THIS BEFORE CHANGING IT.
//
// THE MOVE IS GATED ON A SIZE MATCH, NOT ON entry.ViaAlternateFold. The flag is
// an additional belt-and-braces short-circuit for one known class of entry; it
// is NOT the control, and an implementation that checks only the flag will
// corrupt a sibling proposal's file under the case below.
//
// Worked failure case, confirmed real during planning:
// proposal A applies simply (ViaAlternateFold = false on A's own entry). Later
// proposal B applies and takes the promote branch, renaming A's file to an
// alternate name and putting B's file at A's recorded path. If undo of A used
// A's own ViaAlternateFold as the gate, it would find a size mismatch at A's
// path and — under a naive "warn but proceed" reading of the best-effort
// policy — move B's FILE to A's source path, destroying B's still-valid Apply.
// The size match closes this with no cross-entry bookkeeping: proving the file
// at that path is not the one this Apply put there means moving it is not
// "proceeding despite drift", it is moving someone else's file. So it degrades
// to the missing-file case instead. This also covers Dedup, Purge, manual
// reorganisation, and every other way a different file could arrive at that
// path — failure modes no flag can enumerate ahead of time.
//
// SIZE, NOT MTIME: os.Rename preserves mtime, so mtime cannot detect a
// same-size file swap at a stable path. Size is the discriminator that matters.
//
// Uses moveUnique (rename_alternates.go), never a raw os.Rename: it MkdirAll's
// the destination directory (covering an original source directory pruned after
// Apply moved the last file out of it) and routes through place.UniquePath for
// collision handling, and it returns the []mode.PathChange the caller needs.
func restoreFileForUndo(entry UndoEntry, res *UndoResult) {
	if entry.PreApplySourcePath == "" {
		res.FileMessage = undoFileNoSourcePath
		return
	}
	// Belt-and-braces short-circuit ONLY — never the primary control. Cheap,
	// and removes all doubt for the one class of entry where the file at the
	// recorded path is known to be entangled with a second proposal's Apply.
	if entry.ViaAlternateFold {
		res.FileMessage = undoFileAlternateFold
		return
	}
	if entry.PostApplyPath == "" {
		res.FileMessage = undoFileNoRecordedPath
		return
	}

	info, err := os.Stat(entry.PostApplyPath)
	if err != nil || info.IsDir() {
		// Missing file: the explicitly approved behaviour (spec Round 5) —
		// revert everything still revertible and say the file was not restored.
		res.FileMessage = fmt.Sprintf(undoFileMissingFmt, entry.PostApplyPath)
		return
	}
	if info.Size() != entry.PostApplySize {
		// THE GATE. Not best-effort territory: this is a different file.
		res.FileMessage = fmt.Sprintf(undoFileDifferentFileFmt, entry.PostApplyPath, info.Size(), entry.PostApplySize)
		res.DriftDetected = true
		res.DriftWarnings = append(res.DriftWarnings, res.FileMessage)
		return
	}

	moved, changes, err := moveUnique(entry.PostApplyPath, entry.PreApplySourcePath)
	if err != nil {
		res.FileMessage = fmt.Sprintf(undoFileMoveFailedFmt, entry.PostApplyPath, entry.PreApplySourcePath, err)
		return
	}
	res.FileRestored = true
	res.RestoredPath = moved
	res.Changes = append(res.Changes, changes...)
	res.FileMessage = fmt.Sprintf(undoFileMovedFmt, moved)
}

// revertTouchedRows is step 3: for each archived touched row, delete it if this
// Apply INSERTed it, or restore it from its pre-Apply snapshot otherwise.
//
// touched_rows_snapshot IS AUTHORITATIVE AND IS DELIBERATELY INCOMPLETE. Two
// shared rows are excluded at capture time, on purpose:
//   - library_series (UNIQUE(tmdb_id)), shared by every episode of a show
//   - library_items keyed (mode='movies', tmdb_id=0), shared by every
//     unidentified-movie Apply
//
// An empty or short array for those Applies is expected, correct behaviour —
// NOT a signal that capture failed, and NOT licence to go look the row up and
// revert it anyway. Doing so would clobber a sibling proposal's later, still
// valid Apply. Do not add a fallback here.
//
// ACCEPTED CONSEQUENCE, recorded rather than solved: library_item_files /
// library_episode_files rows are never captured or reverted. For an
// alternate-fold entry that means the restored library_items/library_episodes
// row points at its pre-Apply primary path while the file rows still describe
// on-disk reality (undo moved nothing, by design — the alternative is moving
// another proposal's live file). A subsequent Scan reconciles it. That is
// strictly better than the corruption the file gate exists to prevent.
func revertTouchedRows(ctx context.Context, libStore *library.Store, entry UndoEntry, res *UndoResult) error {
	var touched []touchedRow
	if entry.TouchedRowsJSON != "" {
		if err := json.Unmarshal([]byte(entry.TouchedRowsJSON), &touched); err != nil {
			return fmt.Errorf("decoding touched rows for undo entry %d: %w", entry.ID, err)
		}
	}
	for _, t := range touched {
		noteRowDrift(ctx, libStore, entry, t, res)
		if t.WasInsert {
			if shared, why := rowNowSharedWithAnotherApply(ctx, libStore, t); shared {
				res.DriftDetected = true
				res.DriftWarnings = append(res.DriftWarnings, why)
				continue
			}
			if err := deleteTouchedRow(ctx, libStore, t); err != nil {
				return err
			}
			res.RowsReverted++
			continue
		}
		if t.PreApplyRowJSON == "" {
			res.DriftDetected = true
			res.DriftWarnings = append(res.DriftWarnings,
				fmt.Sprintf("%s row %d had no usable pre-Apply snapshot — left as-is", t.Table, t.RowID))
			continue
		}
		if err := restoreTouchedRow(ctx, libStore, t); err != nil {
			return err
		}
		res.RowsReverted++
	}
	return nil
}

// rowNowSharedWithAnotherApply reports whether a row this Apply INSERTed has
// since been folded into by a LATER proposal's alternate-fold Apply, making the
// delete unsafe.
//
// THIS IS THE ROW-SIDE HALF OF THE CROSS-PROPOSAL CORRUPTION GUARD, and it is
// as load-bearing as restoreFileForUndo's size-match gate. Worked case, the same
// one that gate exists for: proposal A simple-applies and INSERTs library_items
// row R (one primary file row). Proposal B later applies for the same TMDB id,
// takes the promote branch, demotes A's file to an alternate name and promotes
// its own into the canonical primary path — writing a SECOND library_item_files
// row against R. R is now shared. Undoing A on the plan's literal "if
// was_insert, delete that row by id" would delete R and cascade away B's file
// rows, destroying B's still-valid Apply. The plan's own §7 test requires the
// opposite ("B's ... library rows are completely untouched by A's undo"), so
// this guard is what makes both halves of the plan satisfiable.
//
// It cannot be folded into noteRowDrift's path comparison: the promote branch
// moves B's file INTO the canonical name A also used, so R.file_path is
// byte-identical before and after. More than one file row is the signal that
// actually distinguishes the two states.
//
// Adult is exempt by construction — library_scenes has no alternates table and
// ApplyLibraryAdult has no promote/demote branch, so a scene row can never
// become shared this way.
func rowNowSharedWithAnotherApply(ctx context.Context, libStore *library.Store, t touchedRow) (bool, string) {
	switch t.Table {
	case tableLibraryItems:
		files, err := libStore.ListFiles(ctx, t.RowID)
		if err != nil || len(files) <= 1 {
			return false, ""
		}
		return true, fmt.Sprintf("library_items row %d now holds %d files — another proposal folded an alternate into it after this Apply, so the row was left in place rather than deleted", t.RowID, len(files))
	case tableLibraryEpisodes:
		files, err := libStore.ListEpisodeFiles(ctx, t.RowID)
		if err != nil || len(files) <= 1 {
			return false, ""
		}
		return true, fmt.Sprintf("library_episodes row %d now holds %d files — another proposal folded an alternate into it after this Apply, so the row was left in place rather than deleted", t.RowID, len(files))
	default:
		return false, ""
	}
}

func deleteTouchedRow(ctx context.Context, libStore *library.Store, t touchedRow) error {
	switch t.Table {
	case tableLibraryItems:
		return libStore.Delete(ctx, t.RowID)
	case tableLibraryEpisodes:
		return libStore.DeleteEpisode(ctx, t.RowID)
	case tableLibraryScenes:
		return libStore.DeleteScene(ctx, t.RowID)
	default:
		return fmt.Errorf("undo: unknown touched table %q", t.Table)
	}
}

// restoreTouchedRow decodes one pre-Apply snapshot and hands it to the matching
// per-table restorer. Three explicit branches, deliberately not a generic
// reflection-based restorer — there are exactly three reversible tables.
func restoreTouchedRow(ctx context.Context, libStore *library.Store, t touchedRow) error {
	switch t.Table {
	case tableLibraryItems:
		return restoreLibraryItem(ctx, libStore, t)
	case tableLibraryEpisodes:
		return restoreLibraryEpisode(ctx, libStore, t)
	case tableLibraryScenes:
		return restoreLibraryScene(ctx, libStore, t)
	default:
		return fmt.Errorf("undo: unknown touched table %q", t.Table)
	}
}

func restoreLibraryItem(ctx context.Context, libStore *library.Store, t touchedRow) error {
	var item library.Item
	if err := json.Unmarshal([]byte(t.PreApplyRowJSON), &item); err != nil {
		return fmt.Errorf("decoding library_items snapshot for row %d: %w", t.RowID, err)
	}
	item.ID = t.RowID
	return libStore.RestoreItem(ctx, item)
}

func restoreLibraryEpisode(ctx context.Context, libStore *library.Store, t touchedRow) error {
	var ep library.Episode
	if err := json.Unmarshal([]byte(t.PreApplyRowJSON), &ep); err != nil {
		return fmt.Errorf("decoding library_episodes snapshot for row %d: %w", t.RowID, err)
	}
	ep.ID = t.RowID
	return libStore.RestoreEpisode(ctx, ep)
}

func restoreLibraryScene(ctx context.Context, libStore *library.Store, t touchedRow) error {
	var scene library.Scene
	if err := json.Unmarshal([]byte(t.PreApplyRowJSON), &scene); err != nil {
		return fmt.Errorf("decoding library_scenes snapshot for row %d: %w", t.RowID, err)
	}
	scene.ID = t.RowID
	return libStore.RestoreScene(ctx, scene)
}

// noteRowDrift records — but never blocks on — a library row that has changed
// since the Apply being undone (a later Re-pick, Dedup, Purge, or a sibling
// Apply's promote branch). Best-effort policy: warn and proceed.
//
// The predicate is "the row no longer points at the path this Apply left
// there". A missing row is drift too: something already deleted it.
func noteRowDrift(ctx context.Context, libStore *library.Store, entry UndoEntry, t touchedRow, res *UndoResult) {
	current, err := currentRowPath(ctx, libStore, t)
	if errors.Is(err, library.ErrNotFound) {
		res.DriftDetected = true
		res.DriftWarnings = append(res.DriftWarnings,
			fmt.Sprintf("%s row %d no longer exists — something removed it after this Apply", t.Table, t.RowID))
		return
	}
	if err != nil {
		return // a read failure is not evidence of drift; the revert below still runs
	}
	if entry.PostApplyPath != "" && current != entry.PostApplyPath {
		res.DriftDetected = true
		res.DriftWarnings = append(res.DriftWarnings,
			fmt.Sprintf("%s row %d now points at %q, not the %q this Apply set", t.Table, t.RowID, current, entry.PostApplyPath))
	}
}

func currentRowPath(ctx context.Context, libStore *library.Store, t touchedRow) (string, error) {
	switch t.Table {
	case tableLibraryItems:
		it, err := libStore.Get(ctx, t.RowID)
		if err != nil {
			return "", err
		}
		return it.FilePath, nil
	case tableLibraryEpisodes:
		ep, err := libStore.GetEpisodeByID(ctx, t.RowID)
		if err != nil {
			return "", err
		}
		return ep.FilePath, nil
	case tableLibraryScenes:
		sc, err := libStore.GetSceneByID(ctx, t.RowID)
		if err != nil {
			return "", err
		}
		return sc.FilePath, nil
	default:
		return "", fmt.Errorf("undo: unknown touched table %q", t.Table)
	}
}

// restoreProposalSnapshot is step 4: put the proposal row back WHOLESALE from
// the archive. Nothing is hand-picked — the snapshot is the complete pre-Apply
// row, so restoring it faithfully is what returns the proposal to Pending.
func restoreProposalSnapshot(ctx context.Context, propStore *proposals.Store, entry UndoEntry) error {
	var p proposals.Proposal
	if err := json.Unmarshal([]byte(entry.PreApplyProposalSnapshot), &p); err != nil {
		return fmt.Errorf("decoding proposal snapshot for undo entry %d: %w", entry.ID, err)
	}
	p.ID = entry.ProposalID
	return propStore.RestoreSnapshot(ctx, p)
}

// logUndoEvent is step 7: one organize_events row per undo, same Append pattern
// every other Rename action uses. Errors are discarded by organizeevents.Log —
// the activity log must never fail the action it is describing.
func logUndoEvent(ctx context.Context, entry UndoEntry, res UndoResult) {
	ok := true
	detail, err := json.Marshal(res)
	if err != nil {
		detail = nil
	}
	organizeevents.Log(ctx, organizeevents.Event{
		Workflow:   string(proposals.Rename),
		Mode:       string(entry.Mode),
		Kind:       organizeevents.KindUndo,
		ProposalID: entry.ProposalID,
		OK:         &ok,
		Message:    res.FileMessage,
		DetailJSON: string(detail),
	})
}
