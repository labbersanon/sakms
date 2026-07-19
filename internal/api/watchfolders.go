package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/dedup"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/parseentity"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/rename"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

// WatchFoldersEnabledKey is the settings key for the watch-folders toggle.
const WatchFoldersEnabledKey = "watch_folders_enabled"

// watchDebounce is how long to wait after the last filesystem event before
// triggering a Scan — absorbs burst events from a download client dropping a
// full directory tree into the root folder.
const watchDebounce = 10 * time.Second

// watchPollInterval is how often RunWatchFolders re-reads configuration from
// the settings store to pick up root-folder-path or enabled/disabled changes.
const watchPollInterval = 30 * time.Second

// RunWatchFolders monitors each mode's library root folder for new files and
// triggers a Rename Scan when new content appears. Gated off by default
// (WatchFoldersEnabledKey = false). Only Scan is ever triggered — proposals
// appear in the Rename queue and still require a human Apply click. Never
// auto-Apply. Must be launched as a goroutine from main.go and cancelled via
// ctx when the server shuts down.
func RunWatchFolders(ctx context.Context, httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, videoHasher rename.PHasher, prober dedup.Prober, entityStore parseentity.EntityStore) {
	for {
		enabled, err := settingsStore.GetBool(ctx, WatchFoldersEnabledKey, false)
		if err != nil || !enabled {
			select {
			case <-ctx.Done():
				return
			case <-time.After(watchPollInterval):
			}
			continue
		}

		// Collect configured root paths for all three modes.
		roots := map[mode.Mode]string{}
		for _, m := range []mode.Mode{mode.Movies, mode.Series, mode.Adult} {
			key, ok := libraryRootFolderKey(m)
			if !ok {
				continue
			}
			path, err := settingsStore.Get(ctx, key)
			if errors.Is(err, settings.ErrNotFound) || path == "" {
				continue
			}
			if err != nil {
				log.Printf("watchfolders: reading root for %s: %v", m, err)
				continue
			}
			roots[m] = path
		}

		if len(roots) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(watchPollInterval):
			}
			continue
		}

		runWatcher(ctx, roots, httpClient, connStore, settingsStore, propStore, libStore, videoHasher, prober, entityStore)

		if ctx.Err() != nil {
			return
		}
		// Fell through from runWatcher (poll tick) — loop to re-read settings.
	}
}

// runWatcher sets up an fsnotify.Watcher on the given roots and serves events
// until ctx is cancelled or the poll interval fires (so the caller can re-read
// settings and restart with updated paths).
func runWatcher(ctx context.Context, roots map[mode.Mode]string, httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, videoHasher rename.PHasher, prober dedup.Prober, entityStore parseentity.EntityStore) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("watchfolders: creating watcher: %v", err)
		return
	}
	defer w.Close()

	// pathToMode maps a watched root path back to its mode.
	pathToMode := map[string]mode.Mode{}
	for m, path := range roots {
		if err := w.Add(path); err != nil {
			log.Printf("watchfolders: watching %s (%s): %v", path, m, err)
			continue
		}
		pathToMode[path] = m
		log.Printf("watchfolders: watching %s (%s)", path, m)
	}

	// Per-mode debounce timers: the timer fires watchDebounce after the last
	// event for that mode, triggering a scan exactly once per burst.
	timers := map[mode.Mode]*time.Timer{}
	triggerScan := func(m mode.Mode) {
		if t, ok := timers[m]; ok {
			t.Stop()
		}
		timers[m] = time.AfterFunc(watchDebounce, func() {
			scanFromWatcher(context.Background(), m, httpClient, connStore, settingsStore, propStore, libStore, videoHasher, prober, entityStore)
		})
	}

	poll := time.NewTicker(watchPollInterval)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			for _, t := range timers {
				t.Stop()
			}
			return

		case <-poll.C:
			// Stop timers and let the caller re-read settings.
			for _, t := range timers {
				t.Stop()
			}
			return

		case event, ok := <-w.Events:
			if !ok {
				return
			}
			if !event.Has(fsnotify.Create) && !event.Has(fsnotify.Rename) {
				continue
			}
			// Map the event's directory back to its mode. fsnotify reports
			// the full path of the created file/dir; we need the parent dir
			// (which is the root we're watching).
			for path, m := range pathToMode {
				cleanPath := filepath.Clean(path)
				if event.Name == cleanPath || strings.HasPrefix(event.Name, cleanPath+string(os.PathSeparator)) {
					triggerScan(m)
					break
				}
			}

		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			log.Printf("watchfolders: %v", err)
		}
	}
}

// scanFromWatcher runs a Rename scan for m and replaces its Rename queue,
// exactly like renameScanHandler — the watch-folder trigger is a Scan-only
// automation, never an Apply. Errors are logged and dropped; the user's
// manual Scan button always remains the fallback.
func scanFromWatcher(ctx context.Context, m mode.Mode, httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, videoHasher rename.PHasher, prober dedup.Prober, entityStore parseentity.EntityStore) {
	log.Printf("watchfolders: scan triggered for %s", m)

	sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, nil, m)
	if err != nil {
		log.Printf("watchfolders: building session for %s: %v", m, err)
		return
	}
	if sess.Identify != nil {
		sess.Identify.EntityStore = entityStore
	}

	key, ok := libraryRootFolderKey(m)
	if !ok {
		return
	}
	rootPath, err := settingsStore.Get(ctx, key)
	if errors.Is(err, settings.ErrNotFound) || rootPath == "" {
		return
	}
	if err != nil {
		log.Printf("watchfolders: reading root for %s: %v", m, err)
		return
	}

	var found []proposals.Proposal
	switch m {
	case mode.Movies:
		preset, err := resolveNamingPreset(ctx, settingsStore, m)
		if err != nil {
			log.Printf("watchfolders: resolving preset for %s: %v", m, err)
			return
		}
		threshold, err := resolveConfidenceThreshold(ctx, settingsStore, m)
		if err != nil {
			log.Printf("watchfolders: resolving threshold for %s: %v", m, err)
			return
		}
		found, err = rename.ScanLibrary(ctx, sess, libStore, rootPath, preset, threshold)
		if err != nil {
			log.Printf("watchfolders: scan movies: %v", err)
			return
		}
	case mode.Series:
		preset, err := resolveNamingPreset(ctx, settingsStore, m)
		if err != nil {
			log.Printf("watchfolders: resolving preset for %s: %v", m, err)
			return
		}
		threshold, err := resolveConfidenceThreshold(ctx, settingsStore, m)
		if err != nil {
			log.Printf("watchfolders: resolving threshold for %s: %v", m, err)
			return
		}
		found, err = rename.ScanLibrarySeries(ctx, sess, libStore, rootPath, preset, threshold)
		if err != nil {
			log.Printf("watchfolders: scan series: %v", err)
			return
		}
	case mode.Adult:
		found, err = rename.ScanLibraryAdult(ctx, sess, libStore, videoHasher, prober, rootPath)
		if err != nil {
			log.Printf("watchfolders: scan adult: %v", err)
			return
		}
	}

	if _, err := propStore.ReplacePending(ctx, m, proposals.Rename, found); err != nil {
		log.Printf("watchfolders: saving proposals for %s: %v", m, err)
	} else {
		log.Printf("watchfolders: %s scan complete, %d proposals", m, len(found))
	}
}

// --- HTTP handlers ---

type watchFoldersStatusResponse struct {
	Enabled bool              `json:"enabled"`
	Roots   map[string]string `json:"roots"` // mode → path (only configured roots)
}

type watchFoldersEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// getWatchFoldersHandler returns whether watch folders is enabled and the
// currently configured root paths — so the frontend can show the user what
// will be watched when they enable the feature.
func getWatchFoldersHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		enabled, err := settingsStore.GetBool(ctx, WatchFoldersEnabledKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		roots := map[string]string{}
		for _, m := range []mode.Mode{mode.Movies, mode.Series, mode.Adult} {
			key, ok := libraryRootFolderKey(m)
			if !ok {
				continue
			}
			path, err := settingsStore.Get(ctx, key)
			if errors.Is(err, settings.ErrNotFound) || path == "" {
				continue
			}
			if err == nil {
				roots[string(m)] = path
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(watchFoldersStatusResponse{Enabled: enabled, Roots: roots})
	}
}

// putWatchFoldersEnabledHandler enables or disables the watch-folders feature.
// The change takes effect on the watcher goroutine's next poll tick (within
// watchPollInterval seconds).
func putWatchFoldersEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req watchFoldersEnabledRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := settingsStore.SetBool(r.Context(), WatchFoldersEnabledKey, req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
