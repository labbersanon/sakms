package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-09-11: usenet segment-resume settings + live apply (Phase 2).
// Reason: operators need skip-completed-segments on by default, plus a
//   force-full rollback that clears sidecars without redeploying.
// Troubleshooting: journal "usenet: resume"; keys UsenetSegmentResumeEnabledKey /
//   UsenetSegmentResumeForceFullKey; staging file .sakms-resume.json
// Review if: resume policy moves into the downloader config document.

const (
	UsenetSegmentResumeEnabledKey   = "usenet_segment_resume_enabled"    // default true
	UsenetSegmentResumeForceFullKey = "usenet_segment_resume_force_full" // default false
)

type usenetSegmentResumeResponse struct {
	Enabled   bool `json:"enabled"`
	ForceFull bool `json:"forceFull"`
}

type usenetSegmentResumeRequest struct {
	Enabled   *bool `json:"enabled"`
	ForceFull *bool `json:"forceFull"`
}

func getUsenetSegmentResumeHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		enabled, err := settingsStore.GetBool(ctx, UsenetSegmentResumeEnabledKey, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		forceFull, err := settingsStore.GetBool(ctx, UsenetSegmentResumeForceFullKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, usenetSegmentResumeResponse{Enabled: enabled, ForceFull: forceFull})
	}
}

// putUsenetSegmentResumeHandler stores resume knobs and applies them live.
// nzb may be nil in tests. Omitted fields keep their stored (or default) values.
func putUsenetSegmentResumeHandler(settingsStore *settings.Store, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req usenetSegmentResumeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		enabled, err := settingsStore.GetBool(ctx, UsenetSegmentResumeEnabledKey, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		forceFull, err := settingsStore.GetBool(ctx, UsenetSegmentResumeForceFullKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		if req.ForceFull != nil {
			forceFull = *req.ForceFull
		}
		if err := settingsStore.Set(ctx, UsenetSegmentResumeEnabledKey, strconv.FormatBool(enabled)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(ctx, UsenetSegmentResumeForceFullKey, strconv.FormatBool(forceFull)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		appliedForceFull := forceFull
		if nzb != nil {
			nzb.SetResumePolicy(enabled, forceFull)
			// SetResumePolicy clears force-full after a one-shot sweep.
			if appliedForceFull {
				_, stillForce := nzb.ResumePolicy()
				forceFull = stillForce
				if !forceFull {
					if err := settingsStore.Set(ctx, UsenetSegmentResumeForceFullKey, "false"); err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
				}
			}
		} else if appliedForceFull {
			// No live manager (tests): still treat force-full as one-shot in settings.
			forceFull = false
			if err := settingsStore.Set(ctx, UsenetSegmentResumeForceFullKey, "false"); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, usenetSegmentResumeResponse{Enabled: enabled, ForceFull: forceFull})
	}
}
