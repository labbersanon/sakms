-- +goose Up
-- Background-populated read cache for the four Discover row categories that
-- previously made a live external API call on every render (internal/
-- discoverrefresh). One row per cached row-source, holding that row's ALREADY
-- PAGINATED-FLAT item list as an opaque JSON blob.
--
--   * source     — 'tmdb' | 'slider' | 'trakt' | 'stashbox'; the discriminator
--     that lets one table back four unrelated upstreams, mirroring
--     adult_newest_releases' row_type precedent and adult_merged_row_cache's
--     one-table-many-rows shape.
--   * cache_key  — the per-source identity: '{mode}:{category}' for tmdb, the
--     slider id for slider, '' for trakt (one connection, single-operator),
--     '{box}:{kind}' for stashbox.
--   * payload    — the accumulated item list serialized whole, in the EXACT
--     wire shape the read handler already emits. Never queried into (the read
--     path fetches the blob and slices it in Go), so a blob column is correct
--     here, not a typed-column violation — the same judgement
--     0045_adult_merged_row_cache.sql recorded for the same reason.
--   * item_count — len(payload) at write time. Denormalized purely so the
--     manual-refresh UI and logs can report "cached N items" without decoding
--     the blob.
--   * page_size  — this row's LOGICAL page size, which is NOT one constant
--     across sources. 20 everywhere EXCEPT a mixed-target slider whose
--     filter_type has both a movie and a TV sibling (upcoming/trending/popular/
--     genre/keyword), which is 40 because fetchFixedFeed concatenates a movie
--     page and a TV page into one logical page. A mixed STUDIO or NETWORK
--     slider is 20, not 40: resolveSlider deliberately degrades those to their
--     one applicable catalog rather than erroring (internal/api/
--     discover_sliders.go:246-249, :281-298). For trakt, which is unpaginated,
--     it is max(len(payload), 1) so that one logical page is the whole list.
--     See internal/discoverrefresh's sliderPageSize. Stored per row rather than
--     derived at read time so the read path cannot re-derive it wrongly.
--   * raw_pages  — how many RAW UPSTREAM pages were consumed to build payload.
--     Not the same as len(payload)/page_size once the US-release filter drops
--     items. This is what lets a request past the cached window ask the live
--     path for the correct NEXT upstream page instead of re-fetching one whose
--     survivors are already in payload.
--   * exhausted  — 1 when accumulation stopped because the upstream returned an
--     empty page (genuine end of data) rather than because the depth target was
--     met. Distinguishes "serve [] and let the row report itself exhausted"
--     from "fall through to live for more".
--   * refreshed_at / attempted_at / last_error — the stale-on-failure triple.
--     refreshed_at moves ONLY on success; attempted_at moves on every attempt;
--     last_error holds the most recent failure and is cleared on success.
--
-- DELIBERATELY NO page COLUMN. The upstreams paginate differently (TMDB 20/page,
-- stash-box 20/page, Trakt not at all) and the frontend's own page counter does
-- not correspond 1:1 to raw upstream pages once the US-release filter drops
-- items (see internal/api/discover.go's ACCEPTED LIMITATION note). Storing one
-- flat accumulated list and slicing it on read makes the cached window
-- internally consistent by construction, unifies all four sources on one
-- schema, and makes a partial-failure write impossible (a refresh either
-- replaces the whole list or leaves the previous one untouched).
--
-- Purely additive: no existing table is altered or dropped. An empty table is
-- not a regression — every read handler falls through to its existing live path
-- on a miss (see 0045's identical contract).
CREATE TABLE discover_row_cache (
    source       TEXT    NOT NULL,
    cache_key    TEXT    NOT NULL,
    payload      TEXT    NOT NULL DEFAULT '[]',
    item_count   INTEGER NOT NULL DEFAULT 0,
    page_size    INTEGER NOT NULL DEFAULT 20,
    raw_pages    INTEGER NOT NULL DEFAULT 0,
    exhausted    INTEGER NOT NULL DEFAULT 0,
    refreshed_at TEXT    NOT NULL DEFAULT '',
    attempted_at TEXT    NOT NULL DEFAULT '',
    last_error   TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (source, cache_key)
);

-- No secondary index: the table holds ~15 rows on a busy install and every
-- access is by full primary key or a WHERE source = ? scan. Adding one would be
-- premature.

-- +goose Down
DROP TABLE discover_row_cache;
