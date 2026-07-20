package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// optionalConnAPI is package api's copy of mode.go's optionalConn — the
// "not-configured is not an error" pattern for the OPTIONAL Adult Discover
// sources (StashDB, FansDB). It collapses connections.ErrNotFound into
// (nil, nil) so a handler can treat an absent connection as "silently skip
// this source" rather than an HTTP error; any other store error propagates.
// mode.optionalConn is unexported and can't be imported here, hence this
// small replica.
func optionalConnAPI(ctx context.Context, store *connections.Store, service string) (*connections.Connection, error) {
	conn, err := store.Get(ctx, service)
	if errors.Is(err, connections.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// adultStashBoxClient builds a stash-box client for an OPTIONAL Adult Discover
// source (service is "stashdb" or "fansdb"). Unlike adultTPDBClient — TPDB is a
// required core dependency and writes a 400 when absent — these sources are
// optional: a missing connection is NOT an error, it just means the source
// isn't available, so this returns (nil, false, nil) rather than writing an
// HTTP error. (client, true, nil) when configured; (nil, false, err) only on a
// real store error. Config mirrors mode.go's buildIdentifier exactly
// (IsBearer:false, HasVoteField:true for both stashdb and fansdb).
func adultStashBoxClient(ctx context.Context, connStore *connections.Store, httpClient *http.Client, service string) (*stashbox.Client, bool, error) {
	conn, err := optionalConnAPI(ctx, connStore, service)
	if err != nil {
		return nil, false, err
	}
	if conn == nil {
		return nil, false, nil
	}
	// StashDB/FansDB are fixed public stash-box instances — the endpoint is the
	// hardcoded per-name constant, never conn.URL (not collected for them).
	endpoint, _ := stashbox.URLForBox(service)
	return stashbox.New(stashbox.Config{
		Endpoint: endpoint, APIKey: conn.APIKey, IsBearer: false, HasVoteField: true,
	}, httpClient), true, nil
}

// writeEmptyJSONArray writes a 200 with a literal empty JSON array — the
// "silently absent when this optional source isn't configured" contract every
// stash-box handler shares (an unconfigured StashDB/FansDB is not an error, so
// the frontend gets [] and renders nothing, never a setup prompt).
func writeEmptyJSONArray(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("[]"))
}

// adultStashBoxScenesHandler is the shared body of the recent/trending
// stash-box scene handlers — identical except for the sort order. box is
// "stashdb" or "fansdb" (also the Source stamped on every card).
func adultStashBoxScenesHandler(httpClient *http.Client, connStore *connections.Store, box string, sort stashbox.SceneSort) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		client, ok, err := adultStashBoxClient(ctx, connStore, httpClient, box)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			writeEmptyJSONArray(w)
			return
		}
		page, perPage := adultPagination(r)
		scenes, err := client.QueryScenes(ctx, sort, page, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out := make([]adultScene, len(scenes))
		for i, s := range scenes {
			out[i] = adultScene{
				ID:              s.ID,
				Title:           s.Title,
				Studio:          s.StudioName,
				Date:            s.ReleaseDate,
				Image:           s.ImageURL,
				DurationSeconds: s.Duration,
				Source:          box,
				// Rating stays 0 — stash-box has no numeric rating field.
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// adultStashBoxRecentHandler backs Adult Discover's optional "<Box> Recently
// Released" row — one page of the box's scenes sorted by date descending.
// Returns [] (200) when the box isn't configured, per the optional-source
// contract.
func adultStashBoxRecentHandler(httpClient *http.Client, connStore *connections.Store, box string) http.HandlerFunc {
	return adultStashBoxScenesHandler(httpClient, connStore, box, stashbox.SceneSortDate)
}

// adultStashBoxTrendingHandler backs Adult Discover's optional "<Box> Trending"
// row — the box's scenes sorted by stash-box's server-side TRENDING order. This
// IS a real server-side ordering (unlike TPDB's page-local rating re-sort);
// stash-box exposes no numeric popularity value, only the sort. Returns []
// (200) when the box isn't configured.
func adultStashBoxTrendingHandler(httpClient *http.Client, connStore *connections.Store, box string) http.HandlerFunc {
	return adultStashBoxScenesHandler(httpClient, connStore, box, stashbox.SceneSortTrending)
}

// adultStashBoxStudiosHandler backs Adult Discover's optional "<Box> Studios"
// row — one page of the box's studio catalog. Returns [] (200) when the box
// isn't configured.
func adultStashBoxStudiosHandler(httpClient *http.Client, connStore *connections.Store, box string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		client, ok, err := adultStashBoxClient(ctx, connStore, httpClient, box)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			writeEmptyJSONArray(w)
			return
		}
		page, perPage := adultPagination(r)
		studios, err := client.QueryStudios(ctx, page, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out := make([]adultStudio, len(studios))
		for i, s := range studios {
			out[i] = adultStudio{ID: s.ID, Name: s.Name, Image: s.ImageURL, Source: box}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// adultStashBoxPerformersHandler backs Adult Discover's optional "<Box>
// Performers" row — one page of the box's performer catalog. Returns [] (200)
// when the box isn't configured.
func adultStashBoxPerformersHandler(httpClient *http.Client, connStore *connections.Store, box string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		client, ok, err := adultStashBoxClient(ctx, connStore, httpClient, box)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			writeEmptyJSONArray(w)
			return
		}
		page, perPage := adultPagination(r)
		performers, err := client.QueryPerformers(ctx, page, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out := make([]adultPerformer, len(performers))
		for i, p := range performers {
			out[i] = adultPerformer{ID: p.ID, Name: p.Name, Image: p.ImageURL, Source: box}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// adultDate parses one of the two date shapes Adult Discover's two sources
// emit — TPDB's/stash-box's "2006-01-02" and, defensively, an RFC3339
// timestamp — returning (zero, false) if neither parses. The merged-recent
// sort uses ok=false to push an unparseable-date item to the end rather than
// failing the whole request (a browse-quality heuristic, not correctness).
func adultDate(s string) (time.Time, bool) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// adultDiscoverMergedRecentHandler backs Adult Discover's merged, deduped
// "Recently Released" row: always TPDB, plus StashDB's exclusive scenes when
// StashDB is configured (skipped entirely when it isn't, so the row is exactly
// TPDB-only — fully backward compatible). TPDB and StashDB are fetched
// CONCURRENTLY — they're independent network round-trips (REST, GraphQL) with
// no data dependency between their inputs, only joined at the merge step
// below, so serializing them would just add their latencies together on an
// interactive browse path.
//
// A StashDB fetch error, when StashDB IS configured, degrades to TPDB-only
// (logged, not surfaced as a request error) — the same graceful-degradation
// contract "not configured" already gets. Before this, a transient StashDB
// hiccup 502'd the entire row and discarded the TPDB scenes already fetched,
// which is a worse failure mode than simply not having StashDB configured at
// all; only a TPDB failure (the required source) fails the request.
//
// Dedup and ordering are both PAGE-LOCAL, not a global guarantee — honestly,
// the same kind of same-page-only limitation this file's "Highest Rated" row
// already documents for its own rating re-sort (see adultdiscover.go). Dedup:
// a StashDB scene is dropped only when one of its pHashes is present in the
// flattened set of pHashes from THIS SAME PAGE's TPDB scenes (a TPDB scene
// with no hashes contributes nothing to the set, so it can never falsely mask
// a StashDB scene) — a StashDB/TPDB duplicate pair split across two different
// "Show more" pages will not be caught. Ordering: each page is sorted
// independently by date descending (unparseable dates sort last) and then
// truncated to perPage; the merged feed is not guaranteed monotonically
// descending ACROSS page boundaries, since TPDB and StashDB are paginated
// independently and their page boundaries don't line up. Both are accepted,
// documented browse-quality tradeoffs (favoring false negatives — when
// unsure, show both rather than risk hiding a real scene) rather than a full
// cross-request session/cursor, which would be a much larger feature for a
// browse row.
func adultDiscoverMergedRecentHandler(httpClient *http.Client, connStore *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		// TPDB is required — reuse adultTPDBClient's exact 400-when-absent
		// behavior (same as adultDiscoverHandler's category=recent path).
		tpdbClient, ok := adultTPDBClient(w, r, httpClient, connStore)
		if !ok {
			return
		}
		page, perPage := adultPagination(r)

		stashClient, ok, err := adultStashBoxClient(ctx, connStore, httpClient, "stashdb")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var (
			wg          sync.WaitGroup
			tpdbScenes  []tpdbrest.Scene
			tpdbErr     error
			stashScenes []stashbox.Scene
			stashErr    error
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			tpdbScenes, tpdbErr = tpdbClient.BrowseScenes(ctx, page, perPage, "recently_released")
		}()
		if ok {
			wg.Add(1)
			go func() {
				defer wg.Done()
				stashScenes, stashErr = stashClient.QueryScenes(ctx, stashbox.SceneSortDate, page, perPage)
			}()
		}
		wg.Wait()

		if tpdbErr != nil {
			http.Error(w, tpdbErr.Error(), http.StatusBadGateway)
			return
		}

		out := make([]adultScene, 0, len(tpdbScenes))
		tpdbHashes := map[string]bool{}
		for _, s := range tpdbScenes {
			for _, h := range s.Hashes {
				tpdbHashes[h] = true
			}
			out = append(out, tpdbSceneToAdultScene(s))
		}

		if ok && stashErr != nil {
			// StashDB configured but errored — degrade to TPDB-only rather
			// than fail the whole row (see doc comment above).
			log.Printf("adult discover merged-recent: stashdb query failed, degrading to tpdb-only: %v", stashErr)
		} else if ok {
			for _, s := range stashScenes {
				if hashSetHasAny(tpdbHashes, s.PHashes) {
					continue // a phash TPDB already carries → duplicate, drop it.
				}
				out = append(out, adultScene{
					ID:              s.ID,
					Title:           s.Title,
					Studio:          s.StudioName,
					Date:            s.ReleaseDate,
					Image:           s.ImageURL,
					DurationSeconds: s.Duration,
					Source:          "stashdb",
				})
			}
		}

		sort.SliceStable(out, func(i, j int) bool {
			ti, iok := adultDate(out[i].Date)
			tj, jok := adultDate(out[j].Date)
			if iok != jok {
				return iok // a parseable date sorts before an unparseable one.
			}
			return ti.After(tj)
		})
		// adultPagination's perPage is a lenient pass-through — 0 means "use
		// each client's own default," which both tpdbrest and stashbox clamp
		// to 20 internally (see their own defaultBrowsePerPage). Replicate
		// that clamp here so truncation compares against the SAME effective
		// page size the two fetches above actually used, not the raw
		// possibly-zero query param.
		effectivePerPage := perPage
		if effectivePerPage <= 0 {
			effectivePerPage = 20
		}
		if len(out) > effectivePerPage {
			out = out[:effectivePerPage] // keep this page's contract at exactly perPage.
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}

// hashSetHasAny reports whether any hash in candidates is present in set.
func hashSetHasAny(set map[string]bool, candidates []string) bool {
	for _, h := range candidates {
		if set[h] {
			return true
		}
	}
	return false
}
