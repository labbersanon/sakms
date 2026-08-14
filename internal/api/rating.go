package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// putItemRatingHandler is PUT /api/modes/{mode}/items/{itemId}/rating for
// Movies and Series. Adult 400s here the same way the generic tag routes do —
// Adult ratings live on the scene route below.
func putItemRatingHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		itemID, ok := parseIntPathValue(w, r, "itemId")
		if !ok {
			return
		}
		rating, ok := decodeRating(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		var err error
		switch m {
		case mode.Movies:
			err = libStore.SetItemRating(ctx, int64(itemID), rating)
		case mode.Series:
			err = libStore.SetSeriesRating(ctx, int64(itemID), rating)
		case mode.Adult:
			http.Error(w, "adult ratings are managed per scene now — use PUT /api/modes/adult/scenes/{sceneId}/rating", http.StatusBadRequest)
			return
		default:
			http.Error(w, "mode \""+string(m)+"\": unknown mode", http.StatusBadRequest)
			return
		}
		writeRatingResult(w, err)
	}
}

// putSceneRatingHandler is PUT /api/modes/adult/scenes/{sceneId}/rating.
func putSceneRatingHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sceneID, ok := parseIntPathValue(w, r, "sceneId")
		if !ok {
			return
		}
		rating, ok := decodeRating(w, r)
		if !ok {
			return
		}
		writeRatingResult(w, libStore.SetSceneRating(r.Context(), int64(sceneID), rating))
	}
}

func decodeRating(w http.ResponseWriter, r *http.Request) (int, bool) {
	var req apidto.LibraryRatingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return 0, false
	}
	return req.Rating, true
}

func writeRatingResult(w http.ResponseWriter, err error) {
	if err == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch {
	case errors.Is(err, library.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, library.ErrInvalidRating):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
