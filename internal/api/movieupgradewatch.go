// This file is the Movies half of the upgrade-watch DISPATCH pass — the
// SIXTH pass inside runUsenetRetryCycle. Series runs after it and shares
// leftover cycle slots (see seriesupgradewatch.go). It hunts for a release
// that meets the title's current quality prefs while library_items.upgrade_watch
// is on and the on-disk file is still below that floor. It does not rank
// against the owned file.
//
// NO GOROUTINE, NO TICKER, NO INTERVAL KEY lives in this file. It is a plain
// function called as the SIXTH step of runUsenetRetryCycle, after adult
// monitor and before park hygiene. The static AST test
// (movieupgradewatch_static_test.go) proves this. It is not on the 60s drain:
// hunting an owned file is not air-date urgent.
//
// Independently deletable: delete this file, its call in runUsenetRetryCycle,
// the movie route registration in handler.go, attachMovieUpgradeWatch in
// titlequality.go, the movie migration, and the library store methods.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
)

// grabOriginUpgradeWatch is grabs.origin for rows minted by this pass.
// A destructive cancel must key off this column, never retry_reason copy.
const grabOriginUpgradeWatch = "upgrade-watch"

const qualityWatchUnwatchedReason = "upgrade watch was turned off, so this search was cancelled"

// attachMovieUpgradeWatch fills quality-prefs GET/PUT with the flag when a
// library_items row exists. No-op for missing titles (toggle stays hidden).
func attachMovieUpgradeWatch(ctx context.Context, lib *library.Store, tmdbID int, out apidto.TitleQualityPrefsResponse) apidto.TitleQualityPrefsResponse {
	if lib == nil || tmdbID <= 0 {
		return out
	}
	item, err := lib.GetByTMDBID(ctx, mode.Movies, tmdbID)
	if err != nil {
		return out
	}
	out.UpgradeWatchAvailable = true
	out.UpgradeWatch = item.UpgradeWatch
	return out
}

func putMovieUpgradeWatchHandler(libStore *library.Store, grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmdbID, ok := tmdbIDPathValue(w, r)
		if !ok {
			return
		}
		var req apidto.MovieUpgradeWatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		item, err := libStore.GetByTMDBID(ctx, mode.Movies, tmdbID)
		if err != nil {
			if errors.Is(err, library.ErrNotFound) {
				http.Error(w, "movie is not in the library", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := libStore.SetItemUpgradeWatch(ctx, item.ID, req.UpgradeWatch); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !req.UpgradeWatch {
			cancelQualityWatchRetries(ctx, grabsStore, mode.Movies, tmdbID)
		}
		writeJSON(w, apidto.MovieUpgradeWatchResponse{UpgradeWatch: req.UpgradeWatch})
	}
}

// monitorMovieUpgradeWatch is the SIXTH pass of runUsenetRetryCycle. For each
// opted-in movie whose on-disk file is still below the title's quality floor,
// it dispatches through RunAutoGrab. libStore may be nil — tests that predate
// this pass skip immediately.
func monitorMovieUpgradeWatch(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc, libStore *library.Store, excluded map[string]bool) {
	_ = runMovieUpgradeWatch(ctx, deps, build, libStore, excluded, loadUsenetCycleSlots(ctx, deps.SettingsStore))
}

func runMovieUpgradeWatch(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc, libStore *library.Store, excluded map[string]bool, budget int) int {
	if libStore == nil || budget <= 0 {
		return 0
	}

	items, err := libStore.ListUpgradeWatchMovies(ctx)
	if err != nil {
		log.Printf("movie upgrade-watch: listing watched movies: %v", err)
		return 0
	}
	if len(items) == 0 {
		return 0
	}

	active := activeMovieUpgradeWatchKeys(ctx, deps)
	var due []library.Item
	for _, item := range items {
		if item.TMDBID <= 0 || item.FilePath == "" {
			continue
		}
		if excluded[excludes.Key(string(mode.Movies), item.TMDBID, item.Title)] {
			continue
		}
		if active[item.TMDBID] {
			continue
		}
		floor := quality.Lowest(resolveAutoGrabTiersForTitle(ctx, libStore, deps.SettingsStore, mode.Movies, item.TMDBID))
		minRes := resolveMinResolutionForTitle(ctx, libStore, mode.Movies, item.TMDBID)
		if quality.Rank(floor) <= 0 {
			continue
		}
		if !fileNeedsQualityUpgrade(item.FilePath, item.QualityTier, floor, minRes) {
			continue
		}
		due = append(due, item)
	}
	if len(due) == 0 {
		return 0
	}

	sess, err := build(ctx, mode.Movies)
	if err != nil {
		log.Printf("movie upgrade-watch: building the movies session failed (%T) — skipping this cycle", rootCause(err))
		return 0
	}
	if sess.Prowlarr == nil {
		log.Printf("movie upgrade-watch: Prowlarr isn't configured — skipping this cycle")
		return 0
	}

	attempts := 0
	cycleCap := budget
	for _, item := range due {
		if attempts >= cycleCap {
			break
		}
		attempts++
		out, err := RunAutoGrab(ctx, deps, sess, AutoGrabRequest{
			Mode:           mode.Movies,
			Title:          item.Title,
			TMDBID:         item.TMDBID,
			RootFolderPath: item.RootFolderPath,
			Trigger:        TriggerQualityWatch,
		})
		switch {
		case err != nil:
			log.Printf("movie upgrade-watch: %q — auto-grab failed (%T)", item.Title, rootCause(err))
		case out.Gated:
			log.Printf("movie upgrade-watch: usenet auto-grab is switched off — abandoning this cycle")
			return attempts
		case out.AlreadyGrabbing:
			log.Printf("movie upgrade-watch: %q is already being downloaded", item.Title)
		case out.Grabbed:
			log.Printf("movie upgrade-watch: %q dispatched", item.Title)
			tagQualityWatchOrigin(ctx, deps.GrabsStore, out.GrabID)
		default:
			log.Printf("movie upgrade-watch: %q has no qualifying candidate yet — parked for re-search", item.Title)
			tagQualityWatchOrigin(ctx, deps.GrabsStore, out.GrabID)
		}
	}
	return attempts
}

func tagQualityWatchOrigin(ctx context.Context, grabsStore *grabs.Store, grabID int64) {
	if grabsStore == nil || grabID == 0 {
		return
	}
	if err := grabsStore.SetOrigin(ctx, grabID, grabOriginUpgradeWatch); err != nil {
		log.Printf("movie upgrade-watch: tagging grab %d origin: %v", grabID, err)
	}
}

func activeMovieUpgradeWatchKeys(ctx context.Context, deps AutoGrabDeps) map[int]bool {
	if deps.GrabsStore == nil {
		return nil
	}
	list, err := deps.GrabsStore.List(ctx, mode.Movies)
	if err != nil {
		log.Printf("movie upgrade-watch: listing movie grabs for prefilter: %v", err)
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

func qualityWatchOriginated(g grabs.Grab, m mode.Mode, tmdbID int) bool {
	return g.Mode == m &&
		g.Origin == grabOriginUpgradeWatch &&
		g.Status == grabs.PendingRetry &&
		g.Indexer == "" &&
		g.DownloadURL == "" &&
		g.TMDBID == tmdbID
}

func cancelQualityWatchRetries(ctx context.Context, grabsStore *grabs.Store, m mode.Mode, tmdbID int) {
	if grabsStore == nil || tmdbID <= 0 {
		return
	}
	list, err := grabsStore.List(ctx, m)
	if err != nil {
		log.Printf("movie upgrade-watch: listing %s grabs for un-watch cleanup: %v", m, err)
		return
	}
	now := time.Now()
	for _, g := range list {
		if !qualityWatchOriginated(g, m, tmdbID) {
			continue
		}
		if err := grabsStore.SetRetryAfter(ctx, g.ID, now, qualityWatchUnwatchedReason); err != nil {
			log.Printf("movie upgrade-watch: recording un-watch reason on grab %d: %v", g.ID, err)
			continue
		}
		if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Failed); err != nil {
			log.Printf("movie upgrade-watch: cancelling grab %d after upgrade watch was turned off: %v", g.ID, err)
		}
	}
}
