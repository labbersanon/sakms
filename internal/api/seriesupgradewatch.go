// This file is the Series half of the upgrade-watch DISPATCH pass — the
// SIXTH pass inside runUsenetRetryCycle, after movies, sharing that pass's
// leftover cycle budget. It hunts owned episodes of a series whose
// library_series.upgrade_watch is on and whose on-disk file is still below
// the title's quality floor. It does not rank against the owned file and
// does not hunt missing episodes (air-date owns those).
//
// NO GOROUTINE, NO TICKER, NO INTERVAL KEY lives in this file. The static
// AST test (movieupgradewatch_static_test.go) covers this file too. It is
// not on the 60s drain: hunting an owned file is not air-date urgent.
//
// Independently deletable: delete this file, its call in runUsenetRetryCycle,
// the two series route registrations in handler.go, attachSeriesUpgradeWatch
// in titlequality.go, the series migration, and the library store methods.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
)

type seriesUpgradeDue struct {
	series library.Series
	ep     library.Episode
}

// attachSeriesUpgradeWatch fills quality-prefs GET/PUT with the flag when a
// library_series row exists. No-op for missing titles (toggle stays hidden).
func attachSeriesUpgradeWatch(ctx context.Context, lib *library.Store, tmdbID int, out apidto.TitleQualityPrefsResponse) apidto.TitleQualityPrefsResponse {
	if lib == nil || tmdbID <= 0 {
		return out
	}
	series, err := lib.GetSeriesByTMDBID(ctx, tmdbID)
	if err != nil {
		return out
	}
	out.UpgradeWatchAvailable = true
	out.UpgradeWatch = series.UpgradeWatch
	return out
}

func putSeriesUpgradeWatchByIDHandler(libStore *library.Store, grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		seriesID, ok := seriesIDPathValue(w, r)
		if !ok {
			return
		}
		series, ok := lookupSeries(r.Context(), w, libStore, seriesID)
		if !ok {
			return
		}
		putSeriesUpgradeWatch(w, r, libStore, grabsStore, series)
	}
}

func putSeriesUpgradeWatchByTMDBHandler(libStore *library.Store, grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmdbID, ok := tmdbIDPathValue(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		series, err := libStore.GetSeriesByTMDBID(ctx, tmdbID)
		if err != nil {
			if errors.Is(err, library.ErrNotFound) {
				http.Error(w, "series is not in the library", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		putSeriesUpgradeWatch(w, r, libStore, grabsStore, series)
	}
}

func putSeriesUpgradeWatch(w http.ResponseWriter, r *http.Request, libStore *library.Store, grabsStore *grabs.Store, series *library.Series) {
	var req apidto.MovieUpgradeWatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if err := libStore.SetSeriesUpgradeWatch(ctx, series.ID, req.UpgradeWatch); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !req.UpgradeWatch {
		cancelQualityWatchRetries(ctx, grabsStore, mode.Series, series.TMDBID)
	}
	writeJSON(w, apidto.MovieUpgradeWatchResponse{UpgradeWatch: req.UpgradeWatch})
}

func monitorSeriesUpgradeWatch(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc, libStore *library.Store, excluded map[string]bool) {
	if libStore == nil {
		return
	}
	_ = runSeriesUpgradeWatch(ctx, deps, build, libStore, excluded, loadUsenetCycleSlots(ctx, deps.SettingsStore))
}

func runSeriesUpgradeWatch(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc, libStore *library.Store, excluded map[string]bool, budget int) int {
	if libStore == nil || budget <= 0 {
		return 0
	}

	shows, err := libStore.ListUpgradeWatchSeries(ctx)
	if err != nil {
		log.Printf("series upgrade-watch: listing watched series: %v", err)
		return 0
	}
	if len(shows) == 0 {
		return 0
	}

	active := activeSeriesUpgradeWatchKeys(ctx, deps)
	var due []seriesUpgradeDue
	for _, ser := range shows {
		if ser.TMDBID <= 0 {
			continue
		}
		if excluded[excludes.Key(string(mode.Series), ser.TMDBID, ser.Title)] {
			continue
		}
		floor := quality.Lowest(resolveAutoGrabTiersForTitle(ctx, libStore, deps.SettingsStore, mode.Series, ser.TMDBID))
		minRes := resolveMinResolutionForTitle(ctx, libStore, mode.Series, ser.TMDBID)
		if quality.Rank(floor) <= 0 {
			continue
		}
		eps, err := libStore.ListEpisodes(ctx, ser.ID)
		if err != nil {
			log.Printf("series upgrade-watch: listing episodes of %q: %v", ser.Title, err)
			continue
		}
		for _, ep := range eps {
			if ep.FilePath == "" {
				continue
			}
			if active[seriesEpisodeKey{tmdbID: ser.TMDBID, season: ep.SeasonNumber, episode: ep.EpisodeNumber}] {
				continue
			}
			if !fileNeedsQualityUpgrade(ep.FilePath, ep.QualityTier, floor, minRes) {
				continue
			}
			due = append(due, seriesUpgradeDue{series: ser, ep: ep})
		}
	}
	if len(due) == 0 {
		return 0
	}

	sess, err := build(ctx, mode.Series)
	if err != nil {
		log.Printf("series upgrade-watch: building the series session failed (%T) — skipping this cycle", rootCause(err))
		return 0
	}
	if sess.Prowlarr == nil {
		log.Printf("series upgrade-watch: Prowlarr isn't configured — skipping this cycle")
		return 0
	}

	attempts := 0
	for _, item := range due {
		if attempts >= budget {
			break
		}
		attempts++
		out, err := RunAutoGrab(ctx, deps, sess, AutoGrabRequest{
			Mode:            mode.Series,
			Title:           item.series.Title,
			TMDBID:          item.series.TMDBID,
			TVDBID:          item.series.TVDBID,
			Season:          item.ep.SeasonNumber,
			Episode:         item.ep.EpisodeNumber,
			SeasonSpecified: true,
			RootFolderPath:  item.series.RootFolderPath,
			Trigger:         TriggerQualityWatch,
		})
		label := item.series.Title
		switch {
		case err != nil:
			log.Printf("series upgrade-watch: %q S%02dE%02d — auto-grab failed (%T)", label, item.ep.SeasonNumber, item.ep.EpisodeNumber, rootCause(err))
		case out.Gated:
			log.Printf("series upgrade-watch: usenet auto-grab is switched off — abandoning this cycle")
			return attempts
		case out.AlreadyGrabbing:
			log.Printf("series upgrade-watch: %q S%02dE%02d is already being downloaded", label, item.ep.SeasonNumber, item.ep.EpisodeNumber)
		case out.Grabbed:
			log.Printf("series upgrade-watch: %q S%02dE%02d dispatched", label, item.ep.SeasonNumber, item.ep.EpisodeNumber)
			tagQualityWatchOrigin(ctx, deps.GrabsStore, out.GrabID)
		default:
			log.Printf("series upgrade-watch: %q S%02dE%02d has no qualifying candidate yet — parked for re-search", label, item.ep.SeasonNumber, item.ep.EpisodeNumber)
			tagQualityWatchOrigin(ctx, deps.GrabsStore, out.GrabID)
		}
	}
	return attempts
}

func activeSeriesUpgradeWatchKeys(ctx context.Context, deps AutoGrabDeps) map[seriesEpisodeKey]bool {
	if deps.GrabsStore == nil {
		return nil
	}
	list, err := deps.GrabsStore.List(ctx, mode.Series)
	if err != nil {
		log.Printf("series upgrade-watch: listing series grabs for prefilter: %v", err)
		return nil
	}
	active := make(map[seriesEpisodeKey]bool, len(list))
	for _, g := range list {
		if g.TMDBID <= 0 || !g.SeasonSpecified {
			continue
		}
		switch g.Status {
		case grabs.Queued, grabs.Downloading, grabs.Completed, grabs.PendingRetry:
			active[seriesEpisodeKey{tmdbID: g.TMDBID, season: g.SeasonNumber, episode: g.EpisodeNumber}] = true
		}
	}
	return active
}
