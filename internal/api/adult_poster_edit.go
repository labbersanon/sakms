package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/imageproxy"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/sectionlock"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// Claude 2026-09-29: Adult catalog-poster picker (GET list + PUT persist).
// Reason: operator overwrite lives on library_scenes.poster_url with
//   poster_source=operator; GET /tracked stays a read of that column.
// Troubleshooting: PUT rejects a URL that is not in the sanitized catalog list.
// Review if: Discover untracked cards gain a persist target.

type adultCatalogPostersResponse struct {
	URLs []string `json:"urls"`
}

type adultPosterRequest struct {
	URL string `json:"url"`
}

func catalogAdultPostersHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyIfAdultLocked(w, r) {
			return
		}
		sceneID, ok := parseIntPathValue(w, r, "sceneId")
		if !ok {
			return
		}
		urls, err := adultCatalogPosterURLs(r.Context(), httpClient, connStore, scStore, settingsStore, libStore, int64(sceneID))
		if err != nil {
			writeAdultPosterErr(w, err)
			return
		}
		if urls == nil {
			urls = []string{}
		}
		writeJSON(w, adultCatalogPostersResponse{URLs: urls})
	}
}

func putAdultScenePosterHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if denyIfAdultLocked(w, r) {
			return
		}
		sceneID, ok := parseIntPathValue(w, r, "sceneId")
		if !ok {
			return
		}
		var req adultPosterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		catalog, err := adultCatalogPosterURLs(r.Context(), httpClient, connStore, scStore, settingsStore, libStore, int64(sceneID))
		if err != nil {
			writeAdultPosterErr(w, err)
			return
		}
		want, err := imageproxy.Validate(r.Context(), strings.TrimSpace(req.URL))
		if err != nil {
			http.Error(w, "poster url is not allowed", http.StatusBadRequest)
			return
		}
		if !catalogContainsURL(catalog, want.String()) {
			http.Error(w, "url is not one of this scene's catalog images", http.StatusBadRequest)
			return
		}
		if err := libStore.SetScenePosterURL(r.Context(), int64(sceneID), want.String()); err != nil {
			writeAdultPosterErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func adultCatalogPosterURLs(ctx context.Context, httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, libStore *library.Store, sceneID int64) ([]string, error) {
	scene, err := libStore.GetSceneByID(ctx, sceneID)
	if err != nil {
		return nil, err
	}
	if library.IsLocalScene(scene.Box) {
		return []string{}, nil
	}
	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Adult)
	if err != nil {
		return nil, err
	}
	if sess.Identify == nil || sess.Identify.Boxes == nil {
		return []string{}, nil
	}
	raw, err := sess.Identify.Boxes.CatalogPosterURLs(ctx, scene.Box, scene.SceneID)
	if err != nil {
		return nil, fmt.Errorf("catalog posters: %w", err)
	}
	return sanitizeCatalogPosterURLs(ctx, raw), nil
}

func sanitizeCatalogPosterURLs(ctx context.Context, raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, u := range raw {
		got, err := imageproxy.Validate(ctx, strings.TrimSpace(u))
		if err != nil {
			continue
		}
		s := got.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func catalogContainsURL(catalog []string, url string) bool {
	for _, u := range catalog {
		if u == url {
			return true
		}
	}
	return false
}

func writeAdultPosterErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, library.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, sectionlock.ErrSectionLocked):
		writeSectionLocked(w, sectionlock.SectionAdultContent)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}
