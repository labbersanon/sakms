package adultnewest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// MatchedRelease is one matched ENTITY (a scene/movie/studio/performer) the
// background scan job (scan.go) resolved from a Prowlarr release — the only
// shape written to adult_newest_releases. Unmatched releases are never
// stored; Adult Discover reads exclusively from this cache, so a release
// that never matched simply never appears as a row. Deliberately keyed by
// entity (RowType + EntitySource + EntityID), not by which release surfaced
// it — see the migration's doc comment for why: several releases can
// legitimately resolve to the same scene/studio/performer, and those must
// collapse to one Discover card, not duplicate.
type MatchedRelease struct {
	ID           int
	RowType      RowType
	EntityID     string
	EntitySource string // "tpdb" | "stashdb" | "fansdb"
	EntityTitle  string
	EntityStudio string
	EntityImage  string
	EntityDate   string
	// EntityDurationSeconds is the matched entity's runtime, 0 if unknown —
	// threaded through from identify.MatchResult.RuntimeSeconds. Always 0 for
	// Studio/Performer rows (a runtime concept doesn't apply to them); real
	// for Scene/Movie rows when the matching lookup path had one in hand.
	// Exists specifically so a Discover card built from this cache can build
	// a grab request with a genuine duration — see the migration's doc
	// comment for the live bug this fixes.
	EntityDurationSeconds int
	Genres                []string
	FirstSeenAt           string
}

// ReleaseStore persists matched-entity cache rows plus the separate
// "already attempted" release-guid set. Kept as a separate type from Store
// (the row-config CRUD) even though both live in this package and share a
// *sql.DB — they're two genuinely different resources (admin config vs. a
// write-mostly cache the scan job owns) with no overlapping methods, so a
// combined type would just be two unrelated method sets glued together.
type ReleaseStore struct {
	db *sql.DB
}

func NewReleaseStore(db *sql.DB) *ReleaseStore {
	return &ReleaseStore{db: db}
}

// SeenGUIDs returns the subset of guids already present in
// adult_newest_seen — releases the scan job has already run through the
// (expensive, per-release) identify pipeline, matched or not. This is
// intentionally a SEPARATE table from the matched-entity cache: an unmatched
// release must never be retried every cycle just because it produced no
// cache row (see the migration's doc comment). Batched into one query
// rather than one lookup per release.
func (s *ReleaseStore) SeenGUIDs(ctx context.Context, guids []string) (map[string]bool, error) {
	seen := make(map[string]bool, len(guids))
	if len(guids) == 0 {
		return seen, nil
	}
	placeholders := make([]byte, 0, len(guids)*2)
	args := make([]any, len(guids))
	for i, g := range guids {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = g
	}
	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT release_guid FROM adult_newest_seen WHERE release_guid IN (%s)`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("checking seen release guids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			return nil, fmt.Errorf("scanning seen release guid: %w", err)
		}
		seen[guid] = true
	}
	return seen, rows.Err()
}

// MarkSeen records that releaseGUID has been run through the identify
// pipeline, regardless of outcome — called once per processed release by
// scan.go's runCycle, whether or not it produced any matched-entity rows.
// Idempotent (ON CONFLICT DO NOTHING): a release is only ever "first seen"
// once.
func (s *ReleaseStore) MarkSeen(ctx context.Context, releaseGUID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO adult_newest_seen (release_guid) VALUES (?)
		ON CONFLICT(release_guid) DO NOTHING
	`, releaseGUID)
	if err != nil {
		return fmt.Errorf("marking release %q seen: %w", releaseGUID, err)
	}
	return nil
}

// Insert writes one matched entity to the cache. Idempotent by design — an
// entity's identity doesn't change over time (unlike internal/recheck's
// availability boolean, which does), so a duplicate (row_type, entity_source,
// entity_id) is silently ignored rather than updated: the release that first
// surfaced an entity wins that cache row.
func (s *ReleaseStore) Insert(ctx context.Context, m MatchedRelease) error {
	genres := m.Genres
	if genres == nil {
		// json.Marshal(nil slice) encodes "null", not "[]" — the column's
		// own DEFAULT is "[]", and every reader (List/DistinctGenres) is
		// written assuming a decodable array; keep every row consistent
		// rather than special-casing a "null" value at every read site.
		genres = []string{}
	}
	genresJSON, err := json.Marshal(genres)
	if err != nil {
		return fmt.Errorf("encoding genres for entity %q: %w", m.EntityID, err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO adult_newest_releases
			(row_type, entity_id, entity_source, entity_title, entity_studio, entity_image, entity_date, entity_duration_seconds, genres)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(row_type, entity_source, entity_id) DO NOTHING
	`, string(m.RowType), m.EntityID, m.EntitySource, m.EntityTitle, m.EntityStudio, m.EntityImage, m.EntityDate, m.EntityDurationSeconds, string(genresJSON))
	if err != nil {
		return fmt.Errorf("inserting matched entity %q: %w", m.EntityID, err)
	}
	return nil
}

// defaultResolvePerPage is List's page size when the caller passes a
// non-positive per-page count — matches tpdbrest.defaultBrowsePerPage's
// convention for the same reason (a sane Discover-grid-sized default).
const defaultResolvePerPage = 20

// List returns one page of cached matches for the given row type, newest
// first, optionally narrowed to entities whose genres include genreFilter.
// The genre match is a plain substring check against the JSON-encoded
// genres array's quoted entry (`"<genre>"`) rather than a SQLite JSON1
// function call — this table is a bounded per-operator cache (dozens to a
// few hundred rows, not a queryable analytics store), so a LIKE scan is
// simple, dependency-free, and fast enough at this scale; revisit only if
// this table's size assumption changes.
func (s *ReleaseStore) List(ctx context.Context, rowType RowType, genreFilter string, page, perPage int) ([]MatchedRelease, error) {
	if perPage <= 0 {
		perPage = defaultResolvePerPage
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * perPage

	query := `
		SELECT id, row_type, entity_id, entity_source, entity_title, entity_studio, entity_image, entity_date, entity_duration_seconds, genres, first_seen_at
		FROM adult_newest_releases
		WHERE row_type = ?`
	args := []any{string(rowType)}
	if genreFilter != "" {
		query += ` AND genres LIKE ?`
		args = append(args, `%"`+genreFilter+`"%`)
	}
	query += ` ORDER BY first_seen_at DESC LIMIT ? OFFSET ?`
	args = append(args, perPage, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing matched entities: %w", err)
	}
	defer rows.Close()

	out := []MatchedRelease{}
	for rows.Next() {
		var m MatchedRelease
		var rowTypeStr, genresJSON string
		if err := rows.Scan(&m.ID, &rowTypeStr, &m.EntityID, &m.EntitySource, &m.EntityTitle, &m.EntityStudio, &m.EntityImage, &m.EntityDate, &m.EntityDurationSeconds, &genresJSON, &m.FirstSeenAt); err != nil {
			return nil, fmt.Errorf("scanning matched entity: %w", err)
		}
		m.RowType = RowType(rowTypeStr)
		if err := json.Unmarshal([]byte(genresJSON), &m.Genres); err != nil {
			return nil, fmt.Errorf("decoding genres for entity %d: %w", m.ID, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DistinctGenres returns every genre name present across cached matches,
// sorted, for the AdultRowAdmin genre picker (mirrors discoversliders'
// fetchGenres reference-list pattern, but sourced from genres that actually
// exist in matched content rather than a static external taxonomy — see
// this feature's plan for why: it guarantees every filter option returns
// results, and sidesteps needing to hardcode any third-party genre list).
func (s *ReleaseStore) DistinctGenres(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT genres FROM adult_newest_releases`)
	if err != nil {
		return nil, fmt.Errorf("listing genres: %w", err)
	}
	defer rows.Close()

	set := map[string]bool{}
	for rows.Next() {
		var genresJSON string
		if err := rows.Scan(&genresJSON); err != nil {
			return nil, fmt.Errorf("scanning genres: %w", err)
		}
		var genres []string
		if err := json.Unmarshal([]byte(genresJSON), &genres); err != nil {
			return nil, fmt.Errorf("decoding genres: %w", err)
		}
		for _, g := range genres {
			set[g] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]string, 0, len(set))
	for g := range set {
		out = append(out, g)
	}
	sort.Strings(out)
	return out, nil
}

// PurgeStale deletes matched-entity rows (adult_newest_releases, by
// first_seen_at) and seen-release records (adult_newest_seen, by seen_at)
// older than before — bounds both tables' otherwise-indefinite growth and
// lets long-stale entities get freshly re-matched (current poster/tags) if
// they ever resurface, rather than showing months-old cached data forever
// (see scan.go's staleAfterMonths for the operator-directed threshold this
// is called with). Returns the number of matched-entity rows removed — the
// count more likely to matter to an operator watching Discover shrink; the
// seen-table purge count isn't returned since nothing surfaces it anywhere.
func (s *ReleaseStore) PurgeStale(ctx context.Context, before time.Time) (int64, error) {
	cutoff := before.UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM adult_newest_seen WHERE seen_at < ?`, cutoff); err != nil {
		return 0, fmt.Errorf("purging stale seen releases: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM adult_newest_releases WHERE first_seen_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purging stale matched entities: %w", err)
	}
	return res.RowsAffected()
}
