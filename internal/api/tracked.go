package api

import (
	"context"
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
	PosterURL      string               `json:"posterUrl,omitempty"`
	// Claude 2026-08-14: stored Adult catalog identity for Library
	// enrichment (Discover-minus-grab). Box/SceneID/Studio/Date are copied
	// from library_scenes at list time — GET /tracked still must not call
	// TPDB/StashDB/FansDB. Box is AdultDiscoverItem.source; SceneID is the
	// catalog UUID (not library_scenes.id, which stays on ID).
	// Reason: Library cards open DetailPopup with allowGrab=false; the
	// description fetch needs source+id, and the hover overlay needs
	// studio/date. Review if: a live catalog lookup is ever added here.
	Box     string `json:"box,omitempty"`
	SceneID string `json:"sceneId,omitempty"`
	Studio  string `json:"studio,omitempty"`
	Date    string `json:"date,omitempty"`
	// Claude 2026-08-14: operator 1–5 star rating copied from the library
	// row. 0/omitted = unrated. GET /tracked must not look up catalog scores.
	// Review if: Discover's existing-library row starts showing stars too.
	Rating int `json:"rating,omitempty"`
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
	// Claude 2026-08-14: per-file in-app playback URL for Library Movies.
	// Reason: GET /tracked/{id}/video now serves movies; the list must say
	// which files a <video> element can actually decode (same omit-when-mkv
	// rule as Adult's item-level videoUrl).
	// Review if: transcoding lands and every container becomes playable.
	VideoURL string `json:"videoUrl,omitempty"`
}

// browserPlayableVideo reports whether a <video> element can decode the file at
// path. A tracked Adult scene or Movies file in a container no browser plays
// (mkv, avi, wmv…) gets no videoUrl at all rather than one that renders as a
// broken element — Adult falls back to the title tile; Movies shows a format
// note in the Files list instead of a Play control.
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
					if browserPlayableVideo(f.FilePath) {
						trackedFiles[j].VideoURL = fmt.Sprintf("/api/modes/movies/tracked/%d/video?fileId=%d", item.ID, f.ID)
					}
					if !seenTier[ft] {
						seenTier[ft] = true
						tiers = append(tiers, ft)
					}
				}
				if len(tiers) == 0 {
					tiers = []string{tier}
				}
				primaryPath := item.FilePath
				for _, f := range files {
					if f.IsPrimary && strings.TrimSpace(f.FilePath) != "" {
						primaryPath = f.FilePath
						break
					}
				}
				itemVideoURL := ""
				if primaryPath != "" && browserPlayableVideo(primaryPath) {
					itemVideoURL = fmt.Sprintf("/api/modes/movies/tracked/%d/video", item.ID)
				}
				out[i] = libraryTrackedItem{
					ID: item.ID, Title: item.Title, Tags: tags, TMDBID: item.TMDBID, Year: item.Year,
					CollectionName: item.CollectionName, Genres: item.Genres, Cast: item.Cast,
					CreatedAt: item.CreatedAt, QualityTiers: tiers, Files: trackedFiles,
					VideoURL: itemVideoURL, Rating: item.Rating,
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
				out[i] = libraryTrackedItem{ID: s.ID, Title: s.Title, Tags: tags, TMDBID: s.TMDBID, Year: s.Year, Genres: s.Genres, Cast: s.Cast, CreatedAt: s.CreatedAt, QualityTiers: tiersBySeries[s.ID], Rating: s.Rating}
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
			aspect, aerr := parseAdultAspectQuery(r)
			if aerr != nil {
				http.Error(w, aerr.Error(), http.StatusBadRequest)
				return
			}
			scenes, err := libStore.ListScenesFiltered(ctx, aspect)
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
					PosterURL: sc.PosterURL,
					Box:       sc.Box, SceneID: sc.SceneID,
					Studio: sc.Studio, Date: sc.Date,
					Rating: sc.Rating,
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

// trackedVideoHandler streams one tracked item's own video file. Adult uses it
// as the Library poster still (no tmdbId, so no artwork). Movies uses it for
// in-app playback in the Library detail panel. The path never crosses the
// wire: the client addresses a library row id (and optional fileId) and the
// file path is resolved here.
//
// The Adult section-lock check runs BEFORE the file is opened, so a locked
// request answers 403 without a byte of the scene reaching the client
// (TestTrackedVideoHandler_AdultLockedRefusesBeforeAnyBytes pins that ordering).
// Movies does not run that check — locking Adult must not blank movie playback
// (TestTrackedVideoHandler_MoviesServesWhileAdultLocked).
func trackedVideoHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		switch m {
		case mode.Adult:
			serveAdultTrackedVideo(w, r, libStore)
		case mode.Movies:
			serveMoviesTrackedVideo(w, r, libStore)
		default:
			http.Error(w, "tracked video is only supported for adult scenes and movies right now", http.StatusBadRequest)
		}
	}
}

func serveAdultTrackedVideo(w http.ResponseWriter, r *http.Request, libStore *library.Store) {
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
	serveLocalVideoFile(w, r, scene.FilePath, "scene")
}

func serveMoviesTrackedVideo(w http.ResponseWriter, r *http.Request, libStore *library.Store) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid tracked id", http.StatusBadRequest)
		return
	}
	fileID, ok := parseOptionalFileID(w, r)
	if !ok {
		return
	}
	path, err := movieTrackedVideoPath(r.Context(), libStore, id, fileID)
	if err != nil {
		if errors.Is(err, library.ErrNotFound) {
			http.Error(w, "no tracked movie video with that id", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	serveLocalVideoFile(w, r, path, "movie")
}

// parseOptionalFileID reads ?fileId= from the query. Missing/empty means
// "primary file". Invalid values 400 and return ok=false so the caller
// returns without a second write.
func parseOptionalFileID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("fileId"))
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		http.Error(w, "invalid fileId", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// movieTrackedVideoPath resolves the on-disk path for a Movies tracked row.
// fileID 0 means the primary (then first listed file, then the denormalized
// item.FilePath). A non-zero fileID must belong to this item — ListFiles is
// scoped to itemID, so a sibling title's file id is a 404, not a path leak.
func movieTrackedVideoPath(ctx context.Context, libStore *library.Store, itemID, fileID int64) (string, error) {
	item, err := libStore.Get(ctx, itemID)
	if err != nil {
		return "", err
	}
	if item.Mode != mode.Movies {
		return "", library.ErrNotFound
	}
	files, err := libStore.ListFiles(ctx, itemID)
	if err != nil {
		return "", err
	}
	if fileID > 0 {
		for _, f := range files {
			if f.ID == fileID {
				return f.FilePath, nil
			}
		}
		return "", library.ErrNotFound
	}
	for _, f := range files {
		if f.IsPrimary && strings.TrimSpace(f.FilePath) != "" {
			return f.FilePath, nil
		}
	}
	if len(files) > 0 && strings.TrimSpace(files[0].FilePath) != "" {
		return files[0].FilePath, nil
	}
	return item.FilePath, nil
}

// serveLocalVideoFile opens path and hands it to http.ServeContent (Range/seek
// works). kind is only for the error string ("scene" / "movie").
func serveLocalVideoFile(w http.ResponseWriter, r *http.Request, path, kind string) {
	if path == "" {
		http.Error(w, "tracked "+kind+" has no video file", http.StatusNotFound)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "tracked "+kind+" video file not found", http.StatusNotFound)
			return
		}
		http.Error(w, "tracked "+kind+" video file is not accessible", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "tracked "+kind+" video file is not accessible", http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "tracked "+kind+" video path is a directory", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func parseAdultAspectQuery(r *http.Request) (string, error) {
	return library.ParsePosterAspectFilter(r.URL.Query().Get("aspect"))
}
