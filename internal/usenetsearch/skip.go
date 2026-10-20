package usenetsearch

import (
	"context"
	"strings"
	"time"
)

// Skip reasons recorded in usenet_nntp_skips.reason.
const (
	skipReasonIncomplete  = "incomplete"
	skipReasonUnnamed     = "unnamed"
	skipReasonImplausible = "implausible"
	skipReasonRejected    = "rejected"
	skipReasonOrphan      = "orphan"
)

// skipAndDelete records skip stubs then removes headers + release rows so a
// later OVER of the same articles does not rebuild the set.
//
// Claude 2026-10-18: persistent skip instead of forget-on-delete.
// Reason: operator 1B/3B — incomplete lives only as a skip key; searchable
// and ≥80%/in-window sets are never passed here.
// Troubleshooting: skip row exists but headers return → ingest DropSkipped
// not applied to that path.
// Review if: liveness STAT removal should use a different reason.
func (idx *Index) skipAndDelete(ctx context.Context, keys []releaseKey, reason string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	if reason == "" {
		reason = skipReasonIncomplete
	}
	now := time.Now().Unix()
	tx, err := idx.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ins, err := tx.PrepareContext(ctx, `
INSERT INTO usenet_nntp_skips (group_name, release_key, reason, first_msg, last_msg, skipped_at)
SELECT group_name, release_key, ?, first_msg, last_msg, ?
FROM usenet_nntp_releases WHERE group_name=? AND release_key=?
ON CONFLICT (group_name, release_key) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	defer ins.Close()
	var total int64
	for _, k := range keys {
		if _, err := ins.ExecContext(ctx, reason, now, k.group, k.key); err != nil {
			return 0, err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM usenet_nntp_headers WHERE group_name=? AND release_name=?`, k.group, k.key)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		total += n
		// Claude 2026-10-20: drop hash_16K rows with the release.
		// Reason: skip stubs must not leave donor hashes pointing at deleted keys.
		// Troubleshooting: parhash lookup returns a missing release → this DELETE skipped.
		// Review if: releases gain an ON DELETE CASCADE FK.
		if _, err := tx.ExecContext(ctx, `DELETE FROM usenet_nntp_par_hashes WHERE group_name=? AND release_key=?`, k.group, k.key); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM usenet_nntp_releases WHERE group_name=? AND release_key=?`, k.group, k.key); err != nil {
			return 0, err
		}
	}
	return total, tx.Commit()
}

// DropSkipped removes header rows whose release key is already skipped.
func (idx *Index) DropSkipped(ctx context.Context, group string, headers []HeaderRow) ([]HeaderRow, int, error) {
	if len(headers) == 0 {
		return headers, 0, nil
	}
	names := make([]string, 0, len(headers))
	seen := map[string]bool{}
	for _, h := range headers {
		if h.ReleaseName == "" || seen[h.ReleaseName] {
			continue
		}
		seen[h.ReleaseName] = true
		names = append(names, h.ReleaseName)
	}
	if len(names) == 0 {
		return headers, 0, nil
	}
	ph := make([]string, len(names))
	args := make([]any, 0, len(names)+1)
	args = append(args, group)
	for i, n := range names {
		ph[i] = "?"
		args = append(args, n)
	}
	rows, err := idx.db.QueryContext(ctx, `
SELECT release_key FROM usenet_nntp_skips
WHERE group_name=? AND release_key IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	skipped := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, 0, err
		}
		skipped[n] = true
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(skipped) == 0 {
		return headers, 0, nil
	}
	kept := headers[:0:0]
	for _, h := range headers {
		if !skipped[h.ReleaseName] {
			kept = append(kept, h)
		}
	}
	return kept, len(headers) - len(kept), nil
}
