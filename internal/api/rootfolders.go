package api

import (
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

// listRootFoldersHandler used to proxy the mode's *arr app root-folder list so
// a Settings UI could offer a real picker. Every mode owns its own library
// now — Movies/Series since their Radarr/Sonarr eliminations, Adult since
// Stage 4's Whisparr elimination — so there is no *arr app to ask for any
// mode. All modes are directed to the free-typed library root-folder setting
// (GET/PUT /api/modes/{mode}/library/root-folder) instead. Kept as a clean
// 400 rather than deleting the route, so an older frontend build gets a clear
// message instead of a 404. connStore/settingsStore/httpClient are retained
// on the signature (NewMux wires them) but no longer used, since there is no
// Servarr session to build.
func listRootFoldersHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "root folders come from each mode's own library setting now — see GET/PUT /api/modes/{mode}/library/root-folder", http.StatusBadRequest)
	}
}
