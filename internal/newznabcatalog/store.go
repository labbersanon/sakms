// Package newznabcatalog stores Newznab t=search pages from the Settings
// hosts (newznab + newznab2) so overlap can be counted. It is not the
// native NNTP OVER index. Enclosure URLs are never stored.
package newznabcatalog

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/newznab"
)

const (
	KindMovies = "movies"
	KindSeries = "series"
	KindAdult  = "adult"
)

var kinds = []struct {
	Kind string
	Cat  int
}{
	{KindMovies, 2000},
	{KindSeries, 5000},
	{KindAdult, 6000},
}

var catalogServices = []string{"newznab", "newznab2"}

// Store persists catalog hits and crawl cursors.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	if db == nil {
		return nil
	}
	return &Store{db: db}
}

// Cursor is one indexer×kind page pointer.
type Cursor struct {
	Indexer     string
	Kind        string
	PageOffset  int
	Complete    bool
	LastPageNew int
	ZeroStreak  int
	LastError   string
	UpdatedAt   int64
}

func (s *Store) Cursor(ctx context.Context, indexer, kind string) (Cursor, error) {
	cur := Cursor{Indexer: indexer, Kind: kind}
	if s == nil || s.db == nil {
		return cur, nil
	}
	err := s.db.QueryRowContext(ctx, `
		SELECT page_offset, complete, last_page_new, zero_streak, last_error, updated_at
		FROM newznab_catalog_cursors
		WHERE indexer = $1 AND kind = $2`, indexer, kind).Scan(
		&cur.PageOffset, &cur.Complete, &cur.LastPageNew, &cur.ZeroStreak, &cur.LastError, &cur.UpdatedAt)
	if err == sql.ErrNoRows {
		return cur, nil
	}
	return cur, err
}

func (s *Store) SaveCursor(ctx context.Context, cur Cursor) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO newznab_catalog_cursors (indexer, kind, page_offset, complete, last_page_new, zero_streak, last_error, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (indexer, kind) DO UPDATE SET
			page_offset = EXCLUDED.page_offset,
			complete = EXCLUDED.complete,
			last_page_new = EXCLUDED.last_page_new,
			zero_streak = EXCLUDED.zero_streak,
			last_error = EXCLUDED.last_error,
			updated_at = EXCLUDED.updated_at`,
		cur.Indexer, cur.Kind, cur.PageOffset, cur.Complete, cur.LastPageNew, cur.ZeroStreak, cur.LastError, cur.UpdatedAt)
	return err
}

// InsertHits stores new guids only. Returns how many rows were inserted.
func (s *Store) InsertHits(ctx context.Context, kind string, hits []newznab.CatalogHit) (int, error) {
	if s == nil || s.db == nil || len(hits) == 0 {
		return 0, nil
	}
	now := time.Now().Unix()
	n := 0
	for _, h := range hits {
		guid := strings.TrimSpace(h.GUID)
		indexer := strings.TrimSpace(h.Indexer)
		if guid == "" || indexer == "" {
			continue
		}
		key := newznab.OverlapKey(h)
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO newznab_catalog (
				indexer, guid, title, size, category, kind,
				imdb_id, tvdb_id, tmdb_id, year, overlap_key, ingested_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (indexer, guid) DO NOTHING`,
			indexer, guid, h.Title, h.Size, h.Category, kind,
			newznab.NormalizeIMDB(h.ImdbID), newznab.DigitsOnly(h.TvdbID), newznab.DigitsOnly(h.TmdbID),
			h.Year, key, now)
		if err != nil {
			return n, err
		}
		aff, _ := res.RowsAffected()
		n += int(aff)
	}
	return n, nil
}

// RecomputeOverlapKeys rewrites overlap_key from the NZB title. Existing
// rows were keyed by IMDB/TVDB/TMDB (same movie, many NZBs).
//
// Claude 2026-10-23: load titles, then one tx with a prepared UPDATE.
// Reason: ~160k rows; per-row Exec blocked crawl. pq cannot UPDATE while
//
//	the SELECT cursor is open (conn busy).
//
// Troubleshooting: crawl silent after boot → still updating one connection
//
//	round-trip per row. conn busy → SELECT was still open.
//
// Review if: overlap_key is only nzb: and this rewrite is a no-op.
func (s *Store) RecomputeOverlapKeys(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT indexer, guid, title FROM newznab_catalog`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var hits []newznab.CatalogHit
	for rows.Next() {
		var h newznab.CatalogHit
		if err := rows.Scan(&h.Indexer, &h.GUID, &h.Title); err != nil {
			return 0, err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		UPDATE newznab_catalog SET overlap_key = $1
		WHERE indexer = $2 AND guid = $3 AND overlap_key <> $1`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	n := 0
	for _, h := range hits {
		res, err := stmt.ExecContext(ctx, newznab.OverlapKey(h), h.Indexer, h.GUID)
		if err != nil {
			return n, err
		}
		aff, _ := res.RowsAffected()
		n += int(aff)
	}
	return n, tx.Commit()
}

// KindCount is NZB rows for one indexer and kind.
type KindCount struct {
	Indexer string
	Kind    string
	NZBs    int
}

// KindOverlap is NZB-release overlap for one kind (sanitized title, not IMDB).
type KindOverlap struct {
	Kind      string
	Host1Nzbs int
	Host2Nzbs int
	Both      int
	OnlyHost1 int
	OnlyHost2 int
}

// Report is the Dock card payload (overlap, On Demand counts, indexer health).
type Report struct {
	IntervalSeconds int
	PageSize        int
	Cursors         []Cursor
	Counts          []KindCount
	Overlap         []KindOverlap
	Indexers        []IndexerHealth
}

func (s *Store) Report(ctx context.Context) (Report, error) {
	out := Report{
		IntervalSeconds: int(Interval / time.Second),
		PageSize:        newznab.CatalogPageSize(),
		Cursors:         []Cursor{},
		Counts:          []KindCount{},
		Overlap:         []KindOverlap{},
		Indexers:        IndexerHealthFromCursors(nil),
	}
	if s == nil || s.db == nil {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT indexer, kind, page_offset, complete, last_page_new, zero_streak, last_error, updated_at
		FROM newznab_catalog_cursors
		ORDER BY indexer, kind`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Cursor
		if err := rows.Scan(&c.Indexer, &c.Kind, &c.PageOffset, &c.Complete, &c.LastPageNew, &c.ZeroStreak, &c.LastError, &c.UpdatedAt); err != nil {
			return out, err
		}
		out.Cursors = append(out.Cursors, c)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}

	crows, err := s.db.QueryContext(ctx, `
		SELECT indexer, kind, COUNT(*) FROM newznab_catalog GROUP BY indexer, kind`)
	if err != nil {
		return out, err
	}
	defer crows.Close()
	for crows.Next() {
		var k KindCount
		if err := crows.Scan(&k.Indexer, &k.Kind, &k.NZBs); err != nil {
			return out, err
		}
		out.Counts = append(out.Counts, k)
	}
	if err := crows.Err(); err != nil {
		return out, err
	}

	orows, err := s.db.QueryContext(ctx, `
		WITH keys AS (
			SELECT kind, overlap_key,
				BOOL_OR(indexer = 'newznab') AS a,
				BOOL_OR(indexer = 'newznab2') AS b
			FROM newznab_catalog
			WHERE overlap_key <> ''
			GROUP BY kind, overlap_key
		)
		SELECT kind,
			COUNT(*) FILTER (WHERE a) AS host1,
			COUNT(*) FILTER (WHERE b) AS host2,
			COUNT(*) FILTER (WHERE a AND b) AS both,
			COUNT(*) FILTER (WHERE a AND NOT b) AS only1,
			COUNT(*) FILTER (WHERE b AND NOT a) AS only2
		FROM keys
		GROUP BY kind
		ORDER BY kind`)
	if err != nil {
		return out, err
	}
	defer orows.Close()
	for orows.Next() {
		var o KindOverlap
		if err := orows.Scan(&o.Kind, &o.Host1Nzbs, &o.Host2Nzbs, &o.Both, &o.OnlyHost1, &o.OnlyHost2); err != nil {
			return out, err
		}
		out.Overlap = append(out.Overlap, o)
	}
	if err := orows.Err(); err != nil {
		return out, err
	}
	out.Indexers = IndexerHealthFromCursors(out.Cursors)
	return out, nil
}
