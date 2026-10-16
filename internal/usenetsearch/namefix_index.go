package usenetsearch

import (
	"context"
	"strings"
	"time"
)

// Name-recovery storage helpers (plan D3). All DB only.

// nameRetryBase is the delay before the next name-recovery try after
// `attempts` failures: base << attempts.
const nameRetryBase = 10 * time.Minute

// maxNameAttempts is the failed-attempt count at which an unresolved set is
// finalized (deleted).
const maxNameAttempts = 3

// DefaultUnnamedTTLDays is how long an unresolved staged set is kept.
const DefaultUnnamedTTLDays = 7

// PendingSet is one staged obfuscated set awaiting a name.
type PendingSet struct {
	Group        string
	Key          string
	PayloadBytes int64
	Attempts     int
	// CompletenessBP is the set's completeness in basis points (log only).
	CompletenessBP int
}

// StagedBytes is the payload size of all unresolved staged sets (as of their
// last recompute; sets staged this very chunk count 0 until then).
func (idx *Index) StagedBytes(ctx context.Context) (int64, error) {
	var n int64
	err := idx.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(payload_bytes),0) FROM usenet_nntp_releases WHERE state='pending_name'`).Scan(&n)
	return n, err
}

// DuePendingNames returns up to limit staged sets in groups whose retry time
// has passed, recomputed, and at/above the completeness gate (a BODY fetch is
// only spent on sets that could become searchable), newest first.
func (idx *Index) DuePendingNames(ctx context.Context, groups []string, limit int) ([]PendingSet, error) {
	if len(groups) == 0 || limit <= 0 {
		return nil, nil
	}
	ph := make([]string, len(groups))
	args := make([]any, 0, len(groups)+3)
	args = append(args, idx.MinCompletenessPct()*100, time.Now().Unix())
	for i, g := range groups {
		ph[i] = "?"
		args = append(args, g)
	}
	args = append(args, limit)
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key, payload_bytes, name_attempts, completeness_bp FROM usenet_nntp_releases
WHERE state='pending_name' AND dirty=FALSE AND declared_segs>0 AND completeness_bp>=?
  AND next_name_at<=? AND group_name IN (`+strings.Join(ph, ",")+`)
ORDER BY ingested_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingSet
	for rows.Next() {
		var p PendingSet
		if err := rows.Scan(&p.Group, &p.Key, &p.PayloadBytes, &p.Attempts, &p.CompletenessBP); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetRows loads every header row of one release/staged-set key.
func (idx *Index) SetRows(ctx context.Context, group, key string) ([]HeaderRow, error) {
	rows, err := idx.db.QueryContext(ctx, `
SELECT msg_num, msgid, subject, from_addr, posted_at, bytes, part_n, part_m, filename, yenc_size, is_meta, file_n, file_m
FROM usenet_nntp_headers WHERE group_name=? AND release_name=? ORDER BY filename, part_n, msg_num`, group, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeaderRow
	for rows.Next() {
		h := HeaderRow{Group: group, ReleaseName: key}
		if err := rows.Scan(&h.MsgNum, &h.MsgID, &h.Subject, &h.From, &h.PostedAt, &h.Bytes, &h.PartN, &h.PartM,
			&h.Filename, &h.YencSize, &h.IsMeta, &h.FileN, &h.FileM); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// RenameRelease sets the recovered display name on a staged set and marks it
// dirty so the recompute pass can make it searchable. Headers are untouched,
// so the rename is reversible (undo SQL in docs/usenet-nntp-native.md).
func (idx *Index) RenameRelease(ctx context.Context, group, key, display, source, confidence string) error {
	_, err := idx.db.ExecContext(ctx, `
UPDATE usenet_nntp_releases SET display_name=?, name_source=?, name_confidence=?,
  state='named', dirty=TRUE, updated_at=? WHERE group_name=? AND release_key=?`,
		display, source, confidence, time.Now().Unix(), group, key)
	return err
}

// BumpNameAttempt records a failed recovery try and schedules the next one.
func (idx *Index) BumpNameAttempt(ctx context.Context, group, key string, attempts int) error {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > 8 {
		attempts = 8
	}
	next := time.Now().Add(nameRetryBase << attempts).Unix()
	_, err := idx.db.ExecContext(ctx, `
UPDATE usenet_nntp_releases SET name_attempts=name_attempts+1, next_name_at=?, updated_at=?
WHERE group_name=? AND release_key=?`, next, time.Now().Unix(), group, key)
	return err
}

// FinalizeUnnamed marks sets unnamed_final and deletes their headers + release
// rows (aggregate logging only; the mark exists for the delete's audit trail).
func (idx *Index) FinalizeUnnamed(ctx context.Context, keys []releaseKey) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	now := time.Now().Unix()
	for _, k := range keys {
		if _, err := idx.db.ExecContext(ctx,
			`UPDATE usenet_nntp_releases SET state=?, updated_at=? WHERE group_name=? AND release_key=?`,
			stateUnnamedFinal, now, k.group, k.key); err != nil {
			return 0, err
		}
	}
	return idx.deleteReleaseSets(ctx, keys)
}

// SweepUnnamed finalizes staged sets older than ttl or out of attempts.
func (idx *Index) SweepUnnamed(ctx context.Context, ttl time.Duration) (int, error) {
	cutoff := time.Now().Add(-ttl).Unix()
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key FROM usenet_nntp_releases
WHERE state='pending_name' AND (ingested_at < ? OR name_attempts >= ?) LIMIT ?`,
		cutoff, maxNameAttempts, incompleteSweepBatch)
	if err != nil {
		return 0, err
	}
	var keys []releaseKey
	for rows.Next() {
		var k releaseKey
		if err := rows.Scan(&k.group, &k.key); err != nil {
			rows.Close()
			return 0, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if _, err := idx.FinalizeUnnamed(ctx, keys); err != nil {
		return 0, err
	}
	return len(keys), nil
}
