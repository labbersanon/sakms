package api

import (
	"context"
	"log"
	"net/http"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mediafolder"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// NewMediafolderMux exposes POST /api/admin/mediafolder/backfill — one-shot
// write of Jellyfin-compatible folder.jpg/backdrop.jpg/NFO for every tracked
// Movies/Series title. Same 202 async shape as recheck/entity-sync triggers.
//
// Claude 2026-09-22: B/c/b — sakms is metadata source (import + /poster +
//   backfill/boot). No lockdata; Jellyfin should prefer NFO + local images.
// Troubleshooting: letter tiles when disk has folder.jpg but sakms has no tmdbId.
// Review if: boot backfill is disabled in favor of admin-only.
// Related files: internal/mediafolder, docs/jellyfin-metadata.md
func NewMediafolderMux(
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/mediafolder/backfill", mediafolderBackfillHandler(httpClient, connStore, scStore, settingsStore, libStore))
	return mux
}

func mediafolderBackfillHandler(
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		go runMediafolderBackfill(context.Background(), httpClient, connStore, scStore, settingsStore, libStore)
		w.WriteHeader(http.StatusAccepted)
	}
}

// RunMediafolderBackfillBoot is the post-ListenAndServe one-shot used from
// main. Idempotent: Ensure* skips non-empty folder.jpg/backdrop.jpg unless
// Force. Soft-fails per title; cancelled via signal-driven ctx between rows.
func RunMediafolderBackfillBoot(
	ctx context.Context,
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
) {
	runMediafolderBackfill(ctx, httpClient, connStore, scStore, settingsStore, libStore)
}

func runMediafolderBackfill(
	ctx context.Context,
	httpClient *http.Client,
	connStore *connections.Store,
	scStore *serviceconn.Store,
	settingsStore *settings.Store,
	libStore *library.Store,
) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Movies)
	if err != nil {
		log.Printf("mediafolder backfill: build session: %v", err)
		return
	}
	preset := naming.Preset("")
	if settingsStore != nil {
		if p, err := resolveNamingPreset(ctx, settingsStore, mode.Series); err == nil {
			preset = p
		}
	}
	d := mediafolder.SyncDeps{
		HTTP: httpClient, TMDB: sess.TMDB, Lib: libStore, Preset: preset, Force: false,
	}
	mOK, sOK, fail := mediafolder.Backfill(ctx, d)
	log.Printf("mediafolder backfill: movies_ok=%d series_ok=%d fail=%d", mOK, sOK, fail)
}

// syncMediafolderAfterImport writes sidecars for a newly imported title.
func syncMediafolderAfterImport(ctx context.Context, libStore *library.Store, sess *mode.Session, settingsStore *settings.Store, m mode.Mode, tmdbID int) {
	if sess == nil || sess.TMDB == nil || libStore == nil || tmdbID <= 0 {
		return
	}
	preset := naming.Preset("")
	if settingsStore != nil {
		if p, err := resolveNamingPreset(ctx, settingsStore, m); err == nil {
			preset = p
		}
	}
	d := mediafolder.SyncDeps{HTTP: http.DefaultClient, TMDB: sess.TMDB, Lib: libStore, Preset: preset}
	switch m {
	case mode.Movies:
		if err := mediafolder.SyncMovie(ctx, d, tmdbID); err != nil {
			log.Printf("mediafolder: sync movie tmdb=%d: %v", tmdbID, err)
		}
	case mode.Series:
		ser, err := libStore.GetSeriesByTMDBID(ctx, tmdbID)
		if err != nil {
			return
		}
		if err := mediafolder.SyncSeries(ctx, d, *ser); err != nil {
			log.Printf("mediafolder: sync series tmdb=%d: %v", tmdbID, err)
		}
	}
}

