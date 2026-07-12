package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/servarr"
	"github.com/curtiswtaylorjr/sakms/internal/stashbox"
	"github.com/curtiswtaylorjr/sakms/internal/whisparrimport"
)

// whisparrImportHandler runs the one-time Whisparr library importer (see
// internal/whisparrimport's package doc) against whatever Whisparr, StashDB
// and FansDB connections are currently configured — a manual, human-triggered
// action, safe to re-run since every write it makes is an idempotent upsert.
// Deliberately builds its own *servarr.Client + BoxSearcher straight from
// connStore rather than going through mode.Build/sess.Servarr: this keeps
// working even after Adult stops requiring a Whisparr connection at all
// (mode.Build would then refuse to construct one), for as long as a user still
// has Whisparr around to migrate from — exactly mirroring sonarrImportHandler's
// precedent for outliving Series' Sonarr removal.
//
// The BoxSearcher is built the same way mode.buildIdentifier does for Adult:
// a stashbox.Client per configured stash-box (stashdb/fansdb), tolerant of a
// missing one (a bare UUID that box can't resolve just imports with box="").
// tpdb is left nil — the importer never probes TPDB (a "tpdbId:"-prefixed
// ForeignID is already unambiguous and needs no lookup).
func whisparrImportHandler(httpClient *http.Client, connStore *connections.Store, libStore *library.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		whisparrConn, err := connStore.Get(ctx, "whisparr")
		if err != nil {
			if errors.Is(err, connections.ErrNotFound) {
				http.Error(w, "whisparr isn't configured — there's nothing to import from", http.StatusBadRequest)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}

		boxes, err := buildImportBoxes(ctx, connStore, httpClient)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		whisparrClient := servarr.New(servarr.Config{BaseURL: whisparrConn.URL, APIKey: whisparrConn.APIKey, App: servarr.Whisparr}, httpClient)

		result, err := whisparrimport.Import(ctx, whisparrClient, boxes, libStore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

// buildImportBoxes assembles the StashDB/FansDB box-searcher the importer uses
// to attribute a bare UUID to its origin box, straight from connStore — the
// same construction mode.buildIdentifier performs for Adult, minus the
// give-back/AI/TPDB plumbing the importer doesn't need. A stash-box with no
// configured connection is simply absent from the map (BoxSearcher treats a
// missing entry as "not configured"); a real store error propagates.
func buildImportBoxes(ctx context.Context, connStore *connections.Store, httpClient *http.Client) (*identify.BoxSearcher, error) {
	boxes := map[string]*stashbox.Client{}
	for _, name := range []string{"stashdb", "fansdb"} {
		conn, err := connStore.Get(ctx, name)
		if err != nil {
			if errors.Is(err, connections.ErrNotFound) {
				continue
			}
			return nil, err
		}
		boxes[name] = stashbox.New(stashbox.Config{
			Endpoint: conn.URL, APIKey: conn.APIKey, IsBearer: false, HasVoteField: true,
		}, httpClient)
	}
	return identify.NewBoxSearcher(boxes, nil), nil
}
