// Shared list-ingest dispatch used by Trakt, TMDB, and IMDb. Each source
// file fetches its titles; this file decides skip / hold / RunAutoGrab /
// monitor-all and shares one cycle slot budget across those sources.
//
// NO GOROUTINE, NO TICKER, NO INTERVAL KEY lives here. monitorListIngests
// is the SEVENTH step of runUsenetRetryCycle.
package api

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
)

// listIngestItem is one title a list source wants ingested. Type is
// "movie" or "show"; TMDBID is required.
type listIngestItem struct {
	Type   string
	TMDBID int
	Title  string
}

type listIngestBudget struct {
	attempts int
	cap      int
}

func newListIngestBudget(ctx context.Context, deps AutoGrabDeps) *listIngestBudget {
	cap := defaultAutoGrabSlotsPerCycle
	if deps.SettingsStore != nil {
		cap = loadUsenetCycleSlots(ctx, deps.SettingsStore)
	}
	return &listIngestBudget{cap: cap}
}

func (b *listIngestBudget) remaining() bool {
	if b == nil {
		return true
	}
	return b.attempts < b.cap
}

func (b *listIngestBudget) take() {
	if b == nil {
		return
	}
	b.attempts++
}

type listIngestKind struct {
	Origin    string
	Trigger   AutoGrabTrigger
	LogPrefix string
}

// monitorListIngests is the SEVENTH pass of runUsenetRetryCycle. Trakt,
// TMDB, and IMDb share one slot budget so three enabled sources cannot
// each spend a full cycle cap.
func monitorListIngests(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc, libStore *library.Store, excluded map[string]bool) {
	budget := newListIngestBudget(ctx, deps)
	if ingestTraktWatchlist(ctx, deps, build, libStore, excluded, budget) {
		return
	}
	if ingestTMDBLists(ctx, deps, build, libStore, excluded, budget) {
		return
	}
	ingestIMDbLists(ctx, deps, build, libStore, excluded, budget)
}

func listIngestAutoGrabOn(ctx context.Context, deps AutoGrabDeps, logPrefix string) bool {
	if deps.SettingsStore == nil {
		return false
	}
	auto, err := deps.SettingsStore.GetBool(ctx, usenetAutoGrabEnabledKey, false)
	if err != nil {
		log.Printf("%s: reading auto-grab toggle: %v", logPrefix, err)
		return false
	}
	return auto
}

func ingestListItems(ctx context.Context, deps AutoGrabDeps, build sessionBuilderFunc, libStore *library.Store, excluded map[string]bool, items []listIngestItem, kind listIngestKind, budget *listIngestBudget) (stop bool) {
	if len(items) == 0 {
		return false
	}
	if budget == nil {
		budget = newListIngestBudget(ctx, deps)
	}
	catalog := listIngestCatalog(deps)
	activeMovies := activeMovieGrabKeys(ctx, deps)
	var movieSess *mode.Session
	skipMovies := false
	for _, item := range items {
		if !budget.remaining() {
			break
		}
		if item.TMDBID <= 0 {
			continue
		}
		switch item.Type {
		case "movie":
			if skipMovies || !shouldIngestListMovie(ctx, libStore, excluded, activeMovies, item, kind.LogPrefix) {
				continue
			}
			if movieSess == nil {
				sess, sessErr := build(ctx, mode.Movies)
				if sessErr != nil {
					log.Printf("%s: building the movies session failed (%T) — skipping remaining movies", kind.LogPrefix, rootCause(sessErr))
					skipMovies = true
					continue
				}
				movieSess = sess
			}
			budget.take()
			stopCycle, skipRest := ingestListMovie(ctx, deps, movieSess, item, kind)
			if stopCycle {
				return true
			}
			if skipRest {
				skipMovies = true
			}
		case "show":
			if catalog.lib == nil {
				continue
			}
			if !shouldIngestListShow(ctx, catalog.lib, excluded, item, kind.LogPrefix) {
				continue
			}
			budget.take()
			ingestListShow(ctx, catalog, item, kind.LogPrefix)
		}
	}
	return false
}

func listIngestCatalog(deps AutoGrabDeps) seasonCatalog {
	if deps.TraktIngest == nil {
		return seasonCatalog{}
	}
	return deps.TraktIngest.Catalog
}

func shouldIngestListMovie(ctx context.Context, libStore *library.Store, excluded map[string]bool, active map[int]bool, item listIngestItem, logPrefix string) bool {
	if excluded[excludes.Key(string(mode.Movies), item.TMDBID, item.Title)] {
		return false
	}
	if active[item.TMDBID] {
		return false
	}
	if libStore == nil {
		return true
	}
	_, err := libStore.GetByTMDBID(ctx, mode.Movies, item.TMDBID)
	if err == nil {
		return false
	}
	if !errors.Is(err, library.ErrNotFound) {
		log.Printf("%s: looking up movie %q: %v", logPrefix, item.Title, err)
		return false
	}
	return true
}

func shouldIngestListShow(ctx context.Context, libStore *library.Store, excluded map[string]bool, item listIngestItem, logPrefix string) bool {
	if excluded[excludes.Key(string(mode.Series), item.TMDBID, item.Title)] {
		return false
	}
	_, err := libStore.GetSeriesByTMDBID(ctx, item.TMDBID)
	if err == nil {
		return false
	}
	if !errors.Is(err, library.ErrNotFound) {
		log.Printf("%s: looking up series %q: %v", logPrefix, item.Title, err)
		return false
	}
	return true
}

func ingestListMovie(ctx context.Context, deps AutoGrabDeps, sess *mode.Session, item listIngestItem, kind listIngestKind) (stopCycle, skipRestMovies bool) {
	rel, blocked, reason := gateMovieGrab(ctx, sess.TMDB, mode.Movies, item.TMDBID)
	if blocked {
		held, err := parkPreReleaseRequest(ctx, deps.GrabsStore, mode.Movies, item.Title, item.TMDBID, rel.HoldUntil)
		switch {
		case errors.Is(err, grabs.ErrHeldRequestExists):
			return false, false
		case err != nil:
			log.Printf("%s: holding %q (%s): %v", kind.LogPrefix, item.Title, reason, err)
			return false, false
		}
		tagListOrigin(ctx, deps.GrabsStore, held.ID, kind.Origin, kind.LogPrefix)
		return false, false
	}
	if sess.Prowlarr == nil {
		log.Printf("%s: Prowlarr isn't configured — skipping remaining movies", kind.LogPrefix)
		return false, true
	}
	out, err := RunAutoGrab(ctx, deps, sess, AutoGrabRequest{
		Mode:    mode.Movies,
		Title:   item.Title,
		TMDBID:  item.TMDBID,
		Trigger: kind.Trigger,
	})
	switch {
	case err != nil:
		log.Printf("%s: %q — auto-grab failed (%T)", kind.LogPrefix, item.Title, rootCause(err))
	case out.Gated:
		log.Printf("%s: usenet auto-grab is switched off — abandoning this cycle", kind.LogPrefix)
		return true, false
	case out.MovieBlocked:
		held, parkErr := parkPreReleaseRequest(ctx, deps.GrabsStore, mode.Movies, item.Title, item.TMDBID, rel.HoldUntil)
		if parkErr != nil && !errors.Is(parkErr, grabs.ErrHeldRequestExists) {
			log.Printf("%s: holding blocked %q: %v", kind.LogPrefix, item.Title, parkErr)
			return false, false
		}
		if parkErr == nil {
			tagListOrigin(ctx, deps.GrabsStore, held.ID, kind.Origin, kind.LogPrefix)
		}
	case out.AlreadyGrabbing:
		log.Printf("%s: %q is already being downloaded", kind.LogPrefix, item.Title)
	case out.Grabbed:
		log.Printf("%s: %q dispatched", kind.LogPrefix, item.Title)
		tagListOrigin(ctx, deps.GrabsStore, out.GrabID, kind.Origin, kind.LogPrefix)
	default:
		log.Printf("%s: %q has no qualifying candidate yet — parked for re-search", kind.LogPrefix, item.Title)
		tagListOrigin(ctx, deps.GrabsStore, out.GrabID, kind.Origin, kind.LogPrefix)
	}
	return false, false
}

func ingestListShow(ctx context.Context, catalog seasonCatalog, item listIngestItem, logPrefix string) {
	series, err := catalog.ensureSeriesByTMDB(ctx, item.TMDBID)
	if err != nil {
		log.Printf("%s: adding series %q: %v", logPrefix, item.Title, err)
		return
	}
	states, err := catalog.statesByTMDB(ctx, item.TMDBID)
	if err != nil {
		log.Printf("%s: listing seasons for %q: %v", logPrefix, item.Title, err)
		return
	}
	touched := map[int]bool{}
	for _, st := range states {
		if err := catalog.lib.SetSeasonMonitored(ctx, series.ID, st.SeasonNumber, true); err != nil {
			log.Printf("%s: monitoring season %d of %q: %v", logPrefix, st.SeasonNumber, item.Title, err)
			break
		}
		touched[st.SeasonNumber] = true
	}
	if len(touched) > 0 {
		catalog.backfill.kick(series.ID, touched)
	}
}

func tagListOrigin(ctx context.Context, grabsStore *grabs.Store, grabID int64, origin, logPrefix string) {
	if grabsStore == nil || grabID == 0 || origin == "" {
		return
	}
	if err := grabsStore.SetOrigin(ctx, grabID, origin); err != nil {
		log.Printf("%s: tagging grab %d origin: %v", logPrefix, grabID, err)
	}
}

func listOriginated(g grabs.Grab, origin string) bool {
	return g.Mode == mode.Movies &&
		g.Origin == origin &&
		g.Status == grabs.PendingRetry &&
		g.Indexer == "" &&
		g.DownloadURL == ""
}

func cancelListOriginRetries(ctx context.Context, grabsStore *grabs.Store, origin, reason, logPrefix string) {
	if grabsStore == nil {
		return
	}
	list, err := grabsStore.List(ctx, mode.Movies)
	if err != nil {
		log.Printf("%s: listing movie grabs for ingest-off cleanup: %v", logPrefix, err)
		return
	}
	now := time.Now()
	for _, g := range list {
		if !listOriginated(g, origin) {
			continue
		}
		if err := grabsStore.SetRetryAfter(ctx, g.ID, now, reason); err != nil {
			log.Printf("%s: recording ingest-off reason on grab %d: %v", logPrefix, g.ID, err)
			continue
		}
		if err := grabsStore.UpdateStatus(ctx, g.ID, grabs.Failed); err != nil {
			log.Printf("%s: cancelling grab %d after ingest was turned off: %v", logPrefix, g.ID, err)
		}
	}
}

func parseSettingLines(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, part := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ';'
		}) {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	return out
}

func loadSettingString(ctx context.Context, store *settings.Store, key, logPrefix string) string {
	if store == nil {
		return ""
	}
	v, err := store.Get(ctx, key)
	if errors.Is(err, settings.ErrNotFound) {
		return ""
	}
	if err != nil {
		log.Printf("%s: reading %s: %v", logPrefix, key, err)
		return ""
	}
	return v
}

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && unicode.IsDigit(rune(s[i])) {
		i++
	}
	if i == 0 {
		return ""
	}
	return s[:i]
}
