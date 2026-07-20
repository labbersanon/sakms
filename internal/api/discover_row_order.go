package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/settings"
)

// discoverRowOrderSettingKey maps a Discover screen to its settings.Store
// key — NOT a new table, just a thin wrapper over the existing flat
// key/value settings store (see settings.Store's package doc), since the
// value is only ever one JSON array of stable string keys. Deliberately not
// validated against a fixed known-id set the way rssfeeds.Store.Reorder is:
// this list mixes static built-in keys (e.g. "builtin:trending-movies")
// with dynamic ids that can be deleted later (e.g. "slider:4", "rssfeed:2")
// — it's a best-effort display hint, not an invariant-enforcing resource.
// The frontend appends any key it knows about but doesn't find in the
// stored order to the end (a new/never-ordered row), and simply skips any
// stored key that no longer resolves to anything live (e.g. a deleted
// slider/feed).
var discoverRowOrderSettingKey = map[string]string{
	"mainstream": "discover_row_order_mainstream",
	"adult":      "discover_row_order_adult",
}

// getRowOrderHandler is GET /api/discover/row-order/{screen} — {screen} must
// be "mainstream" or "adult". Returns an empty key list (not a 404) when
// nothing has been saved yet — a fresh install simply has no display-order
// hint, which is a normal, not an error, state.
func getRowOrderHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := discoverRowOrderSettingKey[r.PathValue("screen")]
		if !ok {
			http.Error(w, "screen path parameter must be \"mainstream\" or \"adult\"", http.StatusBadRequest)
			return
		}

		resp := apidto.RowOrderResponse{Keys: []string{}}
		stored, err := settingsStore.Get(r.Context(), key)
		if err != nil {
			if !errors.Is(err, settings.ErrNotFound) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if err := json.Unmarshal([]byte(stored), &resp.Keys); err != nil {
			http.Error(w, "stored row order is corrupt: "+err.Error(), http.StatusInternalServerError)
			return
		}

		writeJSON(w, resp)
	}
}

// putRowOrderHandler is PUT /api/discover/row-order/{screen} — saves the
// full replacement key order as one JSON-encoded settings value.
func putRowOrderHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := discoverRowOrderSettingKey[r.PathValue("screen")]
		if !ok {
			http.Error(w, "screen path parameter must be \"mainstream\" or \"adult\"", http.StatusBadRequest)
			return
		}

		var req apidto.RowOrderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		encoded, err := json.Marshal(req.Keys)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(r.Context(), key, string(encoded)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
