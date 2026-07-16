package api

import (
	"context"
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/recheck"
)

// NewRecheckTriggerMux is a small, separately-dependent mux for the recheck
// feature's manual "Refresh now" trigger — deliberately NOT folded into
// NewMux's giant parameter list, same reasoning as NewAPIKeyMux/
// NewAuthModeMux/NewOIDCMux: a route needing a dependency shape the main mux
// doesn't already carry gets its own small mux, rather than growing NewMux's
// signature (which would force updating every one of its ~190 existing test
// call sites for a route almost none of them exercise).
//
// Unlike getRecheckIntervalHandler/putRecheckIntervalHandler in recheck.go
// (deliberately import-avoided so the interval setting keeps working even if
// internal/recheck is deleted), this mux necessarily imports internal/recheck
// — there is no way to trigger a recheck without the code that performs one.
// Deleting internal/recheck now also means deleting this file and its one
// wiring call in main.go; the interval GET/PUT endpoints are unaffected
// either way and keep managing an inert setting, per recheck.go's contract.
func NewRecheckTriggerMux(connStore *connections.Store, watchStore *recheck.WatchStore) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/recheck/trigger", triggerRecheckHandler(connStore, watchStore))
	return mux
}

// triggerRecheckHandler fires an on-demand recheck cycle over every watched
// pick right now (recheck.TriggerOnce), regardless of the configured
// interval or each entry's last-checked time. Runs in a background
// goroutine; the handler returns 202 Accepted immediately — same
// asynchronous contract as triggerEntitySyncHandler (entity_sync.go).
func triggerRecheckHandler(connStore *connections.Store, watchStore *recheck.WatchStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		go recheck.TriggerOnce(context.Background(), connStore, watchStore)
		w.WriteHeader(http.StatusAccepted)
	}
}
