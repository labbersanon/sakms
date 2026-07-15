package api

// TEMPORARY diagnostic route — added 2026-07-15 to settle a specific
// question live, against production TPDB credentials, without exposing the
// API key anywhere: does TPDB's search endpoint (GET /scenes?q=) return
// `duration` for results the same way GET /scenes/{id} does? 46 of 51
// cached Adult newest-rows scene entities have entity_duration_seconds=0
// despite TPDB's own site showing a real duration for at least one of
// them — this compares both endpoints' raw duration for that exact scene
// to find out whether the gap is a search-endpoint payload omission or a
// decode bug. Remove this file and its route registration in handler.go
// once the question is answered and the real fix is implemented.

import (
	"encoding/json"
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

type tpdbDiagResponse struct {
	SearchDuration int    `json:"searchDuration"`
	SearchTitle    string `json:"searchTitle"`
	SearchErr      string `json:"searchErr,omitempty"`
	ByIDDuration   int    `json:"byIdDuration"`
	ByIDTitle      string `json:"byIdTitle"`
	ByIDErr        string `json:"byIdErr,omitempty"`
}

// tpdbDiagHandler is GET /api/modes/adult/tpdb-diag — hardcoded to the one
// known-broken scene (TPDB id 11034171, "June 2026 Flavor Of The Month
// Poppy Applegate", studio "Step Siblings Caught") found live in this
// session; not a general-purpose diagnostic endpoint.
func tpdbDiagHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, mode.Adult)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.Identify == nil || sess.Identify.Boxes == nil {
			http.Error(w, "adult identify pipeline isn't configured", http.StatusBadRequest)
			return
		}

		resp := tpdbDiagResponse{}
		if m, err := sess.Identify.Boxes.SearchTPDB(ctx, "June 2026 Flavor Of The Month Poppy Applegate", "Step Siblings Caught"); err != nil {
			resp.SearchErr = err.Error()
		} else if m != nil {
			resp.SearchDuration = m.RuntimeSeconds
			resp.SearchTitle = m.Title
		} else {
			resp.SearchErr = "no match returned"
		}

		if scene, err := sess.Identify.Boxes.GetSceneByIDDiag(ctx, "11034171"); err != nil {
			resp.ByIDErr = err.Error()
		} else if scene != nil {
			resp.ByIDDuration = scene.Duration
			resp.ByIDTitle = scene.Title
		} else {
			resp.ByIDErr = "no scene returned"
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
