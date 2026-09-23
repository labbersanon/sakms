package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// localPosterNames are the sidecar images Jellyfin already stores beside a film.
var localPosterNames = []string{"folder.jpg", "poster.jpg", "cover.jpg", "folder.png", "poster.png"}

// localPosterFile returns a poster image in the same directory as the video.
// Only those fixed names are accepted, so a library path cannot be turned
// into an arbitrary file read.
func localPosterFile(videoPath string) (string, error) {
	if videoPath == "" {
		return "", os.ErrNotExist
	}
	dir := filepath.Dir(videoPath)
	for _, name := range localPosterNames {
		p := filepath.Join(dir, name)
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		return p, nil
	}
	return "", os.ErrNotExist
}

func localPosterURL(itemID int64) string {
	return fmt.Sprintf("/api/posters/local?itemId=%d", itemID)
}

func applyLocalFilmPoster(ctx context.Context, libStore *library.Store, it library.Item) bool {
	if _, err := localPosterFile(it.FilePath); err != nil {
		return false
	}
	url := localPosterURL(it.ID)
	if err := libStore.SetMoviePosterByID(ctx, it.ID, url, library.PosterSourceLocal); err != nil {
		log.Printf("poster backfill: local poster movie id=%d: %v", it.ID, err)
		return false
	}
	log.Printf("poster backfill: local poster %q id=%d", it.Title, it.ID)
	return true
}

func localPosterHandler(libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if libStore == nil {
			http.Error(w, "library unavailable", http.StatusInternalServerError)
			return
		}
		id, err := strconv.ParseInt(r.URL.Query().Get("itemId"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "itemId is required", http.StatusBadRequest)
			return
		}
		item, err := libStore.Get(r.Context(), id)
		if err != nil || item.Mode != mode.Movies {
			http.NotFound(w, r)
			return
		}
		path, err := localPosterFile(item.FilePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	}
}
