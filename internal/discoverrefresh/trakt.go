package discoverrefresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/trakt"
)

// sourceTrakt is this source's value in discover_row_cache's source column.
const sourceTrakt = "trakt"

// traktCacheKey is the ONE key this source ever writes: the empty string.
//
// Trakt's watchlist is a single account-wide list with no per-key dimension —
// no mode, no category, no id — so there is nothing for a key to name. It is
// deliberately "" rather than a descriptive "watchlist": the read path
// (traktWatchlistHandler) looks up exactly ("trakt", ""), and because a cache
// miss falls through to the live path rather than erroring, a key mismatch
// here would be a PERMANENT SILENT MISS — every Discover load would make a
// live Trakt call forever and no test on either side would fail. Keep the two
// in step.
const traktCacheKey = ""

// RefreshTrakt refreshes the Trakt watchlist row and logs rather than returns.
//
// It is the exported single-source entry point the Trakt-linked hook uses
// fire-and-forget (`go discoverrefresh.RefreshTrakt(context.Background(), d)`
// from traktDevicePollHandler's linked branch), so a populate is never
// cancelled when the operator's HTTP request returns and can never make that
// response slower or able to fail.
//
// Claude 2026-08-03: added the KeyInFlight consultation (BE-16, plan §5.1).
// Reason: same second single-flight guard RefreshSlider now applies — if a
// RefreshAll cycle is already refreshing the trakt key (refreshTraktDue
// marks it before calling this), a fire-and-forget populate from the
// device-poll hook must not race that cycle's own Put for the same key.
// refreshTrakt already nil-guards d.Cache/d.TraktStore, so no separate guard
// is needed here.
// Troubleshooting: a Trakt link during a running cycle whose watchlist row
// doesn't appear until the cycle's own pass writes it is this branch working
// as designed, not a bug.
func RefreshTrakt(ctx context.Context, d Deps) {
	if KeyInFlight(sourceTrakt, traktCacheKey) {
		return // an in-flight cycle is about to write this key anyway
	}
	if err := refreshTrakt(ctx, d); err != nil {
		log.Printf("discoverrefresh: trakt watchlist refresh failed: %v", err)
	}
}

// refreshTrakt fetches the linked account's watchlist and caches it whole.
//
// # Why there is no accumulation loop here
//
// Unlike the TMDB and slider sources, this one makes exactly ONE upstream call
// and stores its entire result. That is not a simplification — Trakt's
// GET /sync/watchlist is genuinely unpaginated and internal/trakt's Client says
// so in its own doc comment ("No pagination is applied; sync/watchlist returns
// the full list in one call", client.go:101-102). There is no page parameter to
// walk, so rawPages is always 1 and exhausted is always true: the row is
// complete by construction.
//
// # pageSize = max(len(payload), 1), and why both halves are load-bearing
//
// The read path is uniform across all four sources — one page-based helper
// delegating to Entry.Slice — so an unpaginated source has to express "one
// logical page IS the whole list" through page_size. Writing len(payload) makes
// cachedPages = ceil(n/n) = 1, so page 1 serves all n items however long the
// list is.
//
// A FIXED page size of 20 would silently truncate a 34-item watchlist to 20
// FOREVER, and nothing downstream would notice: TraktWatchlistRow passes no
// onLoadMore/hasMore and frontend/src/api/trakt.ts sends no page param at all,
// so no client ever asks for page 2 and the other 14 items would simply cease
// to exist. See TestRefreshTrakt_LongWatchlistIsNotTruncated (T-12g), which is
// the only assertion that catches this.
//
// The max(…, 1) guard covers the other end: Slice divides by PageSize, so an
// empty watchlist must not store 0. With it, an empty list stores pageSize=1,
// cachedPages=0, exhausted=true, and Slice's exhausted branch serves [] from
// cache with ZERO external calls.
//
// # Empty writes a row here — the OPPOSITE of the slider source
//
// §2.3's empty-result rule is SOURCE-SPECIFIC and this source is on the other
// side of it from refreshSliders. A zero-item slider accumulation writes
// nothing (its live encoder emits null, which a cached [] could not reproduce);
// a zero-item watchlist DOES write a row, because an empty watchlist is a
// legitimate, stable state and traktWatchlistHandler's own encoder already
// emits [] for it (make([]traktWatchlistItem, len(items))). Skipping the write
// would mean an operator with an empty watchlist paid a live Trakt call on
// every single Discover load, forever — a direct hole in AC1. Do not copy the
// slider sibling's write-nothing-on-empty check into this function.
//
// # The three silent skips, and the one failure mark
//
// Not configured, not linked (checked locally via Tokens.Linked), and
// ErrNotLinked from the call itself all return with NO row and NO MarkFailure:
// an unconfigured source is not a fault, and marking it would fill last_error
// with noise on every install that does not use Trakt. Any OTHER Watchlist
// error is a real failure and marks the row — which, per store.go's UPDATE-only
// MarkFailure, preserves the previous payload rather than blanking it (AC4).
func refreshTrakt(ctx context.Context, d Deps) error {
	if d.Cache == nil || d.TraktStore == nil {
		return nil
	}

	conn, err := d.TraktStore.Get(ctx)
	if errors.Is(err, trakt.ErrNotConfigured) {
		return nil
	}
	if err != nil {
		// A store/decrypt failure is a per-SOURCE fault (§3.7 level 2): there is
		// nothing to fetch with, so log and skip — no MarkFailure, since this
		// says nothing about the cached content's validity.
		return fmt.Errorf("loading trakt connection: %w", err)
	}
	if !conn.Tokens.Linked() {
		return nil
	}

	// Built per refresh from whatever credentials are currently stored, the same
	// reason internal/api/trakt.go builds one per request: client_id/secret can
	// change at any time and a long-lived client would keep using the old pair.
	client := trakt.New(trakt.Config{
		BaseURL:      d.TraktBaseURL,
		ClientID:     conn.ClientID,
		ClientSecret: conn.ClientSecret,
	}, d.HTTPClient)

	callCtx, cancel := context.WithTimeout(ctx, outboundTimeout)
	defer cancel()

	// Session, not the bare Client: it refreshes and persists an expiring access
	// token first, so a watchlist row backed by a ~90-day-old token cannot
	// silently start failing on the scheduled path.
	items, err := trakt.NewSession(d.TraktStore, client).Watchlist(callCtx)
	if errors.Is(err, trakt.ErrNotLinked) {
		return nil
	}
	if err != nil {
		// Deliberately the OUTER ctx, not callCtx — callCtx is already cancelled
		// by the deferred cancel on a timeout, which would make the failure mark
		// itself fail and lose the operator-visible last_error.
		if markErr := d.Cache.MarkFailure(ctx, sourceTrakt, traktCacheKey, err); markErr != nil {
			log.Printf("discoverrefresh: marking trakt failure: %v", markErr)
		}
		return fmt.Errorf("fetching trakt watchlist: %w", err)
	}

	// apidto.TraktWatchlistItem, NOT a hand-declared local copy: it is the
	// already-generated, already-drift-tested mirror of internal/api's unexported
	// traktWatchlistItem (pinned by internal/api/dto_drift_test.go), so a cached
	// response is byte-identical to the live one. Marshalling per element rather
	// than the whole slice is what keeps the stored bodies verbatim for
	// writeRawJSONArray to re-emit.
	payload := make([]json.RawMessage, 0, len(items))
	for _, it := range items {
		body, err := json.Marshal(apidto.TraktWatchlistItem{
			Type:   it.Type,
			Title:  it.Title,
			Year:   it.Year,
			TMDBID: it.TMDBID,
		})
		if err != nil {
			return fmt.Errorf("encoding trakt watchlist item: %w", err)
		}
		payload = append(payload, json.RawMessage(body))
	}

	if err := d.Cache.Put(ctx, sourceTrakt, traktCacheKey, payload,
		max(len(payload), 1), 1, true); err != nil {
		return err
	}
	return nil
}
