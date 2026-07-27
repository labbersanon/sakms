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
	// FirstSeenReleaseTitle is the raw Prowlarr release title
	// (prowlarr.Release.Title) that first matched this entity — reused as
	// the Grab-time Prowlarr search query instead of reconstructing one from
	// TPDB's own studio+title metadata, which includes tokens (e.g. TPDB's
	// "S6:E10" episode notation) real indexer release filenames never
	// contain. Empty for Studio/Performer rows (no associated Grab) and for
	// entities matched before this field existed.
	FirstSeenReleaseTitle string
	Genres                []string
	// Performers is the matched entity's own performer names — see
	// identify.MatchResult.Performers' doc comment for sourcing (the box
	// scene's own performer list, not an AI filename-parse guess). Same
	// JSON-array-encoded TEXT column convention as Genres. Empty for
	// Studio/Performer rows and for entities matched before this field
	// existed.
	Performers []string
	// DownloadURL is the feed enclosure URL for a feed-sourced entity (empty for
	// a browse-only entity). When present, Adult Discover grabs directly via
	// dispatchToDownloadClient, skipping the Prowlarr search entirely (D4). It is
	// exposed to a card only while the row's feed is fresh (see
	// FeedHealth.DirectGrabURL); it is never cleared when the feed goes unhealthy.
	DownloadURL string
	// DownloadProtocol is the feed's admin-set protocol ("torrent" | "usenet")
	// for a feed enclosure, threaded into the direct-grab dispatch.
	DownloadProtocol string
	// SizeBytes is the feed enclosure's byte length (0 if absent/unknown).
	SizeBytes int64
	// BrowseConfirmed is the VISIBILITY fact: the cat-6000 browse pass currently
	// confirms this entity, independent of every feed. Set true by the browse
	// pass (and for Studio/Performer entities, which are never feed-gated); never
	// cleared by the feed pass. A row is shown iff BrowseConfirmed OR its feed is
	// fresh (see FeedHealth.Available).
	BrowseConfirmed bool
	// FeedID is the GRAB-PATH fact: which feed (rss_feeds.id) offers a direct-grab
	// enclosure for this entity, or 0 for "no feed source." Selects which
	// feed-health entry the row is gated against.
	FeedID int64
	// FeedItemKey is the row's stable identity within that feed (D6: enclosure
	// URL, else link, else a hash of feed id + title) — the same derivation as
	// the per-poll presence key set, so a row and the poll that confirms it
	// always agree on the key (used by RefreshLastConfirmedSeen).
	FeedItemKey string
	// LastConfirmedSeen is the persisted TTL clock: the Unix timestamp (seconds)
	// of the last successful poll that confirmed this feed enclosure present. 0 =
	// never confirmed in a poll (correct for browse-only rows). Read at request
	// time by FeedHealth.feedFresh.
	LastConfirmedSeen int64
	FirstSeenAt       string
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

// Insert writes one matched entity to the cache, merging two orthogonal facts
// independently on a (row_type, entity_source, entity_id) conflict (M1):
//
//   - browse_confirmed = MAX(existing, incoming) — a boolean OR: a feed insert
//     (browse_confirmed=0) never clears an existing browse confirmation, and a
//     browse insert (1) raises it. Visibility is preserved regardless of order.
//   - the enclosure (download_url/protocol/size/feed_id/feed_item_key/
//     last_confirmed_seen) is adopted ONLY onto a row that has none yet
//     (existing download_url is empty and incoming is not) — first-feed-wins: a
//     browse insert (whose download_url is empty) never overwrites a feed's enclosure, and a
//     second feed never downgrades the first feed's enclosure.
//
// Identity-stable metadata (title/studio/image/genres/performers) keeps the
// first-writer-wins contract — it is intentionally NOT in the SET clause, so it
// is preserved on conflict. This closes M1 (a feed match can no longer be
// silently dropped by a pre-cached browse row) without letting a browse match
// steal a feed row's grab path.
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
	performers := m.Performers
	if performers == nil {
		performers = []string{} // same "never store JSON null" convention as genres above
	}
	performersJSON, err := json.Marshal(performers)
	if err != nil {
		return fmt.Errorf("encoding performers for entity %q: %w", m.EntityID, err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO adult_newest_releases
			(row_type, entity_id, entity_source, entity_title, entity_studio, entity_image, entity_date,
			 entity_duration_seconds, first_seen_release_title, genres, performers,
			 download_url, download_protocol, size_bytes, browse_confirmed, feed_id, feed_item_key, last_confirmed_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(row_type, entity_source, entity_id) DO UPDATE SET
			browse_confirmed  = MAX(adult_newest_releases.browse_confirmed, excluded.browse_confirmed),
			download_url      = CASE WHEN adult_newest_releases.download_url = '' AND excluded.download_url != ''
			                    THEN excluded.download_url      ELSE adult_newest_releases.download_url      END,
			download_protocol = CASE WHEN adult_newest_releases.download_url = '' AND excluded.download_url != ''
			                    THEN excluded.download_protocol ELSE adult_newest_releases.download_protocol END,
			size_bytes        = CASE WHEN adult_newest_releases.download_url = '' AND excluded.download_url != ''
			                    THEN excluded.size_bytes        ELSE adult_newest_releases.size_bytes        END,
			feed_id           = CASE WHEN adult_newest_releases.download_url = '' AND excluded.download_url != ''
			                    THEN excluded.feed_id           ELSE adult_newest_releases.feed_id           END,
			feed_item_key     = CASE WHEN adult_newest_releases.download_url = '' AND excluded.download_url != ''
			                    THEN excluded.feed_item_key     ELSE adult_newest_releases.feed_item_key     END,
			last_confirmed_seen = CASE WHEN adult_newest_releases.download_url = '' AND excluded.download_url != ''
			                    THEN excluded.last_confirmed_seen ELSE adult_newest_releases.last_confirmed_seen END
	`, string(m.RowType), m.EntityID, m.EntitySource, m.EntityTitle, m.EntityStudio, m.EntityImage, m.EntityDate,
		m.EntityDurationSeconds, m.FirstSeenReleaseTitle, string(genresJSON), string(performersJSON),
		m.DownloadURL, m.DownloadProtocol, m.SizeBytes, m.BrowseConfirmed, m.FeedID, m.FeedItemKey, m.LastConfirmedSeen)
	if err != nil {
		return fmt.Errorf("inserting matched entity %q: %w", m.EntityID, err)
	}
	return nil
}

// RefreshLastConfirmedSeen advances the persisted TTL clock (last_confirmed_seen)
// to nowUnix for every already-cached row of feedID whose feed_item_key is in
// keys — writer (ii) of the two last-seen writers (D3). It is run once per
// successful poll over the FULL current window (every item the feed returned,
// not just the newly-identified ones), so a continuously-listed item's TTL never
// lapses while its feed is healthy — including a re-seen item the Insert upsert
// no-ops on and an item skipped by the identify budget. A no-op for empty keys.
func (s *ReleaseStore) RefreshLastConfirmedSeen(ctx context.Context, feedID int64, keys []string, nowUnix int64) error {
	if len(keys) == 0 {
		return nil
	}
	placeholders := make([]byte, 0, len(keys)*2)
	args := make([]any, 0, len(keys)+2)
	args = append(args, nowUnix, feedID)
	for i, k := range keys {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, k)
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		UPDATE adult_newest_releases SET last_confirmed_seen = ?
		WHERE feed_id = ? AND feed_item_key IN (%s)
	`, placeholders), args...)
	if err != nil {
		return fmt.Errorf("refreshing last_confirmed_seen for feed %d: %w", feedID, err)
	}
	return nil
}

// defaultResolvePerPage is List's page size when the caller passes a
// non-positive per-page count — matches tpdbrest.defaultBrowsePerPage's
// convention for the same reason (a sane Discover-grid-sized default).
const defaultResolvePerPage = 20

// releaseColumns is the full SELECT column list shared by List/SearchScenes/
// ListRecentScenes so every read scans the same shape via scanRelease — one
// place to keep in sync with the schema (and with scanRelease's Scan order).
const releaseColumns = `id, row_type, entity_id, entity_source, entity_title, entity_studio, entity_image, entity_date, entity_duration_seconds, first_seen_release_title, genres, performers, download_url, download_protocol, size_bytes, browse_confirmed, feed_id, feed_item_key, last_confirmed_seen, first_seen_at`

// scanRelease decodes one row selected via releaseColumns into a MatchedRelease,
// unmarshalling the JSON-encoded genres/performers arrays. Column order here
// must match releaseColumns exactly.
func scanRelease(rows *sql.Rows) (MatchedRelease, error) {
	var m MatchedRelease
	var rowTypeStr, genresJSON, performersJSON string
	if err := rows.Scan(&m.ID, &rowTypeStr, &m.EntityID, &m.EntitySource, &m.EntityTitle, &m.EntityStudio,
		&m.EntityImage, &m.EntityDate, &m.EntityDurationSeconds, &m.FirstSeenReleaseTitle, &genresJSON, &performersJSON,
		&m.DownloadURL, &m.DownloadProtocol, &m.SizeBytes, &m.BrowseConfirmed, &m.FeedID, &m.FeedItemKey,
		&m.LastConfirmedSeen, &m.FirstSeenAt); err != nil {
		return MatchedRelease{}, fmt.Errorf("scanning matched entity: %w", err)
	}
	m.RowType = RowType(rowTypeStr)
	if err := json.Unmarshal([]byte(genresJSON), &m.Genres); err != nil {
		return MatchedRelease{}, fmt.Errorf("decoding genres for entity %d: %w", m.ID, err)
	}
	if err := json.Unmarshal([]byte(performersJSON), &m.Performers); err != nil {
		return MatchedRelease{}, fmt.Errorf("decoding performers for entity %d: %w", m.ID, err)
	}
	return m, nil
}

// ByFeedItemKeys returns the cached matched entities whose feed_item_key is in
// keys, mapped by feed_item_key — the join the Adult RSS feed resolve handler
// (internal/api/rss_feeds.go) uses to enrich a live feed item with the identify
// pipeline's already-resolved poster/title/studio for the same enclosure.
// Batched into one IN query (same shape as SeenGUIDs) rather than a lookup per
// item. NOT scoped by feed_id: a feed_item_key IS the enclosure URL (D6), which
// globally identifies the release, so a cross-feed duplicate still resolves.
// first_seen_at DESC, id DESC + first-wins collapses the case where one key
// maps to more than one entity row (a real, reachable state — e.g. the same
// enclosure identified as both a scene and a performer/studio row, or matched
// independently by two different feeds; feed_item_key is NOT the table's
// uniqueness key, (row_type, entity_source, entity_id) is) onto the newest
// match, with id as a strict tie-breaker so the result is a total order even
// when first_seen_at collides. A no-op for empty keys; browse-only rows
// (whose feed_item_key is empty) never match a non-empty enclosure key.
func (s *ReleaseStore) ByFeedItemKeys(ctx context.Context, keys []string) (map[string]MatchedRelease, error) {
	out := make(map[string]MatchedRelease, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	placeholders := make([]byte, 0, len(keys)*2)
	args := make([]any, len(keys))
	for i, k := range keys {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = k
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT `+releaseColumns+`
		FROM adult_newest_releases
		WHERE feed_item_key IN (%s)
		ORDER BY first_seen_at DESC, id DESC`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("looking up matched entities by feed item key: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		m, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		if _, ok := out[m.FeedItemKey]; !ok {
			out[m.FeedItemKey] = m
		}
	}
	return out, rows.Err()
}

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

	query := `SELECT ` + releaseColumns + `
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
		m, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// sceneRowTypes is the row_type set the pooled scene listings (SearchScenes,
// ListRecentScenes) draw from — grabbable scene/movie entities, never
// Studio/Performer aggregates (those have no enclosure and aren't a "scene").
var sceneRowTypes = []any{string(RowScene), string(RowMovie)}

// SearchScenes returns one page of cached scene/movie entities whose title
// matches q (case-insensitive substring), newest-first. This is Adult search
// re-pointed off a live TPDB call and onto the identified-available pool (D4b):
// a title LIKE over the cache works regardless of a pooled entity's source
// (TPDB/StashDB/FansDB), which an id-based TPDB cross-reference could not. The
// caller applies the D5 visibility gate + DirectGrabURL exposure to the result.
func (s *ReleaseStore) SearchScenes(ctx context.Context, q string, page int) ([]MatchedRelease, error) {
	if page <= 0 {
		page = 1
	}
	perPage := defaultResolvePerPage
	offset := (page - 1) * perPage

	args := append([]any{}, sceneRowTypes...)
	args = append(args, `%`+q+`%`, perPage, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+releaseColumns+`
		FROM adult_newest_releases
		WHERE row_type IN (?, ?) AND entity_title LIKE ? COLLATE NOCASE
		ORDER BY first_seen_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("searching pooled scenes: %w", err)
	}
	defer rows.Close()

	out := []MatchedRelease{}
	for rows.Next() {
		m, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListRecentScenes returns one page of cached scene/movie entities ordered by
// the entity's own release date descending (unknown/blank dates sort last) —
// the pooled replacement for the live TPDB+StashDB "Recently Released" merge
// (adultDiscoverMergedRecentHandler). The caller applies the D5 gate + exposure.
func (s *ReleaseStore) ListRecentScenes(ctx context.Context, page int) ([]MatchedRelease, error) {
	if page <= 0 {
		page = 1
	}
	perPage := defaultResolvePerPage
	offset := (page - 1) * perPage

	args := append([]any{}, sceneRowTypes...)
	args = append(args, perPage, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+releaseColumns+`
		FROM adult_newest_releases
		WHERE row_type IN (?, ?)
		ORDER BY entity_date = '' ASC, entity_date DESC, first_seen_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("listing recent pooled scenes: %w", err)
	}
	defer rows.Close()

	out := []MatchedRelease{}
	for rows.Next() {
		m, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SceneCredit is the subset of a pooled Scene/Movie row's fields relevant to
// building a "currently grabbable" performer/studio name-set (see
// internal/api/adultdiscover_merge.go's availability hard filter) — a
// deliberately lean shape (5 columns, not the full 20-column MatchedRelease)
// since this feeds a per-request rollup, not a display list.
type SceneCredit struct {
	Performers        []string
	Studio            string
	BrowseConfirmed   bool
	FeedID            int64
	LastConfirmedSeen int64
}

// ScenePoolCredits returns every pooled Scene/Movie row's credit-relevant
// fields, unpaginated — the pool is small by design (bounded scan cadence,
// 6-month purge, see scan.go), so a full unpaginated read here is the
// correct scale, unlike ListRecentScenes/SearchScenes which page for
// display. Deliberately excludes Performer/Studio rows (row_type
// RowPerformer/RowStudio): those are written BrowseConfirmed=true
// unconditionally (scan.go), so including them would silently bypass the
// FeedHealth-gated "grabbable" signal this method exists to provide — the
// caller applies FeedHealth.Available itself, this method only returns raw
// per-row facts.
func (s *ReleaseStore) ScenePoolCredits(ctx context.Context) ([]SceneCredit, error) {
	args := append([]any{}, sceneRowTypes...)
	rows, err := s.db.QueryContext(ctx, `SELECT entity_studio, performers, browse_confirmed, feed_id, last_confirmed_seen
		FROM adult_newest_releases
		WHERE row_type IN (?, ?)`, args...)
	if err != nil {
		return nil, fmt.Errorf("listing scene pool credits: %w", err)
	}
	defer rows.Close()

	out := []SceneCredit{}
	for rows.Next() {
		var c SceneCredit
		var performersJSON string
		var browseConfirmed int
		if err := rows.Scan(&c.Studio, &performersJSON, &browseConfirmed, &c.FeedID, &c.LastConfirmedSeen); err != nil {
			return nil, fmt.Errorf("scanning scene pool credit: %w", err)
		}
		c.BrowseConfirmed = browseConfirmed != 0
		if performersJSON != "" {
			if err := json.Unmarshal([]byte(performersJSON), &c.Performers); err != nil {
				return nil, fmt.Errorf("decoding performers for scene pool credit: %w", err)
			}
		}
		out = append(out, c)
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
