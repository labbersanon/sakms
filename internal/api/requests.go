package api

import (
	"net/http"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/excludes"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// requestsModes is the fixed cross-mode order /api/requests aggregates over —
// the rollup is deliberately NOT mode-scoped in its path (it's the one screen
// that spans all three at once).
var requestsModes = []mode.Mode{mode.Movies, mode.Series, mode.Adult}

// requestsHandler backs GET /api/requests — a cross-mode (NOT mode-scoped)
// status worklist, aggregated live on read with NO new persisted table. Three
// sources feed it:
//
//   - In Library: every tracked item/series/scene (Status "In Library").
//   - Downloading: every in-flight grab (Status queued/downloading/completed-
//     but-not-yet-imported) → Status "Downloading". In sakms's single-operator
//     model there is no approval queue, so "Requested" is not a real state — a
//     grab IS the request. Collapsing it into "Downloading" is honest, not a
//     faked stage (documented rather than inventing an empty "Requested").
//   - Missing: Series-only, surfaced as the MissingCount ANNOTATION on a
//     series' row (episodes TMDB knows about with no file on disk — a real
//     library.MissingEpisodes query, not a filesystem scan). It is not a
//     separate Status: a partially-missing tracked series stays "In Library"
//     (or "Downloading" if also grabbing) with MissingCount=N. Movies/Adult
//     don't track not-owned titles, so their MissingCount is always 0.
//
// Dedup: a title that is BOTH tracked and actively grabbing collapses to one
// row and the grab status wins (Status → "Downloading", GrabID set), keeping
// any MissingCount the In-Library pass already computed. Keyed by TMDB id when
// present, else by lowercased title (Adult scenes carry no TMDB id) — a
// pragmatic key for the single-operator model; a title-only match across an
// Adult scene and its grab is best-effort.
//
// Positioning vs existing screens (deliberately distinct, not a third
// overlapping grab view): /grabs is the raw per-mode grab log (read-only,
// no-bulk); /downloads is live download-client status. /requests ADDS the
// cross-mode status rollup + missing-episode surfacing that neither provides —
// it is a worklist, not a fourth grab log.
func requestsHandler(grabsStore *grabs.Store, libStore *library.Store, excludesStore *excludes.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Excluded titles are suppressed from the whole worklist regardless of
		// which pass would surface them — a title an operator removed must never
		// reappear as either an In-Library or a Downloading row. Keyed by the same
		// requestKey both passes below use, so the match is exact-string.
		excluded, err := excludesStore.Keys(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		items := []apidto.RequestStatusItem{}
		index := map[string]int{} // dedup key -> position in items

		// Pass 1: tracked library items become "In Library" rows.
		for _, m := range requestsModes {
			switch m {
			case mode.Movies:
				tracked, err := libStore.List(ctx, m)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				for _, it := range tracked {
					key := requestKey(m, it.TMDBID, it.Title)
					if excluded[key] {
						continue
					}
					index[key] = len(items)
					items = append(items, apidto.RequestStatusItem{Mode: string(m), Title: it.Title, TMDBID: it.TMDBID, Status: "In Library"})
				}
			case mode.Series:
				seriesList, err := libStore.ListSeries(ctx)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				for _, s := range seriesList {
					key := requestKey(m, s.TMDBID, s.Title)
					if excluded[key] {
						continue
					}
					missing, err := libStore.MissingEpisodes(ctx, s.ID)
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					index[key] = len(items)
					items = append(items, apidto.RequestStatusItem{Mode: string(m), Title: s.Title, TMDBID: s.TMDBID, Status: "In Library", MissingCount: len(missing)})
				}
			case mode.Adult:
				scenes, err := libStore.ListScenes(ctx)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				for _, sc := range scenes {
					key := requestKey(m, 0, sc.Title)
					if excluded[key] {
						continue
					}
					index[key] = len(items)
					items = append(items, apidto.RequestStatusItem{Mode: string(m), Title: sc.Title, Status: "In Library"})
				}
			}
		}

		// Pass 2: in-flight grabs. An existing row for the same key flips to
		// "Downloading" (grab wins) and gets its GrabID; a grab with no tracked
		// match adds a new "Downloading" row (e.g. a brand-new title still
		// downloading, not yet imported into the library).
		for _, m := range requestsModes {
			grabList, err := grabsStore.List(ctx, m)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			for _, g := range grabList {
				if !isActiveGrab(g.Status) {
					continue
				}
				key := requestKey(m, g.TMDBID, g.Title)
				if excluded[key] {
					continue
				}
				// One derivation of the label for both branches, so a
				// pending-retry row can never read "Downloading" in one and
				// "Pending Retry" in the other.
				status := grabStatusLabel(g.Status)
				if pos, ok := index[key]; ok {
					items[pos].Status = status
					items[pos].GrabID = g.ID
					items[pos].RetryAfter = g.RetryAfter
					items[pos].RetryReason = g.RetryReason
					continue
				}
				index[key] = len(items)
				items = append(items, apidto.RequestStatusItem{Mode: string(m), Title: g.Title, TMDBID: g.TMDBID, Status: status, GrabID: g.ID, RetryAfter: g.RetryAfter, RetryReason: g.RetryReason})
			}
		}

		writeJSON(w, apidto.RequestStatusResponse{Items: items})
	}
}

// requestKey is the dedup key for one title within a mode — TMDB id when
// present (Movies/Series), else lowercased title (Adult scenes have no TMDB
// id). Delegates to excludes.Key so a stored exclusion key and a live row's key
// are byte-identical (the exclusion suppression is an exact-string match).
func requestKey(m mode.Mode, tmdbID int, title string) string {
	return excludes.Key(string(m), tmdbID, title)
}

// isActiveGrab reports whether a grab is still in flight for the worklist —
// queued/downloading/completed-but-not-yet-imported count, as does
// pending_retry (an auto-grab awaiting its next re-search is very much still
// outstanding work); Imported (already in the library, surfaced by the
// In-Library pass) and Failed do not.
//
// This gate decides whether a grab reaches the worklist AT ALL — omitting
// PendingRetry here would make a pending-retry row vanish from /api/requests
// entirely, no matter what the status mapping above says about it.
func isActiveGrab(s grabs.Status) bool {
	switch s {
	case grabs.Queued, grabs.Downloading, grabs.Completed, grabs.PendingRetry:
		return true
	default:
		return false
	}
}

// grabStatusLabel is the worklist status string for an in-flight grab.
// Everything still moving collapses to "Downloading" (in sakms's
// single-operator model a grab IS the request, so there is no "Requested"
// stage), but a pending_retry grab is NOT downloading anything — reporting it
// as such would hide a request that found no qualifying release and is waiting
// on a re-search. It gets its own honest state instead.
func grabStatusLabel(s grabs.Status) string {
	if s == grabs.PendingRetry {
		return "Pending Retry"
	}
	return "Downloading"
}
