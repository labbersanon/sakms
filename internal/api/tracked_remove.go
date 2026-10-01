package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/purge"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// deleteTrackedHandler is DELETE /api/modes/{mode}/tracked/{id}.
// Claude 2026-10-01: owned DetailPopup remove-from-library.
// Reason: operator-confirmed permanent file+row delete, same disk contract as Purge.
// Troubleshooting: nested Modal would close both overlays on one backdrop click.
// Review if: Purge Apply becomes a wrapper around purge.RemoveOwned.
func deleteTrackedHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, libStore *library.Store, grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		switch m {
		case mode.Movies, mode.Series, mode.Adult:
		default:
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			http.Error(w, "invalid tracked id", http.StatusBadRequest)
			return
		}
		var req apidto.RemoveTrackedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid remove body", http.StatusBadRequest)
			return
		}

		ctx := r.Context()
		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			if errors.Is(err, sectionlock.ErrSectionLocked) {
				writeSectionLocked(w, sectionlock.SectionAdultContent)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		tmdbID := trackedTMDBID(ctx, libStore, m, id)
		changes, gone, err := purge.RemoveOwned(ctx, libStore, m, id, purge.RemoveSpec{
			EntireSeries: req.EntireSeries,
			Seasons:      req.Seasons,
		})
		sess.NotifyPlayers(ctx, changes)
		if err != nil {
			switch {
			case errors.Is(err, library.ErrNotFound):
				http.Error(w, err.Error(), http.StatusNotFound)
			case errors.Is(err, purge.ErrRemoveSpec):
				http.Error(w, err.Error(), http.StatusBadRequest)
			default:
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		// Claude 2026-10-01: delete also un-monitors, same as flipping a season off.
		// Reason: leftover pending_retry grabs kept the title on Requests / Monitored.
		// Troubleshooting: remove a show, air-date retries still re-search it.
		// Review if: in-flight operator grabs should be cancelled too (they are not).
		unmonitorAfterLibraryRemove(ctx, grabsStore, m, tmdbID, req.Seasons, gone)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apidto.RemoveTrackedResponse{Gone: gone})
	}
}

func trackedTMDBID(ctx context.Context, libStore *library.Store, m mode.Mode, id int64) int {
	switch m {
	case mode.Movies:
		item, err := libStore.Get(ctx, id)
		if err != nil {
			return 0
		}
		return item.TMDBID
	case mode.Series:
		series, err := libStore.GetSeries(ctx, id)
		if err != nil {
			return 0
		}
		return series.TMDBID
	default:
		return 0
	}
}

func unmonitorAfterLibraryRemove(ctx context.Context, grabsStore *grabs.Store, m mode.Mode, tmdbID int, seasons []int, gone bool) {
	if grabsStore == nil || tmdbID <= 0 {
		return
	}
	switch m {
	case mode.Movies:
		cancelQualityWatchRetries(ctx, grabsStore, mode.Movies, tmdbID)
	case mode.Series:
		if gone {
			cancelQualityWatchRetries(ctx, grabsStore, mode.Series, tmdbID)
		}
		want := map[int]bool{}
		for _, n := range seasons {
			want[n] = true
		}
		if gone {
			list, err := grabsStore.List(ctx, mode.Series)
			if err != nil {
				log.Printf("remove-from-library: listing series grabs for un-monitor: %v", err)
				return
			}
			for _, g := range list {
				if g.TMDBID == tmdbID {
					want[g.SeasonNumber] = true
				}
			}
		}
		if len(want) > 0 {
			cancelAirDateRetriesForSeasons(ctx, grabsStore, tmdbID, want, time.Now())
		}
	}
}
