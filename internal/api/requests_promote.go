package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
)

// promoteRequestReason is operator-facing copy only. Nothing branches on it;
// DueForRetry keys off retry_after / hold_until.
const promoteRequestReason = "operator promoted to top of schedule"

// promoteRequestHandler backs POST /api/requests/promote — move a Pending /
// Pending Retry / Scheduled grab to the front of the retry schedule. See
// grabs.PromoteToFront for what it deliberately leaves alone.
func promoteRequestHandler(grabsStore *grabs.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.PromoteRequestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.GrabID <= 0 {
			http.Error(w, "grabId is required", http.StatusBadRequest)
			return
		}
		if err := grabsStore.PromoteToFront(r.Context(), req.GrabID, time.Now(), promoteRequestReason); err != nil {
			if errors.Is(err, grabs.ErrNotFound) {
				http.Error(w, "grab not found or not eligible to promote", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
