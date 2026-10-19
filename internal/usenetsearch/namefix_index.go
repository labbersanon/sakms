package usenetsearch

import (
	"context"
	"database/sql"
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
	// HasPar2 is true when the last recompute saw a PAR2/meta row.
	HasPar2 bool
	// PreDBOnly skips BODY: exhausted keepCompleteYield waiting on dump/IRC.
	PreDBOnly bool
}

// StagedBytes is the payload size of unresolved staged sets in group (as of
// their last recompute; sets staged this very chunk count 0 until then).
//
// Claude 2026-10-16: cap is per-group, not a global SUM.
// Reason: boneless/teevee pending (~1 TiB real header bytes) filled a global
// unnamed_max_pct so moovee (0 pending) skip_obf_cap'd every new set.
// Troubleshooting: skip_obf_cap on a core with staged_obf=0 → that group's
// own pending_name payload is over unnamed_max_pct of MaxGB.
// Review if: a shared unnamed pool for step-1 cores is added as a Settings knob.
func (idx *Index) StagedBytes(ctx context.Context, group string) (int64, error) {
	var n int64
	// Claude 2026-10-16: exhausted kept yield sets do not fill the unnamed cap.
	// Reason: hash-named PAR2/RAR sets sit at name_attempts>=3 (keepCompleteYield
	// skipped the sweep) and SUM(payload_bytes) kept every group in skip_obf_cap.
	// Troubleshooting: skip_obf_cap with only shelved complete pending → this
	// filter not applied. Review if: PreDB starts naming those hashes (retry).
	err := idx.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(payload_bytes),0) FROM usenet_nntp_releases WHERE state='pending_name' AND group_name=? AND name_attempts < ?`,
		group, maxNameAttempts).Scan(&n)
	return n, err
}

// DuePendingNames returns up to limit staged sets in groups whose retry time
// has passed, recomputed, and at/above the completeness gate (a BODY fetch is
// only spent on sets that could become grab-complete). PreDB-only below-gate
// sets go through DuePendingPreDB instead. Only yield groups are
// queried (staging cores, package step ≤4, plus anime); within that set,
// staging cores are filled before the rest, newest first.
//
// Claude 2026-10-16: yield groups only; boneless is not padded in.
// Reason: 644 due boneless nicovideo dumps filled every namefix slot so
//
//	searchable stalled at 36 while anime/hdtv.x264 sat idle.
//
// Troubleshooting: namefix pass renamed=0 with due_now hundreds → boneless
//
//	still in the IN list.
//
// Review if: an operator knob to namefix step-5 firehose is added.
func (idx *Index) DuePendingNames(ctx context.Context, groups []string, limit int) ([]PendingSet, error) {
	return idx.dueYieldPrefixed(ctx, groups, limit, idx.duePendingNamesIn)
}

func (idx *Index) duePendingNamesIn(ctx context.Context, groups []string, limit int) ([]PendingSet, error) {
	if len(groups) == 0 || limit <= 0 {
		return nil, nil
	}
	// Claude 2026-10-18: BODY retries stop at maxNameAttempts; PreDB continues
	// via DuePendingPreDB (no BODY).
	// Reason: keepCompleteYield left hash-named 100% sets on a 10m<<n backoff
	//   forever; namefix spent 12–17 slots/pass re-fetching the same PAR2.
	// Troubleshooting: namefix unresolved=N with the same keys every loop →
	//   this filter missing. Review if: BODY is retried after a dump import.
	in, args := groupLimitArgs([]any{idx.MinCompletenessPct() * 100, time.Now().Unix(), maxNameAttempts}, groups, limit)
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key, payload_bytes, name_attempts, completeness_bp, has_par2 FROM usenet_nntp_releases
WHERE state='pending_name' AND dirty=FALSE AND declared_segs>0 AND completeness_bp>=?
  AND next_name_at<=? AND name_attempts < ? AND group_name IN (`+in+`)
ORDER BY ingested_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return scanPendingSets(rows, false)
}

// DuePendingIdentBody returns ≥80% yield pending_name sets still below the
// 95% BODY/autograb gate that have PAR2/meta. A first-segment BODY can
// recover a title even when the dump (through 2024) has no hash for them.
//
// Claude 2026-10-19: identification BODY at 80%, not only 95% grab-complete.
// Reason: live pending hashes are 2026 and miss the nZEDb dump; indexers
//
//	decode from PAR2 FileDesc regardless of payload completeness.
//
// Troubleshooting: 90% yield with has_par2 never BODY → this query's
//
//	completeness_bp < gate or has_par2 filter. Review if: BODY budget
//	(namefix_body_mb_per_hour) starts hitting BudgetStop every pass.
func (idx *Index) DuePendingIdentBody(ctx context.Context, groups []string, limit int) ([]PendingSet, error) {
	return idx.dueYieldPrefixed(ctx, groups, limit, idx.duePendingIdentBodyIn)
}

func (idx *Index) duePendingIdentBodyIn(ctx context.Context, groups []string, limit int) ([]PendingSet, error) {
	if len(groups) == 0 || limit <= 0 {
		return nil, nil
	}
	gateBP := idx.MinCompletenessPct() * 100
	in, args := groupLimitArgs([]any{spanRepairFloorBP, gateBP, time.Now().Unix(), maxNameAttempts}, groups, limit)
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key, payload_bytes, name_attempts, completeness_bp, has_par2 FROM usenet_nntp_releases
WHERE state='pending_name' AND dirty=FALSE AND declared_segs>0 AND completeness_bp>=? AND completeness_bp<?
  AND has_par2 AND next_name_at<=? AND name_attempts < ? AND group_name IN (`+in+`)
ORDER BY completeness_bp DESC, ingested_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	return scanPendingSets(rows, false)
}

func (idx *Index) dueYieldPrefixed(ctx context.Context, groups []string, limit int, query func(context.Context, []string, int) ([]PendingSet, error)) ([]PendingSet, error) {
	var yield []string
	for _, g := range groups {
		if IsNameFixYieldGroup(g) {
			yield = append(yield, g)
		}
	}
	cores, rest := splitCoreRest(yield)
	out, err := query(ctx, cores, limit)
	if err != nil || len(out) >= limit {
		return out, err
	}
	more, err := query(ctx, rest, limit-len(out))
	if err != nil {
		return out, err
	}
	return append(out, more...), nil
}

func scanPendingSets(rows *sql.Rows, preDBOnly bool) ([]PendingSet, error) {
	defer rows.Close()
	var out []PendingSet
	for rows.Next() {
		var p PendingSet
		if err := rows.Scan(&p.Group, &p.Key, &p.PayloadBytes, &p.Attempts, &p.CompletenessBP, &p.HasPar2); err != nil {
			return nil, err
		}
		p.PreDBOnly = preDBOnly
		out = append(out, p)
	}
	return out, rows.Err()
}

// DuePendingPreDB returns ≥80% pending_name sets in any group for a local
// PreDB lookup (no BODY). Boneless and other firehose groups are included:
// a hash hit is free and is what NZB indexers do at ingest.
//
// Claude 2026-10-19: all groups, 200/pass, no payload floor.
// Reason: yield-only PreDB left 1883 complete boneless pending unnamed;
//
//	namefix ran twice in 2h with hash_hits=1. Review if: firehose PreDB
//	starves yield BODY slots (BODY still uses DuePendingNames).
func (idx *Index) DuePendingPreDB(ctx context.Context, limit int) ([]PendingSet, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key, payload_bytes, name_attempts, completeness_bp, has_par2
FROM usenet_nntp_releases
WHERE state='pending_name' AND dirty=FALSE AND declared_segs>0 AND completeness_bp>=?
  AND next_name_at<=?
ORDER BY completeness_bp DESC, ingested_at DESC
LIMIT ?`, spanRepairFloorBP, time.Now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	return scanPendingSets(rows, true)
}

func groupLimitArgs(prefix []any, groups []string, limit int) (string, []any) {
	ph := make([]string, len(groups))
	args := make([]any, 0, len(prefix)+len(groups)+1)
	args = append(args, prefix...)
	for i, g := range groups {
		ph[i] = "?"
		args = append(args, g)
	}
	return strings.Join(ph, ","), append(args, limit)
}

// ScheduleNameRetry sets next_name_at without incrementing name_attempts
// (PreDB-only exhausted yield waiting on dump/IRC).
func (idx *Index) ScheduleNameRetry(ctx context.Context, group, key string, wait time.Duration) error {
	if wait <= 0 {
		wait = exhaustedPreDBRetry
	}
	_, err := idx.db.ExecContext(ctx, `
UPDATE usenet_nntp_releases SET next_name_at=?, updated_at=? WHERE group_name=? AND release_key=?`,
		time.Now().Add(wait).Unix(), time.Now().Unix(), group, key)
	return err
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

// RenameRelease sets the recovered display name on a staged set and recomputes
// completeness. A 95%+ set, or a PreDB-exact name at ≥80%, becomes searchable
// in this pass. Headers are untouched, so the rename is reversible (undo SQL
// in docs/usenet-nntp-native.md).
//
// Claude 2026-10-16: recompute here, do not wait for RecomputeDirty.
// Reason: namefix ran after recompute, so renamed 100% sets sat dirty behind 95k pending and searchable stayed at 12.
// Troubleshooting: renamed= log with searchable still false → recomputeOne failed.
// Review if: RenameRelease is called from a second site that must not hit the DB twice.
func (idx *Index) RenameRelease(ctx context.Context, group, key, display, source, confidence string) error {
	_, err := idx.db.ExecContext(ctx, `
UPDATE usenet_nntp_releases SET display_name=?, name_source=?, name_confidence=?,
  state='named', dirty=TRUE, updated_at=? WHERE group_name=? AND release_key=?`,
		display, source, confidence, time.Now().Unix(), group, key)
	if err != nil {
		return err
	}
	_, _, _, err = idx.recomputeOne(ctx, releaseKey{group, key}, idx.MinCompletenessPct())
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

// FinalizeUnnamed skip-stubs unnamed sets, then deletes headers + release
// rows so a later OVER of the same key does not rebuild them.
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
	return idx.skipAndDelete(ctx, keys, skipReasonUnnamed)
}

// keepCompleteYield reports whether a staged set is real yield-group inventory
// that namefix may still recover (do not TTL / attempt-cap delete it).
func keepCompleteYield(group string, completenessBP int, payload int64) bool {
	return IsNameFixYieldGroup(group) && completenessBP >= spanRepairFloorBP && payload >= namefixMinPayload
}

// SweepUnnamed finalizes staged sets older than ttl or out of attempts.
//
// Claude 2026-10-16: do not delete ≥80% complete yield sets with real payload.
// Reason: 51 complete moovee/hdtv/tv/xxx sets (0.3–4.8GB) failed BODY twice
// (hash-named PAR2/RAR) and would be swept on attempt 3 — searchable bursted
// then stopped. Firehose/junk still TTL.
// Troubleshooting: namefix finalized=N while yield complete pending vanishes
// → this filter not applied. Review if: PreDB names those hashes (then TTL).
func (idx *Index) SweepUnnamed(ctx context.Context, ttl time.Duration) (int, error) {
	cutoff := time.Now().Add(-ttl).Unix()
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key, completeness_bp, payload_bytes FROM usenet_nntp_releases
WHERE state='pending_name' AND (ingested_at < ? OR name_attempts >= ?) LIMIT ?`,
		cutoff, maxNameAttempts, incompleteSweepBatch)
	if err != nil {
		return 0, err
	}
	var keys []releaseKey
	for rows.Next() {
		var k releaseKey
		var bp int
		var payload int64
		if err := rows.Scan(&k.group, &k.key, &bp, &payload); err != nil {
			rows.Close()
			return 0, err
		}
		if keepCompleteYield(k.group, bp, payload) {
			continue
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

// implausibleYencSweepBatch bounds pending_name sets deleted per sweep call
// for unique-ID yEnc (n/m) floods (see maxYencPartM).
const implausibleYencSweepBatch = 2000

// SweepImplausibleYenc deletes pending_name sets whose stored yEnc part_m is
// above maxYencPartM (unique-ID floods that can never meet the completeness gate).
//
// Claude 2026-10-16: ingest now refuses these pairs; this drains the existing pile.
// Reason: 104k live pending_name rows were seen=1 / declared≈967646, so namefix had nothing complete on cores.
// Troubleshooting: pending_name still ~100k after deploy → this query not running or part_m already ≤ maxYencPartM.
// Review if: maxYencPartM changes; dirty the leftover rows instead of deleting.
func (idx *Index) SweepImplausibleYenc(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = implausibleYencSweepBatch
	}
	rows, err := idx.db.QueryContext(ctx, `
SELECT r.group_name, r.release_key
FROM usenet_nntp_releases r
WHERE r.state='pending_name'
  AND EXISTS (
    SELECT 1 FROM usenet_nntp_headers h
    WHERE h.group_name=r.group_name AND h.release_name=r.release_key AND h.part_m>?
  )
LIMIT ?`, maxYencPartM, limit)
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
	if _, err := idx.skipAndDelete(ctx, keys, skipReasonImplausible); err != nil {
		return 0, err
	}
	return len(keys), nil
}
