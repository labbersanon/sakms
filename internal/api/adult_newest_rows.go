// Package api: adult_newest_rows.go — CRUD + reorder + resolve for
// admin-defined Adult "newest" Discover rows (internal/adultnewest), the
// Prowlarr-backed sibling of discover_sliders.go's TMDB-backed sliders.
//
// resolveRowHandler is the one deliberate divergence from
// resolveSliderHandler's pattern: it reads ONLY the adult_newest_releases
// cache table adultnewest's background scan job writes — it makes no
// Prowlarr call and runs no identify-pipeline work at request time. Doing
// what resolveSliderHandler does (call the live upstream on every resolve)
// here would mean a Prowlarr search plus a per-release AI pipeline call on
// every Discover page load — precisely the failure shape CLAUDE.md's
// "Discover never queries Prowlarr" rule exists to prevent. Do not "fix"
// this to match discoverSliders' pattern; it's intentional.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/adultnewest"
	"github.com/curtiswtaylorjr/sakms/internal/apidto"
)

// toDTOAdultNewestRow maps an internal adultnewest.Row onto the exported
// apidto.AdultNewestRow wire DTO — direct sibling of discover_sliders.go's
// toDTOSlider.
func toDTOAdultNewestRow(row adultnewest.Row) apidto.AdultNewestRow {
	return apidto.AdultNewestRow{
		ID:          row.ID,
		Title:       row.Title,
		RowType:     string(row.RowType),
		GenreFilter: row.GenreFilter,
		SortOrder:   row.SortOrder,
		Enabled:     row.Enabled,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func toDTOAdultNewestRows(rows []adultnewest.Row) []apidto.AdultNewestRow {
	out := make([]apidto.AdultNewestRow, len(rows))
	for i, row := range rows {
		out[i] = toDTOAdultNewestRow(row)
	}
	return out
}

// adultNewestRowStoreError maps an adultnewest.Store validation/lookup error
// onto an HTTP status — same split as discoverSliderStoreError: the fixed
// enum/required-field errors are always a bad request body, ErrNotFound is a
// 404, anything else is an internal error.
func adultNewestRowStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, adultnewest.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, adultnewest.ErrInvalidRowType),
		errors.Is(err, adultnewest.ErrTitleRequired),
		errors.Is(err, adultnewest.ErrReorderMismatch):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// listAdultNewestRowsHandler is GET /api/modes/adult/newest-rows.
func listAdultNewestRowsHandler(store *adultnewest.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toDTOAdultNewestRows(rows))
	}
}

// createAdultNewestRowHandler is POST /api/modes/adult/newest-rows.
func createAdultNewestRowHandler(store *adultnewest.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.AdultNewestRowUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		row, err := store.Create(r.Context(), req.Title, adultnewest.RowType(req.RowType), req.GenreFilter, req.Enabled)
		if err != nil {
			adultNewestRowStoreError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toDTOAdultNewestRow(*row))
	}
}

// updateAdultNewestRowHandler is PUT /api/modes/adult/newest-rows/{id}.
func updateAdultNewestRowHandler(store *adultnewest.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "id path parameter must be an integer", http.StatusBadRequest)
			return
		}
		var req apidto.AdultNewestRowUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		row, err := store.Update(r.Context(), id, req.Title, adultnewest.RowType(req.RowType), req.GenreFilter, req.Enabled)
		if err != nil {
			adultNewestRowStoreError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(toDTOAdultNewestRow(*row))
	}
}

// deleteAdultNewestRowHandler is DELETE /api/modes/adult/newest-rows/{id}.
func deleteAdultNewestRowHandler(store *adultnewest.Store) http.HandlerFunc {
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

// reorderAdultNewestRowsHandler is POST /api/modes/adult/newest-rows/reorder.
func reorderAdultNewestRowsHandler(store *adultnewest.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.AdultNewestRowReorderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := store.Reorder(r.Context(), req.IDs); err != nil {
			adultNewestRowStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// findAdultNewestRow looks up id in store's full list — adultnewest.Store has
// no Get-by-id (List/Create/Update/Delete/Reorder only), same reasoning as
// discover_sliders.go's findSlider: a single-operator admin's row count is
// small enough a linear scan costs nothing worth a new Store method for.
func findAdultNewestRow(ctx context.Context, store *adultnewest.Store, id int) (*adultnewest.Row, error) {
	rows, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i], nil
		}
	}
	return nil, adultnewest.ErrNotFound
}

// toDTOReleaseItem maps a cached adultnewest.MatchedRelease onto the wire DTO
// resolveAdultNewestRowHandler returns.
func toDTOReleaseItem(m adultnewest.MatchedRelease) apidto.AdultNewestReleaseItem {
	return apidto.AdultNewestReleaseItem{
		ID:              m.EntityID,
		Title:           m.EntityTitle,
		Studio:          m.EntityStudio,
		Date:            m.EntityDate,
		Image:           m.EntityImage,
		Source:          m.EntitySource,
		RowType:         string(m.RowType),
		DurationSeconds: m.EntityDurationSeconds,
		ReleaseTitle:    m.FirstSeenReleaseTitle,
		Genres:          m.Genres,
		Performers:      m.Performers,
	}
}

// resolveAdultNewestRowHandler is GET /api/modes/adult/newest-rows/{id}/resolve
// — reads the row's config, then lists matching cached releases for the
// requested page. See this file's package doc for why this never touches
// Prowlarr or the identify pipeline at request time.
func resolveAdultNewestRowHandler(rowStore *adultnewest.Store, releaseStore *adultnewest.ReleaseStore) http.HandlerFunc {
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

		row, err := findAdultNewestRow(ctx, rowStore, id)
		if err != nil {
			if errors.Is(err, adultnewest.ErrNotFound) {
				http.Error(w, err.Error(), http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		matches, err := releaseStore.List(ctx, row.RowType, row.GenreFilter, page, 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		items := make([]apidto.AdultNewestReleaseItem, len(matches))
		for i, m := range matches {
			items[i] = toDTOReleaseItem(m)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	}
}

// adultNewestGenresHandler is GET /api/modes/adult/newest-rows/genres — the
// reference list backing AdultRowAdmin's genre picker, sourced from genres
// that actually exist in matched content (see
// adultnewest.ReleaseStore.DistinctGenres' doc comment for why, versus a
// static hardcoded taxonomy).
func adultNewestGenresHandler(releaseStore *adultnewest.ReleaseStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		genres, err := releaseStore.DistinctGenres(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(genres)
	}
}
