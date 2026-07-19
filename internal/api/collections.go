package api

import (
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
)

type collectionSummaryResponse struct {
	TMDBCollectionID int    `json:"tmdbCollectionId"`
	Name             string `json:"name"`
	Count            int    `json:"count"`
}

// collectionsHandler serves GET /api/modes/{mode}/collections — the list of
// TMDB franchise collections that at least one tracked movie belongs to, with
// the count of tracked movies per collection. Movies-only: Series and Adult
// have no equivalent TMDB collection concept.
func collectionsHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mode.Mode(r.PathValue("mode")) != mode.Movies {
			http.Error(w, "collections are Movies-only", http.StatusBadRequest)
			return
		}
		summaries, err := libStore.ListCollections(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]collectionSummaryResponse, len(summaries))
		for i, cs := range summaries {
			out[i] = collectionSummaryResponse{
				TMDBCollectionID: cs.TMDBCollectionID,
				Name:             cs.Name,
				Count:            cs.Count,
			}
		}
		writeJSON(w, out)
	}
}
