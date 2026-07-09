package api

import (
	"encoding/json"
	"net/http"

	"github.com/curtiswtaylorjr/sak/internal/connections"
	"github.com/curtiswtaylorjr/sak/internal/mode"
	"github.com/curtiswtaylorjr/sak/internal/settings"
)

type rootFolderSummary struct {
	Path       string `json:"path"`
	Accessible bool   `json:"accessible"`
}

// listRootFoldersHandler returns {mode}'s currently configured root folders
// as reported by its own *arr app — the same call Rename's Scan already
// makes, exposed read-only so a Settings UI can offer a real picker (e.g.
// for kids-root-path) instead of free-text entry, which would reintroduce
// exactly the typo/staleness risk that control is designed to avoid.
func listRootFoldersHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		folders, err := sess.Servarr.RootFolders(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		out := make([]rootFolderSummary, len(folders))
		for i, f := range folders {
			out[i] = rootFolderSummary{Path: f.Path, Accessible: f.Accessible}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}
