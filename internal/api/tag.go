package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// libraryTagEntry is Movies' vocabulary shape — a local tag is just a
// string with no numeric id, so ID and Label are always the same value.
// This keeps the response shape compatible with the frontend's existing
// {id, label} handling (id-keyed lookups, matching against
// libraryTrackedItem.Tags) regardless of which mode it's browsing.
type libraryTagEntry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// listTagsHandler returns {mode}'s current tag vocabulary for Movies/Series —
// entirely local (libStore's TagVocabulary/SeriesTagVocabulary, distinct tags
// already in use, imported live from usage). Adult's tags moved to the
// scene-tag routes when Whisparr was eliminated (Stage 4), so this old
// *arr-item route 400s for Adult; any other mode string is simply unknown.
func listTagsHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		var vocab []string
		var err error
		switch m {
		case mode.Movies:
			vocab, err = libStore.TagVocabulary(ctx, m)
		case mode.Series:
			vocab, err = libStore.SeriesTagVocabulary(ctx)
		case mode.Adult:
			// Adult owns its own library now (Whisparr eliminated, Stage 4), so
			// there is no *arr Tags resource to browse. Adult tags are
			// scene-level: fail cleanly and point at the scene-tag route.
			http.Error(w, "adult tags are managed per scene now — use /api/modes/adult/scenes/tags and /api/modes/adult/scenes/{sceneId}/tags", http.StatusBadRequest)
			return
		default:
			http.Error(w, "mode \""+string(m)+"\": unknown mode", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]libraryTagEntry, len(vocab))
		for i, label := range vocab {
			out[i] = libraryTagEntry{ID: label, Label: label}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

type addItemTagRequest struct {
	Label string `json:"label"`
}

// addItemTagHandler assigns a tag to one tracked item — a single,
// immediately-committed action, not staged through the proposals queue like
// Rename/Purge/Dedup: it's already a single, deliberate, atomic action a
// human takes (pick a tag, click it), the same shape as Settings' own
// Save/Delete actions. For Movies/Series, itemId is a library item/series'
// own id and this writes straight to libStore.
func addItemTagHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		itemID, ok := parseIntPathValue(w, r, "itemId")
		if !ok {
			return
		}
		var req addItemTagRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Label == "" {
			http.Error(w, "label is required", http.StatusBadRequest)
			return
		}
		ctx := r.Context()

		switch m {
		case mode.Movies:
			if err := libStore.AddTag(ctx, int64(itemID), req.Label); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case mode.Series:
			if err := libStore.AddSeriesTag(ctx, int64(itemID), req.Label); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case mode.Adult:
			// Adult tags are scene-level now (Whisparr eliminated, Stage 4) —
			// this old *arr-item route no longer applies. Point at the
			// scene-tag route.
			http.Error(w, "adult tags are managed per scene now — use POST /api/modes/adult/scenes/{sceneId}/tags", http.StatusBadRequest)
		default:
			http.Error(w, "mode \""+string(m)+"\": unknown mode", http.StatusBadRequest)
		}
	}
}

// removeItemTagHandler unassigns a tag from one tracked item. The route's
// {tagId} path segment means different things per mode: a numeric Servarr
// tag id for Adult, or the tag string itself for Movies/Series (a local tag
// has no numeric id at all — it's just a string in library_tags/
// library_series_tags).
func removeItemTagHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		itemID, ok := parseIntPathValue(w, r, "itemId")
		if !ok {
			return
		}
		ctx := r.Context()

		switch m {
		case mode.Movies:
			tagLabel := r.PathValue("tagId") // string label for Movies, not a numeric Servarr tag id
			if err := libStore.RemoveTag(ctx, int64(itemID), tagLabel); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case mode.Series:
			tagLabel := r.PathValue("tagId") // string label for Series too, same reasoning as Movies
			if err := libStore.RemoveSeriesTag(ctx, int64(itemID), tagLabel); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case mode.Adult:
			// Adult tags are scene-level now (Whisparr eliminated, Stage 4) —
			// this old *arr-item route no longer applies. Point at the
			// scene-tag route.
			http.Error(w, "adult tags are managed per scene now — use DELETE /api/modes/adult/scenes/{sceneId}/tags/{tagId}", http.StatusBadRequest)
		default:
			http.Error(w, "mode \""+string(m)+"\": unknown mode", http.StatusBadRequest)
		}
	}
}

// Adult scene tags are a parallel, fully library-backed path — the exact
// Movies/Series precedent (libStore called directly, no *arr app), just
// keyed on a library_scenes row instead of a library item/series. They live
// under /api/modes/adult/scenes/... rather than the {mode}/items/... routes,
// which 400 for Adult (the generic *arr-item tag routes have no Adult backing
// now). A scene id is a library_scenes.id (int64); a tag is just a string, so
// there's no numeric-tag-id step and DELETE's {tagId} segment is the label
// itself, same as Movies/Series.

// sceneTagVocabularyHandler returns Adult's scene-tag vocabulary — every
// distinct tag any scene currently uses. Sibling of listTagsHandler's
// Movies/Series branch, returning the same {id, label} shape.
func sceneTagVocabularyHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vocab, err := libStore.SceneTagVocabulary(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]libraryTagEntry, len(vocab))
		for i, label := range vocab {
			out[i] = libraryTagEntry{ID: label, Label: label}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// listSceneTagsHandler returns one scene's assigned tags as a string list.
func listSceneTagsHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sceneID, ok := parseIntPathValue(w, r, "sceneId")
		if !ok {
			return
		}
		tags, err := libStore.SceneTags(r.Context(), int64(sceneID))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tags)
	}
}

// addSceneTagHandler assigns a tag to one scene — a single, immediately-
// committed action (not staged through proposals), matching addItemTagHandler.
func addSceneTagHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sceneID, ok := parseIntPathValue(w, r, "sceneId")
		if !ok {
			return
		}
		var req addItemTagRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Label == "" {
			http.Error(w, "label is required", http.StatusBadRequest)
			return
		}
		if err := libStore.AddSceneTag(r.Context(), int64(sceneID), req.Label); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// removeSceneTagHandler unassigns a tag from one scene. The {tagId} segment
// is the tag string itself (a local tag has no numeric id), same as
// removeItemTagHandler's Movies/Series branch.
func removeSceneTagHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sceneID, ok := parseIntPathValue(w, r, "sceneId")
		if !ok {
			return
		}
		if err := libStore.RemoveSceneTag(r.Context(), int64(sceneID), r.PathValue("tagId")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func parseIntPathValue(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	v, err := strconv.Atoi(r.PathValue(name))
	if err != nil {
		http.Error(w, "invalid "+name, http.StatusBadRequest)
		return 0, false
	}
	return v, true
}
