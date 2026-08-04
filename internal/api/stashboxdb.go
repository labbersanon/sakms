package api

// Claude 2026-08-04: new file — HTTP surface for the admin-configurable
// stash-box database registry (Stage 5, plan
// .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md Wave 2 /
// .omc/plans/ralplan-adult-identify-configurable-databases.md §2.6).
// Reason: CRUD mirrors pruning_rules.go handler-for-handler, and the two test
// routes mirror connectionsTestHandler / connectionsTestStoredHandler —
// including the latter's deliberate no-detail error contract, which matters
// MORE here than there because a row's endpoint is operator-supplied and a raw
// dial error would echo it back.
// Troubleshooting: sbStore MAY BE NIL (a test mux built with a nil connStore
// has no database to build it from). Every handler answers 503 rather than
// panicking in that case, exactly as the pruning handlers do.
// Review if: OQ7 lands and secret_ref disappears — the connSet/connDelete
// plumbing below becomes dead at that point.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/stashboxdb"
)

// stashBoxSecretHandles wires stashboxdb's three injected secret-source funcs
// onto a connections.Store. They exist because internal/stashboxdb
// deliberately does not import internal/connections (see its package doc):
// the two seeded rows' keys still live in `connections` under their
// secret_ref, so the registry needs read/write/delete access to that store
// without depending on it.
//
// connGet reports a MISSING connection as ("", nil), never an error — that is
// the ordinary "not configured yet" state, exactly as it is everywhere else
// in the app, not a failure.
type stashBoxSecretHandles struct {
	get    stashboxdb.ConnGet
	set    stashboxdb.ConnSet
	delete stashboxdb.ConnDelete
}

func newStashBoxSecretHandles(connStore *connections.Store) stashBoxSecretHandles {
	if connStore == nil {
		return stashBoxSecretHandles{}
	}
	return stashBoxSecretHandles{
		get: func(ctx context.Context, service string) (string, error) {
			conn, err := connStore.Get(ctx, service)
			if errors.Is(err, connections.ErrNotFound) {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			return conn.APIKey, nil
		},
		set: func(ctx context.Context, service string, apiKey *string) error {
			// URL and username stay blank: a stash-box connection has never
			// collected either, and UpsertPreservingSecret's nil-secret branch
			// is what preserves an untouched key.
			return connStore.UpsertPreservingSecret(ctx, service, "", "", apiKey)
		},
		delete: func(ctx context.Context, service string) error {
			return connStore.Delete(ctx, service)
		},
	}
}

// newStashBoxStore builds the registry store from the connections store every
// NewMux call site already passes — see connections.Store.DB's Claude comment
// for why this is derived rather than threaded as a new parameter. Returns
// nil when there is no connections store, which every handler here treats as
// "not available on this server" (503).
func newStashBoxStore(connStore *connections.Store) *stashboxdb.Store {
	if connStore == nil {
		return nil
	}
	return stashboxdb.New(connStore.DB(), connStore.Secrets())
}

func toDTOStashBoxDatabase(s stashboxdb.Summary) apidto.StashBoxDatabase {
	return apidto.StashBoxDatabase{
		ID:          s.ID,
		Name:        s.Name,
		Endpoint:    s.Endpoint,
		Priority:    s.Priority,
		Enabled:     s.Enabled,
		FansiteOnly: s.FansiteOnly,
		HasAPIKey:   s.HasAPIKey,
		KeySuffix:   s.KeySuffix,
		UpdatedAt:   s.UpdatedAt,
	}
}

func toDTOStashBoxDatabases(list []stashboxdb.Summary) []apidto.StashBoxDatabase {
	// make(...,0,n), never a nil slice — an empty registry must serialize as
	// [] not null, same convention as listPruningRulesHandler.
	out := make([]apidto.StashBoxDatabase, 0, len(list))
	for _, s := range list {
		out = append(out, toDTOStashBoxDatabase(s))
	}
	return out
}

// stashBoxStoreError maps a stashboxdb error onto an HTTP status. Every
// validation error is a bad request body, never a server fault; the cap and
// the two name guards are all 400 so the UI can surface the store's own
// message verbatim (they are written for an operator to read).
func stashBoxStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, stashboxdb.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, stashboxdb.ErrCapReached),
		errors.Is(err, stashboxdb.ErrNameRequired),
		errors.Is(err, stashboxdb.ErrNameReserved),
		errors.Is(err, stashboxdb.ErrNameTaken),
		errors.Is(err, stashboxdb.ErrNameHaunted),
		errors.Is(err, stashboxdb.ErrInvalidEndpoint),
		errors.Is(err, stashboxdb.ErrKeyRequired):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func stashBoxStoreUnavailable(w http.ResponseWriter, store *stashboxdb.Store) bool {
	if store != nil {
		return false
	}
	http.Error(w, "stash-box databases are not available on this server", http.StatusServiceUnavailable)
	return true
}

// stashBoxDatabaseID parses and reports the {id} path value, answering 400
// itself when it isn't an integer.
func stashBoxDatabaseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id path parameter must be an integer", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// listStashBoxDatabasesHandler is GET /api/stashbox-databases — every row
// (including disabled ones) in cascade order, redacted. There is no
// enabled-only variant on the API: the Settings surface must be able to show
// and re-enable a disabled database, so filtering happens in the pipeline
// (stashboxdb.Store.List), never here.
func listStashBoxDatabasesHandler(store *stashboxdb.Store, secrets stashBoxSecretHandles) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if stashBoxStoreUnavailable(w, store) {
			return
		}
		list, err := store.ListSummaries(r.Context(), secrets.get)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, toDTOStashBoxDatabases(list))
	}
}

// createStashBoxDatabaseHandler is POST /api/stashbox-databases. The cap, the
// reserved "tpdb" name, the duplicate-name check and the §2.8 name-reuse
// tombstone are all enforced inside Store.Create (AC16/AC17) — this handler
// adds no second copy of them.
func createStashBoxDatabaseHandler(store *stashboxdb.Store, secrets stashBoxSecretHandles) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if stashBoxStoreUnavailable(w, store) {
			return
		}
		var req apidto.StashBoxDatabaseCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		created, err := store.Create(r.Context(), req.Name, req.Endpoint, req.APIKey)
		if err != nil {
			stashBoxStoreError(w, err)
			return
		}
		// Re-read as a Summary rather than hand-building one from `created`:
		// HasAPIKey/KeySuffix must come from the same resolve path the list
		// uses, or a freshly-created row would render differently from the
		// same row after a refresh.
		summary, err := stashBoxSummaryByID(r.Context(), store, secrets, created.ID)
		if err != nil {
			stashBoxStoreError(w, err)
			return
		}
		writeJSON(w, toDTOStashBoxDatabase(summary))
	}
}

// updateStashBoxDatabaseHandler is PUT /api/stashbox-databases/{id}. Every
// field is optional and every field is editable on EVERY row — including the
// two seeded ones, which have no reserved tier. APIKey carries the three-state
// secret rule verbatim (absent = preserve, "" = clear, non-empty = set); the
// store routes the write to `connections` or to the registry table by the
// row's own secret_ref, which the API never exposes and never mutates.
func updateStashBoxDatabaseHandler(store *stashboxdb.Store, secrets stashBoxSecretHandles) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if stashBoxStoreUnavailable(w, store) {
			return
		}
		id, ok := stashBoxDatabaseID(w, r)
		if !ok {
			return
		}
		var req apidto.StashBoxDatabaseUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		in := stashboxdb.UpdateInput{
			Name:        req.Name,
			Endpoint:    req.Endpoint,
			Priority:    req.Priority,
			Enabled:     req.Enabled,
			FansiteOnly: req.FansiteOnly,
			APIKey:      req.APIKey,
		}
		if err := store.Update(r.Context(), id, in, secrets.set); err != nil {
			stashBoxStoreError(w, err)
			return
		}
		summary, err := stashBoxSummaryByID(r.Context(), store, secrets, id)
		if err != nil {
			stashBoxStoreError(w, err)
			return
		}
		writeJSON(w, toDTOStashBoxDatabase(summary))
	}
}

// reorderStashBoxDatabasesHandler is PUT /api/stashbox-databases/reorder —
// the drag-and-drop persist path. The body must list every stored id exactly
// once; a partial list is a 400 rather than a silent stale-priority write.
func reorderStashBoxDatabasesHandler(store *stashboxdb.Store, secrets stashBoxSecretHandles) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if stashBoxStoreUnavailable(w, store) {
			return
		}
		var req apidto.StashBoxDatabaseReorderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := store.Reorder(r.Context(), req.IDs); err != nil {
			// Reorder's mismatch errors are all operator-fixable input
			// problems, so they are 400 rather than 500 — but they are not
			// sentinel values, so they cannot go through stashBoxStoreError.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		list, err := store.ListSummaries(r.Context(), secrets.get)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, toDTOStashBoxDatabases(list))
	}
}

// deleteStashBoxDatabaseHandler is DELETE /api/stashbox-databases/{id}. ANY
// row is deletable, seeded or not; for a seeded row the store also clears the
// paired `connections` secret so none is orphaned.
func deleteStashBoxDatabaseHandler(store *stashboxdb.Store, secrets stashBoxSecretHandles) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if stashBoxStoreUnavailable(w, store) {
			return
		}
		id, ok := stashBoxDatabaseID(w, r)
		if !ok {
			return
		}
		if err := store.Delete(r.Context(), id, secrets.delete); err != nil {
			stashBoxStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// testStashBoxDatabaseHandler is POST /api/stashbox-databases/test — the
// STATELESS check against typed-but-unsaved field values. It replaces
// TestConnection's removed "stashdb"/"fansdb" cases, and unlike them it takes
// the endpoint from the request rather than a hardcoded constant, because a
// registry row's endpoint is operator-supplied.
//
// The raw downstream error IS returned here, unlike test-stored below: every
// value in it came from this same request body, so there is no stored config
// to leak.
func testStashBoxDatabaseHandler(httpClient *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.StashBoxDatabaseTestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		writeJSON(w, testStashBox(r.Context(), httpClient,
			ConnectionTestRequest{APIKey: req.APIKey}, req.Endpoint))
	}
}

// testStoredStashBoxDatabaseHandler is POST /api/stashbox-databases/{id}/test-stored
// — the per-row Test button. A saved row's key is masked in the UI, so the
// only way to test it without round-tripping the real secret to the browser is
// to resolve it server-side.
//
// Security contract, inherited verbatim from connectionsTestStoredHandler: on
// failure the raw downstream error is NEVER propagated. A Go http-client error
// echoes the target host:port, which for a registry row is stored operator
// config the client is not entitled to read back. Any non-OK result is
// reported with the same fixed, detail-free message.
func testStoredStashBoxDatabaseHandler(httpClient *http.Client, store *stashboxdb.Store, secrets stashBoxSecretHandles) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if stashBoxStoreUnavailable(w, store) {
			return
		}
		id, ok := stashBoxDatabaseID(w, r)
		if !ok {
			return
		}
		db, err := store.Get(r.Context(), id, secrets.get)
		if err != nil {
			stashBoxStoreError(w, err)
			return
		}
		result := testStashBox(r.Context(), httpClient,
			ConnectionTestRequest{APIKey: db.APIKey}, db.Endpoint)
		if !result.OK {
			result.Error = "connection test failed"
		}
		writeJSON(w, result)
	}
}

// stashBoxSummaryByID re-reads one row as its redacted Summary. There is no
// Store.GetSummary — the list path is the single place the HasAPIKey/KeySuffix
// resolve rule lives, so this filters that list rather than duplicating it.
func stashBoxSummaryByID(ctx context.Context, store *stashboxdb.Store, secrets stashBoxSecretHandles, id int64) (stashboxdb.Summary, error) {
	list, err := store.ListSummaries(ctx, secrets.get)
	if err != nil {
		return stashboxdb.Summary{}, err
	}
	for _, s := range list {
		if s.ID == id {
			return s, nil
		}
	}
	return stashboxdb.Summary{}, stashboxdb.ErrNotFound
}

// stashBoxRegistryServices are the two connections services that migration
// 0061 turned into registry rows. They are filtered out of GET /api/connections
// so they render ONLY in the new Settings section and never in two places at
// once (AC13). Their `connections` rows still exist and still hold the
// secrets — this hides them from the old list, it does not delete anything.
//
// Filtering here rather than in the frontend is deliberate (ralplan §2.6): the
// backend is the single source of truth for what the old list contains, so a
// stale frontend or a direct API consumer sees the same thing the UI does.
var stashBoxRegistryServices = map[string]bool{
	"stashdb": true,
	"fansdb":  true,
}
