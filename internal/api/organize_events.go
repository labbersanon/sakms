package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/organizeevents"
)

// NewOrganizeEventsMux returns GET /api/organize/events?workflow=&limit=.
// Mounted outside NewMux (same precedent as apikey/recheck triggers) so the
// events store does not grow NewMux's parameter list.
func NewOrganizeEventsMux(store *organizeevents.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/organize/events", func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "organize events unavailable", http.StatusServiceUnavailable)
			return
		}
		workflow := r.URL.Query().Get("workflow")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, err := store.List(r.Context(), workflow, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})
	return mux
}
