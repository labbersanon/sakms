package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
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
//
// CreatedAt is another additive omitempty field, populated for every mode so
// the frontend's Library screen can offer an added-date sort.
//
// QualityTiers is additive too, backing the Library screen's tier filter and
// the Dashboard Storage Allocation grid's drill-down. It is a SLICE, not a
// single value, because a series genuinely has more than one: its episodes are
// grabbed at different times under different tier settings, and collapsing that
// to one value would be a fabricated majority vote. Movies carry a 1-element
// slice; Adult scenes carry a 1-element slice. A stored "" (a row the boot-time
// backfill hasn't reached) folds to "unknown" here rather than being omitted:
// the Dashboard grid folds "" into the same visible, clickable Unknown cell, so
// omitting it would make that cell drill down to an empty list. The fold is
// display-only — the stored "" is untouched.
type libraryTrackedItem struct {
	ID             int64                `json:"id"`
	Title          string               `json:"title"`
	Tags           []string             `json:"tags"`
	TMDBID         int                  `json:"tmdbId,omitempty"`
	Year           int                  `json:"year,omitempty"`
	CollectionName string               `json:"collectionName,omitempty"`
	Genres         []string             `json:"genres,omitempty"`
	Cast           []string             `json:"cast,omitempty"`
	CreatedAt      string               `json:"createdAt,omitempty"`
	QualityTiers   []string             `json:"qualityTiers,omitempty"`
	Files          []libraryTrackedFile `json:"files,omitempty"`
	VideoURL       string               `json:"videoUrl,omitempty"`
}

// libraryTrackedFile mirrors apidto.TrackedItemFile for Movies multi-file titles.
type libraryTrackedFile struct {
	ID          int64   `json:"id"`
	FilePath    string  `json:"filePath"`
	IsPrimary   bool    `json:"isPrimary"`
	QualityTier string  `json:"qualityTier,omitempty"`
	Size        int64   `json:"size,omitempty"`
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	VideoCodec  string  `json:"videoCodec,omitempty"`
	BitRate     int64   `json:"bitrate,omitempty"`
	DurationSec float64 `json:"durationSec,omitempty"`
}

// browserPlayableVideo reports whether a <video> element can decode the file at
// path. An Adult scene in a container no browser plays (mkv, avi, wmv…) gets no
// videoUrl at all rather than one that renders as a broken element — the poster
// falls back to the title tile instead.
func browserPlayableVideo(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".webm", ".mov":
		return true
	default:
		return false
	}
}

// listTrackedHandler returns every item {mode} currently tracks — straight
// from libStore for every mode now (no *arr app involved): items for Movies,
// series for Series, scenes for Adult (Whisparr eliminated, Stage 4). Backs
// the Tag workflow's item picker (there's no other way to browse what's
// trackable to assign/remove a tag on) and is generically useful anywhere a
// UI needs real item context instead of guessing an ID.
func listTrackedHandler(libStore *library.Store) http.HandlerFunc {
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
				// A movie has exactly one file, so exactly one tier — but a
				// never-captured "" folds to "unknown" rather than being
				// omitted, so the Dashboard's clickable Unknown cell drills
				// down to these rows instead of an empty list.
				tier := item.QualityTier
				if tier == "" {
					tier = "unknown"
				}
				files, err := libStore.ListFiles(ctx, item.ID)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				trackedFiles := make([]libraryTrackedFile, len(files))
				tiers := []string{}
				seenTier := map[string]bool{}
				for j, f := range files {
					ft := f.QualityTier
					if ft == "" {
						ft = "unknown"
					}
					trackedFiles[j] = libraryTrackedFile{
						ID: f.ID, FilePath: f.FilePath, IsPrimary: f.IsPrimary,
						QualityTier: ft, Size: f.Size, Width: f.Width, Height: f.Height,
						VideoCodec: f.VideoCodec, BitRate: f.BitRate, DurationSec: f.DurationSec,
					}
					if !seenTier[ft] {
						seenTier[ft] = true
						tiers = append(tiers, ft)
					}
				}
				if len(tiers) == 0 {
					tiers = []string{tier}
				}
				out[i] = libraryTrackedItem{
					ID: item.ID, Title: item.Title, Tags: tags, TMDBID: item.TMDBID, Year: item.Year,
					CollectionName: item.CollectionName, Genres: item.Genres, Cast: item.Cast,
					CreatedAt: item.CreatedAt, QualityTiers: tiers, Files: trackedFiles,
				}
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
			// One query for every series' distinct episode tiers, hoisted out
			// of the loop — this route already does a per-item SeriesTags()
			// lookup and a second N+1 on it isn't worth adding. A series with
			// no episodes on disk simply has no entry, so its QualityTiers
			// stays nil (omitted), matching the fact that it contributes
			// nothing to the Storage Allocation grid either.
			tiersBySeries, err := libStore.EpisodeTiersBySeries(ctx)
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
				out[i] = libraryTrackedItem{ID: s.ID, Title: s.Title, Tags: tags, TMDBID: s.TMDBID, Year: s.Year, Genres: s.Genres, Cast: s.Cast, CreatedAt: s.CreatedAt, QualityTiers: tiersBySeries[s.ID]}
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
				tier := sc.QualityTier
				if tier == "" {
					tier = "unknown"
				}
				out[i] = libraryTrackedItem{
					ID: sc.ID, Title: sc.Title, Tags: tags,
					CreatedAt: sc.CreatedAt, QualityTiers: []string{tier},
				}
				if sc.FilePath != "" && browserPlayableVideo(sc.FilePath) {
					out[i].VideoURL = fmt.Sprintf("/api/modes/adult/tracked/%d/video", sc.ID)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(out)
			return
		}

		http.Error(w, fmt.Sprintf("unknown mode %q", m), http.StatusBadRequest)
	}
}

// trackedVideoHandler streams one tracked scene's own video file, which the
// Library grid uses as the poster still — an Adult scene has no tmdbId and so no
// poster artwork to fetch. The path never crosses the wire: the client addresses
// a library row id and the file path is resolved here.
//
// The Adult section-lock check runs BEFORE the file is opened, so a locked
// request answers 403 without a byte of the scene reaching the client
// (TestTrackedVideoHandler_AdultLockedRefusesBeforeAnyBytes pins that ordering).
func trackedVideoHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		if m != mode.Adult {
			http.Error(w, "tracked video is only supported for adult scenes right now", http.StatusBadRequest)
			return
		}
		if denyIfAdultLocked(w, r) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid tracked id", http.StatusBadRequest)
			return
		}
		scene, err := libStore.GetSceneByID(r.Context(), id)
		if err != nil {
			if errors.Is(err, library.ErrNotFound) {
				http.Error(w, "no tracked scene with that id", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if scene.FilePath == "" {
			http.Error(w, "tracked scene has no video file", http.StatusNotFound)
			return
		}
		f, err := os.Open(scene.FilePath)
		if err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "tracked scene video file not found", http.StatusNotFound)
				return
			}
			http.Error(w, "tracked scene video file is not accessible", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.Error(w, "tracked scene video file is not accessible", http.StatusInternalServerError)
			return
		}
		if info.IsDir() {
			http.Error(w, "tracked scene video path is a directory", http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	}
}
