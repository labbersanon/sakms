package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
)

// libraryTrackedItem is Movies' shape for the Tag workflow's item picker —
// Tags is a list of label strings (a local tag has no numeric id), matching
// the label-as-id shape listTagsHandler's Movies branch returns for its
// vocabulary, so the frontend's existing id-keyed matching logic works
// unchanged for either mode. ID is always the library row id (a
// library_scenes.id for Adult, which the scene-tag routes take directly) —
// never overwritten by TMDBID, which is a different id space.
//
// TMDBID/Year are additive omitempty fields, populated for Movies/Series (both
// carry them in the library) so Discover's existing-library row can render a
// real poster card: TMDBID drives the lazy poster-fetch + availability probe +
// auto-grab, Year is display. They stay zero (omitted) for Adult, whose scenes
// are keyed on (box, sceneId) and have no TMDB identity — the Tag picker, this
// type's original caller, ignores them for every mode.
type libraryTrackedItem struct {
	ID             int64    `json:"id"`
	Title          string   `json:"title"`
	Tags           []string `json:"tags"`
	TMDBID         int      `json:"tmdbId,omitempty"`
	Year           int      `json:"year,omitempty"`
	CollectionName string   `json:"collectionName,omitempty"`
	Genres         []string `json:"genres,omitempty"`
	Cast           []string `json:"cast,omitempty"`
}

// listTrackedHandler returns every item {mode} currently tracks — straight
// from libStore for every mode now (no *arr app involved): items for Movies,
// series for Series, scenes for Adult (Whisparr eliminated, Stage 4). Backs
// the Tag workflow's item picker (there's no other way to browse what's
// trackable to assign/remove a tag on) and is generically useful anywhere a
// UI needs real item context instead of guessing an ID. connStore/
// settingsStore/httpClient are retained on the signature (NewMux wires them)
// but no longer used, since no mode builds a Servarr session.
func listTrackedHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		if m == mode.Movies {
			items, err := libStore.List(ctx, mode.Movies)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out := make([]libraryTrackedItem, len(items))
			for i, item := range items {
				tags, err := libStore.Tags(ctx, item.ID)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				out[i] = libraryTrackedItem{ID: item.ID, Title: item.Title, Tags: tags, TMDBID: item.TMDBID, Year: item.Year, CollectionName: item.CollectionName, Genres: item.Genres, Cast: item.Cast}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(out)
			return
		}
		if m == mode.Series {
			series, err := libStore.ListSeries(ctx)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out := make([]libraryTrackedItem, len(series))
			for i, s := range series {
				tags, err := libStore.SeriesTags(ctx, s.ID)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				out[i] = libraryTrackedItem{ID: s.ID, Title: s.Title, Tags: tags, TMDBID: s.TMDBID, Year: s.Year, Genres: s.Genres, Cast: s.Cast}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(out)
			return
		}

		if m == mode.Adult {
			// Adult owns its own library now too (Whisparr eliminated, Stage 4)
			// — served straight from libStore, same {id, title, tags} shape as
			// Movies/Series, keyed on a library_scenes row instead of an
			// item/series row.
			scenes, err := libStore.ListScenes(ctx)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out := make([]libraryTrackedItem, len(scenes))
			for i, sc := range scenes {
				tags, err := libStore.SceneTags(ctx, sc.ID)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				out[i] = libraryTrackedItem{ID: sc.ID, Title: sc.Title, Tags: tags}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(out)
			return
		}

		http.Error(w, fmt.Sprintf("unknown mode %q", m), http.StatusBadRequest)
	}
}
