package api

// TEMPORARY, one-off migration endpoint — same "built once, removed once
// done" precedent as internal/sonarrimport/internal/whisparrimport (see
// CLAUDE.md's Series/Adult sections). Corrects entity_image on every
// already-cached TPDB-sourced adult_newest_releases row, captured before
// tpdbrest.Scene's image-preference fix (background.large/poster over the
// unreliable raw studio-passthrough field — see that type's doc comment
// for the live evidence). Remove this file and its one route registration
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

type backfillAdultImagesResponse struct {
	Checked int      `json:"checked"`
	Updated int      `json:"updated"`
	Errors  []string `json:"errors,omitempty"`
}

// backfillAdultImagesHandler is POST /api/modes/adult/newest-rows/backfill-images.
func backfillAdultImagesHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, releaseStore *adultnewest.ReleaseStore) http.HandlerFunc {
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

		entities, err := releaseStore.ListAll(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		resp := backfillAdultImagesResponse{}
		for _, e := range entities {
			if e.EntitySource != "tpdb" {
				continue // StashDB/FansDB image reliability is a separate, unconfirmed question — out of scope for this backfill
			}
			resp.Checked++

			var image string
			var lookupErr error
			switch e.RowType {
			case adultnewest.RowScene, adultnewest.RowMovie:
				image, lookupErr = sess.Identify.Boxes.RefreshTPDBSceneImage(ctx, e.EntityID)
			case adultnewest.RowStudio:
				image, _ = sess.Identify.StudioImage(ctx, e.EntityID)
			case adultnewest.RowPerformer:
				image, _ = sess.Identify.PerformerImage(ctx, e.EntityID)
			default:
				continue
			}
			if lookupErr != nil {
				log.Printf("backfillAdultImages: entity %d (%s): %v", e.ID, e.EntityTitle, lookupErr)
				resp.Errors = append(resp.Errors, e.EntityTitle+": "+lookupErr.Error())
				continue
			}
			if image == "" || image == e.EntityImage {
				continue
			}
			if err := releaseStore.UpdateImage(ctx, e.ID, image); err != nil {
				log.Printf("backfillAdultImages: updating entity %d (%s): %v", e.ID, e.EntityTitle, err)
				resp.Errors = append(resp.Errors, e.EntityTitle+": "+err.Error())
				continue
			}
			resp.Updated++
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
