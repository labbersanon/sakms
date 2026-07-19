package api

import (
	"encoding/json"
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

// adultNewestScanIntervalKey is the settings key for the background
// adultnewest scan cadence, in whole seconds. Mirrors
// recheckIntervalKey's import-avoidance rationale by value rather than
// importing internal/adultnewest's own copy of this constant, for the same
// reason: this endpoint's build shouldn't depend on that package.
//
// Unlike recheck, an UNSET key here means the job's actual default
// (adultNewestScanDefaultSeconds, 24h — an explicit operator directive,
// 2026-07-15), not off — see adultnewest.IntervalSettingKey's doc comment
// for the full rationale. This GET handler must mirror
// adultnewest.LoadInterval's unset-vs-explicit-zero distinction exactly, or
// Settings would show "0" while the background job is actually running
// every 24h — a real bug caught during this feature's own live deploy
// verification, not a hypothetical.
const adultNewestScanIntervalKey = "adult_newest_scan_interval_seconds"

// adultNewestScanDefaultSeconds duplicates adultnewest.defaultIntervalHours
// (in seconds) for the same import-avoidance reason as the key above.
const adultNewestScanDefaultSeconds = 24 * 60 * 60

type adultNewestScanIntervalResponse struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

type adultNewestScanIntervalRequest struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

// getAdultNewestScanIntervalHandler returns the configured scan interval in
// seconds — adultNewestScanDefaultSeconds when the key was never explicitly
// saved, 0 when an operator explicitly saved "0" (turning the job off), and
// whatever positive value was last saved otherwise. See this file's package
// doc for why the unset case can't just return 0 here. Parsing/degrade logic
// lives in loadIntervalSeconds (interval.go), shared with recheck.go and
// entity_sync.go's equivalents.
func getAdultNewestScanIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secs, err := loadIntervalSeconds(r.Context(), settingsStore, adultNewestScanIntervalKey, adultNewestScanDefaultSeconds)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, adultNewestScanIntervalResponse{IntervalSeconds: secs})
	}
}

// putAdultNewestScanIntervalHandler stores the scan interval in seconds. 0
// disables the job; a negative value is rejected. Validation/persistence
// logic lives in storeIntervalSeconds (interval.go).
func putAdultNewestScanIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req adultNewestScanIntervalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		badRequest, err := storeIntervalSeconds(r.Context(), settingsStore, adultNewestScanIntervalKey, req.IntervalSeconds)
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
