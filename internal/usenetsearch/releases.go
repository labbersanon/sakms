package usenetsearch

import (
	"context"
	"database/sql"
	"log"
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
const incompleteSweepBatch = 500

// incompleteForwardMargin: the forward watermark must be this far past a
// release's last article before it is considered permanently incomplete.
const incompleteForwardMargin = 50_000

// SweepIncomplete skip-stubs named and pending sets that stayed below 80%
// when the forward watermark is past last_msg+incompleteForwardMargin and no
// queued repair range overlaps the span. Searchable and ≥80% sets are kept.
// ttl is accepted for call-site compatibility and is not used: once HIGH has
// passed, remaining parts will not arrive (operator 1B).
//
// Claude 2026-10-18: skip stub instead of forget; include pending_name; no TTL.
// Reason: 149k named 0% scraps were already behind HIGH; 3-day TTL never
// fired because ingested_at was fresh; deleting without a skip let backfill
// rebuild them.
// Troubleshooting: index still 1.3GB of 0% sets → this query's HIGH/repair
// predicates or DropSkipped on ingest.
// Review if: span-repair floor moves off 80%.
func (idx *Index) SweepIncomplete(ctx context.Context, ttl time.Duration) (int, error) {
	_ = ttl
	rows, err := idx.db.QueryContext(ctx, `
SELECT r.group_name, r.release_key
FROM usenet_nntp_releases r
JOIN usenet_nntp_watermarks w ON w.group_name = r.group_name
WHERE r.searchable = FALSE
  AND r.state IN ('named', 'pending_name')
  AND r.completeness_bp < ?
  AND r.last_msg > 0
  AND w.high > r.last_msg + ?
  AND NOT EXISTS (
    SELECT 1 FROM usenet_nntp_missed_ranges m
    WHERE m.group_name = r.group_name AND m.lo <= r.last_msg + 200 AND m.hi >= r.first_msg - 200)
LIMIT ?`, spanRepairFloorBP, incompleteForwardMargin, incompleteSweepBatch)
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
	if _, err := idx.skipAndDelete(ctx, keys, skipReasonIncomplete); err != nil {
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
	return idx.recomputeDirtyBounded(ctx, limit, time.Time{})
}

// recomputeDirtyBounded is RecomputeDirty with a wall-clock stop. A zero
// deadline means no time cap. Remaining dirty rows stay queued.
//
// Claude 2026-10-16: stop mid-batch when the crawl loop's budget expires.
// Reason: 9×500 recomputeOne on huge pending keys held WALWrite for ~10 min
// with no log (dashboard looked crashed; crawl/namefix starved).
// Troubleshooting: "completeness recompute working" then crawl chunks again
// within ~20s. If silence returns, deadline is not passed from runRecompute.
// Review if: dirty backlog is small and each recomputeOne is cheap.
func (idx *Index) recomputeDirtyBounded(ctx context.Context, limit int, deadline time.Time) (RecomputeStats, error) {
	var st RecomputeStats
	if limit <= 0 {
		limit = recomputeBatch
	}
	// Claude 2026-10-16: named before complete pending before the rest.
	// Reason: pending-first (#183) left 16 namefix-renamed 100% sets dirty so
	//   searchable stayed at 12 while 95k incomplete pending ate every batch.
	// Troubleshooting: namefix "renamed=N" but dashboard searchable flat → this
	//   ORDER BY not applied, or RenameRelease skipped recomputeOne.
	// Review if: named-complete dirty exceeds a few hundred (crawl re-dirtying).
	// Claude 2026-10-19: all named, including completeness_bp=0.
	// Reason: quoted ident ingest is named/subject at 0% declared; ELSE 3
	//   put those 68 rows behind 13k pending so they never got a BP.
	gateBP := idx.MinCompletenessPct() * 100
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, release_key FROM usenet_nntp_releases
WHERE dirty
ORDER BY CASE
  WHEN state='named' THEN 0
  WHEN state='pending_name' AND completeness_bp>=? THEN 1
  WHEN state='pending_name' THEN 2
  ELSE 3
END, updated_at
LIMIT ?`, gateBP, limit)
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
	if len(keys) > 0 && !deadline.IsZero() {
		log.Printf("usenetsearch: completeness recompute working keys=%d", len(keys))
	}
	gate := idx.MinCompletenessPct()
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			break
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
			if err := idx.enqueueSpan(ctx, k); err != nil {
				return st, err
			}
		}
	}
	if err := idx.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_releases WHERE dirty`).Scan(&st.Backlog); err != nil {
		return st, err
	}
	return st, nil
}

// spanRepairFloorBP / spanRepairMaxArticles bound which near-complete named
// releases get a one-shot release-span repair range (plan D5).
const (
	spanRepairFloorBP     = 8000
	spanRepairMaxArticles = 20_000
	spanRepairPad         = 200
)

// enqueueSpan queues one reason='span' range [first-200, last+200] for a
// named or pending_name release sitting at 80%..gate whose span is already
// behind the forward watermark. span_requeued makes it once-per-release.
//
// Claude 2026-10-16: pending_name too, not only named.
// Reason: 15 moovee sets at 82% (and 1 at 97%) sit behind HIGH with no
// missed_ranges; named-only span repair never re-OVERed them, so they
// never reached the namefix gate and searchable stalled after the leftover
// complete queue drained.
// Troubleshooting: completeness 80–94% pending behind watermark, no
// reason=span rows → this UPDATE still filters state='named'.
// Review if: span repair is limited to yield groups (today any group).
func (idx *Index) enqueueSpan(ctx context.Context, k releaseKey) error {
	if !idx.spanRepair.Load() {
		return nil
	}
	var first, last int
	err := idx.db.QueryRowContext(ctx, `
UPDATE usenet_nntp_releases r SET span_requeued = TRUE
FROM usenet_nntp_watermarks w
WHERE r.group_name = ? AND r.release_key = ? AND w.group_name = r.group_name
  AND r.state IN ('named', 'pending_name') AND r.searchable = FALSE AND r.span_requeued = FALSE
  AND r.gate_reason = 'below_gate' AND r.completeness_bp >= ? AND w.high > r.last_msg
RETURNING r.first_msg, r.last_msg`, k.group, k.key, spanRepairFloorBP).Scan(&first, &last)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	lo := first - spanRepairPad
	if lo < 1 {
		lo = 1
	}
	hi := last + spanRepairPad
	if hi-lo+1 > spanRepairMaxArticles {
		return nil
	}
	return idx.AddMissedRanges(ctx, k.group, [][2]int{{lo, hi}}, "span")
}

// EnqueueStalePendingSpans queues span repair for already-recomputed 80%+
// pending_name sets behind HIGH (they will not pass through RecomputeDirty).
func (idx *Index) EnqueueStalePendingSpans(ctx context.Context) (int, error) {
	if !idx.spanRepair.Load() {
		return 0, nil
	}
	rows, err := idx.db.QueryContext(ctx, `
SELECT r.group_name, r.release_key FROM usenet_nntp_releases r
JOIN usenet_nntp_watermarks w ON w.group_name = r.group_name
WHERE r.state='pending_name' AND r.span_requeued=FALSE AND r.searchable=FALSE
  AND r.gate_reason='below_gate' AND r.completeness_bp>=? AND w.high > r.last_msg
LIMIT ?`, spanRepairFloorBP, repairRangesPerPass)
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
	n := 0
	for _, k := range keys {
		if err := idx.enqueueSpan(ctx, k); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (idx *Index) dirtyIdentifiableNamed(ctx context.Context) {
	_, _ = idx.db.ExecContext(ctx, `
UPDATE usenet_nntp_releases SET dirty=TRUE
WHERE state='named' AND searchable=FALSE AND dirty=FALSE AND completeness_bp>=?
  AND (`+identNameSQL+`)`, spanRepairFloorBP)
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
		if meta {
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
			addFilePayload(&payload, agg, yenc, bytes)
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
	// Claude 2026-10-19: ident-quoted episode keys must not inherit [k/N]
	// collection fileM (12 episodes × 3248 parts).
	// Reason: live Chivalry S01E09 is one mkv (n/3248) but file_m=12 made
	//   declared≈50k and completeness 4.5%, so header namefix never ran.
	// Troubleshooting: ident episode at ~70% of its own parts but BP≈450 →
	//   fileM still filling missing collection files.
	// Review if: pack-as-one-key mode is added.
	if identShapedName(k.key) {
		fileM = 0
	}
	comp := computeCompleteness(files, fileM)
	meets := comp.Meets(gate)
	near := comp.BP() >= spanRepairFloorBP
	// Claude 2026-10-19: recovered/scene names are searchable at ≥80%, not 0%.
	err = idx.db.QueryRowContext(ctx, `
UPDATE usenet_nntp_releases SET
  declared_segs=?, seen_segs=?, completeness_bp=?, gate_reason=?,
  searchable=(state='named' AND display_name<>'' AND (?::boolean OR ((`+identNameSQL+`) AND ?::boolean))),
  first_msg=?, last_msg=?, posted_at=?, payload_bytes=?, has_par2=?, dirty=FALSE
WHERE group_name=? AND release_key=?
RETURNING searchable`,
		comp.Declared, comp.Seen, comp.BP(), comp.Reason(gate), meets, near,
		first, last, posted, payload, hasPar2, k.group, k.key).Scan(&searchable)
	if err == sql.ErrNoRows {
		return false, false, true, nil
	}
	if err != nil {
		return false, false, false, err
	}
	return searchable, comp.Undeclared, false, nil
}
