package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/apidto"
	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/discoversliders"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
	"github.com/curtiswtaylorjr/sakms/internal/tmdb"
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toDTOSliders(sliders))
	}
}

// createSliderHandler is POST /api/discover/sliders — validated by
// discoversliders.Store.Create (title/filter_type/target enums, the
// filter_type/filter_value pairing rule).
func createSliderHandler(store *discoversliders.Store) http.HandlerFunc {
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toDTOSlider(*sl))
	}
}

// updateSliderHandler is PUT /api/discover/sliders/{id} — overwrites every
// editable field (sort_order is untouched; see Store.Update's doc comment,
// reordering is a separate action below).
func updateSliderHandler(store *discoversliders.Store) http.HandlerFunc {
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toDTOSlider(*sl))
	}
}

// deleteSliderHandler is DELETE /api/discover/sliders/{id}. Deleting an id
// that doesn't exist is not an error (Store.Delete's convention).
func deleteSliderHandler(store *discoversliders.Store) http.HandlerFunc {
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
// (still just normalized TMDB movie/TV titles); see resolveSlider below for
// the per-filter-type/target dispatch.
func resolveSliderHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, slidersStore *discoversliders.Store) http.HandlerFunc {
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
		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, mode.Movies)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		items, err := resolveSlider(ctx, sess.TMDB, *sl, page)
		if err != nil {
			if errors.Is(err, errSliderMisconfigured) {
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

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	}
}

// resolveSlider fetches sl's items for the given 1-based page. Target ==
// mixed concatenates the movie-catalog and TV-catalog results (movies
// first, then TV) where both exist — the simplest well-defined combination;
// Seerr-style interleaving-by-popularity is not attempted here (no
// premature sorting logic ahead of a real need). Studio/network have no
// cross-media equivalent (a movie production company isn't a TV concept and
// vice versa — see tmdb.Studio/tmdb.Network's doc comments), so a "mixed"
// studio/network slider degrades to its one applicable catalog rather than
// erroring; a slider whose Target names ONLY the inapplicable catalog
// (studio+tv, network+movie) is a genuine misconfiguration and errors.
func resolveSlider(ctx context.Context, client *tmdb.Client, sl discoversliders.Slider, page int) ([]tmdb.Item, error) {
	wantMovies := sl.Target == discoversliders.TargetMovie || sl.Target == discoversliders.TargetMixed
	wantTV := sl.Target == discoversliders.TargetTV || sl.Target == discoversliders.TargetMixed

	switch sl.FilterType {
	case discoversliders.FilterUpcoming:
		return fetchFixedFeed(ctx, client, wantMovies, wantTV, page, client.UpcomingMovies, client.UpcomingTV)
	case discoversliders.FilterTrending:
		return fetchFixedFeed(ctx, client, wantMovies, wantTV, page,
			func(ctx context.Context, page int) ([]tmdb.Item, error) { return client.Trending(ctx, tmdb.Movie, "week", page) },
			func(ctx context.Context, page int) ([]tmdb.Item, error) { return client.Trending(ctx, tmdb.TV, "week", page) })
	case discoversliders.FilterPopular:
		return fetchFixedFeed(ctx, client, wantMovies, wantTV, page,
			func(ctx context.Context, page int) ([]tmdb.Item, error) { return client.Popular(ctx, tmdb.Movie, page) },
			func(ctx context.Context, page int) ([]tmdb.Item, error) { return client.Popular(ctx, tmdb.TV, page) })
	case discoversliders.FilterGenre:
		id, err := sliderFilterValueInt(sl)
		if err != nil {
			return nil, err
		}
		return fetchFixedFeed(ctx, client, wantMovies, wantTV, page,
			func(ctx context.Context, page int) ([]tmdb.Item, error) { return client.DiscoverMoviesByGenre(ctx, id, page) },
			func(ctx context.Context, page int) ([]tmdb.Item, error) { return client.DiscoverTVByGenre(ctx, id, page) })
	case discoversliders.FilterKeyword:
		id, err := sliderFilterValueInt(sl)
		if err != nil {
			return nil, err
		}
		return fetchFixedFeed(ctx, client, wantMovies, wantTV, page,
			func(ctx context.Context, page int) ([]tmdb.Item, error) { return client.DiscoverMoviesByKeyword(ctx, id, page) },
			func(ctx context.Context, page int) ([]tmdb.Item, error) { return client.DiscoverTVByKeyword(ctx, id, page) })
	case discoversliders.FilterStudio:
		if sl.Target == discoversliders.TargetTV {
			return nil, fmt.Errorf("%w: slider %d: studio filter is movie-only, not valid for a tv-target slider", errSliderMisconfigured, sl.ID)
		}
		id, err := sliderFilterValueInt(sl)
		if err != nil {
			return nil, err
		}
		return client.DiscoverMoviesByStudio(ctx, id, page)
	case discoversliders.FilterNetwork:
		if sl.Target == discoversliders.TargetMovie {
			return nil, fmt.Errorf("%w: slider %d: network filter is series-only, not valid for a movie-target slider", errSliderMisconfigured, sl.ID)
		}
		id, err := sliderFilterValueInt(sl)
		if err != nil {
			return nil, err
		}
		return client.DiscoverTVByNetwork(ctx, id, page)
	default:
		return nil, fmt.Errorf("%w: slider %d: unrecognized filter type %q", errSliderMisconfigured, sl.ID, sl.FilterType)
	}
}

// errSliderMisconfigured marks a resolveSlider error as a permanent,
// per-slider configuration problem (bad filter_type/target pairing, a
// non-numeric filter_value) rather than a transient TMDB call failure —
// resolveSliderHandler maps it to 400 instead of 502, so the frontend can
// tell "edit this slider" from "TMDB is down, retry."
var errSliderMisconfigured = errors.New("slider misconfigured")

// sliderFilterValueInt parses sl.FilterValue (a stringified TMDB id) as an
// int — every non-fixed-feed FilterType stores one (see
// discoversliders.Store's validate). A parse failure means the stored value
// predates some future non-numeric FilterValue convention or was corrupted;
// either way it's a config problem, not a transient TMDB error (see
// errSliderMisconfigured).
func sliderFilterValueInt(sl discoversliders.Slider) (int, error) {
	id, err := strconv.Atoi(sl.FilterValue)
	if err != nil {
		return 0, fmt.Errorf("%w: slider %d: filter_value %q is not a valid TMDB id", errSliderMisconfigured, sl.ID, sl.FilterValue)
	}
	return id, nil
}

// fetchFixedFeed calls movieFn/tvFn depending on wantMovies/wantTV and
// concatenates the results — the shared dispatch shape behind every
// FilterType case above that has both a movie and a TV sibling method
// (upcoming/trending/popular/genre/keyword all do; studio/network don't,
// handled separately in resolveSlider).
func fetchFixedFeed(ctx context.Context, client *tmdb.Client, wantMovies, wantTV bool, page int, movieFn, tvFn func(context.Context, int) ([]tmdb.Item, error)) ([]tmdb.Item, error) {
	var items []tmdb.Item
	if wantMovies {
		mv, err := movieFn(ctx, page)
		if err != nil {
			return nil, err
		}
		items = append(items, mv...)
	}
	if wantTV {
		tv, err := tvFn(ctx, page)
		if err != nil {
			return nil, err
		}
		items = append(items, tv...)
	}
	return items, nil
}
