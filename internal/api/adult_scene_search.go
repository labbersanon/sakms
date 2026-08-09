package api

import (
	"errors"
	"net/http"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// adultSceneCandidate mirrors apidto.AdultSceneCandidate byte-for-byte on the
// wire — see that type's doc comment (internal/apidto/dto.go) for the full
// design rationale (a LIST shape backing a "pick one of these" UI, since
// identify.BoxSearcher's existing single-result search cannot).
type adultSceneCandidate struct {
	Box             string `json:"box"`
	SceneID         string `json:"sceneId"`
	Title           string `json:"title"`
	Studio          string `json:"studio,omitempty"`
	Date            string `json:"date,omitempty"`
	ImageURL        string `json:"imageUrl,omitempty"`
	DurationSeconds int    `json:"durationSeconds,omitempty"`
}

// adultSceneSearchResponse mirrors apidto.AdultSceneSearchResponse. Note:
// this type is deliberately NOT registered in dto_drift_test.go's case table
// — its Items field is a nested named type ([]adultSceneCandidate vs
// []apidto.AdultSceneCandidate), whose field.Type.String() differs by
// package by construction, same reason applyBatchRequest and
// storageAllocationRow are excluded there.
type adultSceneSearchResponse struct {
	Items  []adultSceneCandidate `json:"items"`
	Errors []string              `json:"errors,omitempty"`
}

// adultSceneSearchHandler implements GET /api/modes/adult/scene-search — the
// multi-box, multi-result text search behind the cross-mode move's Adult
// target picker (plan §5.3). Unlike identify.BoxSearcher's existing
// SearchStashBox/SearchTPDB (a single collapsed best guess used by automatic
// identification), this returns the raw union so an operator can pick one.
//
// This route is GATED by the Adult section lock even though the move/commit
// endpoint it feeds is not gated for every direction (D-1): it RENDERS Adult
// scene metadata, so the view-only PIN gate applies here regardless of what
// the eventual move target ends up being.
func adultSceneSearchHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		q := r.URL.Query().Get("q")
		if q == "" {
			http.Error(w, "q query parameter is required", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Adult)
		if err != nil {
			if errors.Is(err, sectionlock.ErrSectionLocked) {
				writeSectionLocked(w, sectionlock.SectionAdultContent)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.Identify == nil || sess.Identify.Boxes == nil {
			http.Error(w, "no adult metadata database is configured yet — add one in Settings first", http.StatusBadRequest)
			return
		}

		items, softErrs := sess.Identify.Boxes.ListSceneCandidates(ctx, q, sess.Identify.StashBoxes)
		resp := adultSceneSearchResponse{Errors: softErrs}
		if len(items) > 0 {
			resp.Items = make([]adultSceneCandidate, len(items))
			for i, it := range items {
				resp.Items[i] = adultSceneCandidate{
					Box:             it.Box,
					SceneID:         it.SceneID,
					Title:           it.Title,
					Studio:          it.Studio,
					Date:            it.Date,
					ImageURL:        it.ImageURL,
					DurationSeconds: it.DurationSeconds,
				}
			}
		}
		writeJSON(w, resp)
	}
}
