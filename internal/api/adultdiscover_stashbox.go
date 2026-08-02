package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/stashbox"
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
				Genres:          s.Tags,
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

// adultStashBoxStudioScenesHandler is the stash-box studio drill-down — clicking
// a <Box> Studios-row card shows just that studio's scenes
// (QueryScenesByStudio, filtered by the opaque stash-box studio id in {id}).
// Mirrors adultStashBoxStudiosHandler's optional-source contract (unconfigured
// box → []/200, never a setup prompt) and stamps Source: box. Scenes are built
// inline rather than via writeAdultScenes, which is typed for []tpdbrest.Scene
// and stamps Source:"tpdb" (both a type mismatch and a provenance-display bug).
func adultStashBoxStudioScenesHandler(httpClient *http.Client, connStore *connections.Store, box string) http.HandlerFunc {
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
		scenes, err := client.QueryScenesByStudio(ctx, r.PathValue("id"), page, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeStashBoxScenes(w, scenes, box)
	}
}

// adultStashBoxPerformerScenesHandler is the stash-box performer drill-down —
// clicking a <Box> Performers-row card shows just that performer's scenes
// (QueryScenesByPerformer, filtered by the opaque stash-box performer id in
// {id}). Same optional-source contract and Source: box stamping as
// adultStashBoxStudioScenesHandler.
func adultStashBoxPerformerScenesHandler(httpClient *http.Client, connStore *connections.Store, box string) http.HandlerFunc {
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
		scenes, err := client.QueryScenesByPerformer(ctx, r.PathValue("id"), page, perPage)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeStashBoxScenes(w, scenes, box)
	}
}

// writeStashBoxScenes encodes a stash-box scene slice into the adultScene wire
// shape with Source stamped to box ("stashdb"/"fansdb") — the stash-box analogue
// of writeAdultScenes, kept separate because it takes []stashbox.Scene (not
// []tpdbrest.Scene) and must stamp the real box provenance, not "tpdb". Mirrors
// the inline adultScene construction adultStashBoxScenesHandler already uses.
func writeStashBoxScenes(w http.ResponseWriter, scenes []stashbox.Scene, box string) {
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
			Genres:          s.Tags,
			// Rating stays 0 — stash-box has no numeric rating field.
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// adultDiscoverMergedRecentHandler backs Adult Discover's "Recently Released"
// sort-bar row. It is re-pointed off the old live TPDB+StashDB merge and onto
// the identified-available pool (D5): one page of cached scene/movie entities
// ordered by the entity's own release date (ReleaseStore.ListRecentScenes),
// each gated by the D5 visibility rule and enclosure-exposed by DirectGrabURL —
// so the row shows exactly the same available universe, gated the same way, as
// the main Scenes / newest rows and search. An install with no cache returns [].
//
// The former concurrent TPDB+StashDB fetch, page-local phash dedup, and
// cross-source date merge are gone: the pool is already a single deduped,
// identified set, so none of that machinery is needed here anymore.
func adultDiscoverMergedRecentHandler(releaseStore *adultnewest.ReleaseStore, feedHealth *adultnewest.FeedHealth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		page, _ := adultPagination(r)
		matches, err := releaseStore.ListRecentScenes(ctx, page)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writePooledScenes(w, matches, feedHealth)
	}
}
