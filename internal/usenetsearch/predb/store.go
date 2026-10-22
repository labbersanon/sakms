// Package predb is the local scene-release (PreDB) corpus: a Postgres table
// fed by an on-demand HTTP lookup (default) and an optional IRC announce
// feed. Stdlib only — it must not import the rest of sakms (see
// usenetsearch/doc.go import discipline).
package predb

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Sources recorded in usenet_nntp_predb.source.
const (
	SourceHTTP = "http"
	SourceIRC  = "irc"
	SourceDump = "dump"
	// SourceMiss marks a cached negative HTTP lookup (never returned as a hit).
	SourceMiss = "miss"
)

// MissTTL is how long a negative HTTP lookup is trusted.
const MissTTL = 30 * 24 * time.Hour

// Entry is one PreDB release record.
type Entry struct {
	Title     string
	SizeBytes int64 // 0 when unknown
	Files     int
	Filename  string
	Category  string
	PreAt     int64
	Nuked     bool
	RequestID int64
	GroupName string
}

// Store reads and writes usenet_nntp_predb.
type Store struct{ db *sql.DB }

// NewStore wraps an already-migrated app database.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// HashesOf returns the lowercase hex md5 and sha1 of title — the forms
// hash-obfuscating posters use as subjects.
func HashesOf(title string) (md5hex, sha1hex string) {
	m, _, s := hashTriple(title)
	return m, s
}

// hashTriple is MD5(title), MD5(MD5(title)) as hex-of-hex (nZEDb/PHP
// md5(md5($title))), and SHA1(title).
func hashTriple(title string) (md5hex, md5md5hex, sha1hex string) {
	m := md5.Sum([]byte(title))
	md5hex = hex.EncodeToString(m[:])
	inner := md5.Sum([]byte(md5hex))
	md5md5hex = hex.EncodeToString(inner[:])
	s := sha1.Sum([]byte(title))
	sha1hex = hex.EncodeToString(s[:])
	return
}

const upsertSQL = `
INSERT INTO usenet_nntp_predb (title, title_md5, title_md5_md5, title_sha1, size_bytes, files, filename, category, pre_at, nuked, source, fetched_at, request_id, group_name)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT (title) DO UPDATE SET title_md5=EXCLUDED.title_md5, title_md5_md5=EXCLUDED.title_md5_md5, title_sha1=EXCLUDED.title_sha1,
  size_bytes=CASE WHEN EXCLUDED.size_bytes>0 THEN EXCLUDED.size_bytes ELSE usenet_nntp_predb.size_bytes END,
  files=CASE WHEN EXCLUDED.files>0 THEN EXCLUDED.files ELSE usenet_nntp_predb.files END,
  filename=CASE WHEN EXCLUDED.filename<>'' THEN EXCLUDED.filename ELSE usenet_nntp_predb.filename END,
  category=CASE WHEN EXCLUDED.category<>'' THEN EXCLUDED.category ELSE usenet_nntp_predb.category END,
  pre_at=CASE WHEN EXCLUDED.pre_at>0 THEN EXCLUDED.pre_at ELSE usenet_nntp_predb.pre_at END,
  nuked=EXCLUDED.nuked, source=EXCLUDED.source, fetched_at=EXCLUDED.fetched_at,
  request_id=CASE WHEN EXCLUDED.request_id>0 THEN EXCLUDED.request_id ELSE usenet_nntp_predb.request_id END,
  group_name=CASE WHEN EXCLUDED.group_name<>'' THEN EXCLUDED.group_name ELSE usenet_nntp_predb.group_name END`

// Upsert stores (or refreshes) a record, replacing any cached miss for it.
func (s *Store) Upsert(ctx context.Context, e Entry, source string) error {
	if s == nil || s.db == nil {
		return errors.New("predb: store not configured")
	}
	title := strings.TrimSpace(e.Title)
	if title == "" {
		return errors.New("predb: empty title")
	}
	m, mm, sh := hashTriple(title)
	_, err := s.db.ExecContext(ctx, upsertSQL,
		title, m, mm, sh, e.SizeBytes, e.Files, e.Filename, e.Category, e.PreAt, e.Nuked, source, time.Now().Unix(),
		e.RequestID, normalizePreDBGroup(e.GroupName))
	return err
}

// SetNuked updates the nuke flag of an existing record (IRC NUKE/UNNUKE).
func (s *Store) SetNuked(ctx context.Context, title string, nuked bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE usenet_nntp_predb SET nuked=? WHERE title=? AND source<>?`, nuked, title, SourceMiss)
	return err
}

const selectEntry = `SELECT title, size_bytes, files, filename, category, pre_at, nuked, request_id, group_name FROM usenet_nntp_predb`

const (
	// Claude 2026-10-22: non-empty filename is required for idx_usenet_nntp_predb_filename.
	// Reason: that index is partial (filename not empty). Postgres does not infer
	//   the predicate from lower(filename)= or a stem range. Range not LIKE:
	//   default btree cannot serve LIKE (needs text_pattern_ops). Do not CREATE
	//   INDEX CONCURRENTLY on deploy (lock).
	// Troubleshooting: FileDesc ByFilename seq-scanned 6.9M rows (~3s). Live
	//   EXPLAIN with this clause is Index Scan (~0.05-55ms).
	// Review if: a text_pattern_ops index is added off-hours.
	byFilenameExact = selectEntry + ` WHERE filename <> '' AND lower(filename)=? AND source<>? LIMIT 1`
	byFilenameStem  = selectEntry + ` WHERE filename <> '' AND lower(filename) >= ? AND lower(filename) < ? AND source<>? LIMIT 1`
)

func scanEntry(row *sql.Row) (Entry, bool, error) {
	var e Entry
	err := row.Scan(&e.Title, &e.SizeBytes, &e.Files, &e.Filename, &e.Category, &e.PreAt, &e.Nuked, &e.RequestID, &e.GroupName)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return e, true, nil
}

// ByTitle returns the exact-title record (cached misses are not hits).
func (s *Store) ByTitle(ctx context.Context, title string) (Entry, bool, error) {
	return scanEntry(s.db.QueryRowContext(ctx, selectEntry+` WHERE title=? AND source<>?`, title, SourceMiss))
}

// ByHash resolves a 32-char md5 / md5(md5(title)) or 40-char sha1 hex string.
func (s *Store) ByHash(ctx context.Context, h string) (Entry, bool, error) {
	h = strings.ToLower(strings.TrimSpace(h))
	switch len(h) {
	case 32:
		return scanEntry(s.db.QueryRowContext(ctx,
			selectEntry+` WHERE (title_md5=? OR title_md5_md5=?) AND source<>? LIMIT 1`, h, h, SourceMiss))
	case 40:
		return scanEntry(s.db.QueryRowContext(ctx,
			selectEntry+` WHERE title_sha1=? AND source<>? LIMIT 1`, h, SourceMiss))
	default:
		return Entry{}, false, nil
	}
}

// ByRequestID resolves an nZEDb request id, preferring an exact group match.
func (s *Store) ByRequestID(ctx context.Context, id int64, group string) (Entry, bool, error) {
	if id <= 0 {
		return Entry{}, false, nil
	}
	group = normalizePreDBGroup(group)
	return scanEntry(s.db.QueryRowContext(ctx, selectEntry+`
 WHERE request_id=? AND source<>?
 ORDER BY CASE WHEN lower(group_name)=lower(?) THEN 0 WHEN group_name='' THEN 1 ELSE 2 END, pre_at DESC
 LIMIT 1`, id, SourceMiss, group))
}

// ByFilename resolves a PreDB inner/archive filename (exact, case-insensitive).
// A 16+ character first label (`hash.mkv`) also matches `hash.part1.rar`
// (IRC [FN:] vs a quoted yEnc name).
func (s *Store) ByFilename(ctx context.Context, filename string) (Entry, bool, error) {
	fn := strings.ToLower(strings.TrimSpace(filename))
	if fn == "" {
		return Entry{}, false, nil
	}
	e, ok, err := scanEntry(s.db.QueryRowContext(ctx, byFilenameExact, fn, SourceMiss))
	if err != nil || ok {
		return e, ok, err
	}
	stem := fn
	if i := strings.IndexByte(fn, '.'); i >= 0 {
		stem = fn[:i]
	}
	if len(stem) < 16 {
		return Entry{}, false, nil
	}
	return scanEntry(s.db.QueryRowContext(ctx, byFilenameStem, stem+".", stem+"/", SourceMiss))
}

// VacuumAnalyze reclaims dead rows and refreshes planner stats.
// skipLocked is for the daily timer so it does not queue behind dump import.
// Dump import calls this with skipLocked=false.
//
// Claude 2026-10-22: PARALLEL 0 + statement_timeout=0 on the same Conn.
// Reason: default parallel VACUUM needs ~64MB DSM (SQLSTATE 53100 on
//
//	sakms-db 64MiB shm). A 15min timeout cancelled PARALLEL 0 on
//	idx_usenet_nntp_predb_filename. SET must share the connection or the
//	pool ignores it. Do not VACUUM FULL. Review if: compose shm is raised.
func (s *Store) VacuumAnalyze(ctx context.Context, skipLocked bool) error {
	if s == nil || s.db == nil {
		return errors.New("predb: store not configured")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET statement_timeout = 0`); err != nil {
		return err
	}
	q := `VACUUM (PARALLEL 0, ANALYZE) usenet_nntp_predb`
	if skipLocked {
		q = `VACUUM (SKIP_LOCKED, PARALLEL 0, ANALYZE) usenet_nntp_predb`
	}
	_, err = conn.ExecContext(ctx, q)
	return err
}

// RecentMiss reports whether title has a cached negative lookup newer than ttl.
func (s *Store) RecentMiss(ctx context.Context, title string, ttl time.Duration) (bool, error) {
	var fetched int64
	err := s.db.QueryRowContext(ctx,
		`SELECT fetched_at FROM usenet_nntp_predb WHERE title=? AND source=?`, title, SourceMiss).Scan(&fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return time.Since(time.Unix(fetched, 0)) < ttl, nil
}

// RecordMiss caches a confirmed negative lookup (never overwrites a real record).
func (s *Store) RecordMiss(ctx context.Context, title string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO usenet_nntp_predb (title, source, fetched_at) VALUES (?,?,?)
ON CONFLICT (title) DO UPDATE SET fetched_at=EXCLUDED.fetched_at WHERE usenet_nntp_predb.source=?`,
		title, SourceMiss, time.Now().Unix(), SourceMiss)
	return err
}

func normalizePreDBGroup(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "a.b.") {
		return "alt.binaries." + s[4:]
	}
	return s
}
