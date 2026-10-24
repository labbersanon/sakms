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
		if aff == 0 {
			continue
		}
		n += int(aff)
		// Claude 2026-10-24: bump overlap summary per new NZB, not on GET.
		// Reason: live GROUP BY kind,overlap_key timed out at 120s on 1.3M rows.
		// Troubleshooting: Same-NZBs stuck at 0 after crawl → this bump skipped
		//   (ON CONFLICT DO NOTHING) or EnsureOverlap never rebuilt.
		// Review if: Lighthouse overlap replaces the Dock summary table.
		if err := s.applyOverlapInsert(ctx, kind, indexer, key); err != nil {
			return n, err
		}
	}
	return n, nil
}

// RecomputeOverlapKeys rewrites overlap_key from the NZB title. Existing
// rows were keyed by IMDB/TVDB/TMDB (same movie, many NZBs).
//
// Claude 2026-10-23: load titles, then one tx with a prepared UPDATE.
// Reason: ~160k rows; per-row Exec blocked crawl. pq cannot UPDATE while
// the SELECT cursor is open (conn busy).
// Troubleshooting: crawl silent after boot → still updating one connection
// round-trip per row. conn busy → SELECT was still open.
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

// Report is the Dock card payload.
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

	// Claude 2026-10-24: Dock overlap reads the summary table, not this CTE.
	// Reason: the live GROUP BY timed out at 120s on ~1.3M catalog rows;
	//   Traefik 499'd GET /api/admin/newznab-catalog and the card never left
	//   "Loading NZB catalog…". COUNT(*) by indexer,kind is still cheap (~140ms).
	// Troubleshooting: empty Same-NZBs with non-zero counts → overlap table
	//   empty (EnsureOverlap) or InsertHits bump skipped.
	// Review if: Lighthouse overlap replaces the Dock card.
	// WITH keys AS (
	//   SELECT kind, overlap_key,
	//     BOOL_OR(indexer = 'newznab') AS a,
	//     BOOL_OR(indexer = 'newznab2') AS b
	//   FROM newznab_catalog
	//   WHERE overlap_key <> ''
	//   GROUP BY kind, overlap_key
	// )
	// SELECT kind, COUNT(*) FILTER (WHERE a), ... FROM keys GROUP BY kind
	orows, err := s.db.QueryContext(ctx, `
		SELECT kind, host1_nzbs, host2_nzbs, both, only_host1, only_host2
		FROM newznab_catalog_overlap
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

// applyOverlapInsert updates unique-key overlap for one newly inserted NZB.
// thisN is counted after the INSERT, so 1 means this indexer just gained the key.
func (s *Store) applyOverlapInsert(ctx context.Context, kind, indexer, key string) error {
	if s == nil || s.db == nil || strings.TrimSpace(key) == "" {
		return nil
	}
	var thisN, otherN int
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE indexer = $1),
			COUNT(*) FILTER (WHERE indexer <> $1)
		FROM newznab_catalog
		WHERE kind = $2 AND overlap_key = $3`, indexer, kind, key).Scan(&thisN, &otherN); err != nil {
		return err
	}
	if thisN != 1 {
		return nil
	}
	h1, h2, both, o1, o2 := 0, 0, 0, 0, 0
	switch indexer {
	case "newznab":
		h1 = 1
		if otherN > 0 {
			both = 1
			o2 = -1
		} else {
			o1 = 1
		}
	case "newznab2":
		h2 = 1
		if otherN > 0 {
			both = 1
			o1 = -1
		} else {
			o2 = 1
		}
	default:
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO newznab_catalog_overlap (kind, host1_nzbs, host2_nzbs, both, only_host1, only_host2)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (kind) DO UPDATE SET
			host1_nzbs = newznab_catalog_overlap.host1_nzbs + EXCLUDED.host1_nzbs,
			host2_nzbs = newznab_catalog_overlap.host2_nzbs + EXCLUDED.host2_nzbs,
			both = newznab_catalog_overlap.both + EXCLUDED.both,
			only_host1 = GREATEST(0, newznab_catalog_overlap.only_host1 + EXCLUDED.only_host1),
			only_host2 = GREATEST(0, newznab_catalog_overlap.only_host2 + EXCLUDED.only_host2)`,
		kind, h1, h2, both, o1, o2)
	return err
}

// EnsureOverlap backfills the Dock summary once when catalog rows exist and
// the table is empty (rows inserted before 0058). Crawl waits so InsertHits
// bumps cannot race the rebuild. GET does not call this.
func (s *Store) EnsureOverlap(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	var hasCatalog, hasOverlap bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM newznab_catalog LIMIT 1)`).Scan(&hasCatalog); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM newznab_catalog_overlap LIMIT 1)`).Scan(&hasOverlap); err != nil {
		return err
	}
	if !hasCatalog || hasOverlap {
		return nil
	}
	return s.RebuildOverlap(ctx)
}

// RebuildOverlap scans newznab_catalog into the summary table. It is the same
// GROUP BY GET used to run; do not call it from the request path.
func (s *Store) RebuildOverlap(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
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
		return err
	}
	defer orows.Close()
	var rows []KindOverlap
	for orows.Next() {
		var o KindOverlap
		if err := orows.Scan(&o.Kind, &o.Host1Nzbs, &o.Host2Nzbs, &o.Both, &o.OnlyHost1, &o.OnlyHost2); err != nil {
			return err
		}
		rows = append(rows, o)
	}
	if err := orows.Err(); err != nil {
		return err
	}
	if err := orows.Close(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM newznab_catalog_overlap`); err != nil {
		return err
	}
	for _, o := range rows {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO newznab_catalog_overlap (kind, host1_nzbs, host2_nzbs, both, only_host1, only_host2)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			o.Kind, o.Host1Nzbs, o.Host2Nzbs, o.Both, o.OnlyHost1, o.OnlyHost2); err != nil {
			return err
		}
	}
	return tx.Commit()
}
