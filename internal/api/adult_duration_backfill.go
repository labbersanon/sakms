package api

// TEMPORARY one-off migration endpoint — same "built once, removed once
// done" precedent as internal/sonarrimport/internal/whisparrimport and the
// poster backfill that preceded this one. Corrects entity_duration_seconds
// on every already-cached tpdb-sourced Scene/Movie row still stuck at 0 from
// before identify.BoxSearcher.resolveTPDBDuration existed (see that
// function's doc comment for the live bug this fixes — TPDB's search
// endpoint sometimes returns duration:0 for a scene that genuinely has a
// real duration on file). Remove this file and its one route registration
// in internal/api/handler.go once run successfully against production.

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/adultnewest"
	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

type backfillAdultDurationsResponse struct {
	Checked int      `json:"checked"`
	Updated int      `json:"updated"`
	Errors  []string `json:"errors,omitempty"`
}

// backfillAdultDurationsHandler is POST /api/modes/adult/newest-rows/backfill-durations.
func backfillAdultDurationsHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, releaseStore *adultnewest.ReleaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, mode.Adult)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.Identify == nil {
			http.Error(w, "adult identify pipeline isn't configured (needs an AI provider + TPDB)", http.StatusBadRequest)
			return
		}

		entities, err := releaseStore.ListZeroDurationTPDBScenes(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		resp := backfillAdultDurationsResponse{}
		for _, e := range entities {
			resp.Checked++
			seconds, err := sess.Identify.Boxes.RefreshSceneDuration(ctx, e.EntityID)
			if err != nil {
				log.Printf("backfillAdultDurations: entity %d (%s): %v", e.ID, e.EntityTitle, err)
				resp.Errors = append(resp.Errors, e.EntityTitle+": "+err.Error())
				continue
			}
			if seconds <= 0 {
				continue
			}
			if err := releaseStore.UpdateDuration(ctx, e.ID, seconds); err != nil {
				log.Printf("backfillAdultDurations: updating entity %d (%s): %v", e.ID, e.EntityTitle, err)
				resp.Errors = append(resp.Errors, e.EntityTitle+": "+err.Error())
				continue
			}
			resp.Updated++
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
