package usenetsearch

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Claude 2026-10-15: release-level table (approved plan nntp-gap-fill.md D1).
// Reason: headers.release_name stays the immutable grouping key; this table
//   carries the searchable display name, strict completeness, and name
//   recovery state so renaming is one UPDATE and search is a narrow scan.
// Troubleshooting: usenet_nntp_releases.dirty=TRUE rows await RecomputeDirty;
//   log "completeness recompute ... backlog=N" shows the queue depth.
// Review if: headers gain a surrogate release id.
// Related files: internal/db/migrations/0044_usenet_nntp_gap_fill.sql

// Release states (usenet_nntp_releases.state).
const (
	stateNamed        = "named"
	statePendingName  = "pending_name"
	stateUnnamedFinal = "unnamed_final"
)

// obfKeyPrefix marks headers.release_name values that are staged obfuscated
// set keys ("obf:<16-hex>") rather than parsed names.
const obfKeyPrefix = "obf:"

// recomputeBatch is the max dirty releases recomputed per loop iteration.
const recomputeBatch = 500

// markDirtyChunk bounds the (group, key) tuples per UPDATE statement.
const markDirtyChunk = 200

type releaseKey struct{ group, key string }

func isObfKey(key string) bool { return strings.HasPrefix(key, obfKeyPrefix) }

// upsertReleaseRows creates missing release rows and marks every touched one
// dirty. A rename (display_name/state) is never overwritten by a re-upsert.
func upsertReleaseRows(ctx context.Context, tx *sql.Tx, keys []releaseKey, now int64) error {
	if len(keys) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO usenet_nntp_releases (group_name, release_key, display_name, name_source, name_confidence,
  state, ingested_at, updated_at, dirty)
VALUES (?,?,?,?,?,?,?,?,TRUE)
ON CONFLICT (group_name, release_key) DO UPDATE SET dirty=TRUE, updated_at=EXCLUDED.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, k := range keys {
		display, source, conf, state := k.key, "subject", "subject", stateNamed
		if isObfKey(k.key) {
			display, source, conf, state = "", "", "", statePendingName
		}
		if _, err := stmt.ExecContext(ctx, k.group, k.key, display, source, conf, state, now, now); err != nil {
			return err
		}
	}
	return nil
}

// scanDeletedKeys drains a DELETE ... RETURNING group_name, release_name
// result: the row count and the distinct non-empty release keys touched.
func scanDeletedKeys(rows *sql.Rows) (int64, []releaseKey, error) {
	defer rows.Close()
	var n int64
	seen := map[releaseKey]bool{}
	var keys []releaseKey
	for rows.Next() {
		var g, r string
		if err := rows.Scan(&g, &r); err != nil {
			return n, nil, err
		}
		n++
		if r == "" {
			continue
		}
		k := releaseKey{g, r}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	return n, keys, rows.Err()
}

// markDirty flags releases for recompute (rows deleted, repaired, renamed).
func (idx *Index) markDirty(ctx context.Context, keys []releaseKey) error {
	now := time.Now().Unix()
	for len(keys) > 0 {
		n := len(keys)
		if n > markDirtyChunk {
			n = markDirtyChunk
		}
		chunk := keys[:n]
		keys = keys[n:]
		ph := make([]string, n)
		args := make([]any, 0, n*2+1)
		args = append(args, now)
		for i, k := range chunk {
			ph[i] = "(?,?)"
			args = append(args, k.group, k.key)
		}
		if _, err := idx.db.ExecContext(ctx,
			`UPDATE usenet_nntp_releases SET dirty=TRUE, updated_at=? WHERE (group_name, release_key) IN (`+strings.Join(ph, ",")+`)`,
			args...); err != nil {
			return err
		}
	}
	return nil
}

// DefaultIncompleteTTLDays is how long a below-gate named release is kept
// while repair/backfill may still bring its missing parts.
const DefaultIncompleteTTLDays = 3

// incompleteSweepBatch bounds releases deleted per sweep call.
const incompleteSweepBatch = 200

// incompleteForwardMargin: the forward watermark must be this far past a
// release's last article before it is considered permanently incomplete.
const incompleteForwardMargin = 50_000

// deleteReleaseSets removes the headers and release rows for keys in one tx.
// Returns the header rows deleted.
func (idx *Index) deleteReleaseSets(ctx context.Context, keys []releaseKey) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	tx, err := idx.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var total int64
	for _, k := range keys {
		res, err := tx.ExecContext(ctx, `DELETE FROM usenet_nntp_headers WHERE group_name=? AND release_name=?`, k.group, k.key)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		total += n
		if _, err := tx.ExecContext(ctx, `DELETE FROM usenet_nntp_releases WHERE group_name=? AND release_key=?`, k.group, k.key); err != nil {
			return 0, err
		}
	}
	return total, tx.Commit()
}

// SweepIncomplete deletes named releases that stayed below the strict gate
// when ALL hold (plan D4): ingested more than ttl ago, no queued repair range
// overlaps the release span, and the forward watermark is past
// last_msg+incompleteForwardMargin. Returns releases deleted.
//
// Claude 2026-10-15: all three conditions so a release still being repaired
// or still being crawled forward is never reaped early.
// Troubleshooting: incomplete releases never reaped → check missed_ranges
//   for the group and that watermarks.high advanced past last_msg+50k.
// Review if: repair queue semantics change.
func (idx *Index) SweepIncomplete(ctx context.Context, ttl time.Duration) (int, error) {
	cutoff := time.Now().Add(-ttl).Unix()
	rows, err := idx.db.QueryContext(ctx, `
SELECT r.group_name, r.release_key
FROM usenet_nntp_releases r
JOIN usenet_nntp_watermarks w ON w.group_name = r.group_name
WHERE r.state = 'named' AND r.searchable = FALSE AND r.dirty = FALSE
  AND r.ingested_at < ? AND w.high > r.last_msg + ?
  AND NOT EXISTS (
    SELECT 1 FROM usenet_nntp_missed_ranges m
    WHERE m.group_name = r.group_name AND m.lo <= r.last_msg + 200 AND m.hi >= r.first_msg - 200)
LIMIT ?`, cutoff, incompleteForwardMargin, incompleteSweepBatch)
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
	if _, err := idx.deleteReleaseSets(ctx, keys); err != nil {
		return 0, err
	}
	return len(keys), nil
}

// RecomputeStats is one RecomputeDirty pass's aggregate counters.
type RecomputeStats struct {
	Releases   int
	Searchable int
	BelowGate  int
	Undeclared int
	Orphans    int
	Backlog    int64
}

// RecomputeDirty recomputes strict completeness for up to limit dirty
// releases (DB only, no NNTP). Releases with no headers left are deleted
// (orphans from prune/liveness/TTL).
func (idx *Index) RecomputeDirty(ctx context.Context, limit int) (RecomputeStats, error) {
	var st RecomputeStats
	if limit <= 0 {
		limit = recomputeBatch
	}
	rows, err := idx.db.QueryContext(ctx,
		`SELECT group_name, release_key FROM usenet_nntp_releases WHERE dirty ORDER BY updated_at LIMIT ?`, limit)
	if err != nil {
		return st, err
	}
	var keys []releaseKey
	for rows.Next() {
		var k releaseKey
		if err := rows.Scan(&k.group, &k.key); err != nil {
			rows.Close()
			return st, err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}
	gate := idx.MinCompletenessPct()
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		searchable, undeclared, orphan, err := idx.recomputeOne(ctx, k, gate)
		if err != nil {
			return st, err
		}
		st.Releases++
		switch {
		case orphan:
			st.Orphans++
		case searchable:
			st.Searchable++
		case undeclared:
			st.Undeclared++
			st.BelowGate++
		default:
			st.BelowGate++
		}
	}
	if err := idx.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_releases WHERE dirty`).Scan(&st.Backlog); err != nil {
		return st, err
	}
	return st, nil
}

func (idx *Index) recomputeOne(ctx context.Context, k releaseKey, gate int) (searchable, undeclared, orphan bool, err error) {
	rows, err := idx.db.QueryContext(ctx, `
SELECT subject, filename, part_n, part_m, file_m, is_meta, bytes, yenc_size, msg_num, posted_at
FROM usenet_nntp_headers WHERE group_name=? AND release_name=?`, k.group, k.key)
	if err != nil {
		return false, false, false, err
	}
	aggs := map[string]*fileAgg{}
	var order []string
	var fileM int
	var first, last, posted, payload int64
	var hasPar2 bool
	var n int
	for rows.Next() {
		var subject, filename string
		var partN, partM, rowFileM int
		var isMeta bool
		var bytes, yenc, msgNum, postedAt int64
		if err := rows.Scan(&subject, &filename, &partN, &partM, &rowFileM, &isMeta, &bytes, &yenc, &msgNum, &postedAt); err != nil {
			rows.Close()
			return false, false, false, err
		}
		n++
		if rowFileM > fileM {
			fileM = rowFileM
		}
		if first == 0 || msgNum < first {
			first = msgNum
		}
		if msgNum > last {
			last = msgNum
		}
		if postedAt > posted {
			posted = postedAt
		}
		meta := isMeta || IsMetaSubject(subject, filename)
		if isMeta {
			hasPar2 = true
		}
		fn := filename
		if fn == "" {
			fn = rePartNM.ReplaceAllString(subject, "")
		}
		agg := aggs[fn]
		if agg == nil {
			agg = &fileAgg{Meta: meta, Seen: map[int]bool{}}
			aggs[fn] = agg
			order = append(order, fn)
		}
		if partM > agg.PartM {
			agg.PartM = partM
		}
		num := partN
		if num <= 0 {
			num = len(agg.Seen) + 1
		}
		if agg.Seen[num] {
			continue
		}
		agg.Seen[num] = true
		if !meta {
			if yenc > 0 {
				payload += yenc
			} else {
				payload += int64(float64(bytes) / 1.37)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, false, false, err
	}
	if n == 0 {
		_, err := idx.db.ExecContext(ctx,
			`DELETE FROM usenet_nntp_releases WHERE group_name=? AND release_key=?`, k.group, k.key)
		return false, false, true, err
	}
	files := make([]fileAgg, 0, len(order))
	for _, fn := range order {
		files = append(files, *aggs[fn])
	}
	comp := computeCompleteness(files, fileM)
	meets := comp.Meets(gate)
	err = idx.db.QueryRowContext(ctx, `
UPDATE usenet_nntp_releases SET
  declared_segs=?, seen_segs=?, completeness_bp=?, gate_reason=?,
  searchable=(state='named' AND display_name<>'' AND ?::boolean),
  first_msg=?, last_msg=?, posted_at=?, payload_bytes=?, has_par2=?, dirty=FALSE
WHERE group_name=? AND release_key=?
RETURNING searchable`,
		comp.Declared, comp.Seen, comp.BP(), comp.Reason(gate), meets,
		first, last, posted, payload, hasPar2, k.group, k.key).Scan(&searchable)
	if err == sql.ErrNoRows {
		return false, false, true, nil
	}
	if err != nil {
		return false, false, false, err
	}
	return searchable, comp.Undeclared, false, nil
}
