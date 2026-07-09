package api

import (
	"encoding/json"
	"net/http"

	"github.com/curtiswtaylorjr/sak/internal/connections"
	"github.com/curtiswtaylorjr/sak/internal/dedup"
	"github.com/curtiswtaylorjr/sak/internal/mode"
	"github.com/curtiswtaylorjr/sak/internal/proposals"
	"github.com/curtiswtaylorjr/sak/internal/settings"
)

// dedupScanHandler runs the Dedup workflow's propose-phase for {mode}:
// identifies every unmapped file, groups it with any already-tracked item
// sharing the same identifier (TMDB ID for Movies, foreignID for Adult),
// ffprobes every candidate, and replaces the live Dedup queue with whatever
// duplicate groups it found. prober takes
// dedup.Prober's interface, not the concrete *mediainfo.Prober, so tests can
// inject a fake instead of depending on a real ffprobe binary.
func dedupScanHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store, propStore *proposals.Store, prober dedup.Prober) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		found, err := dedup.Scan(ctx, sess, prober)
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
