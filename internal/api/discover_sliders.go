package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/discoverrefresh"
	"github.com/labbersanon/sakms/internal/discoversliders"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

// toDTOSlider maps an internal discoversliders.Slider onto the exported
// apidto.Slider wire DTO (field-for-field, since apidto.Slider mirrors
// Slider's JSON tags exactly) — direct sibling of autograb.go's toDTOGrab.
func toDTOSlider(sl discoversliders.Slider) apidto.Slider {
	return apidto.Slider{
		ID:          sl.ID,
		Title:       sl.Title,
		FilterType:  string(sl.FilterType),
		FilterValue: sl.FilterValue,
		Target:      string(sl.Target),
		SortOrder:   sl.SortOrder,
		Enabled:     sl.Enabled,
		CreatedAt:   sl.CreatedAt,
		UpdatedAt:   sl.UpdatedAt,
	}
}

func toDTOSliders(sliders []discoversliders.Slider) []apidto.Slider {
	out := make([]apidto.Slider, len(sliders))
	for i, sl := range sliders {
		out[i] = toDTOSlider(sl)
	}
	return out
}

// discoverSliderStoreError maps a discoversliders.Store validation/lookup
// error onto an HTTP status: the fixed enum/pairing errors
// (ErrInvalidFilterType, ErrInvalidTarget, ErrTitleRequired,
// ErrFilterValueRequired, ErrFilterValueNotAllowed, ErrReorderMismatch) are
// always a bad request body, never a server fault; ErrNotFound is a 404.
// Anything else is treated as an internal error.
func discoverSliderStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, discoversliders.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, discoversliders.ErrInvalidFilterType),
		errors.Is(err, discoversliders.ErrInvalidTarget),
		errors.Is(err, discoversliders.ErrTitleRequired),
		errors.Is(err, discoversliders.ErrFilterValueRequired),
		errors.Is(err, discoversliders.ErrFilterValueNotAllowed),
		errors.Is(err, discoversliders.ErrReorderMismatch):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// listSlidersHandler returns every admin-defined custom Discover slider,
// ordered by display position — GET /api/discover/sliders.
func listSlidersHandler(store *discoversliders.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sliders, err := store.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, toDTOSliders(sliders))
	}
}

// createSliderHandler is POST /api/discover/sliders — validated by
// discoversliders.Store.Create (title/filter_type/target enums, the
// filter_type/filter_value pairing rule).
//
// Claude 2026-08-03: added the post-create populate hook (BE-16, plan §5.2,
// spec AC5).
// Reason: fire-and-forget so a slider an operator just saved is cached
// immediately instead of waiting up to a whole refresh interval; d's Cache
// field may be nil (no discover cache configured), which RefreshSlider now
// guards against (internal/discoverrefresh/sliders.go).
// Troubleshooting: if the new slider's cache row never appears, check
// RefreshSlider's own log lines — this handler never awaits or inspects its
// error.
func createSliderHandler(store *discoversliders.Store, d discoverrefresh.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.SliderUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		sl, err := store.Create(r.Context(), req.Title, discoversliders.FilterType(req.FilterType), req.FilterValue, discoversliders.Target(req.Target), req.Enabled)
		if err != nil {
			discoverSliderStoreError(w, err)
			return
		}
		go discoverrefresh.RefreshSlider(context.Background(), d, sl.ID)
		writeJSON(w, toDTOSlider(*sl))
	}
}

// updateSliderHandler is PUT /api/discover/sliders/{id} — overwrites every
// editable field (sort_order is untouched; see Store.Update's doc comment,
// reordering is a separate action below).
//
// Claude 2026-08-03: added the invalidate-then-repopulate hook (BE-16, plan
// §5.2/§5.3, spec AC5).
// Reason: the synchronous Delete (before Update's response is written)
// deliberately happens before the fire-and-forget RefreshSlider — between
// the edit and the repopulate, a render must fall through to live (correct,
// if slower) rather than serve the OLD filter's content (wrong). This
// covers both the update AND the disable-via-update case (§5.3): a
// disabled slider's row is removed and RefreshSlider's own Enabled check
// leaves it uncached.
// Troubleshooting: invalidateDiscoverCache never fails this request; a
// cleanup failure is logged and caught within one cycle by
// DeleteOrphanSliders.
func updateSliderHandler(store *discoversliders.Store, d discoverrefresh.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "id path parameter must be an integer", http.StatusBadRequest)
			return
		}
		var req apidto.SliderUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		sl, err := store.Update(r.Context(), id, req.Title, discoversliders.FilterType(req.FilterType), req.FilterValue, discoversliders.Target(req.Target), req.Enabled)
		if err != nil {
			discoverSliderStoreError(w, err)
			return
		}
		invalidateDiscoverCache(r.Context(), d.Cache, "slider", strconv.Itoa(id))
		go discoverrefresh.RefreshSlider(context.Background(), d, id)
		writeJSON(w, toDTOSlider(*sl))
	}
}

// deleteSliderHandler is DELETE /api/discover/sliders/{id}. Deleting an id
// that doesn't exist is not an error (Store.Delete's convention).
//
// Claude 2026-08-03: added the post-delete invalidation (BE-16, plan §5.3).
// Reason: a deleted slider's cache row must not keep serving for up to a
// whole refresh interval; only *discoverrefresh.Store is threaded through
// (not the full Deps) since this handler never repopulates anything.
func deleteSliderHandler(store *discoversliders.Store, cache *discoverrefresh.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "id path parameter must be an integer", http.StatusBadRequest)
			return
		}
		if err := store.Delete(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		invalidateDiscoverCache(r.Context(), cache, "slider", strconv.Itoa(id))
		w.WriteHeader(http.StatusNoContent)
	}
}

// reorderSlidersHandler is POST /api/discover/sliders/reorder — one explicit
// "here is the full new order" action covering every existing slider exactly
// once (see discoversliders.Store.Reorder's doc comment), not a per-item
// bulk mutation.
func reorderSlidersHandler(store *discoversliders.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.SliderReorderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := store.Reorder(r.Context(), req.IDs); err != nil {
			discoverSliderStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// findSlider looks up id in store's full list — discoversliders.Store has no
// Get-by-id (List/Create/Update/Delete/Reorder only), and a single-operator
// admin's slider count is small enough that this linear scan costs nothing
// worth a new Store method for.
func findSlider(ctx context.Context, store *discoversliders.Store, id int) (*discoversliders.Slider, error) {
	sliders, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range sliders {
		if sliders[i].ID == id {
			return &sliders[i], nil
		}
	}
	return nil, discoversliders.ErrNotFound
}

// resolveSliderHandler is GET /api/discover/sliders/{id}/resolve — given a
// stored slider's config, fetches its actual TMDB items for the requested
// page, dispatching on FilterType (and Target) to the matching internal/tmdb
// method. Response items reuse apidto.DiscoverItem's wire shape unchanged
// (still just normalized TMDB movie/TV titles); see
// discoverrefresh.ResolveSlider for the per-filter-type/target dispatch.
//
// discoverCache backs the read-through cache lookup below (§4.2); it MAY BE
// NIL, meaning "no cache" — every lookup misses and this handler behaves
// exactly as it did before this cache existed.
func resolveSliderHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, slidersStore *discoversliders.Store, discoverCache *discoverrefresh.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "id path parameter must be an integer", http.StatusBadRequest)
			return
		}
		page := 1
		if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
			page = p
		}

		sl, err := findSlider(ctx, slidersStore, id)
		if err != nil {
			if err == discoversliders.ErrNotFound {
				http.Error(w, err.Error(), http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		// Slider resolve always builds a Movies-mode session purely to reach
		// the one shared "tmdb" connection — same reasoning as
		// discoverKeywordsHandler; a slider's own Target picks the media
		// type(s) actually queried, not {mode}.
		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, mode.Movies)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		// Claude 2026-08-03: read-through cache lookup, keyed on the
		// slider's own id (BE-12, discover-scheduled-refresh plan §4.2).
		// Reason: runs after findSlider (so an unknown id still 404s from
		// the store, not from a cache miss) and after the TMDB-configured
		// check above, matching §4.0's ordering rule. perPage is
		// deliberately NOT recomputed here — lookupDiscoverCache uses the
		// cached row's own stored page_size (Entry.PageSize), never a
		// value this handler derives, since a mixed fixed-feed slider's
		// page size (40) differs from every other slider's (20) and
		// re-deriving it on the read path is the exact defect this plan
		// already shipped once (§4.2).
		upstreamPage := page
		if items, hit, liveRawPage := lookupDiscoverCache(ctx, discoverCache, "slider", strconv.Itoa(sl.ID), page); hit {
			writeRawJSONArray(w, items)
			return
		} else if liveRawPage > 0 {
			upstreamPage = liveRawPage
		}

		items, err := discoverrefresh.ResolveSlider(ctx, sess.TMDB, *sl, upstreamPage)
		if err != nil {
			if errors.Is(err, discoverrefresh.ErrSliderMisconfigured) {
				// A bad filter_value/target combination is a permanent
				// per-slider config problem (fix by editing the slider), not
				// a transient upstream failure — 400, not 502, so the
				// frontend can tell "edit this slider" from "TMDB is down,
				// retry."
				http.Error(w, err.Error(), http.StatusBadRequest)
			} else {
				http.Error(w, err.Error(), http.StatusBadGateway)
			}
			return
		}

		writeJSON(w, items)
	}
}

// resolveSlider, fetchFixedFeed, sliderFilterValueInt and
// errSliderMisconfigured moved to internal/discoverrefresh (sliders.go) as
// ResolveSlider / ErrSliderMisconfigured and their unexported helpers: the
// background refresh scheduler resolves the same sliders this handler does, and
// two copies would let the cached and live answers drift on what a slider
// returns.
