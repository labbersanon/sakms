// Claude 2026-08-02: this file was adultdiscover_stashbox.go and held the 12
// optional StashDB/FansDB Adult Discover browse handlers (recent/trending/
// studios/performers plus the two entity drill-downs). Those handlers, their 12
// routes, and their helpers (adultStashBoxClient, writeEmptyJSONArray,
// writeStashBoxScenes) are all deleted — Adult Discover no longer has
// structural stash-box rows.
// Reason: the file itself was NOT deleted, and must not be. The surviving
// adultDiscoverMergedRecentHandler below is unrelated to stash boxes despite
// the old filename, is registered at handler.go's
// GET /api/modes/adult/discover/recent-merged, and backs a live sort-bar row;
// optionalConnAPI likewise survives with an external caller (setup.go).
// Troubleshooting: the old name described contents the file no longer had,
// which is how a draft of this change came to propose deleting it outright.
// Review if: adultDiscoverMergedRecentHandler moves out, or optionalConnAPI
// loses its last caller — at that point this file has no reason to exist.
// Related files: internal/api/handler.go, internal/api/adultdiscover_merged_test.go

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/connections"
)

// optionalConnAPI is package api's copy of mode.go's optionalConn — the
// "not-configured is not an error" pattern for an OPTIONAL Adult Discover
// source. It collapses connections.ErrNotFound into (nil, nil) so a handler can
// treat an absent connection as "silently skip this source" rather than an HTTP
// error; any other store error propagates. mode.optionalConn is unexported and
// can't be imported here, hence this small replica.
func optionalConnAPI(ctx context.Context, store *connections.Store, service string) (*connections.Connection, error) {
	conn, err := store.Get(ctx, service)
	if errors.Is(err, connections.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// adultDiscoverMergedRecentHandler backs Adult Discover's "Recently Released"
// sort-bar row. It is re-pointed off the old live TPDB+StashDB merge and onto
// the identified-available pool (D5): one page of cached scene/movie entities
// ordered by the entity's own release date (ReleaseStore.ListRecentScenes),
// each gated by the D5 visibility rule and enclosure-exposed by DirectGrabURL —
// so the row shows exactly the same available universe, gated the same way, as
// the main Scenes / newest rows and search. An install with no cache returns [].
//
// The former concurrent TPDB+StashDB fetch, page-local phash dedup, and
// cross-source date merge are gone: the pool is already a single deduped,
// identified set, so none of that machinery is needed here anymore.
func adultDiscoverMergedRecentHandler(releaseStore *adultnewest.ReleaseStore, feedHealth *adultnewest.FeedHealth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		page, _ := adultPagination(r)
		matches, err := releaseStore.ListRecentScenes(ctx, page)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writePooledScenes(w, matches, feedHealth)
	}
}
