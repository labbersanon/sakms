package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Settings keys for the unified downloader's operator-tunable knobs.
//
// Claude 2026-08-01: DownloaderMaxConnectionsKey narrowed to torrent-only.
// Reason: it used to also seed the usenet Manager's connection count
// (cmd/sakms/main.go's old buildUsenetManager), which collided with the new
// per-subscription MaxConns field once multiple Usenet subscriptions were
// possible. Usenet connection counts are now configured per-subscription in
// the Usenet settings page, with internal/usenet's defaultMaxConnsPerServer
// covering an unset value — this setting no longer has any effect on Usenet.
// Troubleshooting: a Usenet connection-count change here doing nothing.
// Review if: this comment predates a future Downloader settings UI, which
// should inherit "torrent engine only" wording rather than re-litigate it.
const (
	DownloaderStagingDirKey     = "downloader_staging_dir"
	DownloaderMaxConcurrentKey  = "downloader_max_concurrent"
	DownloaderMaxConnectionsKey = "downloader_max_connections"
)

// Defaults for the concurrency knobs when unset (per the feature spec).
const (
	downloaderDefaultMaxConcurrent  = 3
	downloaderDefaultMaxConnections = 4
)

// downloadsGlobalPausedKey is the settings flag backing the system-wide download
// pause toggle (GET/PUT /api/downloads/pause-state). It's a simple app-wide
// bool, so it lives in the flat settings KV store — the established home for
// one-off flags (see internal/settings' package doc) — rather than its own table.
const downloadsGlobalPausedKey = "downloads_global_paused"

// errDownloadsPaused is returned by dispatchToDownloadClient when the global
// pause is on, so the frontend can distinguish "blocked because paused" from any
// other grab failure. The frontend keys on this exact message (the codebase's
// fixed-error-string convention — see Discover's GrabError matching), so the
// "globally paused" token must stay stable; it deliberately does NOT reuse the
// 409 grabHandler already gives for "already grabbing this release" (it surfaces
// as 423 Locked instead — see dispatchToDownloadClient).
var errDownloadsPaused = errors.New("downloads are globally paused — resume in the Downloads screen before grabbing new releases")

// toDTODownload maps a downloader.Download to the wire DTO, deriving a display
// filename (basename of the first file, GID fallback).
func toDTODownload(d downloader.Download) apidto.Download {
	name := d.Filename
	if name != "" {
		name = filepath.Base(name)
	}
	if name == "" {
		name = d.GID
	}
	return apidto.Download{
		GID:             d.GID,
		Status:          d.Status,
		Filename:        name,
		TotalLength:     d.TotalLength,
		CompletedLength: d.CompletedLength,
		DownloadSpeed:   d.DownloadSpeed,
		Connections:     d.Connections,
		ErrorMessage:    d.ErrorMessage,
	}
}

// toUsenetDTODownload maps a usenet.Download to the wire DTO. Mirrors toDTODownload.
func toUsenetDTODownload(d usenet.Download) apidto.Download {
	name := d.Filename
	if name != "" {
		name = filepath.Base(name)
	}
	if name == "" {
		name = d.GID
	}
	return apidto.Download{
		GID:             d.GID,
		Status:          d.Status,
		Filename:        name,
		TotalLength:     d.TotalLength,
		CompletedLength: d.CompletedLength,
		DownloadSpeed:   d.DownloadSpeed,
		Connections:     d.Connections,
		ErrorMessage:    d.ErrorMessage,
	}
}

// mergedDownloads returns the combined torrent + usenet download queue as a
// DTO slice. Returns a non-nil empty slice so JSON encodes [] not null.
func mergedDownloads(dl *downloader.Manager, nzb *usenet.Manager) []apidto.Download {
	out := make([]apidto.Download, 0)
	if dl != nil {
		for _, d := range dl.List() {
			out = append(out, toDTODownload(d))
		}
	}
	if nzb != nil {
		for _, d := range nzb.List() {
			out = append(out, toUsenetDTODownload(d))
		}
	}
	return out
}

// listDownloadsHandler returns the current download queue.
func listDownloadsHandler(dl *downloader.Manager, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dl == nil && nzb == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, mergedDownloads(dl, nzb))
	}
}

// downloadsStreamHandler streams the combined torrent + usenet download queue
// as server-sent events. It subscribes to both managers; an event from either
// triggers a full re-snapshot so the UI always sees the merged queue.
// Nil channels (unconfigured engine) simply never fire in the select, so the
// handler works correctly when only one engine is running.
func downloadsStreamHandler(dl *downloader.Manager, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dl == nil && nzb == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ctx := r.Context()

		// Paint an initial snapshot immediately so the screen isn't blank until
		// the queue next changes.
		writeSSEData(w, flusher, mergedDownloads(dl, nzb))

		// Subscribe to both managers. A nil channel blocks forever in a select,
		// so the unconfigured engine's case simply never fires — correct behavior.
		var dlCh <-chan []downloader.Download
		var dlCancel func()
		if dl != nil {
			dlCh, dlCancel = dl.Subscribe()
			defer dlCancel()
		}
		var nzbCh <-chan []usenet.Download
		var nzbCancel func()
		if nzb != nil {
			nzbCh, nzbCancel = nzb.Subscribe()
			defer nzbCancel()
		}

		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-dlCh:
				if !ok {
					return
				}
				writeSSEData(w, flusher, mergedDownloads(dl, nzb))
			case _, ok := <-nzbCh:
				if !ok {
					return
				}
				writeSSEData(w, flusher, mergedDownloads(dl, nzb))
			}
		}
	}
}

// writeSSEData marshals v and writes it as one SSE data frame, then flushes.
func writeSSEData(w http.ResponseWriter, flusher http.Flusher, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// routeGIDAction routes a single-GID download action to the correct engine by
// GID prefix ("nzb-" → usenet; anything else → torrent). Returns true on
// success, false (with the error already written) on failure.
func routeGIDAction(w http.ResponseWriter, r *http.Request, dl *downloader.Manager, nzb *usenet.Manager, dlFn, nzbFn func(string) error) bool {
	gid := r.PathValue("gid")
	var err error
	if strings.HasPrefix(gid, "nzb-") {
		if nzb == nil {
			http.Error(w, "the usenet engine isn't running", http.StatusServiceUnavailable)
			return false
		}
		err = nzbFn(gid)
	} else {
		if dl == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return false
		}
		err = dlFn(gid)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return false
	}
	return true
}

func cancelDownloadHandler(dl *downloader.Manager, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if routeGIDAction(w, r, dl, nzb, dl.Cancel, nzb.Cancel) {
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func pauseDownloadHandler(dl *downloader.Manager, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if routeGIDAction(w, r, dl, nzb, dl.Pause, nzb.Pause) {
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func resumeDownloadHandler(dl *downloader.Manager, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if routeGIDAction(w, r, dl, nzb, dl.Resume, nzb.Resume) {
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

// cancelOne routes a single GID to the correct engine's Cancel (same "nzb-"
// prefix routing as routeGIDAction) and returns any error — the non-HTTP core
// the bulk-cancel loop reuses so one item's failure can be recorded and skipped
// rather than aborting the whole request.
func cancelOne(dl *downloader.Manager, nzb *usenet.Manager, gid string) error {
	if strings.HasPrefix(gid, "nzb-") {
		if nzb == nil {
			return errors.New("the usenet engine isn't running")
		}
		return nzb.Cancel(gid)
	}
	if dl == nil {
		return errors.New("the download engine isn't running")
	}
	return dl.Cancel(gid)
}

// bulkCancelHandler backs POST /api/downloads/cancel-batch — cancel several
// downloads (deleting their files, same as the per-item DELETE) in one call.
// Reuses the per-GID Cancel methods in a loop with skip-and-continue semantics
// (one item's failure never blocks the rest), matching this project's bulk-apply
// convention. Always HTTP 200 (except the empty-body 400); per-item results
// carry each GID's outcome.
func bulkCancelHandler(dl *downloader.Manager, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dl == nil && nzb == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		var req apidto.BulkCancelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(req.GIDs) == 0 {
			http.Error(w, "gids is required", http.StatusBadRequest)
			return
		}
		results := make([]apidto.BulkCancelResultItem, 0, len(req.GIDs))
		for _, gid := range req.GIDs {
			res := apidto.BulkCancelResultItem{GID: gid}
			if err := cancelOne(dl, nzb, gid); err != nil {
				res.Error = err.Error()
			} else {
				res.OK = true
			}
			results = append(results, res)
		}
		writeJSON(w, apidto.BulkCancelResponse{Results: results})
	}
}

// getPauseStateHandler returns the global download pause flag.
func getPauseStateHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		paused, err := settingsStore.GetBool(r.Context(), downloadsGlobalPausedKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, apidto.DownloadPauseState{Paused: paused})
	}
}

// putPauseStateHandler sets the global download pause flag. Setting it true
// pauses every currently-active download (reusing each engine's per-item Pause,
// best-effort/skip-and-continue) AND — via the gate in dispatchToDownloadClient
// keyed on the same flag — blocks any new grab until it's set back to false.
// Setting it false only lifts that gate: it does NOT auto-resume the downloads
// this toggle paused (per-item Resume stays the operator's tool, and usenet has
// no resume anyway — re-submit the NZB), scoped deliberately to the spec.
func putPauseStateHandler(settingsStore *settings.Store, dl *downloader.Manager, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.DownloadPauseState
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if err := settingsStore.SetBool(ctx, downloadsGlobalPausedKey, req.Paused); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if req.Paused {
			pauseAllActive(dl, nzb)
		}
		writeJSON(w, apidto.DownloadPauseState{Paused: req.Paused})
	}
}

// pauseAllActive pauses every currently in-flight download across both engines,
// best-effort: a per-item Pause failure (e.g. an entry that just completed) is
// skipped, never fatal. nil engines are simply skipped.
func pauseAllActive(dl *downloader.Manager, nzb *usenet.Manager) {
	if dl != nil {
		for _, d := range dl.List() {
			if d.Status == "active" || d.Status == "waiting" {
				_ = dl.Pause(d.GID)
			}
		}
	}
	if nzb != nil {
		for _, d := range nzb.List() {
			if d.Status == "active" {
				_ = nzb.Pause(d.GID)
			}
		}
	}
}

// getDownloaderConfigHandler returns the downloader's staging dir + concurrency
// knobs, filling in defaults for unset numeric fields (staging dir "" when
// unset — the caller/boot supplies the real default path).
func getDownloaderConfigHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		staging, err := getSetting(ctx, settingsStore, DownloaderStagingDirKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		conc, err := getSettingInt(ctx, settingsStore, DownloaderMaxConcurrentKey, downloaderDefaultMaxConcurrent)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		conn, err := getSettingInt(ctx, settingsStore, DownloaderMaxConnectionsKey, downloaderDefaultMaxConnections)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, apidto.DownloaderConfig{
			StagingDir:     staging,
			MaxConcurrent:  conc,
			MaxConnections: conn,
		})
	}
}

// putDownloaderConfigHandler stores the torrent downloader's staging dir +
// concurrency knobs. MaxConnections here applies to the torrent engine only —
// Usenet connection counts are configured per-subscription in the Usenet
// settings page instead (see DownloaderMaxConnectionsKey's doc comment).
// Concurrency values must be positive; staging dir is free-typed (it's
// validated for existence/writability the next time the engine restarts, same
// tolerance as a library root folder). A change takes effect on restart.
func putDownloaderConfigHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.DownloaderConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.MaxConcurrent < 1 || req.MaxConnections < 1 {
			http.Error(w, "maxConcurrent and maxConnections must be at least 1", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if err := settingsStore.Set(ctx, DownloaderStagingDirKey, req.StagingDir); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(ctx, DownloaderMaxConcurrentKey, strconv.Itoa(req.MaxConcurrent)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(ctx, DownloaderMaxConnectionsKey, strconv.Itoa(req.MaxConnections)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getSetting returns a settings value, "" when unset (ErrNotFound is a normal
// "not configured" state, not an error).
func getSetting(ctx context.Context, store *settings.Store, key string) (string, error) {
	v, err := store.Get(ctx, key)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return "", err
	}
	return v, nil
}

// getSettingInt returns a settings value parsed as int, or def when unset or
// unparseable.
func getSettingInt(ctx context.Context, store *settings.Store, key string, def int) (int, error) {
	v, err := getSetting(ctx, store, key)
	if err != nil {
		return 0, err
	}
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, nil
	}
	return n, nil
}
