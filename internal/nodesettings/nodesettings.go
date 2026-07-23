// Package nodesettings persists a connected node's operator-set path
// mappings and concurrency cap (MaxJobs) — the durable record backing the
// authoritative reconnect re-push and the settings-edit form's "current
// values" prefill (both previously nonexistent: nodes.Registry is pure
// in-memory, so before this package a node's settings only ever lived in
// its own local config.json, never on the server). Keyed by the durable
// node id resolved via internal/nodekeys/internal/auth (see
// internal/nodes/registry.go's Connect).
package nodesettings

import (
	"context"
	"database/sql"
	"time"
)

// VerificationStatus records how a PathMappingEntry's NodePath was
// confirmed to correspond to its LibraryPathKey's server-side value, per
// the security-hardening addendum's mapping-verification safeguard.
type VerificationStatus string

const (
	// VerificationVerified means a live directory-listing comparison ran
	// between the server's ServerPath and the node's NodePath and passed
	// the containment threshold.
	VerificationVerified VerificationStatus = "verified"
	// VerificationUnverifiedBootstrap means a live comparison ran but one
	// or both listings were empty — nothing to compare, so the row is
	// accepted but not confirmed correct.
	VerificationUnverifiedBootstrap VerificationStatus = "unverified_bootstrap"
	// VerificationUnverifiedApproval means the row was persisted at
	// approval time, before the node has an authenticated channel a live
	// comparison could use at all (see the Reachability constraint in the
	// security-hardening addendum) — structurally distinct from the
	// bootstrap case, which did attempt a comparison.
	VerificationUnverifiedApproval VerificationStatus = "unverified_approval"
)

// PathMappingEntry is one persisted (library path key → node-local path)
// mapping row.
type PathMappingEntry struct {
	LibraryPathKey     string
	NodePath           string
	VerificationStatus VerificationStatus
	VerifiedAt         *time.Time
}

// Settings is everything persisted for one node: its path mappings and its
// concurrency cap. Both travel together because the wire format pushed to
// the node (nodes.NodeSettings) carries both together — see this package's
// doc comment and node_max_jobs's migration comment for why MaxJobs must be
// persisted alongside PathMappings, not just the mappings alone.
type Settings struct {
	PathMappings []PathMappingEntry
	MaxJobs      int
	// CPUCapPercent is the operator-owned max-CPU governor ("% of total CPU",
	// 0 = unlimited). Like MaxJobs — and unlike PauseDispatch — it is operator-
	// owned with no parallel-write conflict, so it rides the shared Set write
	// path alongside MaxJobs rather than getting its own column-scoped method.
	// Shares the node_max_jobs row with MaxJobs/PauseDispatch.
	CPUCapPercent int
	// PauseDispatch is the server-owned per-node dispatch-exclusion bit. It is
	// read here (Get includes it) but written ONLY by the dedicated,
	// column-scoped SetPauseDispatch — never by Set — so a MaxJobs/PathMap save
	// can never reset it and vice versa (the parallel-write footgun, resolved
	// at the storage layer). Shares the node_max_jobs row with MaxJobs.
	PauseDispatch bool
}

// Store is a SQLite-backed store for persisted per-node settings.
type Store struct {
	db *sql.DB
}

// New returns a Store backed by db.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Get returns nodeID's persisted settings. ok is false when nothing has ever
// been persisted for this node (e.g. every already-approved node before this
// feature shipped, or before its first save) — callers must not treat a
// zero-value Settings{} as "MaxJobs should be reset to unlimited" in that
// case; ok=false means "nothing to push," not "push zero values."
func (s *Store) Get(ctx context.Context, nodeID string) (settings Settings, ok bool, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT library_path_key, node_path, verification_status, verified_at FROM node_path_mappings WHERE node_id = ?`, nodeID)
	if err != nil {
		return Settings{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var e PathMappingEntry
		var status string
		var verifiedAt sql.NullString
		if err := rows.Scan(&e.LibraryPathKey, &e.NodePath, &status, &verifiedAt); err != nil {
			return Settings{}, false, err
		}
		e.VerificationStatus = VerificationStatus(status)
		if verifiedAt.Valid {
			t, err := time.Parse(time.RFC3339, verifiedAt.String)
			if err != nil {
				return Settings{}, false, err
			}
			e.VerifiedAt = &t
		}
		settings.PathMappings = append(settings.PathMappings, e)
	}
	if err := rows.Err(); err != nil {
		return Settings{}, false, err
	}

	var maxJobs sql.NullInt64
	var pauseDispatch sql.NullInt64
	var cpuCapPercent sql.NullInt64
	row := s.db.QueryRowContext(ctx, `SELECT max_jobs, pause_dispatch, cpu_cap_percent FROM node_max_jobs WHERE node_id = ?`, nodeID)
	switch err := row.Scan(&maxJobs, &pauseDispatch, &cpuCapPercent); {
	case err == sql.ErrNoRows:
		// No max_jobs row yet — fine on its own, doesn't change whether
		// this Get found anything overall (path mappings may still exist).
	case err != nil:
		return Settings{}, false, err
	default:
		settings.MaxJobs = int(maxJobs.Int64)
		settings.PauseDispatch = pauseDispatch.Int64 != 0
		settings.CPUCapPercent = int(cpuCapPercent.Int64)
	}

	found := len(settings.PathMappings) > 0 || maxJobs.Valid
	return settings, found, nil
}

// Set persists nodeID's settings, transactionally: every path mapping entry
// is upserted (existing rows for keys not present in entries.PathMappings
// are left as-is — this Store never deletes a row implicitly), and MaxJobs
// is upserted into node_max_jobs.
func (s *Store) Set(ctx context.Context, nodeID string, settings Settings) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op if Commit already succeeded

	now := time.Now().UTC().Format(time.RFC3339)
	for _, e := range settings.PathMappings {
		var verifiedAt sql.NullString
		if e.VerifiedAt != nil {
			verifiedAt = sql.NullString{String: e.VerifiedAt.UTC().Format(time.RFC3339), Valid: true}
		}
		status := e.VerificationStatus
		if status == "" {
			status = VerificationUnverifiedBootstrap
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO node_path_mappings (node_id, library_path_key, node_path, verification_status, verified_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT (node_id, library_path_key) DO UPDATE SET node_path = excluded.node_path, verification_status = excluded.verification_status, verified_at = excluded.verified_at, updated_at = excluded.updated_at`,
			nodeID, e.LibraryPathKey, e.NodePath, string(status), verifiedAt, now,
		); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO node_max_jobs (node_id, max_jobs, cpu_cap_percent, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (node_id) DO UPDATE SET max_jobs = excluded.max_jobs, cpu_cap_percent = excluded.cpu_cap_percent, updated_at = excluded.updated_at`,
		nodeID, settings.MaxJobs, settings.CPUCapPercent, now,
	); err != nil {
		return err
	}

	return tx.Commit()
}

// SetPauseDispatch persists nodeID's dispatch-pause bit with a column-scoped
// upsert that touches ONLY pause_dispatch/updated_at — never max_jobs or any
// path mapping. This is the storage-layer half of the parallel-write footgun
// elimination: pause is the only field this method writes, and Set (MaxJobs/
// PathMap) never writes pause, so the two writers cannot interfere.
//
// The INSERT names max_jobs explicitly because node_max_jobs.max_jobs is
// INTEGER NOT NULL with no default — a fresh-row insert omitting it would be a
// NOT NULL violation. A new row seeds max_jobs = 0 (unlimited/unset, matching
// the no-row default); the ON CONFLICT clause leaves an existing max_jobs
// untouched.
func (s *Store) SetPauseDispatch(ctx context.Context, nodeID string, paused bool) error {
	pauseInt := 0
	if paused {
		pauseInt = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO node_max_jobs (node_id, max_jobs, pause_dispatch, updated_at)
		 VALUES (?, 0, ?, ?)
		 ON CONFLICT (node_id) DO UPDATE SET
		   pause_dispatch = excluded.pause_dispatch,
		   updated_at     = excluded.updated_at`,
		nodeID, pauseInt, now,
	)
	return err
}

// Delete removes the single (nodeID, key) path-mapping row. Unlike Set — which
// upserts and treats a blank NodePath as "skip", so it can never express a
// deletion — this is a real row delete (D7): it is how a node authors the
// removal of a now-stale mapping (e.g. after a reimage), so the old row cannot
// survive and re-push to the node on its next reconnect. Deleting a key that
// has no row is a no-op, not an error. MaxJobs (node_max_jobs) is untouched.
func (s *Store) Delete(ctx context.Context, nodeID, key string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM node_path_mappings WHERE node_id = ? AND library_path_key = ?`,
		nodeID, key,
	)
	return err
}
