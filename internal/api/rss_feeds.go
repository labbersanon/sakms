package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/apidto"
	"github.com/curtiswtaylorjr/sakms/internal/rssfeed"
	"github.com/curtiswtaylorjr/sakms/internal/rssfeeds"
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
		ID:        f.ID,
		Title:     f.Title,
		FeedURL:   f.FeedURL,
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
		f, err := store.Create(r.Context(), req.Title, req.FeedURL, rssfeeds.Target(req.Target), rssfeeds.Protocol(req.Protocol), req.Enabled)
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
func resolveRssFeedHandler(httpClient *http.Client, store *rssfeeds.Store) http.HandlerFunc {
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
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if len(items) > maxResolvedRssFeedItems {
			items = items[:maxResolvedRssFeedItems]
		}

		out := make([]apidto.RssFeedItem, len(items))
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
		}

		writeJSON(w, out)
	}
}
