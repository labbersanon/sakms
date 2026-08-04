package api

import (
	"encoding/json"
	"net/http"

	"github.com/labbersanon/sakms/internal/settings"
)

// discoverRefreshIntervalKey is the settings key for the Discover cache
// background refresh cadence, in whole seconds. Mirrored BY VALUE from
// discoverrefresh.IntervalSettingKey rather than importing that package,
// per the same import-avoidance convention adult_newest_scan.go
// (:10-23) and recheck.go (:9-18) already follow.
//
// Like adult-newest-scan and unlike recheck, an UNSET key here means the
// job's actual default (discoverRefreshDefaultSeconds, 24h), not off — see
// discoverrefresh.IntervalSettingKey's doc comment for the full rationale.
// This GET handler must mirror discoverrefresh.LoadInterval's
// unset-vs-explicit-zero distinction exactly, or Settings would show "0"
// while the background job is actually running every 24h — the same bug
// class adult_newest_scan.go:16-23 records as a real live-deploy defect.
const discoverRefreshIntervalKey = "discover_refresh_interval_seconds"

// discoverRefreshDefaultSeconds duplicates discoverrefresh.defaultIntervalHours
// (in seconds) for the same import-avoidance reason as the key above.
const discoverRefreshDefaultSeconds = 24 * 60 * 60

type discoverRefreshIntervalResponse struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

type discoverRefreshIntervalRequest struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

// getDiscoverRefreshIntervalHandler returns the configured refresh interval
// in seconds — discoverRefreshDefaultSeconds when the key was never
// explicitly saved, 0 when an operator explicitly saved "0" (turning the
// job off), and whatever positive value was last saved otherwise.
// Parsing/degrade logic lives in loadIntervalSeconds (interval.go), shared
// with recheck.go, entity_sync.go and adult_newest_scan.go's equivalents.
func getDiscoverRefreshIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secs, err := loadIntervalSeconds(r.Context(), settingsStore, discoverRefreshIntervalKey, discoverRefreshDefaultSeconds)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, discoverRefreshIntervalResponse{IntervalSeconds: secs})
	}
}

// putDiscoverRefreshIntervalHandler stores the refresh interval in seconds.
// 0 disables the job; a negative value is rejected. Validation/persistence
// logic lives in storeIntervalSeconds (interval.go).
func putDiscoverRefreshIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req discoverRefreshIntervalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		badRequest, err := storeIntervalSeconds(r.Context(), settingsStore, discoverRefreshIntervalKey, req.IntervalSeconds, 0)
		if err != nil {
			status := http.StatusInternalServerError
			if badRequest {
				status = http.StatusBadRequest
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
