package mediafolder

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/nfo"
)

// Claude 2026-09-22: Jellyfin-compatible sidecars written by sakms.
// Reason: sakms is the metadata source; Jellyfin should prefer NFO/local art
//   instead of scraping. Matches folder.jpg + backdrop.jpg + tvshow.nfo/movie.nfo.
// Troubleshooting: Ancient Aliens had Jellyfin art on disk but sakms letter tile
//   (no tmdbId, no local-sidecar reader/writer).
// Review if: Emby/Plex need different filenames.
// Related files: internal/nfo, internal/api/poster.go, docs/jellyfin-metadata.md

const (
	PosterFile   = "folder.jpg"
	BackdropFile = "backdrop.jpg"
	MovieNFOFile = "movie.nfo"
	SeriesNFOFile = "tvshow.nfo"
)

// Art holds absolute https image URLs plus IDs to embed in NFO.
type Art struct {
	Title    string
	Year     int
	Plot     string
	TMDBID   int
	TVDBID   int
	IMDBID   string
	Poster   string // absolute https URL
	Backdrop string // absolute https URL
}

// EnsureMovie writes folder.jpg, backdrop.jpg, and movie.nfo into movieDir.
// Skips image rewrite when the file already exists and is non-empty unless
// force is true. Always refreshes NFO when TMDBID > 0.
func EnsureMovie(ctx context.Context, client *http.Client, movieDir string, art Art, force bool) error {
	if strings.TrimSpace(movieDir) == "" {
		return fmt.Errorf("mediafolder: empty movie dir")
	}
	if err := os.MkdirAll(movieDir, 0o755); err != nil {
		return fmt.Errorf("mediafolder: mkdir %s: %w", movieDir, err)
	}
	if err := fetchIfNeeded(ctx, client, art.Poster, filepath.Join(movieDir, PosterFile), force); err != nil {
		return err
	}
	if err := fetchIfNeeded(ctx, client, art.Backdrop, filepath.Join(movieDir, BackdropFile), force); err != nil {
		return err
	}
	return nfo.WriteMovie(filepath.Join(movieDir, MovieNFOFile), nfo.MovieNFO{
		TMDBID: art.TMDBID,
		TVDBID: art.TVDBID,
		IMDBID: art.IMDBID,
		Title:  art.Title,
		Year:   art.Year,
		Plot:   art.Plot,
	})
}

// EnsureSeries writes folder.jpg, backdrop.jpg, and tvshow.nfo into seriesDir.
func EnsureSeries(ctx context.Context, client *http.Client, seriesDir string, art Art, force bool) error {
	if strings.TrimSpace(seriesDir) == "" {
		return fmt.Errorf("mediafolder: empty series dir")
	}
	if err := os.MkdirAll(seriesDir, 0o755); err != nil {
		return fmt.Errorf("mediafolder: mkdir %s: %w", seriesDir, err)
	}
	if err := fetchIfNeeded(ctx, client, art.Poster, filepath.Join(seriesDir, PosterFile), force); err != nil {
		return err
	}
	if err := fetchIfNeeded(ctx, client, art.Backdrop, filepath.Join(seriesDir, BackdropFile), force); err != nil {
		return err
	}
	return nfo.WriteSeries(filepath.Join(seriesDir, SeriesNFOFile), nfo.SeriesNFO{
		TMDBID: art.TMDBID,
		TVDBID: art.TVDBID,
		IMDBID: art.IMDBID,
		Title:  art.Title,
		Year:   art.Year,
		Plot:   art.Plot,
	})
}

func fetchIfNeeded(ctx context.Context, client *http.Client, rawURL, dest string, force bool) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	if !strings.HasPrefix(rawURL, "https://") {
		return fmt.Errorf("mediafolder: refusing non-https image URL")
	}
	if !force {
		if st, err := os.Stat(dest); err == nil && st.Size() > 0 {
			return nil
		}
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("mediafolder: fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mediafolder: fetch %s: status %d", rawURL, resp.StatusCode)
	}
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	limited := io.LimitReader(resp.Body, 10<<20)
	if _, err := io.Copy(f, limited); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// MovieDirFromFile returns the movie folder containing videoPath.
func MovieDirFromFile(videoPath string) string {
	return filepath.Dir(videoPath)
}

// SeriesDirFromEpisode returns the series root for a file under Season NN/,
// or the episode's directory when not in a season folder.
func SeriesDirFromEpisode(episodePath string) string {
	dir := filepath.Dir(episodePath)
	base := filepath.Base(dir)
	if len(base) >= 6 && strings.EqualFold(base[:6], "season") {
		return filepath.Dir(dir)
	}
	return dir
}

// HasLocalPoster reports whether folder.jpg (or poster.jpg) exists and is non-empty.
func HasLocalPoster(mediaDir string) bool {
	for _, name := range []string{PosterFile, "poster.jpg"} {
		st, err := os.Stat(filepath.Join(mediaDir, name))
		if err == nil && st.Size() > 0 {
			return true
		}
	}
	return false
}

// LocalPosterPath returns the preferred local poster path if present.
func LocalPosterPath(mediaDir string) string {
	for _, name := range []string{PosterFile, "poster.jpg"} {
		p := filepath.Join(mediaDir, name)
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			return p
		}
	}
	return ""
}

// FileModTime returns mtime or zero when the path is missing.
func FileModTime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}
