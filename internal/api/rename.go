package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dedup"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/parseentity"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/settings"
)

type kidsRootPathResponse struct {
	Path string `json:"path"`
}

type kidsRootPathRequest struct {
	Path string `json:"path"`
}

// getKidsRootPathHandler returns {mode}'s configured Kids root folder path,
// or an empty string if unset (a normal state — the feature is off for that
// mode). 400s for Adult, which has no kids/general split concept.
func getKidsRootPathHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := mode.Mode(r.PathValue("mode")).KidsRootPathKey()
		if !ok {
			http.Error(w, "kids root path isn't applicable to this mode", http.StatusBadRequest)
			return
		}
		path, err := settingsStore.Get(r.Context(), key)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(kidsRootPathResponse{Path: path})
	}
}

// putKidsRootPathHandler stores {mode}'s Kids root folder path. An empty
// path is accepted (turns the feature back off) — unlike the AI model
// setting, "off" is a perfectly normal, common choice here, not a mistake to
// reject.
func putKidsRootPathHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := mode.Mode(r.PathValue("mode")).KidsRootPathKey()
		if !ok {
			http.Error(w, "kids root path isn't applicable to this mode", http.StatusBadRequest)
			return
		}
		var req kidsRootPathRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), key, req.Path); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// renameScanHandler runs the Rename workflow's propose-phase for {mode} and
// replaces that mode's live Rename queue with the result — the HTTP
// equivalent of the top bar's Scan button. Every mode dispatches to a
// library-backed sibling now (no *arr app involved): Movies/Series to
// rename.ScanLibrary/ScanLibrarySeries, Adult to rename.ScanLibraryAdult
// (Whisparr eliminated, Stage 4), threading in the videophash hasher and the
// mediainfo prober for its phash-first identification cascade. prober is the
// mux's shared *mediainfo.Prober (its method set satisfies rename.Prober).
func renameScanHandler(httpClient *http.Client, connStore *connections.Store, scStore *serviceconn.Store, settingsStore *settings.Store, propStore *proposals.Store, libStore *library.Store, prober dedup.Prober, videoHasher rename.PHasher, entityStore parseentity.EntityStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		aspect, aerr := parseAdultAspectQuery(r)
		if aerr != nil {
			http.Error(w, aerr.Error(), http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, scStore, settingsStore, httpClient, nil, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Inject the DB-first entity store into the Identify pipeline. Always
		// done (even for Movies/Series, where Identify is nil and the nil-check
		// makes this a no-op) so the handler stays mode-agnostic.
		if sess.Identify != nil {
			sess.Identify.EntityStore = entityStore
		}

		var found []proposals.Proposal
		// Movies/Series dispatch to the library-backed scan; Adult stays on
		// the Servarr-backed rename.Scan. This gate is deliberately explicit
		// rather than keyed off libraryRootFolderKey(m)'s ok: Adult now has a
		// free-typed library-root-folder key too, but its library-backed
		// rename sibling isn't wired here yet (that lands with Whisparr
		// elimination).
		if m == mode.Movies || m == mode.Series {
			key, _ := libraryRootFolderKey(m)
			rootPath, rpErr := settingsStore.Get(ctx, key)
			if rpErr != nil && !errors.Is(rpErr, settings.ErrNotFound) {
				http.Error(w, rpErr.Error(), http.StatusInternalServerError)
				return
			}
			preset, presetErr := resolveNamingPreset(ctx, settingsStore, m)
			if presetErr != nil {
				http.Error(w, presetErr.Error(), http.StatusInternalServerError)
				return
			}
			matchCfg, ctErr := resolveMatchConfig(ctx, settingsStore, m)
			if ctErr != nil {
				http.Error(w, ctErr.Error(), http.StatusInternalServerError)
				return
			}
			if m == mode.Movies {
				found, err = rename.ScanLibrary(ctx, sess, libStore, rootPath, preset, matchCfg, prober)
			} else {
				found, err = rename.ScanLibrarySeries(ctx, sess, libStore, rootPath, preset, matchCfg, prober)
			}
		} else {
			// Adult owns its own library now too (Whisparr eliminated, Stage 4):
			// dispatch to the library-backed sibling, the mirror image of the
			// Movies/Series branch above. Adult has its own free-typed
			// library-root-folder key (libraryRootFolderKey). Identification is
			// always run (ScanLibraryAdult requires sess.Identify), so the old
			// adult_identify_enabled toggle no longer gates the scan.
			key, _ := libraryRootFolderKey(m)
			rootPath, rpErr := settingsStore.Get(ctx, key)
			if rpErr != nil && !errors.Is(rpErr, settings.ErrNotFound) {
				http.Error(w, rpErr.Error(), http.StatusInternalServerError)
				return
			}
			// Claude 2026-08-12: upgrade local scenes before scanning (D6).
			// Reason: deep-interview-adult-rename-review-alts — UpgradeLocalAdultScenes
			//   silently upgrades any local (box="local") scenes whose phash now resolves
			//   to a catalog identity. Must run before ScanLibraryAdult so the upgraded
			//   files appear in the known map and are not re-proposed. Soft-fail: errors
			//   are logged and swallowed; the Scan still runs.
			// Review if: UpgradeLocalAdultScenes is folded into ScanLibraryAdult.
			if upgradeChanges, upgradeErr := rename.UpgradeLocalAdultScenes(ctx, sess, libStore, prober); upgradeErr != nil {
				log.Printf("rename scan: upgrade local adult scenes: %v", upgradeErr)
			} else if len(upgradeChanges) > 0 {
				sess.NotifyPlayers(ctx, upgradeChanges)
			}
			found, err = rename.ScanLibraryAdult(ctx, sess, libStore, videoHasher, prober, rootPath, aspect)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		saved, err := propStore.ReplacePending(ctx, m, proposals.Rename, found)
		if err != nil {
			if errors.Is(err, proposals.ErrReplaceDeferred) {
				http.Error(w, "scan deferred: an apply is currently in flight — retry after the apply completes", http.StatusServiceUnavailable)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		maybeAutoGiveBack(ctx, m, sess, settingsStore, propStore, saved)
		if m == mode.Adult {
			maybeAutoApplyAdultLibrary(ctx, sess, settingsStore, propStore, libStore, prober, saved)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(saved)
	}
}
