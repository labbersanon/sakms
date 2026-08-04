package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/labbersanon/sakms/internal/discoverrefresh"
)

// lookupDiscoverCache fetches (source, key) from s and delegates to
// Entry.Slice — the one shared miss/short-circuit helper behind every
// Discover read handler (discover-scheduled-refresh plan §4.6), so that
// logic exists in exactly one place instead of once per handler.
//
// Returns (items, true, 0) on a hit: serve items, make no external call.
//
// Returns (nil, false, liveRawPage) to mean "fall through to the live path".
// liveRawPage > 0 is the upstream page the caller MUST request instead of
// the one the client asked for (see Entry.Slice's doc comment); liveRawPage
// == 0 means "no opinion — use the requested page unchanged" and covers a
// nil store, a missing row, a store error, and a payload that fails to
// decode. Every one of those is logged and treated as a miss: a cache fault
// must degrade to today's live behaviour, never to an HTTP error (§4.0).
//
// perPage is deliberately NOT a parameter — it is the row's own
// Entry.PageSize. A caller that could pass its own page size could pass the
// wrong one, which is exactly the mixed-slider defect this plan's first
// revision shipped once already.
func lookupDiscoverCache(ctx context.Context, s *discoverrefresh.Store, source, key string, page int) (items []json.RawMessage, hit bool, liveRawPage int) {
	if s == nil {
		return nil, false, 0
	}
	entry, err := s.Get(ctx, source, key)
	if err != nil {
		if !errors.Is(err, discoverrefresh.ErrNotFound) {
			log.Printf("api: discover cache lookup for %s/%s failed, falling through to live: %v", source, key, err)
		}
		return nil, false, 0
	}
	return entry.Slice(page)
}

// writeRawJSONArray serves a cache hit's items verbatim, byte-transparent for
// each item body (they are re-emitted exactly as stored, never decoded into a
// typed slice and re-encoded — see discoverrefresh.Entry's doc comment).
//
// It must emit a trailing "\n" after the closing bracket, matching every live
// encoder's json.NewEncoder(...).Encode(...) (Critic finding H1) — omitting it
// would make a cached response one byte different from the live one it
// replaces.
func writeRawJSONArray(w http.ResponseWriter, items []json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte("["))
	for i, item := range items {
		if i > 0 {
			w.Write([]byte(","))
		}
		w.Write(item)
	}
	w.Write([]byte("]\n"))
}

// Claude 2026-08-03: added the three invalidation helpers below (BE-16,
// discover-scheduled-refresh plan §5.3).
// Reason: five lifecycle-hook call sites (slider update/delete, Trakt
// disconnect, tmdb connection upsert/delete) all need the same
// "delete a cache row/source, log a failure, never fail the operator's
// request" shape lookupDiscoverCache already established for reads —
// centralizing it here keeps that posture in exactly one place instead of
// five copies that could drift.
// Troubleshooting: a cache-cleanup failure here is ALWAYS non-fatal to the
// caller; the per-cycle DeleteOrphanSliders sweep (or, for a pinned single
// key like trakt, the next successful refresh) is the backstop.
// Review if: a future hook needs the cleanup failure surfaced to the
// operator — none of today's five do.

// invalidateDiscoverCache deletes one (source, key) row after a mutation
// makes its cached content wrong — slider update (before the repopulate)
// and slider delete both call this with source "slider"; Trakt disconnect
// calls it with source "trakt", key "". s may be nil (no cache configured);
// a nil store or a delete error is logged and otherwise ignored.
func invalidateDiscoverCache(ctx context.Context, s *discoverrefresh.Store, source, key string) {
	if s == nil {
		return
	}
	if err := s.Delete(ctx, source, key); err != nil {
		log.Printf("api: invalidating discover cache %s/%s: %v", source, key, err)
	}
}

// invalidateDiscoverCacheSource deletes every row for one source — the
// connection-change invalidation (plan §5.3's fourth lifecycle gap): the
// tmdb connection backs BOTH the "tmdb" and "slider" sources, so an upsert
// or delete of it clears both. Same never-fail-the-request posture as
// invalidateDiscoverCache.
func invalidateDiscoverCacheSource(ctx context.Context, s *discoverrefresh.Store, source string) {
	if s == nil {
		return
	}
	if err := s.DeleteBySource(ctx, source); err != nil {
		log.Printf("api: invalidating discover cache source %q: %v", source, err)
	}
}

// invalidateDiscoverCacheForConnectionChange is upsertConnectionHandler/
// deleteConnectionHandler's post-mutation hook (plan §5.3). Only "tmdb" is
// wired: it is the one credential that backs both the "tmdb" source
// (Mainstream's six fixed rows) and "slider" (every custom slider resolves
// through the same TMDB client), so re-keying or removing it must clear
// both — otherwise the previous credential's cached content would keep
// serving for up to the refresh interval.
//
// stashdb/fansdb are deliberately NOT handled: the StashDB/FansDB catalog
// rows ("stashbox" source) were retired before this task landed (see
// internal/discoverrefresh/consts.go's package doc) — there is nothing left
// for a stashdb/fansdb credential change to invalidate.
func invalidateDiscoverCacheForConnectionChange(ctx context.Context, s *discoverrefresh.Store, service string) {
	if service != "tmdb" {
		return
	}
	invalidateDiscoverCacheSource(ctx, s, "tmdb")
	invalidateDiscoverCacheSource(ctx, s, "slider")
}
