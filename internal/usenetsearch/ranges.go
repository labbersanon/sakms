package usenetsearch

import (
	"context"
	"strings"
	"time"
)

// Claude 2026-10-15: missed-range queue + backfill cursor storage (plan D5/D6).
// Reason: ranges (not NNTmux per-article rows) because 2000-article chunks
//   produce far fewer gap runs than missing articles.
// Troubleshooting: queue growing without draining → check "repair pass" log
//   lines for busy=true, and usenet_nntp_missed_ranges.attempts/next_try_at.
// Review if: per-article missed parts become necessary.

const (
	// repairMaxAttempts mirrors NNTmux partRepairMaxTries.
	repairMaxAttempts = 3
	// repairBaseDelay is the first retry delay; doubles per attempt.
	repairBaseDelay = 10 * time.Minute
)

// MissedRange is one queued OVER hole.
type MissedRange struct {
	Group    string
	Lo, Hi   int
	Reason   string // gap|span
	Attempts int
}

// repairBackoff is the delay before the next try after `attempts` failures.
func repairBackoff(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > 8 {
		attempts = 8
	}
	return repairBaseDelay << attempts
}

// AddMissedRanges queues ranges (idempotent) and enforces the per-group cap,
// dropping the oldest (lowest-numbered) ranges first.
func (idx *Index) AddMissedRanges(ctx context.Context, group string, ranges [][2]int, reason string) error {
	if len(ranges) == 0 {
		return nil
	}
	tx, err := idx.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO usenet_nntp_missed_ranges (group_name, lo, hi, reason, attempts, next_try_at)
VALUES (?,?,?,?,0,?) ON CONFLICT (group_name, lo, hi) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	next := time.Now().Add(repairBackoff(0)).Unix()
	for _, r := range ranges {
		if _, err := stmt.ExecContext(ctx, group, r[0], r[1], reason, next); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM usenet_nntp_missed_ranges WHERE group_name=? AND lo < (
  SELECT lo FROM usenet_nntp_missed_ranges WHERE group_name=? ORDER BY lo DESC OFFSET ? LIMIT 1)`,
		group, group, maxGapRangesPerGroup-1); err != nil {
		return err
	}
	return tx.Commit()
}

// DueMissedRanges returns up to limit ranges whose next_try_at has passed,
// for the given groups, oldest-due first.
func (idx *Index) DueMissedRanges(ctx context.Context, groups []string, limit int) ([]MissedRange, error) {
	if len(groups) == 0 || limit <= 0 {
		return nil, nil
	}
	ph := make([]string, len(groups))
	args := make([]any, 0, len(groups)+2)
	args = append(args, time.Now().Unix())
	for i, g := range groups {
		ph[i] = "?"
		args = append(args, g)
	}
	args = append(args, limit)
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, lo, hi, reason, attempts FROM usenet_nntp_missed_ranges
WHERE next_try_at <= ? AND group_name IN (`+strings.Join(ph, ",")+`)
ORDER BY next_try_at ASC, hi DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MissedRange
	for rows.Next() {
		var r MissedRange
		if err := rows.Scan(&r.Group, &r.Lo, &r.Hi, &r.Reason, &r.Attempts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteMissedRange removes a recovered (or exhausted) range.
func (idx *Index) DeleteMissedRange(ctx context.Context, r MissedRange) error {
	_, err := idx.db.ExecContext(ctx,
		`DELETE FROM usenet_nntp_missed_ranges WHERE group_name=? AND lo=? AND hi=?`, r.Group, r.Lo, r.Hi)
	return err
}

// BumpMissedRange records a failed attempt. At repairMaxAttempts the range is
// deleted (exhausted=true); otherwise next_try_at backs off exponentially.
func (idx *Index) BumpMissedRange(ctx context.Context, r MissedRange) (exhausted bool, err error) {
	attempts := r.Attempts + 1
	if attempts >= repairMaxAttempts {
		return true, idx.DeleteMissedRange(ctx, r)
	}
	next := time.Now().Add(repairBackoff(attempts)).Unix()
	_, err = idx.db.ExecContext(ctx, `
UPDATE usenet_nntp_missed_ranges SET attempts=?, next_try_at=? WHERE group_name=? AND lo=? AND hi=?`,
		attempts, next, r.Group, r.Lo, r.Hi)
	return false, err
}

// EnsureLow initializes the backfill cursor for a group that has none (low=0).
func (idx *Index) EnsureLow(ctx context.Context, group string, low int) error {
	_, err := idx.db.ExecContext(ctx,
		`UPDATE usenet_nntp_watermarks SET low=? WHERE group_name=? AND low=0`, low, group)
	return err
}
