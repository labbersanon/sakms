// This file is Trakt watchlist ingest — the SEVENTH pass inside
// runUsenetRetryCycle. Discover already fetches GET /sync/watchlist for a
// browse row; this pass turns that same list into Requests (movies) or a
// monitored series (shows) while the operator has opted in.
//
// NO GOROUTINE, NO TICKER, NO INTERVAL KEY lives in this file. It is a
// plain function called as the SEVENTH step of runUsenetRetryCycle, after
// movie upgrade-watch and before park hygiene. The static AST test
// (traktwatchlistingest_static_test.go) proves this. It is not on the 60s
// drain: a watchlist is not air-date urgent, and live Trakt should not be
// hit every minute.
//
// Independently deletable: delete this file, its one call in
// runUsenetRetryCycle, the two route registrations in handler.go, the
// TriggerTraktWatchlist const, TraktIngest on AutoGrabDeps, the traktStore
// argument on RunUsenetRetry / main.go, and the Settings switch.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/trakt"
)

// grabOriginTraktWatchlist is grabs.origin for rows minted by this pass.
// A destructive cancel must key off this column, never retry_reason copy.
const grabOriginTraktWatchlist = "trakt-watchlist"

const traktWatchlistIngestEnabledKey = "trakt_watchlist_ingest_enabled"

const traktWatchlistIngestOffReason = "Trakt watchlist ingest was turned off, so this search was cancelled"

// traktWatchlistIngest is the extra wiring RunUsenetRetry attaches so this
// pass can fetch Trakt and ensure series without growing runUsenetRetryCycle's
// signature. Nil on AutoGrabDeps skips the pass (every test that predates it).
type traktWatchlistIngest struct {
	Store      *trakt.Store
	HTTPClient *http.Client
	BaseURL    string
	Catalog    seasonCatalog
}

type traktWatchlistIngestResponse struct {
	Enabled bool `json:"enabled"`
}

type traktWatchlistIngestRequest struct {
	Enabled bool `json:"enabled"`
}

func getTraktWatchlistIngestHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled, err := settingsStore.GetBool(r.Context(), traktWatchlistIngestEnabledKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, traktWatchlistIngestResponse{Enabled: enabled})
	}
}

func putTraktWatchlistIngestHandler(settingsStore *settings.Store, grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req traktWatchlistIngestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if err := settingsStore.SetBool(ctx, traktWatchlistIngestEnabledKey, req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !req.Enabled {
			cancelTraktWatchlistRetries(ctx, grabsStore)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// monitorTraktWatchlist is the SEVENTH pass of runUsenetRetryCycle. It
// fetches the linked account's watchlist once and, for each usable title
// that is not already owned / excluded / in-flight:
//   - movies: parkPreReleaseRequest when gateMovieGrab blocks, else RunAutoGrab
//   - shows: ensureSeriesByTMDB + monitor every season, then backfill.kick
//
// A show already in library_series is skipped so operator monitor choices
// are not overwritten. Turning ingest off cancels never-dispatched movie
// parks; it does not un-monitor series.
func monitorTraktWatchlist(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc, libStore *library.Store, excluded map[string]bool) {
	ingestTraktWatchlist(ctx, deps, build, libStore, excluded, nil)
}

func ingestTraktWatchlist(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc, libStore *library.Store, excluded map[string]bool, budget *listIngestBudget) bool {
	ing := deps.TraktIngest
	if ing == nil || ing.Store == nil || deps.SettingsStore == nil {
		return false
	}
	enabled, err := deps.SettingsStore.GetBool(ctx, traktWatchlistIngestEnabledKey, false)
	if err != nil {
		log.Printf("trakt watchlist ingest: reading ingest toggle: %v", err)
		return false
	}
	if !enabled {
		return false
	}
	if !listIngestAutoGrabOn(ctx, deps, "trakt watchlist ingest") {
		return false
	}

	items, err := fetchTraktWatchlistLive(ctx, ing)
	if err != nil {
		log.Printf("trakt watchlist ingest: fetching watchlist: %v", err)
		return false
	}
	mapped := make([]listIngestItem, 0, len(items))
	for _, item := range items {
		mapped = append(mapped, listIngestItem{Type: item.Type, TMDBID: item.TMDBID, Title: item.Title})
	}
	return ingestListItems(ctx, deps, build, libStore, excluded, mapped, listIngestKind{
		Origin:    grabOriginTraktWatchlist,
		Trigger:   TriggerTraktWatchlist,
		LogPrefix: "trakt watchlist ingest",
		OffReason: traktWatchlistIngestOffReason,
	}, budget)
}

func fetchTraktWatchlistLive(ctx context.Context, ing *traktWatchlistIngest) ([]trakt.WatchlistItem, error) {
	client, err := traktClientFromStore(ctx, ing.Store, ing.HTTPClient, ing.BaseURL)
	if errors.Is(err, trakt.ErrNotConfigured) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	conn, err := ing.Store.Get(ctx)
	if err != nil {
		return nil, err
	}
	if !conn.Tokens.Linked() {
		return nil, nil
	}
	items, err := trakt.NewSession(ing.Store, client).Watchlist(ctx)
	if errors.Is(err, trakt.ErrNotLinked) {
		return nil, nil
	}
	return items, err
}

func activeMovieGrabKeys(ctx context.Context, deps AutoGrabDeps) map[int]bool {
	if deps.GrabsStore == nil {
		return nil
	}
	list, err := deps.GrabsStore.List(ctx, mode.Movies)
	if err != nil {
		log.Printf("trakt watchlist ingest: listing movie grabs for prefilter: %v", err)
		return nil
	}
	active := make(map[int]bool, len(list))
	for _, g := range list {
		if g.TMDBID <= 0 {
			continue
		}
		switch g.Status {
		case grabs.Queued, grabs.Downloading, grabs.Completed, grabs.PendingRetry:
			active[g.TMDBID] = true
		}
	}
	return active
}

func traktWatchlistOriginated(g grabs.Grab) bool {
	return listOriginated(g, grabOriginTraktWatchlist)
}

func cancelTraktWatchlistRetries(ctx context.Context, grabsStore *grabs.Store) {
	cancelListOriginRetries(ctx, grabsStore, grabOriginTraktWatchlist, traktWatchlistIngestOffReason, "trakt watchlist ingest")
}
