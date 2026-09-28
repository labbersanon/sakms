// TMDB list ingest — public v3 list IDs plus the optional give-back account
// watchlist. Same movie/series semantics as Trakt watchlist ingest.
//
// NO GOROUTINE, NO TICKER, NO INTERVAL KEY. Called from monitorListIngests,
// the SEVENTH step of runUsenetRetryCycle.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
)

const grabOriginTMDBList = "tmdb-list"

const (
	tmdbListIngestEnabledKey = "tmdb_list_ingest_enabled"
	tmdbListIDsKey           = "tmdb_list_ids"
)

const tmdbListIngestOffReason = "TMDB list ingest was turned off, so this search was cancelled"

const tmdbListIngestLog = "tmdb list ingest"

type tmdbListIngestResponse struct {
	Enabled           bool   `json:"enabled"`
	ListIDs           string `json:"listIds"`
	HasAccountSession bool   `json:"hasAccountSession"`
}

type tmdbListIngestRequest struct {
	Enabled bool   `json:"enabled"`
	ListIDs string `json:"listIds"`
}

func getTMDBListIngestHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled, err := settingsStore.GetBool(r.Context(), tmdbListIngestEnabledKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, tmdbListIngestResponse{
			Enabled:           enabled,
			ListIDs:           loadSettingString(r.Context(), settingsStore, tmdbListIDsKey, tmdbListIngestLog),
			HasAccountSession: strings.TrimSpace(loadSettingString(r.Context(), settingsStore, mode.TMDBSessionIDKey, tmdbListIngestLog)) != "",
		})
	}
}

func putTMDBListIngestHandler(settingsStore *settings.Store, grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req tmdbListIngestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if err := settingsStore.SetBool(ctx, tmdbListIngestEnabledKey, req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(ctx, tmdbListIDsKey, req.ListIDs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !req.Enabled {
			cancelListOriginRetries(ctx, grabsStore, grabOriginTMDBList, tmdbListIngestOffReason, tmdbListIngestLog)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func ingestTMDBLists(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc, libStore *library.Store, excluded map[string]bool, budget *listIngestBudget) bool {
	if deps.SettingsStore == nil {
		return false
	}
	enabled, err := deps.SettingsStore.GetBool(ctx, tmdbListIngestEnabledKey, false)
	if err != nil {
		log.Printf("%s: reading ingest toggle: %v", tmdbListIngestLog, err)
		return false
	}
	if !enabled {
		return false
	}
	if !listIngestAutoGrabOn(ctx, deps, tmdbListIngestLog) {
		return false
	}
	client := tmdbIngestClient(ctx, deps)
	if client == nil {
		return false
	}
	items := fetchTMDBListItems(ctx, deps, client)
	return ingestListItems(ctx, deps, build, libStore, excluded, items, listIngestKind{
		Origin:    grabOriginTMDBList,
		Trigger:   TriggerTMDBList,
		LogPrefix: tmdbListIngestLog,
		OffReason: tmdbListIngestOffReason,
	}, budget)
}

func tmdbIngestClient(ctx context.Context, deps AutoGrabDeps) *tmdb.Client {
	catalog := listIngestCatalog(deps)
	if catalog.connStore == nil {
		return nil
	}
	httpClient := catalog.httpClient
	if deps.TraktIngest != nil && deps.TraktIngest.HTTPClient != nil {
		httpClient = deps.TraktIngest.HTTPClient
	}
	if httpClient == nil {
		return nil
	}
	conn, err := catalog.connStore.Get(ctx, "tmdb")
	if errors.Is(err, connections.ErrNotFound) {
		return nil
	}
	if err != nil {
		log.Printf("%s: loading TMDB connection: %v", tmdbListIngestLog, err)
		return nil
	}
	if conn == nil || strings.TrimSpace(conn.APIKey) == "" {
		return nil
	}
	return tmdb.New(tmdb.Config{
		BaseURL:     tmdb.DefaultBaseURL,
		APIKey:      conn.APIKey,
		BypassCache: true,
	}, httpClient)
}

func fetchTMDBListItems(ctx context.Context, deps AutoGrabDeps, client *tmdb.Client) []listIngestItem {
	var items []listIngestItem
	seen := map[string]bool{}
	add := func(tmdbItems []tmdb.Item, fallbackType string) {
		for _, it := range tmdbItems {
			typ := fallbackType
			if it.MediaType == tmdb.TV {
				typ = "show"
			} else if it.MediaType == tmdb.Movie {
				typ = "movie"
			}
			if it.ID <= 0 || (typ != "movie" && typ != "show") {
				continue
			}
			key := typ + ":" + strconv.Itoa(it.ID)
			if seen[key] {
				continue
			}
			seen[key] = true
			title := it.Title
			if title == "" {
				title = "TMDB " + strconv.Itoa(it.ID)
			}
			items = append(items, listIngestItem{Type: typ, TMDBID: it.ID, Title: title})
		}
	}

	sessionID := strings.TrimSpace(loadSettingString(ctx, deps.SettingsStore, mode.TMDBSessionIDKey, tmdbListIngestLog))
	if sessionID != "" {
		accountID, err := client.Account(ctx, sessionID)
		if err != nil {
			log.Printf("%s: loading TMDB account: %v", tmdbListIngestLog, err)
		} else if accountID > 0 {
			movies, err := client.WatchlistMovies(ctx, accountID, sessionID)
			if err != nil {
				log.Printf("%s: fetching account movie watchlist: %v", tmdbListIngestLog, err)
			} else {
				add(movies, "movie")
			}
			shows, err := client.WatchlistTV(ctx, accountID, sessionID)
			if err != nil {
				log.Printf("%s: fetching account TV watchlist: %v", tmdbListIngestLog, err)
			} else {
				add(shows, "show")
			}
		}
	}

	for _, raw := range parseSettingLines(loadSettingString(ctx, deps.SettingsStore, tmdbListIDsKey, tmdbListIngestLog)) {
		listID := parseTMDBListID(raw)
		if listID == "" {
			continue
		}
		listed, err := client.ListItems(ctx, listID)
		if err != nil {
			log.Printf("%s: fetching list %s: %v", tmdbListIngestLog, listID, err)
			continue
		}
		add(listed, "movie")
	}
	return items
}

func parseTMDBListID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if i := strings.Index(strings.ToLower(raw), "/list/"); i >= 0 {
		rest := raw[i+6:]
		if slash := strings.IndexAny(rest, "/?#"); slash >= 0 {
			rest = rest[:slash]
		}
		if dash := strings.Index(rest, "-"); dash >= 0 {
			rest = rest[:dash]
		}
		return leadingDigits(rest)
	}
	if dash := strings.Index(raw, "-"); dash >= 0 {
		raw = raw[:dash]
	}
	return leadingDigits(raw)
}
