package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-09-01: usenet_max_concurrent_downloads setting + live apply.
// Reason: operators need NZB job concurrency separate from per-server MaxConns;
//   default 1; must not count PAR2 (enforced in usenet.Manager.runDownload).
// Troubleshooting: many NZBs hammering the provider; one repair blocking others.
// Review if: repair concurrency gets its own knob.

const (
	UsenetMaxConcurrentDownloadsKey = "usenet_max_concurrent_downloads"
)

type usenetMaxConcurrentDownloadsResponse struct {
	MaxConcurrentDownloads int `json:"maxConcurrentDownloads"`
}

type usenetMaxConcurrentDownloadsRequest struct {
	MaxConcurrentDownloads int `json:"maxConcurrentDownloads"`
}

// getUsenetMaxConcurrentDownloadsHandler reports the NZB job concurrency cap.
func getUsenetMaxConcurrentDownloadsHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n, err := getSettingInt(r.Context(), settingsStore, UsenetMaxConcurrentDownloadsKey, usenet.DefaultMaxConcurrentDownloads)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, usenetMaxConcurrentDownloadsResponse{MaxConcurrentDownloads: n})
	}
}

// putUsenetMaxConcurrentDownloadsHandler stores the cap and applies it live to
// the running usenet Manager when one is wired. nzb may be nil in tests.
func putUsenetMaxConcurrentDownloadsHandler(settingsStore *settings.Store, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req usenetMaxConcurrentDownloadsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.MaxConcurrentDownloads < 1 {
			http.Error(w, "maxConcurrentDownloads must be at least 1", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if err := settingsStore.Set(ctx, UsenetMaxConcurrentDownloadsKey, strconv.Itoa(req.MaxConcurrentDownloads)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if nzb != nil {
			nzb.SetMaxConcurrentDownloads(req.MaxConcurrentDownloads)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
