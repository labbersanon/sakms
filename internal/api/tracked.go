package api

import (
	"encoding/json"
	"net/http"

	"github.com/curtiswtaylorjr/sak/internal/connections"
	"github.com/curtiswtaylorjr/sak/internal/mode"
	"github.com/curtiswtaylorjr/sak/internal/settings"
)

// listTrackedHandler returns every item {mode}'s app currently tracks —
// read-only, straight from the live app. Backs the Tag workflow's item
// picker (there's no other way to browse what's trackable to assign/remove
// a tag on) and is generically useful anywhere a UI needs real item
// context instead of guessing an ID.
func listTrackedHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		tracked, err := sess.Servarr.AllTracked(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tracked)
	}
}
