package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/rssfeed"
	"github.com/labbersanon/sakms/internal/rssfeeds"
	"github.com/labbersanon/sakms/internal/sectionlock"
)

// maxResolvedRssFeedItems caps how many items GET
// /api/discover/rss-feeds/{id}/resolve returns — no client pagination; a
// live external feed fetched fresh per call has no stable page cursor to
// offer anyway, so this is a flat cap rather than a page size.
const maxResolvedRssFeedItems = 50

// toDTORssFeed maps an internal rssfeeds.Feed onto the exported
// apidto.RssFeed wire DTO — direct sibling of discover_sliders.go's
// toDTOSlider.
func toDTORssFeed(f rssfeeds.Feed) apidto.RssFeed {
	return apidto.RssFeed{
		ID:    f.ID,
		Title: f.Title,
		// FeedURL is intentionally masked — the URL is an encrypted secret
		// (commonly embeds an indexer API key) and must never round-trip to the
		// client. See apidto.RssFeed.FeedURL's doc comment; the frontend sends
		// null to preserve it on an untouched save.
		FeedURL:   "",
		Target:    string(f.Target),
		Protocol:  string(f.Protocol),
		SortOrder: f.SortOrder,
		Enabled:   f.Enabled,
		CreatedAt: f.CreatedAt,
		UpdatedAt: f.UpdatedAt,
	}
}

func toDTORssFeeds(feeds []rssfeeds.Feed) []apidto.RssFeed {
	out := make([]apidto.RssFeed, len(feeds))
	for i, f := range feeds {
		out[i] = toDTORssFeed(f)
	}
	return out
}

// rssFeedStoreError maps an rssfeeds.Store validation/lookup error onto an
// HTTP status: the fixed enum/required-field errors (ErrInvalidTarget,
// ErrInvalidProtocol, ErrTitleRequired, ErrFeedURLRequired,
// ErrReorderMismatch) are always a bad request body, never a server fault;
// ErrNotFound is a 404. Anything else is treated as an internal error.
func rssFeedStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, rssfeeds.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, rssfeeds.ErrInvalidTarget),
		errors.Is(err, rssfeeds.ErrInvalidProtocol),
		errors.Is(err, rssfeeds.ErrTitleRequired),
		errors.Is(err, rssfeeds.ErrFeedURLRequired),
		errors.Is(err, rssfeeds.ErrReorderMismatch):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// The seven /api/discover/rss-feeds* routes are §4.3 item 1 — the family AC6
// names explicitly. Layer 1 classifies all of them as {discover} and nothing
// more, because a feed's Adult scope lives in its ROW (target: "adult"), not
// in the URL. So Adult being locked while Discover is not leaves every one of
// them wide open without the explicit checks below.
//
// Two shapes, and the split is deliberate:
//
//   - READS filter. GET rss-feeds drops the Adult rows and returns the rest,
//     so Mainstream's feed list still works while Adult is locked — refusing
//     the whole list would lock Discover's Settings panel whenever Adult was
//     locked, the exact outcome AC6's closing clause forbids.
//   - WRITES refuse, with 403 section_locked, so the frontend raises its PIN
//     overlay.

// listRssFeedsHandler returns every admin-defined RSS feed row, ordered by
// display position — GET /api/discover/rss-feeds.
func listRssFeedsHandler(store *rssfeeds.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		feeds, err := store.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if adultLocked(r.Context()) {
			feeds = withoutAdultFeeds(feeds)
		}
		writeJSON(w, toDTORssFeeds(feeds))
	}
}

// withoutAdultFeeds drops every target:"adult" row. Returns a fresh slice
// rather than filtering in place — the caller's slice comes straight from the
// Store and must not be rearranged under it.
func withoutAdultFeeds(feeds []rssfeeds.Feed) []rssfeeds.Feed {
	out := make([]rssfeeds.Feed, 0, len(feeds))
	for _, f := range feeds {
		if f.Target == rssfeeds.TargetAdult {
			continue
		}
		out = append(out, f)
	}
	return out
}

// denyIfAdultFeed refuses a write addressed to an Adult feed row while
// adult-content is locked. It reports true when it has answered the request.
//
// A lookup failure answers too (404/500), so a call site is a plain early
// return; the feed is returned on the allow path so the caller does not pay a
// second List.
func denyIfAdultFeed(w http.ResponseWriter, r *http.Request, store *rssfeeds.Store, id int) (*rssfeeds.Feed, bool) {
	f, err := findRssFeed(r.Context(), store, id)
	if err != nil {
		if errors.Is(err, rssfeeds.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return nil, true
	}
	if f.Target == rssfeeds.TargetAdult && denyIfAdultLocked(w, r) {
		return nil, true
	}
	return f, false
}

// detectProtocolSampleSize caps how many of a feed's leading items
// detectProtocol inspects — enough to tolerate a malformed or atypical first
// entry (one item with no enclosure and no scheme signal) without scanning a
// whole feed, since a well-formed feed's protocol is decided by its very first
// real enclosure.
const detectProtocolSampleSize = 5

// enclosureTypeProtocols maps a known RSS 2.0 <enclosure type> MIME value to
// the protocol it implies. Anything not in this map (or an absent type
// attribute) falls back to the enclosure URL's scheme in detectProtocol.
var enclosureTypeProtocols = map[string]rssfeeds.Protocol{
	"application/x-bittorrent": rssfeeds.Torrent,
	"x-scheme-handler/magnet":  rssfeeds.Torrent,
	"application/x-nzb":        rssfeeds.Usenet,
}

// detectProtocol infers a feed's download protocol from its parsed items,
// sampling the first detectProtocolSampleSize entries. For each sampled item it
// checks the enclosure's MIME type against enclosureTypeProtocols first, then
// falls back to the enclosure URL's scheme (a "magnet:" URL is decisively
// torrent on its own). It returns confident=false when no sampled item yields a
// determination — the expected outcome for a bare-.torrent-over-https feed with
// no `type` attribute and no magnet scheme, where guessing (e.g. "https means
// usenet") would reintroduce exactly the wrong-protocol bug class this
// detection exists to eliminate. Placed in internal/api (not rssfeed/rssfeeds)
// so it needs no new cross-package import edge — this package already imports
// both.
func detectProtocol(items []rssfeed.Item) (rssfeeds.Protocol, bool) {
	for i, it := range items {
		if i >= detectProtocolSampleSize {
			break
		}
		mime := strings.ToLower(strings.TrimSpace(it.EnclosureType))
		if p, ok := enclosureTypeProtocols[mime]; ok {
			return p, true
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(it.EnclosureURL)), "magnet:") {
			return rssfeeds.Torrent, true
		}
	}
	return "", false
}

// writeProtocolUndetected emits the shared 422 protocol_undetected response
// used by both the create (omitted-protocol) and rescan paths when detection is
// inconclusive — the frontend's fallback pop-up keys on this exact body.
func writeProtocolUndetected(w http.ResponseWriter) {
	writeJSONStatus(w, http.StatusUnprocessableEntity, apidto.ProtocolUndetectedResponse{Error: "protocol_undetected"})
}

// createRssFeedHandler is POST /api/discover/rss-feeds. When the request
// carries an explicit protocol (Mainstream's AddRssFeedModal, or the Adult
// fallback pop-up's retry) it is used as-is and Store.Create is called exactly
// as before. When protocol is nil (the Adult Add flow, which has no manual
// protocol field), the feed is fetched and detectProtocol runs: a confident
// result is stored; an inconclusive one returns 422 protocol_undetected for the
// frontend's manual-pick fallback. Store.Create still validates
// title/feed_url/target/protocol.
func createRssFeedHandler(httpClient *http.Client, store *rssfeeds.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var req apidto.RssFeedCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// Checked BEFORE the auto-detect fetch below: creating an Adult feed
		// while Adult is locked must not first go and fetch the operator's
		// Adult feed URL.
		if rssfeeds.Target(req.Target) == rssfeeds.TargetAdult && denyIfAdultLocked(w, r) {
			return
		}

		var protocol rssfeeds.Protocol
		if req.Protocol != nil {
			protocol = rssfeeds.Protocol(*req.Protocol)
		} else {
			// Auto-detect: fetch the feed once and sniff its enclosures. A fetch
			// error embeds the feed URL (which may carry an indexer API key), so
			// log it server-side only and return a generic 502.
			items, err := rssfeed.FetchItems(ctx, httpClient, req.FeedURL)
			if err != nil {
				log.Printf("detecting protocol for new rss feed %q: %v", req.Title, err)
				http.Error(w, "could not fetch the feed — check the feed URL and try again", http.StatusBadGateway)
				return
			}
			detected, confident := detectProtocol(items)
			if !confident {
				writeProtocolUndetected(w)
				return
			}
			protocol = detected
		}

		f, err := store.Create(ctx, req.Title, req.FeedURL, rssfeeds.Target(req.Target), protocol, req.Enabled)
		if err != nil {
			rssFeedStoreError(w, err)
			return
		}
		writeJSON(w, toDTORssFeed(*f))
	}
}

// updateRssFeedHandler is PUT /api/discover/rss-feeds/{id} — overwrites
// every editable field (sort_order is untouched; see Store.Update's doc
// comment, reordering is a separate action below).
func updateRssFeedHandler(store *rssfeeds.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "id path parameter must be an integer", http.StatusBadRequest)
			return
		}
		var req apidto.RssFeedUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// BOTH sides are checked: the stored row (editing an Adult feed) and
		// the incoming target (re-pointing a Mainstream feed at Adult, which
		// would otherwise create an Adult row from behind the lock).
		if _, done := denyIfAdultFeed(w, r, store, id); done {
			return
		}
		if rssfeeds.Target(req.Target) == rssfeeds.TargetAdult && denyIfAdultLocked(w, r) {
			return
		}
		// req.FeedURL is three-state (*string): nil preserves the stored
		// (encrypted) URL — the toggle/edit path the frontend uses when it only
		// changes enabled/title never re-sends the masked URL — a non-empty value
		// replaces it, "" is rejected by the Store as ErrFeedURLRequired.
		f, err := store.Update(r.Context(), id, req.Title, req.FeedURL, rssfeeds.Target(req.Target), rssfeeds.Protocol(req.Protocol), req.Enabled)
		if err != nil {
			rssFeedStoreError(w, err)
			return
		}
		writeJSON(w, toDTORssFeed(*f))
	}
}

// deleteRssFeedHandler is DELETE /api/discover/rss-feeds/{id}. Returns 404
// when the id has no stored feed (Store.Delete returns ErrNotFound).
func deleteRssFeedHandler(store *rssfeeds.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "id path parameter must be an integer", http.StatusBadRequest)
			return
		}
		if _, done := denyIfAdultFeed(w, r, store, id); done {
			return
		}
		if err := store.Delete(r.Context(), id); err != nil {
			rssFeedStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// reorderRssFeedsHandler is POST /api/discover/rss-feeds/reorder — one
// explicit "here is the full new order" action covering every existing feed
// exactly once (see rssfeeds.Store.Reorder's doc comment), not a per-item
// bulk mutation.
func reorderRssFeedsHandler(store *rssfeeds.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.RssFeedReorderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// Reorder is all-or-nothing by construction — Store.Reorder demands
		// every existing feed's id exactly once, across every target — so a
		// reorder issued while Adult is locked necessarily rearranges the
		// Adult rows too. Refuse it whole rather than inventing a partial
		// semantic the Store cannot express. With no Adult feed configured
		// there is nothing to protect and reordering stays available.
		if adultLocked(r.Context()) {
			feeds, err := store.List(r.Context())
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if len(withoutAdultFeeds(feeds)) != len(feeds) {
				writeSectionLocked(w, sectionlock.SectionAdultContent)
				return
			}
		}
		if err := store.Reorder(r.Context(), req.IDs); err != nil {
			rssFeedStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// findRssFeed looks up id in store's full list — rssfeeds.Store has no
// Get-by-id (List/Create/Update/Delete/Reorder only), and a single-operator
// admin's feed count is small enough that this linear scan costs nothing
// worth a new Store method for (same reasoning as discover_sliders.go's
// findSlider).
func findRssFeed(ctx context.Context, store *rssfeeds.Store, id int) (*rssfeeds.Feed, error) {
	feeds, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range feeds {
		if feeds[i].ID == id {
			return &feeds[i], nil
		}
	}
	return nil, rssfeeds.ErrNotFound
}

// rescanRssFeedHandler is POST /api/discover/rss-feeds/{id}/rescan — re-runs
// protocol auto-detection against an existing feed and, on a confident result,
// overwrites only its stored protocol (title/feedURL/target/enabled/sortOrder
// all preserved from the current row: feedURL is passed nil so Store.Update
// keeps the encrypted URL, and sort_order is never touched by Update). An
// inconclusive result returns the same 422 protocol_undetected shape as create,
// so the frontend's fallback pop-up can offer a one-time manual protocol pick.
func rescanRssFeedHandler(httpClient *http.Client, store *rssfeeds.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "id path parameter must be an integer", http.StatusBadRequest)
			return
		}

		f, done := denyIfAdultFeed(w, r, store, id)
		if done {
			return
		}

		items, err := rssfeed.FetchItems(ctx, httpClient, f.FeedURL)
		if err != nil {
			// err embeds f.FeedURL, which may carry an indexer API key — log it
			// server-side only, never in the client-facing response.
			log.Printf("rescanning rss feed %d: %v", id, err)
			http.Error(w, "could not fetch the feed — check the feed URL and try again", http.StatusBadGateway)
			return
		}

		detected, confident := detectProtocol(items)
		if !confident {
			writeProtocolUndetected(w)
			return
		}

		// Preserve the full current row, changing only protocol: feedURL nil keeps
		// the stored (encrypted) URL, sort_order is untouched by Store.Update.
		updated, err := store.Update(ctx, id, f.Title, nil, f.Target, detected, f.Enabled)
		if err != nil {
			rssFeedStoreError(w, err)
			return
		}
		writeJSON(w, toDTORssFeed(*updated))
	}
}

// resolveRssFeedHandler is GET /api/discover/rss-feeds/{id}/resolve — loads
// the feed config, fetches+parses its live RSS 2.0 feed
// (rssfeed.FetchItems), caps to the first maxResolvedRssFeedItems items, and
// maps each rssfeed.Item to the wire DTO: DownloadURL is the item's
// enclosure URL, falling back to its Link when the item has no enclosure
// (a malformed/no-enclosure item); SizeBytes is the enclosure's byte length;
// Protocol is the feed's own admin-set protocol (not sniffed from the XML);
// Indexer is the feed's own Title, reusing the existing free-form Indexer
// display field grabs already have.
//
// For an Adult-targeted feed only, each item is additionally joined against the
// Adult identify pipeline's matched-entity pool (adult_newest_releases) by its
// enclosure key — the same feed_item_key adultnewest.feedItemKey() derives, which
// for every non-degenerate item equals the DownloadURL computed here — and the
// response is filtered down to only the items that matched, each enriched with
// that pipeline's resolved poster/title/studio (ResolvedTitle/ResolvedStudio/
// ResolvedImage). An Adult item with no pool match is dropped from the response
// entirely (not returned unenriched): an unidentified raw release is noise in the
// resolved feed row, so it never reaches the client. A Movies/Series feed has no
// pool to join against (the lookup is skipped entirely) and returns every raw item
// unfiltered, with no resolved fields. The pool join is best-effort: a lookup
// error is logged and the raw items are returned unfiltered and un-enriched
// (rather than blanking the whole feed row over a transient pool hiccup), never
// failing the whole feed view.
func resolveRssFeedHandler(httpClient *http.Client, store *rssfeeds.Store, releaseStore *adultnewest.ReleaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "id path parameter must be an integer", http.StatusBadRequest)
			return
		}

		f, done := denyIfAdultFeed(w, r, store, id)
		if done {
			return
		}

		items, err := rssfeed.FetchItems(ctx, httpClient, f.FeedURL)
		if err != nil {
			// err embeds f.FeedURL, which may carry an indexer API key in its
			// query string (the same secret migration 0043 encrypts at rest) —
			// log it server-side only, never in the client-facing response.
			log.Printf("resolving rss feed %d: %v", id, err)
			http.Error(w, "could not fetch the feed — check the feed URL and try again", http.StatusBadGateway)
			return
		}
		if len(items) > maxResolvedRssFeedItems {
			items = items[:maxResolvedRssFeedItems]
		}

		out := make([]apidto.RssFeedItem, len(items))
		// keys collects each Adult item's enclosure key for the single batched
		// pool join below; nil (and never allocated) for a Movies/Series feed.
		var keys []string
		adult := f.Target == rssfeeds.TargetAdult
		for i, it := range items {
			downloadURL := it.EnclosureURL
			if downloadURL == "" {
				downloadURL = it.Link
			}
			out[i] = apidto.RssFeedItem{
				Title:       it.Title,
				Link:        it.Link,
				PubDate:     it.PubDate,
				SizeBytes:   it.EnclosureLength,
				DownloadURL: downloadURL,
				Protocol:    string(f.Protocol),
				Indexer:     f.Title,
			}
			// downloadURL == "" only for a degenerate item with no enclosure and
			// no link (feedItemKey would hash it instead) — never a real pool key,
			// so skip it and avoid a spurious "" lookup.
			if adult && downloadURL != "" {
				keys = append(keys, downloadURL)
			}
		}

		if adult {
			matches, err := releaseStore.ByFeedItemKeys(ctx, keys)
			if err != nil {
				// Best-effort enrichment: a transient pool-join hiccup shouldn't
				// blank the whole feed row, so log and return the raw items
				// unfiltered rather than dropping everything as "unmatched".
				log.Printf("enriching rss feed %d against adult pool: %v", id, err)
			} else {
				// Adult feeds surface only pool-matched items: an item with no
				// resolved entity is dropped entirely (not returned unenriched),
				// so the resolved row never shows unidentified raw releases.
				// Standard in-place filter — filtered[j] is only ever written from
				// out[i] with j <= i, so reusing out's backing array is safe.
				filtered := out[:0]
				for i := range out {
					m, ok := matches[out[i].DownloadURL]
					if !ok {
						continue
					}
					out[i].ResolvedTitle = m.EntityTitle
					out[i].ResolvedStudio = m.EntityStudio
					out[i].ResolvedImage = m.EntityImage
					filtered = append(filtered, out[i])
				}
				out = filtered
			}
		}

		writeJSON(w, out)
	}
}
