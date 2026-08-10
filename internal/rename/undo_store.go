package rename

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/labbersanon/sakms/internal/mode"
)

// Rename Undo — persistence layer (deep-interview-rename-undo, backend pass).
//
// This file owns the rename_undo_archive table (migration 0006): one row per
// undoable Apply, per mode, with a rolling depth. undo_archive.go writes rows
// through it at Apply time; undo_apply.go reads and consumes them.

// ErrNoUndoEntry is returned by LatestActiveFor when a proposal has no
// still-undoable archive row. An EVICTED entry is indistinguishable from
// "nothing was ever archived" here, deliberately: eviction is exactly the
// statement "this is no longer undoable", so the caller's 404 is the correct,
// deterministic outcome for both.
var ErrNoUndoEntry = errors.New("rename: no undoable archive entry for that proposal")

// DefaultUndoDepth is the per-mode rolling undo depth used when the mode has
// no configured {mode}_undo_depth setting. Applied where the depth is READ,
// never pre-seeded into the settings table (mirrors every other optional
// per-mode setting in this codebase).
const DefaultUndoDepth = 10

// touchedRow is one library row a single Apply created or overwrote.
//
// JSON shape persisted in rename_undo_archive.touched_rows_snapshot (a JSON
// array of these):
//
//	[
//	  {
//	    "table": "library_items | library_episodes | library_scenes",
//	    "rowId": 123,
//	    "wasInsert": true,
//	    "preApplyRowJson": "...(full row snapshot, or \"\" if wasInsert — nothing
//	                        to restore, just delete the row)...",
//	    "filePath": "the path THIS row pointed at before this Apply, \"\" if none"
//	  }
//	]
//
// THE ARRAY IS DELIBERATELY INCOMPLETE FOR TWO SHARED ROWS and an empty or
// short array is expected, correct behaviour rather than a capture failure:
//
//   - library_series (UNIQUE(tmdb_id)) is shared by every episode of a show.
//     A Series entry NEVER carries one. Restoring it from episode A's snapshot
//     would clobber whatever episode B's later Apply legitimately wrote.
//   - library_items keyed (mode='movies', tmdb_id=0) is shared by every
//     unidentified-movie Apply, for exactly the same structural reason
//     (Upsert's ON CONFLICT(mode, tmdb_id) target). A Movies entry with
//     TMDBID == 0 NEVER carries one.
//
// Undo's row-revert step must never go looking for either row to "fill in" a
// revert that capture omitted. See undo_apply.go's revertTouchedRows.
type touchedRow struct {
	Table           string `json:"table"`
	RowID           int64  `json:"rowId"`
	WasInsert       bool   `json:"wasInsert"`
	PreApplyRowJSON string `json:"preApplyRowJson,omitempty"`
	FilePath        string `json:"filePath,omitempty"`
}

// Table names permitted in touchedRow.Table. library_series and
// library_item_files/library_episode_files are deliberately absent.
const (
	tableLibraryItems    = "library_items"
	tableLibraryEpisodes = "library_episodes"
	tableLibraryScenes   = "library_scenes"
)

// UndoEntry is one persisted rename_undo_archive row.
type UndoEntry struct {
	ID                 int64     `json:"id"`
	ProposalID         int64     `json:"proposalId"`
	Mode               mode.Mode `json:"mode"`
	AppliedAt          string    `json:"appliedAt"`
	PreApplySourcePath string    `json:"preApplySourcePath"`
	// PreApplyProposalSnapshot is the JSON-encoded pre-Apply proposals.Proposal
	// row, restored WHOLESALE on undo (never field-by-field cherry-picking).
	PreApplyProposalSnapshot string `json:"preApplyProposalSnapshot"`
	TouchedRowsJSON          string `json:"touchedRowsJson"`
	// PostApplyPath/PostApplySize describe the file this Apply left on disk.
	// They are the ONLY inputs to undo's size-match file gate — see
	// undo_apply.go's restoreFileForUndo and migration 0006's column comment
	// for why they cannot be derived from TouchedRowsJSON.
	PostApplyPath     string `json:"postApplyPath"`
	PostApplySize     int64  `json:"postApplySize"`
	ViaAlternateFold  bool   `json:"viaAlternateFold"`
	GiveBackSubmitted bool   `json:"giveBackSubmitted"`
	ConsumedAt        string `json:"consumedAt,omitempty"`
	EvictedAt         string `json:"evictedAt,omitempty"`
	CreatedAt         string `json:"createdAt"`
}

// DepthFunc resolves a mode's configured rolling undo depth. It is supplied by
// the caller (internal/api owns the settings key) so this package keeps its
// standing rule of never importing internal/settings. A nil DepthFunc, or a
// non-positive return, falls back to DefaultUndoDepth.
type DepthFunc func(ctx context.Context, m mode.Mode) int

// UndoStore reads and writes rename_undo_archive.
type UndoStore struct {
	db    *sql.DB
	depth DepthFunc
}

func NewUndoStore(db *sql.DB, depth DepthFunc) *UndoStore {
	return &UndoStore{db: db, depth: depth}
}

var (
	defaultUndoMu    sync.RWMutex
	defaultUndoStore *UndoStore
)

// SetDefaultUndoStore registers the process-wide store recordUndoArchive
// writes through. Mirrors organizeevents.SetDefault exactly, and for the same
// reason: the alternative is growing ApplyLibrary/ApplyLibrarySeries/
// ApplyLibraryAdult's signatures (and every one of their call sites and tests)
// solely to thread one optional dependency. main.go calls this once at boot.
//
// Archiving NO-OPS when unset — an Apply on an install that never registered a
// store behaves exactly as it did before this feature existed.
func SetDefaultUndoStore(s *UndoStore) {
	defaultUndoMu.Lock()
	defaultUndoStore = s
	defaultUndoMu.Unlock()
}

// DefaultUndoStore returns the registered process-wide store, or nil.
func DefaultUndoStore() *UndoStore {
	defaultUndoMu.RLock()
	defer defaultUndoMu.RUnlock()
	return defaultUndoStore
}

// DepthFor resolves m's rolling undo depth, falling back to DefaultUndoDepth.
func (s *UndoStore) DepthFor(ctx context.Context, m mode.Mode) int {
	if s == nil || s.depth == nil {
		return DefaultUndoDepth
	}
	if n := s.depth(ctx, m); n > 0 {
		return n
	}
	return DefaultUndoDepth
}

const undoEntryColumns = `id, proposal_id, mode, applied_at, pre_apply_source_path,
	pre_apply_proposal_snapshot, touched_rows_snapshot, post_apply_path, post_apply_size,
	via_alternate_fold, give_back_submitted, COALESCE(consumed_at, ''), COALESCE(evicted_at, ''), created_at`

func scanUndoEntry(row interface{ Scan(...any) error }) (UndoEntry, error) {
	var e UndoEntry
	var m string
	if err := row.Scan(&e.ID, &e.ProposalID, &m, &e.AppliedAt, &e.PreApplySourcePath,
		&e.PreApplyProposalSnapshot, &e.TouchedRowsJSON, &e.PostApplyPath, &e.PostApplySize,
		&e.ViaAlternateFold, &e.GiveBackSubmitted, &e.ConsumedAt, &e.EvictedAt, &e.CreatedAt); err != nil {
		return UndoEntry{}, err
	}
	e.Mode = mode.Mode(m)
	return e, nil
}

// Insert persists one archive row, then evicts that mode's excess entries.
// A failed eviction does not fail the insert: the entry is already recorded and
// correct, and the next Apply's eviction pass sweeps the same backlog.
func (s *UndoStore) Insert(ctx context.Context, e UndoEntry) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO rename_undo_archive (
			proposal_id, mode, applied_at, pre_apply_source_path,
			pre_apply_proposal_snapshot, touched_rows_snapshot,
			post_apply_path, post_apply_size, via_alternate_fold, give_back_submitted
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id
	`, e.ProposalID, string(e.Mode), e.AppliedAt, e.PreApplySourcePath,
		e.PreApplyProposalSnapshot, e.TouchedRowsJSON,
		e.PostApplyPath, e.PostApplySize, e.ViaAlternateFold, e.GiveBackSubmitted)
	var id int64
	if err := row.Scan(&id); err != nil {
		return 0, fmt.Errorf("recording undo archive for proposal %d: %w", e.ProposalID, err)
	}
	if err := s.EvictBeyondDepth(ctx, e.Mode); err != nil {
		return id, err
	}
	return id, nil
}

// LatestActiveFor returns the newest still-undoable entry for proposalID.
// "Active" is consumed_at IS NULL AND evicted_at IS NULL — both conditions,
// independently: a consumed entry was undone (terminal) and an evicted one was
// pushed out by the mode's rolling depth. Neither is undoable, and the two are
// never conflated.
func (s *UndoStore) LatestActiveFor(ctx context.Context, proposalID int64) (UndoEntry, error) {
	if s == nil || s.db == nil {
		return UndoEntry{}, ErrNoUndoEntry
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT `+undoEntryColumns+`
		FROM rename_undo_archive
		WHERE proposal_id = ? AND consumed_at IS NULL AND evicted_at IS NULL
		ORDER BY id DESC LIMIT 1
	`, proposalID)
	e, err := scanUndoEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return UndoEntry{}, ErrNoUndoEntry
	}
	if err != nil {
		return UndoEntry{}, fmt.Errorf("loading undo archive for proposal %d: %w", proposalID, err)
	}
	return e, nil
}

// ListActive returns m's still-undoable entries, newest first. Backs the
// "Recently Applied" list Pass 2 renders; bounded by the mode's own depth so
// the list IS the full undoable set by construction.
func (s *UndoStore) ListActive(ctx context.Context, m mode.Mode) ([]UndoEntry, error) {
	if s == nil || s.db == nil {
		return []UndoEntry{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+undoEntryColumns+`
		FROM rename_undo_archive
		WHERE mode = ? AND consumed_at IS NULL AND evicted_at IS NULL
		ORDER BY id DESC LIMIT ?
	`, string(m), s.DepthFor(ctx, m))
	if err != nil {
		return nil, fmt.Errorf("listing undo archive for %s: %w", m, err)
	}
	defer rows.Close()
	out := []UndoEntry{}
	for rows.Next() {
		e, err := scanUndoEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkConsumed records that entry id was undone. It sets consumed_at ONLY and
// never reads, clears or backdates evicted_at — the two lifecycle fields track
// different transitions and undoing an entry must never look like eviction.
func (s *UndoStore) MarkConsumed(ctx context.Context, id int64) error {
	if s == nil || s.db == nil {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE rename_undo_archive SET consumed_at = sakms_now() WHERE id = ? AND consumed_at IS NULL`,
		id); err != nil {
		return fmt.Errorf("marking undo archive %d consumed: %w", id, err)
	}
	return nil
}

// EvictBeyondDepth marks m's oldest excess active entries evicted, keeping the
// mode's configured depth. It sets evicted_at ONLY and NEVER consumed_at, which
// is reserved exclusively for "this entry was undone" — conflating the two
// means undoing an entry silently frees an eviction slot and the undo window
// grows back past the configured N.
//
// Check-on-Apply, not check-on-settings-change: lowering a mode's depth does
// not retroactively prune, it takes effect as new Applies occur (spec Non-Goal).
//
// The OFFSET-inside-a-subselect shape mirrors organizeevents.Prune.
func (s *UndoStore) EvictBeyondDepth(ctx context.Context, m mode.Mode) error {
	if s == nil || s.db == nil {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE rename_undo_archive SET evicted_at = sakms_now()
		WHERE id IN (
			SELECT id FROM (
				SELECT id FROM rename_undo_archive
				WHERE mode = ? AND consumed_at IS NULL AND evicted_at IS NULL
				ORDER BY id DESC OFFSET ?
			) excess
		)
	`, string(m), s.DepthFor(ctx, m)); err != nil {
		return fmt.Errorf("evicting undo archive entries for %s: %w", m, err)
	}
	return nil
}
