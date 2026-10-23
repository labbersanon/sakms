package usenetsearch

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// Claude 2026-09-18: header index on app Postgres (not modernc SQLite).
// Reason: TestCmdSakms_DoesNotImportModerncSQLite forbids modernc in cmd/sakms
//   after the Postgres cutover. Dedicated tables keep the index prunable.
// Troubleshooting: migration 0026; Ready reports index empty until crawl.
// Review if: index moves to a separate Postgres database for size isolation.

// Index is the local header store (usenet_nntp_* tables on the app DB).
type Index struct {
	db    *sql.DB
	owned bool // if true, Close closes db (tests only; production shares app DB)
	// gatePct is the strict completeness threshold (0 = default 95). Set by
	// Service.Apply via SetMinCompletenessPct; read on every search.
	gatePct atomic.Int64
	// spanRepair enables release-span repair enqueueing from the recompute
	// pass (usenet_nntp_repair_enabled); false by default.
	spanRepair atomic.Bool
}

// SetSpanRepair toggles release-span range enqueueing (the repair kill switch).
func (idx *Index) SetSpanRepair(on bool) { idx.spanRepair.Store(on) }

// SetMinCompletenessPct sets the strict completeness gate (clamped 80-100).
func (idx *Index) SetMinCompletenessPct(p int) {
	idx.gatePct.Store(int64(ClampMinCompletenessPct(p)))
}

// MinCompletenessPct is the active strict gate threshold.
func (idx *Index) MinCompletenessPct() int {
	return ClampMinCompletenessPct(int(idx.gatePct.Load()))
}

// OpenIndex wraps an already-migrated app *sql.DB. Does not take ownership.
func OpenIndex(db *sql.DB) (*Index, error) {
	if db == nil {
		return nil, fmt.Errorf("usenetsearch: nil database")
	}
	return &Index{db: db, owned: false}, nil
}

func (idx *Index) Close() error {
	if idx == nil || idx.db == nil || !idx.owned {
		return nil
	}
	return idx.db.Close()
}

// HeaderRow is one stored overview article.
type HeaderRow struct {
	Group       string
	MsgNum      int
	MsgID       string
	Subject     string
	From        string
	PostedAt    int64
	Bytes       int64
	ReleaseName string
	PartN       int
	PartM       int
	Filename    string
	YencSize    int64
	Obfuscated  bool
	IsMeta      bool
	FileN       int
	FileM       int
}

// Upsert inserts overview rows. A new msgid or a grouping-field change marks
// the release dirty; an identical restage leaves dirty unchanged.
//
// Claude 2026-10-15: release rows are created here (plan D1/D4), not by the
// crawler, so every writer (forward crawl, repair, backfill, tests) keeps the
// release table consistent. Rows with an empty ReleaseName (legacy
// obfuscated junk) get no release row.
// Troubleshooting: release missing from search after upsert → run
// RecomputeDirty; check usenet_nntp_releases.dirty.
// Review if: headers gain a surrogate release id.
// Claude 2026-10-21: dirty only when a header row is inserted or grouping fields change.
// Reason: forward OVER restages the same msgids and ON CONFLICT always set
//
//	ingested_at + dirty; live backlog grew 1.94M→1.98M of 0% pending while
//	recompute searchable=0 every batch. Identical restage must stay clean.
//
// Troubleshooting: completeness recompute backlog climbing with staged_obf
//
//	and no new msgids → WHERE IS DISTINCT FROM missing / ingested_at still
//	updated on conflict. Review if: rewrite regroups via Upsert (then
//	release_name DISTINCT dirties, which is correct).
func (idx *Index) Upsert(ctx context.Context, rows []HeaderRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := idx.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO usenet_nntp_headers (group_name, msg_num, msgid, subject, from_addr, posted_at, bytes,
  release_name, part_n, part_m, filename, yenc_size, obfuscated, is_meta, ingested_at, file_n, file_m)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT (group_name, msgid) DO UPDATE SET
  msg_num=EXCLUDED.msg_num, subject=EXCLUDED.subject, from_addr=EXCLUDED.from_addr,
  posted_at=EXCLUDED.posted_at, bytes=EXCLUDED.bytes, release_name=EXCLUDED.release_name,
  part_n=EXCLUDED.part_n, part_m=EXCLUDED.part_m, filename=EXCLUDED.filename,
  yenc_size=EXCLUDED.yenc_size, obfuscated=EXCLUDED.obfuscated, is_meta=EXCLUDED.is_meta,
  file_n=EXCLUDED.file_n, file_m=EXCLUDED.file_m
WHERE usenet_nntp_headers.release_name IS DISTINCT FROM EXCLUDED.release_name
   OR usenet_nntp_headers.part_n IS DISTINCT FROM EXCLUDED.part_n
   OR usenet_nntp_headers.part_m IS DISTINCT FROM EXCLUDED.part_m
   OR usenet_nntp_headers.file_n IS DISTINCT FROM EXCLUDED.file_n
   OR usenet_nntp_headers.file_m IS DISTINCT FROM EXCLUDED.file_m
   OR usenet_nntp_headers.filename IS DISTINCT FROM EXCLUDED.filename
   OR usenet_nntp_headers.subject IS DISTINCT FROM EXCLUDED.subject
   OR usenet_nntp_headers.bytes IS DISTINCT FROM EXCLUDED.bytes
`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().Unix()
	seen := map[releaseKey]bool{}
	var keys []releaseKey
	for _, r := range rows {
		res, err := stmt.ExecContext(ctx, r.Group, r.MsgNum, r.MsgID, r.Subject, r.From, r.PostedAt, r.Bytes,
			r.ReleaseName, r.PartN, r.PartM, r.Filename, r.YencSize, r.Obfuscated, r.IsMeta, now, r.FileN, r.FileM)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 || r.ReleaseName == "" {
			continue
		}
		k := releaseKey{r.Group, r.ReleaseName}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if err := upsertReleaseRows(ctx, tx, keys, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (idx *Index) Watermark(ctx context.Context, group string) (int, error) {
	var high int
	err := idx.db.QueryRowContext(ctx, `SELECT high FROM usenet_nntp_watermarks WHERE group_name=?`, group).Scan(&high)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return high, err
}

func (idx *Index) SetWatermark(ctx context.Context, group string, high int) error {
	_, err := idx.db.ExecContext(ctx, `
INSERT INTO usenet_nntp_watermarks(group_name, high, updated_at) VALUES(?,?,?)
ON CONFLICT (group_name) DO UPDATE SET high=EXCLUDED.high, updated_at=EXCLUDED.updated_at
`, group, high, time.Now().Unix())
	return err
}

func (idx *Index) PruneOlderThan(ctx context.Context, cutoffUnix int64) (int64, error) {
	res, err := idx.db.ExecContext(ctx, `DELETE FROM usenet_nntp_headers WHERE posted_at > 0 AND posted_at < ?`, cutoffUnix)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// approxHeaderRowBytes is the logical size used for the soft index budget.
// Claude 2026-10-15: do NOT use pg_total_relation_size for prune loops.
// Reason: DELETE does not shrink relation size until VACUUM FULL/rewrite, so
//
//	PruneOldest kept deleting until the table was empty.
//
// Troubleshooting: index wiped after crossing index_max_gb — check ApproxBytes.
// Review if: monthly/continuous VACUUM ANALYZE reports real disk separately.
const approxHeaderRowBytes int64 = 200

// ApproxBytes is the soft budget metric (index_max_gb), not on-disk relation size.
func (idx *Index) ApproxBytes(ctx context.Context) (int64, error) {
	rows, err := idx.CountHeaders(ctx)
	if err != nil {
		return 0, err
	}
	return rows * approxHeaderRowBytes, nil
}

// CountHeaders returns the number of indexed header rows.
func (idx *Index) CountHeaders(ctx context.Context) (int64, error) {
	var rows int64
	err := idx.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_headers`).Scan(&rows)
	return rows, err
}

// PruneOldest deletes oldest headers until ApproxBytes <= targetBytes or no
// rows remain. Availability-based removal is the monitor loop's job.
// Claude 2026-10-15: batch size = overage rows (cap 5000), not a fixed 5000.
// Reason: a fixed LIMIT 5000 wiped small indexes in one DELETE (budget test
//
//	with 100 rows → 0). Overage-sized batches stop at the soft floor.
//
// Troubleshooting: index emptied after prune — check batch limit math here.
// Review if: VACUUM FULL is scheduled separately for real disk reclaim.
func (idx *Index) PruneOldest(ctx context.Context, targetBytes int64) error {
	if targetBytes <= 0 {
		return nil
	}
	const maxBatch = 5000
	for {
		size, err := idx.ApproxBytes(ctx)
		if err != nil {
			return err
		}
		if size <= targetBytes {
			return nil
		}
		overRows := (size - targetBytes + approxHeaderRowBytes - 1) / approxHeaderRowBytes
		if overRows < 1 {
			overRows = 1
		}
		if overRows > maxBatch {
			overRows = maxBatch
		}
		// Claude 2026-10-15: evict by post date, not ingest date (plan D6).
		// Reason: backfilled old posts are ingested LAST, so ingested_at
		//   ordering would evict the newest forward-crawled rows first.
		//   idx_usenet_nntp_headers_prune (migration 0044) backs this ORDER BY.
		// Troubleshooting: recent releases vanish after backfill → check this
		//   ORDER BY still uses COALESCE(NULLIF(posted_at,0), ingested_at).
		// Review if: backfill is removed.
		// Old: ORDER BY ingested_at ASC, posted_at ASC
		rows, err := idx.db.QueryContext(ctx, `
DELETE FROM usenet_nntp_headers WHERE ctid IN (
  SELECT ctid FROM usenet_nntp_headers ORDER BY COALESCE(NULLIF(posted_at, 0), ingested_at) ASC LIMIT ?
) RETURNING group_name, release_name`, overRows)
		if err != nil {
			return err
		}
		n, keys, err := scanDeletedKeys(rows)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if err := idx.markDirty(ctx, keys); err != nil {
			return err
		}
	}
}

// LivenessRow is one indexed article selected for STAT availability checks.
type LivenessRow struct {
	Group string
	MsgID string
}

// ListOldestMsgIDs returns up to limit headers oldest-first, skipping offset
// rows (the liveness scan cursor).
func (idx *Index) ListOldestMsgIDs(ctx context.Context, limit, offset int) ([]LivenessRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, msgid FROM usenet_nntp_headers
ORDER BY ingested_at ASC, posted_at ASC, msgid ASC
LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LivenessRow
	for rows.Next() {
		var r LivenessRow
		if err := rows.Scan(&r.Group, &r.MsgID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteByMsgIDs removes headers whose msgid is in msgIDs (any group).
func (idx *Index) DeleteByMsgIDs(ctx context.Context, msgIDs []string) (int64, error) {
	if len(msgIDs) == 0 {
		return 0, nil
	}
	ph := make([]string, len(msgIDs))
	args := make([]any, len(msgIDs))
	for i, id := range msgIDs {
		ph[i] = "?"
		args[i] = id
	}
	rows, err := idx.db.QueryContext(ctx,
		`DELETE FROM usenet_nntp_headers WHERE msgid IN (`+strings.Join(ph, ",")+`) RETURNING group_name, release_name`, args...)
	if err != nil {
		return 0, err
	}
	n, keys, err := scanDeletedKeys(rows)
	if err != nil {
		return 0, err
	}
	if err := idx.markDirty(ctx, keys); err != nil {
		return 0, err
	}
	return n, nil
}

func (idx *Index) Empty(ctx context.Context) (bool, error) {
	var n int
	err := idx.db.QueryRowContext(ctx, `SELECT 1 FROM usenet_nntp_headers LIMIT 1`).Scan(&n)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func (idx *Index) HasGroup(ctx context.Context, group string) (bool, error) {
	var n int
	err := idx.db.QueryRowContext(ctx, `SELECT 1 FROM usenet_nntp_headers WHERE group_name=? LIMIT 1`, group).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// GroupHasNamedRelease is true when group has at least one searchable release
// whose name did not come from a raw subject stem. Used to decide NNTmux-style
// backfill/staging investment on non-staging groups.
//
// Claude 2026-10-16: require namefix-quality names, not any subject stem.
// Reason: boneless had 146k subject-named 1-seg rows so obfStageAllowed kept
//
//	staging dumps into the unnamed cap.
//
// Troubleshooting: staged_obf high on boneless with skip_obf_cap=0 → this
//
//	still treating subject-named junk as investment.
//
// Review if: complete subject-named scene releases should also unlock staging.
func (idx *Index) GroupHasNamedRelease(ctx context.Context, group string) (bool, error) {
	var n int
	err := idx.db.QueryRowContext(ctx, `
SELECT 1 FROM usenet_nntp_releases
WHERE group_name=? AND searchable AND name_source<>'' AND name_source<>'subject'
LIMIT 1`, group).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// SearchReleases finds distinct release names matching q, then loads segments.
func (idx *Index) SearchReleases(ctx context.Context, q Query) ([]Candidate, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	groups := q.Groups
	var args []any
	where := []string{`searchable=TRUE`, `display_name<>''`}
	if len(groups) > 0 {
		ph := make([]string, len(groups))
		for i, g := range groups {
			ph[i] = "?"
			args = append(args, g)
		}
		where = append(where, `group_name IN (`+strings.Join(ph, ",")+`)`)
	}
	for _, t := range q.Terms {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		where = append(where, `display_name ILIKE ?`)
		args = append(args, "%"+escapeLike(t)+"%")
	}
	for _, ex := range q.Exclude {
		ex = strings.TrimSpace(ex)
		if ex == "" {
			continue
		}
		where = append(where, `display_name NOT ILIKE ?`)
		args = append(args, "%"+escapeLike(ex)+"%")
	}
	if !q.PostedAfter.IsZero() {
		where = append(where, `posted_at >= ?`)
		args = append(args, q.PostedAfter.Unix())
	}
	sqlStr := `SELECT group_name, release_key, display_name FROM usenet_nntp_releases WHERE ` + strings.Join(where, " AND ") + ` ORDER BY posted_at DESC LIMIT ?`
	args = append(args, limit*3)
	rows, err := idx.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ g, k, name string }
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.g, &k.k, &k.name); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Candidate
	for _, k := range keys {
		c, err := idx.loadCandidateNamed(ctx, k.g, k.k, k.name)
		if err != nil || c.Name == "" {
			continue
		}
		if q.MinBytes > 0 && c.PayloadBytes < q.MinBytes {
			continue
		}
		if q.MaxBytes > 0 && c.PayloadBytes > q.MaxBytes {
			continue
		}
		if q.GrabReady && !c.Complete {
			continue
		}
		out = append(out, c)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// loadCandidate loads group/release (grouping key), resolving the display
// name from the release table (falls back to the key for pre-table rows).
func (idx *Index) loadCandidate(ctx context.Context, group, release string) (Candidate, error) {
	var display string
	err := idx.db.QueryRowContext(ctx,
		`SELECT display_name FROM usenet_nntp_releases WHERE group_name=? AND release_key=?`, group, release).Scan(&display)
	if err != nil && err != sql.ErrNoRows {
		return Candidate{}, err
	}
	return idx.loadCandidateNamed(ctx, group, release, display)
}

func (idx *Index) loadCandidateNamed(ctx context.Context, group, release, display string) (Candidate, error) {
	rows, err := idx.db.QueryContext(ctx, `
SELECT msgid, subject, from_addr, posted_at, bytes, part_n, part_m, filename, yenc_size, is_meta, file_m
FROM usenet_nntp_headers WHERE group_name=? AND release_name=? ORDER BY filename, part_n, msg_num
`, group, release)
	if err != nil {
		return Candidate{}, err
	}
	defer rows.Close()
	return assembleFromRows(group, release, display, rows, idx.MinCompletenessPct())
}

// LoadBySeed reconstructs a candidate from group + seed message-id (locator).
// The locator path does not enforce the strict gate (a queued grab must still
// resolve); Candidate.Complete reports it.
func (idx *Index) LoadBySeed(ctx context.Context, group, seedMsgID string) (Candidate, error) {
	var release string
	err := idx.db.QueryRowContext(ctx, `
SELECT release_name FROM usenet_nntp_headers WHERE group_name=? AND msgid=?`, group, seedMsgID).Scan(&release)
	if err != nil {
		return Candidate{}, err
	}
	return idx.loadCandidate(ctx, group, release)
}

// Claude 2026-10-15: admission support queries (crawl-time quality gate).
// Reason: ExistingReleases batches the "already indexed" STAT short-circuit
//   into one query per OVER chunk (plan: "1 query/chunk") instead of one
//   per release; DistinctReleases/DeleteReleases back the one-shot prune of
//   already-indexed rows that fail the quality gate.
// Troubleshooting: a release re-STATed every chunk despite being indexed →
//   check names actually reach ExistingReleases (bucketed release names,
//   not raw subjects).
// Related files: usenetsearch/crawler.go, usenetsearch/admission.go

// ExistingReleases reports which of names are already indexed for group —
// the "stat=existing" short-circuit for a release that started in an
// earlier OVER chunk (same or an earlier crawl pass) and still has rows in
// the current chunk.
func (idx *Index) ExistingReleases(ctx context.Context, group string, names []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(names) == 0 {
		return out, nil
	}
	ph := make([]string, len(names))
	args := make([]any, 0, len(names)+1)
	args = append(args, group)
	for i, n := range names {
		ph[i] = "?"
		args = append(args, n)
	}
	rows, err := idx.db.QueryContext(ctx, `
SELECT DISTINCT release_name FROM usenet_nntp_headers
WHERE group_name=? AND release_name IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// DistinctReleases lists every distinct non-obfuscated release_name already
// indexed for group — the one-shot prune's input set.
func (idx *Index) DistinctReleases(ctx context.Context, group string) ([]string, error) {
	rows, err := idx.db.QueryContext(ctx, `
SELECT DISTINCT release_name FROM usenet_nntp_headers
WHERE group_name=? AND obfuscated=FALSE AND release_name!=''`, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteReleases removes every header row for group whose release_name is
// in names — the one-shot prune's delete step. No STAT check: pruning is
// quality-gate-only (plan: "no STAT needed for prune").
func (idx *Index) DeleteReleases(ctx context.Context, group string, names []string) (int64, error) {
	if len(names) == 0 {
		return 0, nil
	}
	ph := make([]string, len(names))
	args := make([]any, 0, len(names)+1)
	args = append(args, group)
	for i, n := range names {
		ph[i] = "?"
		args = append(args, n)
	}
	res, err := idx.db.ExecContext(ctx, `
DELETE FROM usenet_nntp_headers WHERE group_name=? AND release_name IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	if _, err := idx.db.ExecContext(ctx, `
DELETE FROM usenet_nntp_par_hashes WHERE group_name=? AND release_key IN (`+strings.Join(ph, ",")+`)`, args...); err != nil {
		return 0, err
	}
	if _, err := idx.db.ExecContext(ctx, `
DELETE FROM usenet_nntp_releases WHERE group_name=? AND release_key IN (`+strings.Join(ph, ",")+`)`, args...); err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
