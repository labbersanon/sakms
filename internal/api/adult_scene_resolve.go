package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

type adultSceneResolveResponse struct {
	Item    *adultSceneCandidate `json:"item,omitempty"`
	Message string               `json:"message,omitempty"`
}

// adultSceneResolveHandler implements GET /api/modes/adult/scene-resolve — paste
// a catalog or studio URL, resolve to one catalog scene for Adult repick.
func adultSceneResolveHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		rawURL := r.URL.Query().Get("url")
		if rawURL == "" {
			http.Error(w, "url query parameter is required", http.StatusBadRequest)
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

		res, err := sess.Identify.ResolveSceneFromURL(ctx, httpClient, rawURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		out := adultSceneResolveResponse{Message: res.Message}
		if res.Item != nil {
			out.Item = &adultSceneCandidate{
				Box:             res.Item.Box,
				SceneID:         res.Item.SceneID,
				Title:           res.Item.Title,
				Studio:          res.Item.Studio,
				Date:            res.Item.Date,
				ImageURL:        res.Item.ImageURL,
				DurationSeconds: res.Item.DurationSeconds,
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}
