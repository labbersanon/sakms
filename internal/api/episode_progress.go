package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/library"
)

const episodeWatchedFraction = 0.9

// putSeriesEpisodeProgressHandler writes one episode's in-app play head.
// Claude 2026-09-24: Series-only; episode must belong to {id}.
// Reason: timeupdate/pause/ended from TrackedPlayback.
// Troubleshooting: a sibling series' episodeId is 404, not a write.
func putSeriesEpisodeProgressHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		seriesID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || seriesID < 1 {
			http.Error(w, "invalid tracked id", http.StatusBadRequest)
			return
		}
		episodeID, err := strconv.ParseInt(r.PathValue("episodeId"), 10, 64)
		if err != nil || episodeID < 1 {
			http.Error(w, "invalid episodeId", http.StatusBadRequest)
			return
		}
		var req apidto.EpisodeProgressRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid progress body", http.StatusBadRequest)
			return
		}
		ep, err := libStore.GetEpisodeByID(r.Context(), episodeID)
		if err != nil {
			if errors.Is(err, library.ErrNotFound) {
				http.Error(w, "no tracked series video with that id", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ep.SeriesID != seriesID {
			http.Error(w, "no tracked series video with that id", http.StatusNotFound)
			return
		}
		watched := req.Watched
		if req.DurationSeconds > 0 && req.PositionSeconds/req.DurationSeconds >= episodeWatchedFraction {
			watched = true
		}
		if _, err := libStore.UpsertEpisodeProgress(r.Context(), library.EpisodeProgress{
			EpisodeID:       episodeID,
			PositionSeconds: req.PositionSeconds,
			DurationSeconds: req.DurationSeconds,
			Watched:         watched,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
