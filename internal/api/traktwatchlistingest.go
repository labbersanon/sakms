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
	"time"

	"github.com/labbersanon/sakms/internal/excludes"
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
	ing := deps.TraktIngest
	if ing == nil || ing.Store == nil || deps.SettingsStore == nil {
		return
	}
	enabled, err := deps.SettingsStore.GetBool(ctx, traktWatchlistIngestEnabledKey, false)
	if err != nil {
		log.Printf("trakt watchlist ingest: reading ingest toggle: %v", err)
		return
	}
	if !enabled {
		return
	}
	auto, err := deps.SettingsStore.GetBool(ctx, usenetAutoGrabEnabledKey, false)
	if err != nil {
		log.Printf("trakt watchlist ingest: reading auto-grab toggle: %v", err)
		return
	}
	if !auto {
		return
	}

	items, err := fetchTraktWatchlistLive(ctx, ing)
	if err != nil {
		log.Printf("trakt watchlist ingest: fetching watchlist: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}

	activeMovies := activeMovieGrabKeys(ctx, deps)
	var movieSess *mode.Session
	skipMovies := false
	attempts := 0
	cycleCap := loadUsenetCycleSlots(ctx, deps.SettingsStore)
	for _, item := range items {
		if attempts >= cycleCap {
			break
		}
		if item.TMDBID <= 0 {
			continue
		}
		switch item.Type {
		case "movie":
			if skipMovies || !shouldIngestWatchlistMovie(ctx, libStore, excluded, activeMovies, item) {
				continue
			}
			if movieSess == nil {
				sess, sessErr := build(ctx, mode.Movies)
				if sessErr != nil {
					log.Printf("trakt watchlist ingest: building the movies session failed (%T) — skipping remaining movies", rootCause(sessErr))
					skipMovies = true
					continue
				}
				movieSess = sess
			}
			attempts++
			stop, skipRest := ingestWatchlistMovie(ctx, deps, movieSess, item)
			if stop {
				return
			}
			if skipRest {
				skipMovies = true
			}
		case "show":
			if ing.Catalog.lib == nil {
				continue
			}
			if !shouldIngestWatchlistShow(ctx, ing.Catalog.lib, excluded, item) {
				continue
			}
			attempts++
			ingestWatchlistShow(ctx, ing.Catalog, item)
		}
	}
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

func shouldIngestWatchlistMovie(ctx context.Context, libStore *library.Store, excluded map[string]bool, active map[int]bool, item trakt.WatchlistItem) bool {
	if excluded[excludes.Key(string(mode.Movies), item.TMDBID, item.Title)] {
		return false
	}
	if active[item.TMDBID] {
		return false
	}
	if libStore == nil {
		return true
	}
	_, err := libStore.GetByTMDBID(ctx, mode.Movies, item.TMDBID)
	if err == nil {
		return false
	}
	if !errors.Is(err, library.ErrNotFound) {
		log.Printf("trakt watchlist ingest: looking up movie %q: %v", item.Title, err)
		return false
	}
	return true
}

func shouldIngestWatchlistShow(ctx context.Context, libStore *library.Store, excluded map[string]bool, item trakt.WatchlistItem) bool {
	if excluded[excludes.Key(string(mode.Series), item.TMDBID, item.Title)] {
		return false
	}
	_, err := libStore.GetSeriesByTMDBID(ctx, item.TMDBID)
	if err == nil {
		return false
	}
	if !errors.Is(err, library.ErrNotFound) {
		log.Printf("trakt watchlist ingest: looking up series %q: %v", item.Title, err)
		return false
	}
	return true
}

// ingestWatchlistMovie parks or auto-grabs one watchlist movie.
// stopCycle: auto-grab gated off mid-pass — abandon the rest of ingest.
// skipRestMovies: remaining movies cannot dispatch (Prowlarr missing).
func ingestWatchlistMovie(ctx context.Context, deps AutoGrabDeps, sess *mode.Session, item trakt.WatchlistItem) (stopCycle, skipRestMovies bool) {
	rel, blocked, reason := gateMovieGrab(ctx, sess.TMDB, mode.Movies, item.TMDBID)
	if blocked {
		held, err := parkPreReleaseRequest(ctx, deps.GrabsStore, mode.Movies, item.Title, item.TMDBID, rel.HoldUntil)
		switch {
		case errors.Is(err, grabs.ErrHeldRequestExists):
			return false, false
		case err != nil:
			log.Printf("trakt watchlist ingest: holding %q (%s): %v", item.Title, reason, err)
			return false, false
		}
		tagTraktWatchlistOrigin(ctx, deps.GrabsStore, held.ID)
		return false, false
	}
	if sess.Prowlarr == nil {
		log.Printf("trakt watchlist ingest: Prowlarr isn't configured — skipping remaining movies")
		return false, true
	}
	out, err := RunAutoGrab(ctx, deps, sess, AutoGrabRequest{
		Mode:    mode.Movies,
		Title:   item.Title,
		TMDBID:  item.TMDBID,
		Trigger: TriggerTraktWatchlist,
	})
	switch {
	case err != nil:
		log.Printf("trakt watchlist ingest: %q — auto-grab failed (%T)", item.Title, rootCause(err))
	case out.Gated:
		log.Printf("trakt watchlist ingest: usenet auto-grab is switched off — abandoning this cycle")
		return true, false
	case out.MovieBlocked:
		held, parkErr := parkPreReleaseRequest(ctx, deps.GrabsStore, mode.Movies, item.Title, item.TMDBID, rel.HoldUntil)
		if parkErr != nil && !errors.Is(parkErr, grabs.ErrHeldRequestExists) {
			log.Printf("trakt watchlist ingest: holding blocked %q: %v", item.Title, parkErr)
			return false, false
		}
		if parkErr == nil {
			tagTraktWatchlistOrigin(ctx, deps.GrabsStore, held.ID)
		}
	case out.AlreadyGrabbing:
		log.Printf("trakt watchlist ingest: %q is already being downloaded", item.Title)
	case out.Grabbed:
		log.Printf("trakt watchlist ingest: %q dispatched", item.Title)
		tagTraktWatchlistOrigin(ctx, deps.GrabsStore, out.GrabID)
	default:
		log.Printf("trakt watchlist ingest: %q has no qualifying candidate yet — parked for re-search", item.Title)
		tagTraktWatchlistOrigin(ctx, deps.GrabsStore, out.GrabID)
	}
	return false, false
}

func ingestWatchlistShow(ctx context.Context, catalog seasonCatalog, item trakt.WatchlistItem) {
	series, err := catalog.ensureSeriesByTMDB(ctx, item.TMDBID)
	if err != nil {
		log.Printf("trakt watchlist ingest: adding series %q: %v", item.Title, err)
		return
	}
	states, err := catalog.statesByTMDB(ctx, item.TMDBID)
	if err != nil {
		log.Printf("trakt watchlist ingest: listing seasons for %q: %v", item.Title, err)
		return
	}
	touched := map[int]bool{}
	for _, st := range states {
		if err := catalog.lib.SetSeasonMonitored(ctx, series.ID, st.SeasonNumber, true); err != nil {
			log.Printf("trakt watchlist ingest: monitoring season %d of %q: %v", st.SeasonNumber, item.Title, err)
			break
		}
		touched[st.SeasonNumber] = true
	}
	if len(touched) > 0 {
		catalog.backfill.kick(series.ID, touched)
	}
}

func tagTraktWatchlistOrigin(ctx context.Context, grabsStore *grabs.Store, grabID int64) {
	if grabsStore == nil || grabID == 0 {
		return
	}
	if err := grabsStore.SetOrigin(ctx, grabID, grabOriginTraktWatchlist); err != nil {
		log.Printf("trakt watchlist ingest: tagging grab %d origin: %v", grabID, err)
	}
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
	return g.Mode == mode.Movies &&
		g.Origin == grabOriginTraktWatchlist &&
		g.Status == grabs.PendingRetry &&
		g.Indexer == "" &&
		g.DownloadURL == ""
}

func cancelTraktWatchlistRetries(ctx context.Context, grabsStore *grabs.Store) {
	if grabsStore == nil {
		return
	}
	list, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		log.Printf("trakt watchlist ingest: listing movie grabs for ingest-off cleanup: %v", err)
		return
	}
	now := time.Now()
	for _, g := range list {
		if !traktWatchlistOriginated(g) {
			continue
		}
		if err := grabsStore.SetRetryAfter(ctx, g.ID, now, traktWatchlistIngestOffReason); err != nil {
			log.Printf("trakt watchlist ingest: recording ingest-off reason on grab %d: %v", g.ID, err)
			continue
		}
		if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Failed); err != nil {
			log.Printf("trakt watchlist ingest: cancelling grab %d after ingest was turned off: %v", g.ID, err)
		}
	}
}
