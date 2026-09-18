package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/xferlimit"
)

// Claude 2026-09-18: global download rate cap (Mbps) for torrent + Usenet.
// Reason: one shared budget across both engines; Advanced → Global UI.
// Troubleshooting: slow downloads with Mbps>0 → expected; 0 = unlimited.
// Review if: per-engine caps return.

const (
	DownloadRateLimitMbpsKey     = "download_rate_limit_mbps"
	DownloadRateLimitMbpsDefault = 0 // unlimited
)

var (
	globalRateCapMu sync.RWMutex
	globalRateCap   *xferlimit.Cap
)

// SetGlobalRateCap registers the process-wide Cap for HTTP handlers.
func SetGlobalRateCap(c *xferlimit.Cap) {
	globalRateCapMu.Lock()
	globalRateCap = c
	globalRateCapMu.Unlock()
}

func getGlobalRateCap() *xferlimit.Cap {
	globalRateCapMu.RLock()
	defer globalRateCapMu.RUnlock()
	return globalRateCap
}

type downloadRateLimitResponse struct {
	Mbps int `json:"mbps"`
}

type downloadRateLimitRequest struct {
	Mbps int `json:"mbps"`
}

// LoadDownloadRateLimitMbps reads the global Mbps setting, soft-migrating from
// the legacy torrent-only bytes/sec key when the global key is unset.
func LoadDownloadRateLimitMbps(ctx context.Context, settingsStore *settings.Store) (int, error) {
	raw, err := settingsStore.Get(ctx, DownloadRateLimitMbpsKey)
	if err == nil && raw != "" {
		n, aerr := strconv.Atoi(raw)
		if aerr != nil {
			return DownloadRateLimitMbpsDefault, nil
		}
		if n < 0 {
			n = 0
		}
		return n, nil
	}
	if err != nil && err != settings.ErrNotFound {
		return 0, err
	}
	// Soft-migrate: torrent_download_rate_limit_bytes → Mbps.
	bps, berr := getSettingInt(ctx, settingsStore, TorrentDownloadRateLimitKey, TorrentDefaultDownloadRateLimit)
	if berr != nil {
		return DownloadRateLimitMbpsDefault, berr
	}
	return xferlimit.BytesPerSecToMbps(bps), nil
}

func getDownloadRateLimitHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mbps, err := LoadDownloadRateLimitMbps(r.Context(), settingsStore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if c := getGlobalRateCap(); c != nil {
			mbps = c.Mbps()
		}
		writeJSON(w, downloadRateLimitResponse{Mbps: mbps})
	}
}

func putDownloadRateLimitHandler(
	settingsStore *settings.Store,
	onApply func(mbps int) error,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req downloadRateLimitRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Mbps < 0 {
			http.Error(w, "mbps must be zero (unlimited) or greater", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if err := settingsStore.Set(ctx, DownloadRateLimitMbpsKey, strconv.Itoa(req.Mbps)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Mirror into legacy torrent key so older readers stay consistent.
		bps := xferlimit.MbpsToBytesPerSec(req.Mbps)
		_ = settingsStore.Set(ctx, TorrentDownloadRateLimitKey, strconv.Itoa(bps))
		if onApply != nil {
			if err := onApply(req.Mbps); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if c := getGlobalRateCap(); c != nil {
			c.SetMbps(req.Mbps)
		}
		writeJSON(w, downloadRateLimitResponse{Mbps: req.Mbps})
	}
}
