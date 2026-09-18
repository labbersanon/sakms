package usenetsearch

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const indexFileName = "nntp-index.sqlite"

// Index is the local header store (separate SQLite under usenet_nntp_index_dir).
type Index struct {
	db *sql.DB
}

// OpenIndex creates/opens <dir>/nntp-index.sqlite and migrates schema.
func OpenIndex(dir string) (*Index, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("usenetsearch: index dir must be absolute, got %q", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("usenetsearch: mkdir index dir: %w", err)
	}
	path := filepath.Join(dir, indexFileName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateIndex(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Index{db: db}, nil
}

func migrateIndex(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS headers (
  group_name   TEXT NOT NULL,
  msg_num      INTEGER NOT NULL,
  msgid        TEXT NOT NULL,
  subject      TEXT NOT NULL DEFAULT '',
  from_addr    TEXT NOT NULL DEFAULT '',
  posted_at    INTEGER NOT NULL DEFAULT 0,
  bytes        INTEGER NOT NULL DEFAULT 0,
  release_name TEXT NOT NULL DEFAULT '',
  part_n       INTEGER NOT NULL DEFAULT 0,
  part_m       INTEGER NOT NULL DEFAULT 0,
  filename     TEXT NOT NULL DEFAULT '',
  yenc_size    INTEGER NOT NULL DEFAULT 0,
  obfuscated   INTEGER NOT NULL DEFAULT 0,
  is_meta      INTEGER NOT NULL DEFAULT 0,
  ingested_at  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (group_name, msgid)
);
CREATE INDEX IF NOT EXISTS idx_headers_release ON headers(group_name, release_name);
CREATE INDEX IF NOT EXISTS idx_headers_posted ON headers(posted_at);
CREATE INDEX IF NOT EXISTS idx_headers_subject ON headers(release_name);
CREATE TABLE IF NOT EXISTS watermarks (
  group_name TEXT PRIMARY KEY,
  high       INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
);
`)
	return err
}

func (idx *Index) Close() error {
	if idx == nil || idx.db == nil {
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
INSERT INTO headers (group_name, msg_num, msgid, subject, from_addr, posted_at, bytes,
  release_name, part_n, part_m, filename, yenc_size, obfuscated, is_meta, ingested_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(group_name, msgid) DO UPDATE SET
  msg_num=excluded.msg_num, subject=excluded.subject, from_addr=excluded.from_addr,
  posted_at=excluded.posted_at, bytes=excluded.bytes, release_name=excluded.release_name,
  part_n=excluded.part_n, part_m=excluded.part_m, filename=excluded.filename,
  yenc_size=excluded.yenc_size, obfuscated=excluded.obfuscated, is_meta=excluded.is_meta,
  ingested_at=excluded.ingested_at
`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().Unix()
	for _, r := range rows {
		ob, meta := 0, 0
		if r.Obfuscated {
			ob = 1
		}
		if r.IsMeta {
			meta = 1
		}
		if _, err := stmt.ExecContext(ctx, r.Group, r.MsgNum, r.MsgID, r.Subject, r.From, r.PostedAt, r.Bytes,
			r.ReleaseName, r.PartN, r.PartM, r.Filename, r.YencSize, ob, meta, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (idx *Index) Watermark(ctx context.Context, group string) (int, error) {
	var high int
	err := idx.db.QueryRowContext(ctx, `SELECT high FROM watermarks WHERE group_name=?`, group).Scan(&high)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return high, err
}

func (idx *Index) SetWatermark(ctx context.Context, group string, high int) error {
	_, err := idx.db.ExecContext(ctx, `
INSERT INTO watermarks(group_name, high, updated_at) VALUES(?,?,?)
ON CONFLICT(group_name) DO UPDATE SET high=excluded.high, updated_at=excluded.updated_at
`, group, high, time.Now().Unix())
	return err
}

func (idx *Index) PruneOlderThan(ctx context.Context, cutoffUnix int64) (int64, error) {
	res, err := idx.db.ExecContext(ctx, `DELETE FROM headers WHERE posted_at > 0 AND posted_at < ?`, cutoffUnix)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (idx *Index) ApproxBytes(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := idx.db.QueryRowContext(ctx, `SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&n)
	if err != nil {
		// fallback row estimate
		var rows int64
		_ = idx.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM headers`).Scan(&rows)
		return rows * 200, nil
	}
	return n.Int64, nil
}

func (idx *Index) PruneOldest(ctx context.Context, targetBytes int64) error {
	for {
		size, err := idx.ApproxBytes(ctx)
		if err != nil || size <= targetBytes {
			return err
		}
		res, err := idx.db.ExecContext(ctx, `
DELETE FROM headers WHERE rowid IN (
  SELECT rowid FROM headers ORDER BY ingested_at ASC, posted_at ASC LIMIT 5000
)`)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil
		}
	}
}

func (idx *Index) Empty(ctx context.Context) (bool, error) {
	var n int
	err := idx.db.QueryRowContext(ctx, `SELECT 1 FROM headers LIMIT 1`).Scan(&n)
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
	err := idx.db.QueryRowContext(ctx, `SELECT 1 FROM headers WHERE group_name=? LIMIT 1`, group).Scan(&n)
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
	where = append(where, `obfuscated=0`, `release_name!=''`)
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
		where = append(where, `release_name LIKE ?`)
		args = append(args, "%"+escapeLike(t)+"%")
	}
	for _, ex := range q.Exclude {
		ex = strings.TrimSpace(ex)
		if ex == "" {
			continue
		}
		where = append(where, `release_name NOT LIKE ?`)
		args = append(args, "%"+escapeLike(ex)+"%")
	}
	if !q.PostedAfter.IsZero() {
		where = append(where, `posted_at >= ?`)
		args = append(args, q.PostedAfter.Unix())
	}
	sqlStr := `SELECT DISTINCT group_name, release_name FROM headers WHERE ` + strings.Join(where, " AND ") + ` LIMIT ?`
	args = append(args, limit*3) // over-fetch then assemble
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
FROM headers WHERE group_name=? AND release_name=? ORDER BY filename, part_n, msg_num
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
SELECT release_name FROM headers WHERE group_name=? AND msgid=?`, group, seedMsgID).Scan(&release)
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
