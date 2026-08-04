package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/parseentity"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/stashapi"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/stashboxdb"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

type entitySyncStatusResponse struct {
	StudioCount    int                      `json:"studioCount"`
	PerformerCount int                      `json:"performerCount"`
	Sources        []entitySyncSourceStatus `json:"sources"`
}

type entitySyncSourceStatus struct {
	Source   string `json:"source"`
	SyncedAt string `json:"syncedAt"`
	Cursor   string `json:"cursor"`
}

// entitySyncStatusHandler returns the current entity cache counts and per-source
// sync state (last synced timestamp + cursor).
//
// Claude 2026-08-04: the source list is now stash + tpdb + every CONFIGURED
// stash-box database, not the fixed four names (Stage 5 Wave 3.5).
// Reason: a renamed or operator-added database has its own sync cursor under
// its own name, so a fixed list would report a stale row and hide the live one.
// The set is deliberately the same one triggerEntitySyncHandler accepts, so
// every row the UI renders has a working "Sync now" button.
// Troubleshooting: the frontend renders SOURCE_LABELS[source] ?? source
// (Global.tsx), so an unlabelled operator-added name shows as its raw name
// rather than blank — no frontend change is needed for a new database.
// Review if: sync cursors ever stop being keyed by database name.
func entitySyncStatusHandler(store parseentity.EntityStore, connStore *connections.Store) http.HandlerFunc {
	sbStore := newStashBoxStore(connStore)
	sbSecrets := newStashBoxSecretHandles(connStore)
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		studioCount, err := store.StudioCount(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		performerCount, err := store.PerformerCount(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		databases, err := listStashBoxDatabases(ctx, sbStore, sbSecrets)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		names := sourceNames(databases)
		sources := make([]entitySyncSourceStatus, 0, len(names))
		for _, src := range names {
			cursor, syncedAt, err := store.GetSyncCursor(ctx, src)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			syncedAtStr := ""
			if !syncedAt.IsZero() {
				syncedAtStr = syncedAt.UTC().Format("2006-01-02T15:04:05Z")
			}
			sources = append(sources, entitySyncSourceStatus{
				Source:   src,
				SyncedAt: syncedAtStr,
				Cursor:   cursor,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entitySyncStatusResponse{
			StudioCount:    studioCount,
			PerformerCount: performerCount,
			Sources:        sources,
		})
	}
}

// triggerEntitySyncHandler fires an on-demand entity cache sync for one source:
// "stash", "tpdb", or the NAME OF ANY CONFIGURED stash-box database (which on a
// default install means "stashdb" or "fansdb", but is operator-editable as of
// Stage 5 — see the default branch). The sync runs in a background goroutine;
// the handler returns 202 Accepted immediately. The caller may poll
// GET /api/admin/entity-sync to observe progress via the updatedAt timestamp.
func triggerEntitySyncHandler(store parseentity.EntityStore, connStore *connections.Store, _ *settings.Store, httpClient *http.Client) http.HandlerFunc {
	sbStore := newStashBoxStore(connStore)
	sbSecrets := newStashBoxSecretHandles(connStore)
	return func(w http.ResponseWriter, r *http.Request) {
		source := r.PathValue("source")
		ctx := r.Context()
		switch source {
		case "stash":
			conn, err := connStore.Get(ctx, "stash")
			if errors.Is(err, connections.ErrNotFound) {
				http.Error(w, "stash connection not configured", http.StatusBadRequest)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			client := stashapi.New(stashapi.Config{URL: conn.URL, APIKey: conn.APIKey}, httpClient)
			go func() { _ = parseentity.SyncFromStash(context.Background(), store, client) }()
		case "tpdb":
			conn, err := connStore.Get(ctx, "tpdb")
			if errors.Is(err, connections.ErrNotFound) {
				http.Error(w, "tpdb connection not configured", http.StatusBadRequest)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// TPDB REST base is fixed and public — hardcoded, never conn.URL.
			client := tpdbrest.New(tpdbrest.DefaultBaseURL, conn.APIKey, httpClient)
			go func() {
				_ = parseentity.SyncFromTPDB(context.Background(), store, client, parseentity.DefaultSyncPages)
			}()
		// Claude 2026-08-04: was `case "stashdb", "fansdb":` with those two
		// names' fixed endpoints (Stage 5 Wave 3, plan §3.5).
		// Reason: source is now ANY configured stash-box database name, and its
		// endpoint and key come from the registry row rather than from
		// stashbox.URLForBox + a same-named `connections` row. Resolving
		// through the registry is also what keeps a SEEDED row working after
		// it has been renamed — its key still lives in `connections` under the
		// old secret_ref, which only stashboxdb knows how to find.
		// Troubleshooting: the 400 below now lists the live database names, so
		// an operator who renamed a row is told what to send instead of being
		// told the request is invalid against a stale two-name list.
		// endpoint, _ := stashbox.URLForBox(source)   // ← was: the fixed constant
		default:
			databases, err := listStashBoxDatabases(ctx, sbStore, sbSecrets)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			database := findByName(databases, source)
			if database == nil {
				http.Error(w, "source must be one of: "+strings.Join(
					sourceNames(databases), ", "), http.StatusBadRequest)
				return
			}
			if database.APIKey == "" {
				http.Error(w, source+" connection not configured", http.StatusBadRequest)
				return
			}
			client := stashbox.New(stashbox.Config{
				Endpoint: database.Endpoint, APIKey: database.APIKey, IsBearer: false, HasVoteField: true,
			}, httpClient)
			name := database.Name
			go func() {
				_ = parseentity.SyncFromStashBox(context.Background(), store, client, name, parseentity.DefaultSyncPages)
			}()
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

type entitySyncIntervalResponse struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

type entitySyncIntervalRequest struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

// getEntitySyncIntervalHandler returns the shared background sync cadence
// (all four sources combined) in seconds, or 0 when unset — 0 is the normal
// "off" default here (entity sync was purely manual before this job
// existed), not an error. A stored-but-unparseable value degrades to 0 for
// the same reason parseentity.LoadInterval does. Reads parseentity's own
// exported key directly rather than mirroring it by value: unlike
// internal/recheck (deliberately import-avoided so it stays independently
// deletable), internal/api already hard-depends on internal/parseentity for
// entity sync's other handlers above, so there is no import to avoid here.
// Parsing/degrade logic lives in loadIntervalSeconds (interval.go), shared
// with recheck.go and adult_newest_scan.go's equivalents.
func getEntitySyncIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secs, err := loadIntervalSeconds(r.Context(), settingsStore, parseentity.IntervalSettingKey, 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entitySyncIntervalResponse{IntervalSeconds: secs})
	}
}

// putEntitySyncIntervalHandler stores the shared entity-sync interval in
// seconds. 0 disables the background job (the opt-in gate, and the default);
// a negative value is rejected. A change takes effect on the running loop's
// next tick if it's already enabled, or on next restart if it was off at
// boot — same contract as putRecheckIntervalHandler. Validation/persistence
// logic lives in storeIntervalSeconds (interval.go).
func putEntitySyncIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req entitySyncIntervalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		badRequest, err := storeIntervalSeconds(r.Context(), settingsStore, parseentity.IntervalSettingKey, req.IntervalSeconds, 0)
		if err != nil {
			status := http.StatusInternalServerError
			if badRequest {
				status = http.StatusBadRequest
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// listStashBoxDatabases reads the registry once for the entity-sync routes,
// which speak database NAMES rather than ids. A nil store (no connections
// store on this server) is an empty registry, not an error — the caller then
// reports the same 400 an unknown name gets.
func listStashBoxDatabases(ctx context.Context, store *stashboxdb.Store, secrets stashBoxSecretHandles) ([]stashboxdb.Database, error) {
	if store == nil {
		return nil, nil
	}
	return store.List(ctx, secrets.get)
}

// findByName returns the named database, or nil when the name came from a path
// segment the operator typed and matches nothing. Nil is a 400 listing the live
// names, never a 404.
func findByName(databases []stashboxdb.Database, name string) *stashboxdb.Database {
	for i := range databases {
		if databases[i].Name == name {
			return &databases[i]
		}
	}
	return nil
}

// sourceNames is the accepted entity-sync source vocabulary: the two fixed
// singletons plus every configured stash-box database, in cascade order. One
// function so the status route's rows and the trigger route's 400 message can
// never disagree about what is syncable.
func sourceNames(databases []stashboxdb.Database) []string {
	out := make([]string, 0, len(databases)+2)
	out = append(out, "stash", "tpdb")
	for _, database := range databases {
		out = append(out, database.Name)
	}
	return out
}
