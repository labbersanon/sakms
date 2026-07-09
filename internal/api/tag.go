package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sak/internal/connections"
	"github.com/curtiswtaylorjr/sak/internal/mode"
	"github.com/curtiswtaylorjr/sak/internal/settings"
	"github.com/curtiswtaylorjr/sak/internal/tag"
)

// listTagsHandler returns {mode}'s current tag vocabulary, straight from
// the live app — the same import-not-duplicate principle Naming already
// follows.
func listTagsHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		tags, err := tag.Vocabulary(ctx, sess)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tags)
	}
}

type addItemTagRequest struct {
	Label string `json:"label"`
}

// addItemTagHandler assigns a tag to one tracked item, creating the tag
// upstream first if it doesn't already exist — a single, immediately-
// committed action, not staged through the proposals queue (see
// internal/tag's doc comment for why Tag doesn't follow the Scan/Apply
// shape the other three workflows do).
func addItemTagHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
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

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := tag.Add(ctx, sess, itemID, req.Label); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// removeItemTagHandler unassigns a tag from one tracked item.
func removeItemTagHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		itemID, ok := parseIntPathValue(w, r, "itemId")
		if !ok {
			return
		}
		tagID, ok := parseIntPathValue(w, r, "tagId")
		if !ok {
			return
		}
		ctx := r.Context()

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := tag.Remove(ctx, sess, itemID, tagID); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
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
