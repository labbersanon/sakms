package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/settings"
)

// discoverRowHiddenSettingKey maps a Discover screen to its settings.Store
// key for the per-screen set of currently-hidden structural row keys. Like
// discoverRowOrderSettingKey, this is a thin wrapper over the existing flat
// key/value settings store — the value is only ever one JSON array of stable
// string keys. This is a sibling of, not an extension to, the row-order key:
// its reconciliation semantics differ ("absent-from-list means visible",
// there is no append-missing concept), so it gets its own key and DTO rather
// than folding hidden state into the order payload.
var discoverRowHiddenSettingKey = map[string]string{
	"mainstream": "discover_row_hidden_mainstream",
	"adult":      "discover_row_hidden_adult",
}

// getRowHiddenHandler is GET /api/discover/row-hidden/{screen} — {screen}
// must be "mainstream" or "adult". Returns an empty key list (not a 404) when
// nothing has been saved yet — a fresh install has no rows hidden, which is a
// normal, not an error, state.
func getRowHiddenHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := discoverRowHiddenSettingKey[r.PathValue("screen")]
		if !ok {
			http.Error(w, "screen path parameter must be \"mainstream\" or \"adult\"", http.StatusBadRequest)
			return
		}

		resp := apidto.RowHiddenResponse{Keys: []string{}}
		stored, err := settingsStore.Get(r.Context(), key)
		if err != nil {
			if !errors.Is(err, settings.ErrNotFound) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if err := json.Unmarshal([]byte(stored), &resp.Keys); err != nil {
			http.Error(w, "stored row hidden state is corrupt: "+err.Error(), http.StatusInternalServerError)
			return
		}

		writeJSON(w, resp)
	}
}

// putRowHiddenHandler is PUT /api/discover/row-hidden/{screen} — saves the
// full replacement set of hidden structural row keys as one JSON-encoded
// settings value.
func putRowHiddenHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := discoverRowHiddenSettingKey[r.PathValue("screen")]
		if !ok {
			http.Error(w, "screen path parameter must be \"mainstream\" or \"adult\"", http.StatusBadRequest)
			return
		}

		var req apidto.RowHiddenRequest
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
