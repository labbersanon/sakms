package usenetsearch

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
}

// Upsert inserts or replaces overview rows.
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
  release_name, part_n, part_m, filename, yenc_size, obfuscated, is_meta, ingested_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT (group_name, msgid) DO UPDATE SET
  msg_num=EXCLUDED.msg_num, subject=EXCLUDED.subject, from_addr=EXCLUDED.from_addr,
  posted_at=EXCLUDED.posted_at, bytes=EXCLUDED.bytes, release_name=EXCLUDED.release_name,
  part_n=EXCLUDED.part_n, part_m=EXCLUDED.part_m, filename=EXCLUDED.filename,
  yenc_size=EXCLUDED.yenc_size, obfuscated=EXCLUDED.obfuscated, is_meta=EXCLUDED.is_meta,
  ingested_at=EXCLUDED.ingested_at
`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().Unix()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.Group, r.MsgNum, r.MsgID, r.Subject, r.From, r.PostedAt, r.Bytes,
			r.ReleaseName, r.PartN, r.PartM, r.Filename, r.YencSize, r.Obfuscated, r.IsMeta, now); err != nil {
			return err
		}
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
		res, err := idx.db.ExecContext(ctx, `
DELETE FROM usenet_nntp_headers WHERE ctid IN (
  SELECT ctid FROM usenet_nntp_headers ORDER BY ingested_at ASC, posted_at ASC LIMIT ?
)`, overRows)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil
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
	res, err := idx.db.ExecContext(ctx,
		`DELETE FROM usenet_nntp_headers WHERE msgid IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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

// SearchReleases finds distinct release names matching q, then loads segments.
func (idx *Index) SearchReleases(ctx context.Context, q Query) ([]Candidate, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	groups := q.Groups
	var args []any
	var where []string
	where = append(where, `obfuscated=FALSE`, `release_name!=''`)
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
		where = append(where, `release_name ILIKE ?`)
		args = append(args, "%"+escapeLike(t)+"%")
	}
	for _, ex := range q.Exclude {
		ex = strings.TrimSpace(ex)
		if ex == "" {
			continue
		}
		where = append(where, `release_name NOT ILIKE ?`)
		args = append(args, "%"+escapeLike(ex)+"%")
	}
	if !q.PostedAfter.IsZero() {
		where = append(where, `posted_at >= ?`)
		args = append(args, q.PostedAfter.Unix())
	}
	sqlStr := `SELECT DISTINCT group_name, release_name FROM usenet_nntp_headers WHERE ` + strings.Join(where, " AND ") + ` LIMIT ?`
	args = append(args, limit*3)
	rows, err := idx.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ g, n string }
	var keys []key
	for rows.Next() {
		var g, n string
		if err := rows.Scan(&g, &n); err != nil {
			return nil, err
		}
		keys = append(keys, key{g, n})
	}
	var out []Candidate
	for _, k := range keys {
		c, err := idx.loadCandidate(ctx, k.g, k.n)
		if err != nil || c.Name == "" {
			continue
		}
		if q.MinBytes > 0 && c.PayloadBytes < q.MinBytes {
			continue
		}
		if q.MaxBytes > 0 && c.PayloadBytes > q.MaxBytes {
			continue
		}
		if !c.Complete && c.MissingParts > max(1, c.SegmentCount/50) {
			continue
		}
		out = append(out, c)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (idx *Index) loadCandidate(ctx context.Context, group, release string) (Candidate, error) {
	rows, err := idx.db.QueryContext(ctx, `
SELECT msgid, subject, from_addr, posted_at, bytes, part_n, part_m, filename, yenc_size, is_meta
FROM usenet_nntp_headers WHERE group_name=? AND release_name=? ORDER BY filename, part_n, msg_num
`, group, release)
	if err != nil {
		return Candidate{}, err
	}
	defer rows.Close()
	return assembleFromRows(group, release, rows)
}

// LoadBySeed reconstructs a candidate from group + seed message-id (locator).
func (idx *Index) LoadBySeed(ctx context.Context, group, seedMsgID string) (Candidate, error) {
	var release string
	err := idx.db.QueryRowContext(ctx, `
SELECT release_name FROM usenet_nntp_headers WHERE group_name=? AND msgid=?`, group, seedMsgID).Scan(&release)
	if err != nil {
		return Candidate{}, err
	}
	return idx.loadCandidate(ctx, group, release)
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
