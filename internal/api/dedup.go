package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/settings"
)

// dedupScanHandler runs the Dedup workflow's propose-phase for {mode} and
// replaces the live Dedup queue with whatever duplicate groups it found.
// prober takes dedup.Prober's interface, not the concrete *mediainfo.Prober,
// so tests can inject a fake instead of depending on a real ffprobe binary.
//
// Movies/Series dispatch to the phash-primary scan (ScanLibraryPHash /
// ScanLibrarySeriesPHash): all files — tracked and orphans — are grouped by
// perceptual similarity alone; TMDB is used only for display labels and never
// determines whether files are grouped. Adult dispatches to ScanLibraryAdult
// (Whisparr eliminated, Stage 4), which groups by (box, scene_id) and refines
// by perceptual similarity.
func dedupScanHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store, prober dedup.Prober, hasher dedup.PHasher, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var found []proposals.Proposal
		// Movies/Series dispatch to the library-backed scan; Adult stays on
		// the Servarr-backed dedup.Scan. This gate is deliberately explicit
		// rather than keyed off libraryRootFolderKey(m)'s ok: Adult now has a
		// free-typed library-root-folder key too, but its library-backed
		// dedup sibling isn't wired here yet (that lands with Whisparr
		// elimination).
		if m == mode.Movies || m == mode.Series {
			key, _ := libraryRootFolderKey(m)
			rootPath, rpErr := settingsStore.Get(ctx, key)
			if rpErr != nil && !errors.Is(rpErr, settings.ErrNotFound) {
				http.Error(w, rpErr.Error(), http.StatusInternalServerError)
				return
			}
			threshold, tErr := resolvePHashThreshold(ctx, settingsStore, m)
			if tErr != nil {
				http.Error(w, tErr.Error(), http.StatusInternalServerError)
				return
			}
			if m == mode.Movies {
				found, err = dedup.ScanLibraryPHash(ctx, sess, libStore, rootPath, prober, hasher, threshold, nil)
			} else {
				found, err = dedup.ScanLibrarySeriesPHash(ctx, sess, libStore, rootPath, prober, hasher, threshold, nil)
			}
		} else {
			threshold, tErr := resolvePHashThreshold(ctx, settingsStore, m)
			if tErr != nil {
				http.Error(w, tErr.Error(), http.StatusInternalServerError)
				return
			}
			// Adult owns its own library now too (Whisparr eliminated, Stage 4):
			// dispatch to the library-backed sibling, the mirror image of the
			// Movies/Series branch above, using Adult's own free-typed
			// library-root-folder key.
			key, _ := libraryRootFolderKey(m)
			rootPath, rpErr := settingsStore.Get(ctx, key)
			if rpErr != nil && !errors.Is(rpErr, settings.ErrNotFound) {
				http.Error(w, rpErr.Error(), http.StatusInternalServerError)
				return
			}
			found, err = dedup.ScanLibraryAdult(ctx, sess, libStore, rootPath, prober, hasher, threshold, nil)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		saved, err := propStore.ReplacePending(ctx, m, proposals.Dedup, found)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(saved)
	}
}
