package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-09-19: optional Usenet staging outside the data volume (A3).
// Reason: some deployments put SAKMS_DATA_DIR on remote/iSCSI storage where heavy
//   NZB assemble/fsync storms can destablize the LUN; others are local disk and
//   must keep the default <dataDir>/downloads path. Opt-in Advanced setting only.
// Troubleshooting: iSCSI SCSI-offline during Usenet drains; media-admin 404.
// Review if: per-engine staging splits further (torrent stays on downloader_staging_dir).
// Related: plan_iscsi_sakms_outage_prevention_20260919.md A3

const (
	UsenetOffDataStagingEnabledKey = "usenet_off_data_staging_enabled"
	UsenetOffDataStagingDirKey     = "usenet_off_data_staging_dir"
)

type usenetOffDataStagingResponse struct {
	Enabled bool   `json:"enabled"`
	Dir     string `json:"dir"`
}

type usenetOffDataStagingRequest struct {
	Enabled bool   `json:"enabled"`
	Dir     string `json:"dir"`
}

func getUsenetOffDataStagingHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		enabled, err := settingsStore.GetBool(ctx, UsenetOffDataStagingEnabledKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		dir, err := settingsStore.Get(ctx, UsenetOffDataStagingDirKey)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, usenetOffDataStagingResponse{Enabled: enabled, Dir: dir})
	}
}

func putUsenetOffDataStagingHandler(settingsStore *settings.Store, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req usenetOffDataStagingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		dir := filepath.Clean(req.Dir)
		if req.Enabled {
			if dir == "" || dir == "." {
				http.Error(w, "dir is required when off-data staging is enabled", http.StatusBadRequest)
				return
			}
			if !filepath.IsAbs(dir) {
				http.Error(w, "dir must be an absolute path", http.StatusBadRequest)
				return
			}
			if msg := validateStagingDir(dir); msg != "" {
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		} else {
			// Keep stored path for when the operator re-enables; empty is fine.
			if dir != "" && dir != "." && !filepath.IsAbs(dir) {
				http.Error(w, "dir must be an absolute path when set", http.StatusBadRequest)
				return
			}
			if dir == "." {
				dir = ""
			}
		}

		ctx := r.Context()
		if err := settingsStore.Set(ctx, UsenetOffDataStagingEnabledKey, strconv.FormatBool(req.Enabled)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(ctx, UsenetOffDataStagingDirKey, dir); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if nzb != nil && req.Enabled {
			if err := nzb.SetStagingDir(dir); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
		}
		// When disabling, live StagingDir stays until restart — operator must
		// restart (or empty active downloads + re-PUT with an explicit path via
		// the shared downloader staging) to move back. Documented in UI copy.
		w.WriteHeader(http.StatusNoContent)
	}
}
