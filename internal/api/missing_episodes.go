package api

import (
	"errors"
	"net/http"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/library"
)

// missingEpisodesByTMDBHandler backs
// GET /api/modes/series/library/tmdb/{tmdbId}/missing-episodes — the episode
// list behind Requests' series detail page.
func missingEpisodesByTMDBHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmdbID, ok := tmdbIDPathValue(w, r)
		if !ok {
			return
		}
		series, err := libStore.GetSeriesByTMDBID(r.Context(), tmdbID)
		if err != nil {
			if errors.Is(err, library.ErrNotFound) {
				http.Error(w, "series not in library", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		missing, err := libStore.MissingEpisodes(r.Context(), series.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := apidto.MissingEpisodesResponse{
			TMDBID:   series.TMDBID,
			Title:    series.Title,
			Episodes: make([]apidto.MissingEpisodeItem, 0, len(missing)),
		}
		for _, ep := range missing {
			out.Episodes = append(out.Episodes, apidto.MissingEpisodeItem{
				SeasonNumber:  ep.SeasonNumber,
				EpisodeNumber: ep.EpisodeNumber,
				Title:         ep.Title,
				AirDate:       ep.AirDate,
			})
		}
		writeJSON(w, out)
	}
}
