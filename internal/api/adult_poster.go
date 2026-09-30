package api

import (
	"context"
	"log"
	"net/http"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// Claude 2026-09-29: Adult empty-poster backfill via catalog id lookup.
// Reason: 0012 stored poster_url at grab but never filled older rows;
//   GET /tracked must stay read-only. Identify() is nil-able without AI
//   and must not run here — only GetSceneByID / FindScene.
// Troubleshooting: local/empty scene_id rows stay skipped; letter tiles
//   after backfill → box client missing or URL failed imageproxy.Validate.
// Review if: aspect is re-measured from the stored URL.
// Related files: internal/library/library_scene.go, internal/identify/boxlookup.go

func backfillAdultPosters(
	ctx context.Context,
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
) (ok, fail int) {
	if libStore == nil {
		return 0, 0
	}
	scenes, err := libStore.ListScenesNeedingPoster(ctx)
	if err != nil {
		log.Printf("poster backfill: list scenes: %v", err)
		return 0, 0
	}
	if len(scenes) == 0 {
		return 0, 0
	}
	boxes := adultPosterBoxes(ctx, httpClient, connStore, scStore, settingsStore)
	if boxes == nil {
		log.Printf("poster backfill: adult catalog clients unavailable, skipping %d scenes", len(scenes))
		return 0, len(scenes)
	}
	for i, sc := range scenes {
		if ctx.Err() != nil {
			log.Printf("poster backfill: cancelled after %d scenes", i)
			return ok, fail
		}
		url := resolveAdultScenePoster(ctx, boxes, sc.Box, sc.SceneID)
		// Claude 2026-09-29: FillScenePosterURL, not SetScenePosterURL.
		// Reason: SetScenePosterURL is the operator overwrite + poster_source lock.
		// Troubleshooting: using Set here would mark backfill as operator and skip later refill.
		// Review if: backfill and operator pick share one write helper.
		if url == "" {
			fail++
		} else if err := libStore.FillScenePosterURL(ctx, sc.ID, url); err != nil {
			log.Printf("poster backfill: scene id=%d: %v", sc.ID, err)
			fail++
		} else {
			ok++
		}
		if err := sleepBackfillGap(ctx); err != nil {
			return ok, fail
		}
	}
	return ok, fail
}

func adultPosterBoxes(
	ctx context.Context,
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
) *identify.BoxSearcher {
	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Adult)
	if err != nil || sess == nil || sess.Identify == nil {
		return nil
	}
	return sess.Identify.Boxes
}

// resolveAdultScenePoster looks up catalog art by stored (box, scene_id).
// TPDB tries a scene then a movie id. Stash-box names use FindScene.
func resolveAdultScenePoster(ctx context.Context, boxes *identify.BoxSearcher, box, sceneID string) string {
	if boxes == nil || box == "" || sceneID == "" || library.IsLocalScene(box) {
		return ""
	}
	if box == "tpdb" {
		if m, err := boxes.ResolveCatalogRef(ctx, box, sceneID, false); err == nil && m != nil && m.Image != "" {
			return m.Image
		}
		if m, err := boxes.ResolveCatalogRef(ctx, box, sceneID, true); err == nil && m != nil && m.Image != "" {
			return m.Image
		}
		return ""
	}
	if m, err := boxes.SceneByID(ctx, box, sceneID); err == nil && m != nil {
		return m.Image
	}
	return ""
}
