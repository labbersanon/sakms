// Package parseentity is the entity cache backing DB-first filename parsing
// (internal/identify/parse_db.go). It stores normalized studio and performer
// names sourced from TPDB, StashDB, FansDB, and local Stash, with an alias
// table for alternate spellings/domain-format names.
//
// Normalization (normName) strips all non-alphanumeric characters and
// lowercases the result, so "Bang Bros", "bangbros.com", and "bang-bros" all
// map to "bangbros" — critical for matching dot-separated filename tokens.
//
// The SQLiteStore returned by NewSQLiteStore is the sole concrete
// implementation; the EntityStore interface exists only so parse_db.go's unit
// tests can inject a table-driven stub without an on-disk database.
package parseentity

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode"
)

// EntityStore is the capability parse_db.go needs from the entity cache.
// Lookup methods accept one already-normalized token string (the sliding-window
// join the parser produces) and return the canonical display name if matched.
type EntityStore interface {
	// LookupStudio checks parse_studios.name_norm and parse_studio_aliases for
	// normToken. Returns the canonical display name and true on a hit.
	LookupStudio(ctx context.Context, normToken string) (name string, found bool, err error)
	// LookupPerformer checks parse_performers.name_norm and
	// parse_performer_aliases for normToken.
	LookupPerformer(ctx context.Context, normToken string) (name string, found bool, err error)

	// UpsertStudio inserts or updates a studio row by name_norm. aliases are
	// additional normalized tokens that should also resolve to this studio (e.g.
	// "bangbroscom" for "bangbros.com").
	UpsertStudio(ctx context.Context, name, source, extID string, aliases ...string) error
	// UpsertPerformer inserts or updates a performer row by name_norm.
	UpsertPerformer(ctx context.Context, name, source, extID string, aliases ...string) error

	// SetSyncCursor persists the sync cursor (page number or timestamp) for
	// the given source key so incremental syncs can resume.
	SetSyncCursor(ctx context.Context, source, cursor string) error
	// GetSyncCursor returns the stored cursor and last-sync time for source.
	// Returns ("", zero, nil) if no sync has run yet.
	GetSyncCursor(ctx context.Context, source string) (cursor string, syncedAt time.Time, err error)

	// StudioCount returns the total number of studio rows.
	StudioCount(ctx context.Context) (int, error)
	// PerformerCount returns the total number of performer rows.
	PerformerCount(ctx context.Context) (int, error)
}

// NormName normalizes a display name for storage and lookup: lowercase, keep
// only letters and digits. "Bang Bros." → "bangbros", "bangbros.com" →
// "bangbroscom". Applied identically at write time (UpsertStudio/Performer)
// and at query time (LookupStudio/Performer) so matching is always consistent.
func NormName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SQLiteStore is an EntityStore backed by the application's existing SQLite
// database. It shares the *sql.DB the rest of the application uses (opened by
// internal/db.Open) — no second connection needed.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore wraps an existing *sql.DB. The caller owns the connection's
// lifecycle; SQLiteStore never closes it.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

func (s *SQLiteStore) LookupStudio(ctx context.Context, normToken string) (string, bool, error) {
	var name string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM parse_studios WHERE name_norm = ?`, normToken).Scan(&name)
	if err == nil {
		return name, true, nil
	}
	if err != sql.ErrNoRows {
		return "", false, err
	}
	// fall through to alias table
	err = s.db.QueryRowContext(ctx,
		`SELECT ps.name FROM parse_studio_aliases pa
		 JOIN parse_studios ps ON ps.id = pa.studio_id
		 WHERE pa.alias_norm = ?`, normToken).Scan(&name)
	if err == nil {
		return name, true, nil
	}
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return "", false, err
}

func (s *SQLiteStore) LookupPerformer(ctx context.Context, normToken string) (string, bool, error) {
	var name string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM parse_performers WHERE name_norm = ?`, normToken).Scan(&name)
	if err == nil {
		return name, true, nil
	}
	if err != sql.ErrNoRows {
		return "", false, err
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT pp.name FROM parse_performer_aliases pa
		 JOIN parse_performers pp ON pp.id = pa.performer_id
		 WHERE pa.alias_norm = ?`, normToken).Scan(&name)
	if err == nil {
		return name, true, nil
	}
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return "", false, err
}

func (s *SQLiteStore) UpsertStudio(ctx context.Context, name, source, extID string, aliases ...string) error {
	norm := NormName(name)
	if norm == "" {
		return nil
	}
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO parse_studios (name, name_norm, source, ext_id)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(name_norm) DO UPDATE SET
		   name       = excluded.name,
		   source     = excluded.source,
		   ext_id     = excluded.ext_id,
		   updated_at = sakms_now()
		 RETURNING id`,
		name, norm, source, nullableStr(extID)).Scan(&id)
	if err != nil {
		return err
	}
	return s.upsertAliases(ctx, "studio", id, aliases)
}

func (s *SQLiteStore) UpsertPerformer(ctx context.Context, name, source, extID string, aliases ...string) error {
	norm := NormName(name)
	if norm == "" {
		return nil
	}
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO parse_performers (name, name_norm, source, ext_id)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(name_norm) DO UPDATE SET
		   name       = excluded.name,
		   source     = excluded.source,
		   ext_id     = excluded.ext_id,
		   updated_at = sakms_now()
		 RETURNING id`,
		name, norm, source, nullableStr(extID)).Scan(&id)
	if err != nil {
		return err
	}
	return s.upsertAliases(ctx, "performer", id, aliases)
}

func (s *SQLiteStore) upsertAliases(ctx context.Context, kind string, parentID int64, aliases []string) error {
	for _, a := range aliases {
		an := NormName(a)
		if an == "" {
			continue
		}
		var q string
		if kind == "studio" {
			q = `INSERT INTO parse_studio_aliases (alias_norm, studio_id)
			     VALUES (?, ?)
			     ON CONFLICT(alias_norm) DO UPDATE SET studio_id = excluded.studio_id`
		} else {
			q = `INSERT INTO parse_performer_aliases (alias_norm, performer_id)
			     VALUES (?, ?)
			     ON CONFLICT(alias_norm) DO UPDATE SET performer_id = excluded.performer_id`
		}
		if _, err := s.db.ExecContext(ctx, q, an, parentID); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) SetSyncCursor(ctx context.Context, source, cursor string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO parse_entity_sync (source, cursor, synced_at)
		 VALUES (?, ?, sakms_now())
		 ON CONFLICT(source) DO UPDATE SET
		   cursor    = excluded.cursor,
		   synced_at = excluded.synced_at`,
		source, cursor)
	return err
}

func (s *SQLiteStore) GetSyncCursor(ctx context.Context, source string) (string, time.Time, error) {
	var cursor string
	var syncedAtStr sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT cursor, synced_at FROM parse_entity_sync WHERE source = ?`, source).
		Scan(&cursor, &syncedAtStr)
	if err == sql.ErrNoRows {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, err
	}
	var syncedAt time.Time
	if syncedAtStr.Valid && syncedAtStr.String != "" {
		syncedAt, _ = time.Parse("2006-01-02T15:04:05.999Z", syncedAtStr.String)
	}
	return cursor, syncedAt, nil
}

func (s *SQLiteStore) StudioCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM parse_studios`).Scan(&n)
	return n, err
}

func (s *SQLiteStore) PerformerCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM parse_performers`).Scan(&n)
	return n, err
}

// nullableStr converts an empty string to sql.NullString{Valid:false} so the
// column stores NULL rather than "".
func nullableStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
