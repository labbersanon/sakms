package api

import (
	"encoding/json"
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/adultnewest"
	"github.com/curtiswtaylorjr/sakms/internal/connections"
)

// TEMPORARY — a one-off migration tool, same "build once, remove once done"
// precedent as internal/sonarrimport/whisparrimport (see also this
// codebase's poster/duration backfill history for adult_newest_releases,
// already run and removed). Adding Performers required a new column
// (migration 0027), so every already-cached TPDB Scene/Movie row has an
// empty list until re-fetched by id — this endpoint does that re-fetch once
// against production, then this file (and ReleaseStore.
// ListTPDBSceneAndMovie/UpdatePerformers, which exist only to serve it) are
// deleted in a follow-up commit. Only entity_source == "tpdb" rows are
// touched — StashDB/FansDB performers sourcing is out of scope this round
// (see identify.MatchResult.Performers' doc comment).
func backfillAdultPerformersHandler(httpClient *http.Client, connStore *connections.Store, releaseStore *adultnewest.ReleaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		client, ok := adultTPDBClient(w, r, httpClient, connStore)
		if !ok {
			return
		}

		entries, err := releaseStore.ListTPDBSceneAndMovie(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		result := struct {
			Checked int      `json:"checked"`
			Updated int      `json:"updated"`
			Errors  []string `json:"errors"`
		}{}

		for _, m := range entries {
			result.Checked++
			scene, err := client.GetSceneByID(ctx, m.EntityID)
			if err != nil {
				result.Errors = append(result.Errors, m.EntityTitle+" ("+m.EntityID+"): "+err.Error())
				continue
			}
			if scene == nil || len(scene.Performers) == 0 {
				continue
			}
			if err := releaseStore.UpdatePerformers(ctx, m.RowType, m.EntitySource, m.EntityID, scene.Performers); err != nil {
				result.Errors = append(result.Errors, m.EntityTitle+" ("+m.EntityID+"): "+err.Error())
				continue
			}
			result.Updated++
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}
