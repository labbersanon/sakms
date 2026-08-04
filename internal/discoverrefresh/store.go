package discoverrefresh

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is returned by Get when no row exists for (source, cacheKey).
//
// It is the SUCCESS case for a cold install, not a fault: the read path treats
// it as a miss and falls through to the existing live handler, which is what
// makes this cache purely additive (see the package doc).
var ErrNotFound = errors.New("discoverrefresh: no cache row for that key")

// timeLayout is the format every timestamp column in discover_row_cache is
// written and read in: RFC3339, UTC, fixed width.
//
// Deliberately NOT RFC3339Nano. That layout strips trailing zeros, so
// "…:05Z" and "…:05.5Z" do not compare lexicographically in the order they
// occurred ('.' sorts before 'Z'). The due-check that decides whether a key
// needs refreshing compares these values, so a sortable fixed-width layout is
// load-bearing rather than cosmetic. Matches internal/trakt/store.go,
// internal/nodesettings and internal/nodekeys.
const timeLayout = time.RFC3339

// Store persists one cache row per (source, cache_key) in discover_row_cache
// (migration 0058).
//
// Every method is safe to call on a *Store built over any *sql.DB; none holds
// state between calls.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Entry is one cached row — the ten migration columns, one field each.
//
// Payload is decoded ONCE, by Get, so Slice never re-parses. Keeping the items
// as []json.RawMessage rather than a typed slice is what makes the cached
// response byte-identical to the live one: the read path re-emits the stored
// item bodies verbatim rather than round-tripping them through a Go type whose
// field order or omitempty rules could drift from the live handler's.
type Entry struct {
	Source   string
	CacheKey string
	Payload  []json.RawMessage
	// ItemCount is len(Payload) at write time, denormalized purely so the
	// manual-refresh UI and logs can report "cached N items" without decoding
	// the blob.
	ItemCount int
	// PageSize is this row's LOGICAL page size — 20 for TMDB and stash-box, 40
	// for a mixed fixed-feed slider, max(len(payload), 1) for the unpaginated
	// Trakt watchlist. Stored per row rather than derived at read time so the
	// read path cannot re-derive it wrongly (slicing a mixed slider's flat list
	// at 20 corrupts every page after the first).
	PageSize int
	// RawPages is how many RAW UPSTREAM pages were consumed to build Payload.
	// Not the same as len(Payload)/PageSize once the US-release filter drops
	// items — this is what lets a request past the cached window ask the live
	// path for the correct NEXT upstream page instead of re-fetching one whose
	// survivors are already cached. See Slice.
	RawPages int
	// Exhausted is true only when accumulation stopped because the upstream
	// itself returned an empty (or short) page — genuine end of data. A run
	// that merely hit its depth target is NOT exhausted, and Slice must not
	// report it as such.
	Exhausted bool
	// RefreshedAt moves ONLY on a fully successful refresh; AttemptedAt moves
	// on every attempt; LastError holds the most recent failure and is cleared
	// on success. All three are timeLayout-formatted, or "" if never set.
	RefreshedAt string
	AttemptedAt string
	LastError   string
}

// Get returns the row for (source, cacheKey), or ErrNotFound when absent.
//
// A payload that fails to decode is returned as an error, not as an empty
// Entry: the read path treats any error here as a miss and falls through live,
// so a corrupt blob degrades to today's behaviour rather than serving nothing.
func (s *Store) Get(ctx context.Context, source, cacheKey string) (*Entry, error) {
	e := &Entry{Source: source, CacheKey: cacheKey}
	var (
		payload   string
		exhausted int
	)
	row := s.db.QueryRowContext(ctx, `
		SELECT payload, item_count, page_size, raw_pages, exhausted, refreshed_at, attempted_at, last_error
		FROM discover_row_cache
		WHERE source = ? AND cache_key = ?
	`, source, cacheKey)
	if err := row.Scan(&payload, &e.ItemCount, &e.PageSize, &e.RawPages, &exhausted,
		&e.RefreshedAt, &e.AttemptedAt, &e.LastError); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("loading discover cache row %s/%s: %w", source, cacheKey, err)
	}
	e.Exhausted = exhausted != 0
	if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
		return nil, fmt.Errorf("decoding discover cache payload %s/%s: %w", source, cacheKey, err)
	}
	return e, nil
}

// Put upserts the row for (source, cacheKey) with a fully accumulated payload.
//
// CALL ONLY ON A FULLY SUCCESSFUL REFRESH. It stamps refreshed_at, so calling
// it with a partial or empty result on an error path is what would turn
// stale-on-failure into blank-on-failure. MarkFailure is the error path.
//
// itemCount is always len(payload) — it is not a parameter, so the column
// cannot disagree with the blob.
//
// Put is deliberately SOURCE-AGNOSTIC. The "never cache an empty list" rule
// applies to the slider source ONLY (a slider's fetchFixedFeed returns a nil
// slice that encodes as null, where every other source encodes []), and it is
// enforced in refreshSliders, its one caller. Moving that check in here would
// silently apply it to all four sources — which would mean an operator with an
// empty Trakt watchlist got a live Trakt call on every Discover load, forever.
func (s *Store) Put(ctx context.Context, source, cacheKey string, payload []json.RawMessage,
	pageSize, rawPages int, exhausted bool) error {
	if payload == nil {
		// json.Marshal of a nil slice is "null"; the column contract is a JSON
		// array. This is the encoder-level guard, not the slider rule above.
		payload = []json.RawMessage{}
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding discover cache payload %s/%s: %w", source, cacheKey, err)
	}
	now := time.Now().UTC().Format(timeLayout)
	exhaustedInt := 0
	if exhausted {
		exhaustedInt = 1
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO discover_row_cache
			(source, cache_key, payload, item_count, page_size, raw_pages, exhausted,
			 refreshed_at, attempted_at, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '')
		ON CONFLICT(source, cache_key) DO UPDATE SET
			payload      = excluded.payload,
			item_count   = excluded.item_count,
			page_size    = excluded.page_size,
			raw_pages    = excluded.raw_pages,
			exhausted    = excluded.exhausted,
			refreshed_at = excluded.refreshed_at,
			attempted_at = excluded.attempted_at,
			last_error   = ''
	`, source, cacheKey, string(blob), len(payload), pageSize, rawPages, exhaustedInt, now, now)
	if err != nil {
		return fmt.Errorf("saving discover cache row %s/%s: %w", source, cacheKey, err)
	}
	return nil
}

// MarkFailure records a failed refresh attempt against an EXISTING row.
//
// IT IS A BARE UPDATE AND MUST NEVER BECOME AN UPSERT. That asymmetry with Put
// is the single most important behavioural rule in this package, and two
// properties rest on it:
//
//  1. Stale-but-available. A refresh that fails after a row exists cannot blank
//     it — payload, item_count and refreshed_at are untouched, so the read path
//     keeps serving the previous content (it never inspects last_error).
//  2. Cold-miss fall-through. A key that has NEVER succeeded has NO ROW AT ALL,
//     so the read path falls through to the live handler instead of serving a
//     phantom empty cache entry. An upsert here would silently create exactly
//     that row and break the guarantee for every source.
//
// Zero rows affected is a no-op, not an error: "nothing to mark" is the normal
// outcome on a cold key and must not be logged as a fault.
func (s *Store) MarkFailure(ctx context.Context, source, cacheKey string, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE discover_row_cache
		SET attempted_at = ?, last_error = ?
		WHERE source = ? AND cache_key = ?
	`, time.Now().UTC().Format(timeLayout), msg, source, cacheKey)
	if err != nil {
		return fmt.Errorf("marking discover cache failure %s/%s: %w", source, cacheKey, err)
	}
	return nil
}

// Delete removes one row. Idempotent — deleting an absent row is not an error.
func (s *Store) Delete(ctx context.Context, source, cacheKey string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM discover_row_cache WHERE source = ? AND cache_key = ?`, source, cacheKey)
	if err != nil {
		return fmt.Errorf("deleting discover cache row %s/%s: %w", source, cacheKey, err)
	}
	return nil
}

// DeleteAll purges the WHOLE table, every source.
//
// Its one caller is the interval-off path: an operator who sets the refresh
// interval to 0 must get LIVE behaviour back, not the last payload frozen
// forever with no refresh path and (by design) no staleness indicator to make
// that visible. Run purges both at boot and on a live retune to 0.
//
// Deliberately takes no source — DeleteBySource is the per-source method.
func (s *Store) DeleteAll(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM discover_row_cache`); err != nil {
		return fmt.Errorf("purging discover cache: %w", err)
	}
	return nil
}

// DeleteBySource removes every row for one source.
//
// Its callers are the connection-change invalidations: re-keying or removing
// the tmdb connection clears "tmdb" AND "slider" (one credential produced
// both), and re-keying stashdb/fansdb clears "stashbox". Coarse rather than
// per-key on purpose — a connection change invalidates everything that
// credential produced, and it happens rarely enough that precision buys
// nothing.
func (s *Store) DeleteBySource(ctx context.Context, source string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM discover_row_cache WHERE source = ?`, source); err != nil {
		return fmt.Errorf("deleting discover cache source %q: %w", source, err)
	}
	return nil
}

// DeleteOrphanSliders removes every source='slider' row whose key is not one of
// keepIDs — the currently-enabled slider ids.
//
// This is the per-cycle BACKSTOP, not the primary invalidation mechanism: the
// delete/update handlers remove a row synchronously. It catches rows orphaned
// by a delete whose cache cleanup failed, by a direct DB edit, or by a slider
// disabled before this feature shipped. Migration
// 0051_delete_orphan_adult_performers_studios.sql is the in-repo precedent for
// treating orphan cache rows as a real defect rather than harmless clutter.
//
// An EMPTY keepIDs means every slider row is an orphan (every slider deleted or
// disabled) and takes its own statement — "NOT IN ()" is a SQLite syntax error,
// so the obvious single-query shape would fail exactly when there is most to
// clean up.
func (s *Store) DeleteOrphanSliders(ctx context.Context, keepIDs []int) error {
	if len(keepIDs) == 0 {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM discover_row_cache WHERE source = 'slider'`); err != nil {
			return fmt.Errorf("sweeping orphan slider cache rows: %w", err)
		}
		return nil
	}
	// cache_key holds the slider's integer id as a decimal string; strconv.Itoa
	// is the same conversion the write path uses, so both agree on the key.
	args := make([]any, 0, len(keepIDs))
	for _, id := range keepIDs {
		args = append(args, strconv.Itoa(id))
	}
	placeholders := strings.Repeat(",?", len(keepIDs))[1:]
	query := `DELETE FROM discover_row_cache WHERE source = 'slider' AND cache_key NOT IN (` + placeholders + `)`
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("sweeping orphan slider cache rows: %w", err)
	}
	return nil
}

// Slice maps a requested LOGICAL page onto this row's flat accumulated list.
//
// It is a method on Entry, not on Store, precisely so it cannot be called
// without the row's own PageSize/RawPages/Exhausted in hand. perPage is
// deliberately NOT a parameter: a caller that could supply a page size could
// supply the wrong one, which is exactly the mixed-slider defect that shipped
// once already.
//
// Returns:
//
//	(items, true, 0)          — HIT. Serve items, make no external call. The
//	                            items slice may be empty (see the exhausted
//	                            branch below); empty-and-hit is still zero
//	                            external calls, which is the point.
//	(nil, false, liveRawPage) — MISS. Fall through to the live path. When
//	                            liveRawPage > 0 the caller MUST request THAT
//	                            upstream page, not the page the client asked
//	                            for. liveRawPage == 0 means "no opinion — use
//	                            the requested page unchanged", i.e. exactly
//	                            today's behaviour.
//
// The cached window is ceil(len(Payload)/PageSize) pages wide — the row's OWN
// width, which may EXCEED carouselCachedPages when the US-release filter
// over-delivered. Never gate on the depth constants here.
//
// Past the window, Exhausted decides: an exhausted row serves [] (the upstream
// genuinely has no more, so the frontend marks the row done rather than firing
// a pointless live call — this branch is also what serves an empty trakt or
// stashbox row, where cachedPages is 0 and even page 1 lands here). A
// non-exhausted row falls through asking for RawPages + (page - cachedPages).
// That rewrite is the whole reason RawPages is stored: because filtering drops
// items, cached page N does not correspond to upstream page N, so falling
// through at the requested page would re-fetch an upstream page whose survivors
// are already cached. For sources that filter nothing RawPages == cachedPages
// and it reduces to page, i.e. today's behaviour exactly.
func (e *Entry) Slice(page int) (items []json.RawMessage, hit bool, liveRawPage int) {
	// A nil Entry, a page below 1, or a page_size of 0 are all treated as a
	// miss with no opinion rather than normalized. page_size 0 is reachable
	// only by direct DB tampering (every write path passes a positive value)
	// and dividing by it would panic; a non-positive page would make the low
	// bound negative and panic on the slice expression. Four independent
	// handlers call this, and only some of them normalize page beforehand.
	if e == nil || page < 1 || e.PageSize <= 0 {
		return nil, false, 0
	}

	perPage := e.PageSize
	total := len(e.Payload)
	cachedPages := (total + perPage - 1) / perPage

	if page <= cachedPages {
		// BOTH bounds are clamped. Clamping only the high bound is not
		// defensive padding that was skipped — it panics: a 37-item list at
		// perPage=20 evaluates page 3 to payload[40:37], and Go requires
		// 0 <= low <= high. (page 3 cannot reach here at cachedPages=2, but
		// the clamp is what makes that true by construction rather than by
		// the caller getting the window arithmetic right.)
		lo := min((page-1)*perPage, total)
		hi := min(page*perPage, total)
		return e.Payload[lo:hi], true, 0
	}

	if e.Exhausted {
		return []json.RawMessage{}, true, 0
	}

	return nil, false, e.RawPages + (page - cachedPages)
}
