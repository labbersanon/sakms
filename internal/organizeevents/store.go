// Package organizeevents persists the Organize activity log (Rename/Dedup/Purge):
// scan, apply, dismiss, and error events with dual retention (age + count).
package organizeevents

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Retention defaults from deep-interview-organize-pagination-log.
const (
	DefaultMaxAgeDays = 30
	DefaultMaxEvents  = 5000
)

// Kind values written by API handlers.
const (
	KindScan       = "scan"
	KindApply      = "apply"
	KindApplyItem  = "apply_item"
	KindDismiss    = "dismiss"
	KindError      = "error"
	KindApplyBatch = "apply_batch"
)

// Event is one persisted organize activity row.
type Event struct {
	ID         int64  `json:"id"`
	Workflow   string `json:"workflow"`
	Mode       string `json:"mode"`
	Kind       string `json:"kind"`
	ProposalID int64  `json:"proposalId,omitempty"`
	OK         *bool  `json:"ok,omitempty"`
	Message    string `json:"message"`
	DetailJSON string `json:"detailJson,omitempty"`
	CreatedAt  string `json:"createdAt"`
}

// Store appends and lists organize_events.
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

var (
	defaultMu    sync.RWMutex
	defaultStore *Store
)

// SetDefault registers the process-wide store used by Log (avoids growing
// every API handler signature). main.go calls this once at boot.
func SetDefault(s *Store) {
	defaultMu.Lock()
	defaultStore = s
	defaultMu.Unlock()
}

// Log appends via the default store; no-op if unset. Errors are discarded
// (activity log must never fail an Apply/Dismiss/Scan).
func Log(ctx context.Context, e Event) {
	defaultMu.RLock()
	s := defaultStore
	defaultMu.RUnlock()
	if s == nil {
		return
	}
	_ = s.Append(ctx, e)
}

// Append inserts one event, then best-effort prunes to retention defaults.
func (s *Store) Append(ctx context.Context, e Event) error {
	if s == nil || s.db == nil {
		return nil
	}
	var ok any
	if e.OK != nil {
		ok = *e.OK
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO organize_events (workflow, mode, kind, proposal_id, ok, message, detail_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, e.Workflow, e.Mode, e.Kind, e.ProposalID, ok, e.Message, e.DetailJSON)
	if err != nil {
		return fmt.Errorf("organizeevents append: %w", err)
	}
	_ = s.Prune(ctx, DefaultMaxAgeDays, DefaultMaxEvents)
	return nil
}

// List returns newest-first events, optionally filtered by workflow (empty = all).
func (s *Store) List(ctx context.Context, workflow string, limit int) ([]Event, error) {
	if s == nil || s.db == nil {
		return []Event{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var (
		rows *sql.Rows
		err  error
	)
	if workflow == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, workflow, mode, kind, proposal_id, ok, message, detail_json, created_at
			FROM organize_events ORDER BY id DESC LIMIT ?
		`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, workflow, mode, kind, proposal_id, ok, message, detail_json, created_at
			FROM organize_events WHERE workflow = ? ORDER BY id DESC LIMIT ?
		`, workflow, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("organizeevents list: %w", err)
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		var e Event
		var okNull sql.NullBool
		if err := rows.Scan(&e.ID, &e.Workflow, &e.Mode, &e.Kind, &e.ProposalID, &okNull, &e.Message, &e.DetailJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		if okNull.Valid {
			v := okNull.Bool
			e.OK = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune deletes events older than maxAgeDays, then trims to maxEvents newest.
func (s *Store) Prune(ctx context.Context, maxAgeDays, maxEvents int) error {
	if s == nil || s.db == nil {
		return nil
	}
	if maxAgeDays <= 0 {
		maxAgeDays = DefaultMaxAgeDays
	}
	if maxEvents <= 0 {
		maxEvents = DefaultMaxEvents
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -maxAgeDays).Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM organize_events WHERE created_at < ?`, cutoff); err != nil {
		return fmt.Errorf("organizeevents prune age: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM organize_events WHERE id IN (
			SELECT id FROM (
				SELECT id FROM organize_events ORDER BY id DESC OFFSET ?
			) old_rows
		)
	`, maxEvents)
	if err != nil {
		return fmt.Errorf("organizeevents prune count: %w", err)
	}
	return nil
}
