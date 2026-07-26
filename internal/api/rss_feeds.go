package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/rssfeed"
	"github.com/labbersanon/sakms/internal/rssfeeds"
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

// listRssFeedsHandler returns every admin-defined RSS feed row, ordered by
// display position — GET /api/discover/rss-feeds.
func listRssFeedsHandler(store *rssfeeds.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		feeds, err := store.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, toDTORssFeeds(feeds))
	}
}

// createRssFeedHandler is POST /api/discover/rss-feeds — validated by
// rssfeeds.Store.Create (title/feed_url/target/protocol).
func createRssFeedHandler(store *rssfeeds.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.RssFeedUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// Create always needs the real URL once (there is no stored value to
		// preserve yet). A nil/absent feedUrl is an empty create → Store.Create
		// returns ErrFeedURLRequired, surfaced as a 400.
		feedURL := ""
		if req.FeedURL != nil {
			feedURL = *req.FeedURL
		}
		f, err := store.Create(r.Context(), req.Title, feedURL, rssfeeds.Target(req.Target), rssfeeds.Protocol(req.Protocol), req.Enabled)
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
		var req apidto.RssFeedUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
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

		f, err := findRssFeed(ctx, store, id)
		if err != nil {
			if errors.Is(err, rssfeeds.ErrNotFound) {
				http.Error(w, err.Error(), http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
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
