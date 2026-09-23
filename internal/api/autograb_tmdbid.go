package api

import (
	"context"
	"log"
	"strings"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// fillMissingTMDBID attaches a unique exact-title TMDB id when a movies/series
// request arrived with none. Title-only Search parked pending_retry rows that
// then failed ExternalIDs(0) on every retry.
//
// Claude 2026-09-23: recover the catalog id Search never stored.
// Reason: GET /search?q= and runToggleGatedSearch carry only a title.
// Troubleshooting: Barnaby Jones / Waltons / Magnum / Rockford letter tiles.
// Review if: operator autograb for movies/series still requires tmdbId on the wire.
func fillMissingTMDBID(ctx context.Context, sess *mode.Session, deps AutoGrabDeps, req *AutoGrabRequest) {
	if req == nil || req.TMDBID > 0 {
		return
	}
	if req.Mode != mode.Movies && req.Mode != mode.Series {
		return
	}
	id := resolveTitleTMDBID(ctx, sess, req.Mode, req.Title)
	if id <= 0 {
		return
	}
	req.TMDBID = id
	log.Printf("autograb: resolved %q (%s) to tmdb %d", req.Title, req.Mode, id)
	if req.ExistingGrabID == 0 || deps.GrabsStore == nil {
		return
	}
	if err := deps.GrabsStore.SetTMDBID(ctx, req.ExistingGrabID, id); err != nil {
		log.Printf("autograb: persist tmdb %d on grab %d: %v", id, req.ExistingGrabID, err)
	}
}

// resolveTitleTMDBID returns a TMDB id only when exactly one catalog hit
// matches the title (case-insensitive). Two remakes with the same name stay 0.
func resolveTitleTMDBID(ctx context.Context, sess *mode.Session, m mode.Mode, title string) int {
	title = strings.TrimSpace(title)
	if sess == nil || sess.TMDB == nil || title == "" {
		return 0
	}
	var items []tmdb.Item
	var err error
	switch m {
	case mode.Series:
		items, err = sess.TMDB.SearchTV(ctx, title)
	case mode.Movies:
		items, err = sess.TMDB.SearchMovies(ctx, title)
	default:
		return 0
	}
	if err != nil {
		return 0
	}
	matched := 0
	id := 0
	for _, it := range items {
		if !strings.EqualFold(strings.TrimSpace(it.Title), title) {
			continue
		}
		matched++
		id = it.ID
		if matched > 1 {
			return 0
		}
	}
	return id
}
