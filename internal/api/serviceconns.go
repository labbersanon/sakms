package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/serviceconn"
	"github.com/labbersanon/sakms/internal/usenet"
)

// serviceConnectionsPath is the route family the multi-connection registry
// lives under. Named once so the M2 rejected-services guard in handler.go can
// point a stale client at the exact endpoint that replaced it.
const serviceConnectionsPath = "/api/service-connections"

// serviceConnectionRequest is POST /api/service-connections/test's body — the
// one decode target in this file apidto has no matching DTO shape for, since
// ServiceConnectionTestRequest omits MaxConns (a real NNTP pool-sizing field
// this stateless test still needs to thread through to usenet.ServerConfig)
// and Kind/Label/Enabled/SortOrder (irrelevant to a one-off test call).
// Create/Update/SetModes decode straight into the apidto request types below
// instead — see createServiceConnectionHandler/updateServiceConnectionHandler/
// setServiceConnectionModesHandler — which makes those apidto.ServiceConnection*
// DTOs load-bearing rather than declaration-only for the generator.
//
// Secret is a pointer for the same three-state reason APIKey is in
// upsertConnectionRequest (json.Decode distinguishes all three; omitempty only
// affects marshaling): absent from the JSON entirely -> PRESERVE the stored
// secret (unused on this stateless path, since there is nothing stored to
// preserve); present as "" -> CLEAR it; present and non-empty -> set/replace
// it. Test always treats nil/absent the same as "" — see testServiceConnectionHandler.
type serviceConnectionRequest struct {
	Kind     string   `json:"kind"`
	Provider string   `json:"provider"`
	Label    string   `json:"label"`
	Enabled  bool     `json:"enabled"`
	URL      string   `json:"url,omitempty"`
	Host     string   `json:"host,omitempty"`
	Port     int      `json:"port,omitempty"`
	TLS      bool     `json:"tls,omitempty"`
	MaxConns int      `json:"maxConns,omitempty"`
	Username string   `json:"username,omitempty"`
	Secret   *string  `json:"secret,omitempty"`
	Modes    []string `json:"modes,omitempty"`
}

// connection maps the request onto a serviceconn.Connection. Secret is left
// zero: the one caller (testServiceConnectionHandler) sets it separately
// after this call, so writing it here would be redundant, not wrong — kept
// zero anyway to match createRequestConnection/updateRequestConnection's
// convention below.
func (req serviceConnectionRequest) connection() serviceconn.Connection {
	return serviceconn.Connection{
		Kind:     serviceconn.Kind(req.Kind),
		Provider: serviceconn.Provider(req.Provider),
		Label:    req.Label,
		Enabled:  req.Enabled,
		URL:      req.URL,
		Host:     req.Host,
		Port:     req.Port,
		TLS:      req.TLS,
		MaxConns: req.MaxConns,
		Username: req.Username,
	}
}

// createRequestConnection maps a POST body onto a serviceconn.Connection.
// Secret is left zero: createServiceConnectionHandler reads req.Secret
// separately (nil means "no secret" on create — there is nothing stored to
// preserve), so setting it here would just be redundant.
func createRequestConnection(req apidto.ServiceConnectionCreateRequest) serviceconn.Connection {
	return serviceconn.Connection{
		Kind:     serviceconn.Kind(req.Kind),
		Provider: serviceconn.Provider(req.Provider),
		Label:    req.Label,
		Enabled:  req.Enabled,
		URL:      req.URL,
		Host:     req.Host,
		Port:     req.Port,
		TLS:      req.TLS,
		MaxConns: req.MaxConns,
		Username: req.Username,
	}
}

// updateRequestConnection maps a PUT body onto a serviceconn.Connection.
// Secret is left zero: Store.Update ignores Connection.Secret entirely in
// favour of its own three-state secret parameter (see
// updateServiceConnectionHandler), so writing it here would silently drop the
// operator's new secret if anyone ever wired it through by mistake.
func updateRequestConnection(req apidto.ServiceConnectionUpdateRequest) serviceconn.Connection {
	return serviceconn.Connection{
		Kind:     serviceconn.Kind(req.Kind),
		Provider: serviceconn.Provider(req.Provider),
		Label:    req.Label,
		Enabled:  req.Enabled,
		URL:      req.URL,
		Host:     req.Host,
		Port:     req.Port,
		TLS:      req.TLS,
		MaxConns: req.MaxConns,
		Username: req.Username,
	}
}

// serviceConnectionSummaryDTO converts one store row into the wire DTO. Field
// names/types/json tags are identical between serviceconn.Summary and
// apidto.ServiceConnectionSummary by design (the latter mirrors the former —
// see its doc comment) — this is a straight copy, not a projection.
func serviceConnectionSummaryDTO(s serviceconn.Summary) apidto.ServiceConnectionSummary {
	return apidto.ServiceConnectionSummary{
		ID:           s.ID,
		Kind:         s.Kind,
		Provider:     s.Provider,
		Label:        s.Label,
		Enabled:      s.Enabled,
		SortOrder:    s.SortOrder,
		URL:          s.URL,
		Host:         s.Host,
		Port:         s.Port,
		TLS:          s.TLS,
		MaxConns:     s.MaxConns,
		Username:     s.Username,
		HasSecret:    s.HasSecret,
		SecretSuffix: s.SecretSuffix,
		Modes:        s.Modes,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
	}
}

// serviceConnectionSummaryDTOs converts a whole listing in display order.
func serviceConnectionSummaryDTOs(list []serviceconn.Summary) []apidto.ServiceConnectionSummary {
	out := make([]apidto.ServiceConnectionSummary, len(list))
	for i, s := range list {
		out[i] = serviceConnectionSummaryDTO(s)
	}
	return out
}

// serviceConnStoreError maps a serviceconn.Store error onto an HTTP status —
// direct sibling of rss_feeds.go's rssFeedStoreError. Every fixed-enum and
// per-kind shape error is a bad request body, never a server fault;
// ErrNotFound is a 404.
func serviceConnStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, serviceconn.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, serviceconn.ErrInvalidKind),
		errors.Is(err, serviceconn.ErrInvalidProvider),
		errors.Is(err, serviceconn.ErrProviderKind),
		errors.Is(err, serviceconn.ErrInvalidMode),
		errors.Is(err, serviceconn.ErrModesNotPlayer),
		errors.Is(err, serviceconn.ErrURLRequired),
		errors.Is(err, serviceconn.ErrHostRequired):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// serviceConnID parses the {id} path value. The store keys on int64, so this is
// ParseInt rather than the Atoi the int-keyed rss-feed/slider routes use.
func serviceConnID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id path parameter must be an integer", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// summarizeServiceConn re-reads the row and returns its wire-safe Summary.
//
// The re-read is load-bearing, not defensive: Create/Update both return the
// Connection they were HANDED, whose Secret is empty on a preserve-the-secret
// update — summarizing that directly would report HasSecret=false for a row
// that has one. Store.Get is the only view that knows what is actually stored.
func summarizeServiceConn(ctx context.Context, store *serviceconn.Store, id int64) (serviceconn.Summary, error) {
	list, err := store.List(ctx)
	if err != nil {
		return serviceconn.Summary{}, err
	}
	for _, s := range list {
		if s.ID == id {
			return s, nil
		}
	}
	return serviceconn.Summary{}, serviceconn.ErrNotFound
}

// listServiceConnectionsHandler is GET /api/service-connections — every
// registry row as a secret-redacted Summary, usenet rows then players, each in
// display order.
func listServiceConnectionsHandler(store *serviceconn.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := store.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, serviceConnectionSummaryDTOs(list))
	}
}

// createServiceConnectionHandler is POST /api/service-connections. Unlike the
// singleton /api/connections/{service} routes this keys on nothing: a registry
// row is addressed by the integer id assigned here, because "the nntp
// connection" is no longer a unique thing.
func createServiceConnectionHandler(store *serviceconn.Store, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		var req apidto.ServiceConnectionCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		c := createRequestConnection(req)
		// Create is the one path that reads Connection.Secret — a nil pointer
		// here means "no secret", since there is no stored value to preserve.
		if req.Secret != nil {
			c.Secret = *req.Secret
		}
		c.Modes = req.Modes

		created, err := store.Create(ctx, c)
		if err != nil {
			serviceConnStoreError(w, err)
			return
		}
		refreshUsenetSubscriptions(ctx, store, nzb)

		summary, err := summarizeServiceConn(ctx, store, created.ID)
		if err != nil {
			serviceConnStoreError(w, err)
			return
		}
		writeJSONStatus(w, http.StatusCreated, serviceConnectionSummaryDTO(summary))
	}
}

// updateServiceConnectionHandler is PUT /api/service-connections/{id} —
// overwrites every editable field. Mode assignment is untouched (see the modes
// route below), matching Store.Update's own contract.
func updateServiceConnectionHandler(store *serviceconn.Store, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, ok := serviceConnID(w, r)
		if !ok {
			return
		}
		var req apidto.ServiceConnectionUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// req.Secret is passed straight through as Update's three-state
		// parameter. Store.Update IGNORES Connection.Secret, so threading it
		// through the struct instead would silently discard a real secret
		// change — the exact data-loss shape the *T rule exists to prevent.
		if _, err := store.Update(ctx, id, updateRequestConnection(req), req.Secret); err != nil {
			serviceConnStoreError(w, err)
			return
		}
		refreshUsenetSubscriptions(ctx, store, nzb)

		summary, err := summarizeServiceConn(ctx, store, id)
		if err != nil {
			serviceConnStoreError(w, err)
			return
		}
		writeJSON(w, serviceConnectionSummaryDTO(summary))
	}
}

// deleteServiceConnectionHandler is DELETE /api/service-connections/{id}.
// Returns 404 when the id has no stored row (Store.Delete returns ErrNotFound).
func deleteServiceConnectionHandler(store *serviceconn.Store, nzb *usenet.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, ok := serviceConnID(w, r)
		if !ok {
			return
		}
		if err := store.Delete(ctx, id); err != nil {
			serviceConnStoreError(w, err)
			return
		}
		refreshUsenetSubscriptions(ctx, store, nzb)
		w.WriteHeader(http.StatusNoContent)
	}
}

// setServiceConnectionModesHandler is PUT /api/service-connections/{id}/modes —
// a full replace of which modes a player is assigned to. Player rows only; the
// Store rejects a non-empty assignment on a usenet row (ErrModesNotPlayer).
func setServiceConnectionModesHandler(store *serviceconn.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id, ok := serviceConnID(w, r)
		if !ok {
			return
		}
		var req apidto.ServiceConnectionModesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := store.SetModes(ctx, id, req.Modes); err != nil {
			serviceConnStoreError(w, err)
			return
		}
		summary, err := summarizeServiceConn(ctx, store, id)
		if err != nil {
			serviceConnStoreError(w, err)
			return
		}
		writeJSON(w, serviceConnectionSummaryDTO(summary))
	}
}

// testServiceConnectionHandler is POST /api/service-connections/test — one
// real, read-only call against the field values in the body, nothing persisted.
// The stateless sibling of the id route below, and the registry twin of
// POST /api/connections/test.
//
// The raw downstream error IS returned here, unlike the stored-row route: every
// value tested came from this same client in this same request, so the error
// can only echo back what the caller already typed. Same reasoning
// connectionsTestHandler already relies on.
func testServiceConnectionHandler(httpClient *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req serviceConnectionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		secret := ""
		if req.Secret != nil {
			secret = *req.Secret
		}
		c := req.connection()
		c.Secret = secret
		writeJSON(w, testServiceConnection(r.Context(), httpClient, c))
	}
}

// testStoredServiceConnectionHandler is POST /api/service-connections/{id}/test
// — tests an ALREADY-SAVED row using its stored secret, which the client never
// holds. The id-keyed replacement for POST /api/connections/{service}/test-stored,
// which cannot address one of N usenet subscriptions.
//
// Two different error contracts, deliberately:
//   - An unknown/unsupported provider is reported verbatim. That names only a
//     fixed enum value, leaks nothing, and is the case the plan calls out —
//     connectionsTestStoredHandler flattens it into "connection test failed",
//     which tells an operator nothing. Do not repeat that here.
//   - A genuine downstream failure is reported detail-free, exactly as
//     connectionsTestStoredHandler does. A Go http-client error echoes the
//     target URL (dial errors include host:port) and some clients put the key
//     in a query param — either leaks stored config the client is not allowed
//     to see. That security contract is unchanged by the id-keyed rewrite.
func testStoredServiceConnectionHandler(httpClient *http.Client, store *serviceconn.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := serviceConnID(w, r)
		if !ok {
			return
		}
		conn, err := store.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, serviceconn.ErrNotFound) {
				http.Error(w, "no connection with that id", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to load stored connection", http.StatusInternalServerError)
			return
		}

		result := testServiceConnection(r.Context(), httpClient, *conn)
		if !result.OK && supportedTestProvider(conn.Provider) {
			result.Error = "connection test failed"
		}
		writeJSON(w, result)
	}
}

// supportedTestProvider reports whether p has a real test path — the
// discriminator testStoredServiceConnectionHandler uses to decide between its
// detail-free failure message and a verbatim "unsupported provider" one.
//
// Always true for a STORED row today (the Store validates provider against this
// same fixed enum on every write), so it reads as belt-and-braces. It is kept
// because it is the one thing standing between a future fifth provider and a
// stored-route failure that says only "connection test failed" — the exact
// uninformative flattening the plan calls out in connectionsTestStoredHandler.
func supportedTestProvider(p serviceconn.Provider) bool {
	switch p {
	case serviceconn.ProviderNNTP, serviceconn.ProviderJellyfin, serviceconn.ProviderEmby, serviceconn.ProviderPlex:
		return true
	}
	return false
}

// testServiceConnection makes one lightweight, real call against c and reports
// whether it worked. Players reuse TestConnection's existing per-provider
// dispatch (the jellyfin/emby/plex cases in connections.go) rather than a second
// copy of it; usenet cannot, because ConnectionTestRequest is URL-shaped and a
// registry subscription is host/port/tls-shaped — hence the direct
// usenet.TestConnect call below.
func testServiceConnection(ctx context.Context, httpClient *http.Client, c serviceconn.Connection) ConnectionTestResult {
	switch c.Provider {
	case serviceconn.ProviderNNTP:
		return testNNTPServer(usenet.ServerConfig{
			Host:     c.Host,
			Port:     c.Port,
			TLS:      c.TLS,
			Username: c.Username,
			Password: c.Secret,
			MaxConns: c.MaxConns,
		})
	case serviceconn.ProviderJellyfin, serviceconn.ProviderEmby, serviceconn.ProviderPlex:
		return TestConnection(ctx, httpClient, ConnectionTestRequest{
			Service:  string(c.Provider),
			URL:      c.URL,
			Username: c.Username,
			APIKey:   c.Secret,
		})
	default:
		return ConnectionTestResult{Error: errUnsupportedProvider(c.Provider).Error()}
	}
}

func errUnsupportedProvider(p serviceconn.Provider) error {
	return errors.New("unsupported provider " + strconv.Quote(string(p)))
}

// refreshUsenetSubscriptions re-points the running Usenet engine at whatever
// the registry now holds, so an added/edited/removed subscription is live
// without a process restart — the whole reason Manager.SetSubscriptions exists.
//
// Called after EVERY successful mutation rather than only usenet-kind ones: a
// PUT can change a row's kind, and a DELETE has no row left to inspect, so
// tracking which mutations "were usenet" would be more branches and one more
// way to miss a refresh. Re-listing is cheap.
//
// Best-effort and never fatal to the mutation, which has already committed —
// but logged, never silent: without the log line an operator saving a
// subscription gets a 200 while the running engine keeps the old pool set, with
// no signal anywhere. nzb is nil in tests.
//
// The enabled filter and the field mapping below MUST stay identical to
// buildUsenetManager's own construction in cmd/sakms/main.go. ListByKind does
// not filter on Enabled (only PlayersForMode does), so dropping the check here
// would make a disabled subscription — correctly excluded at boot — silently
// reappear after the next unrelated save.
func refreshUsenetSubscriptions(ctx context.Context, store *serviceconn.Store, nzb *usenet.Manager) {
	if nzb == nil {
		return
	}
	conns, err := store.ListByKind(ctx, serviceconn.KindUsenet)
	if err != nil {
		log.Printf("refreshing usenet subscriptions after a registry mutation: %v", err)
		return
	}
	cfgs := make([]usenet.ServerConfig, 0, len(conns))
	for _, c := range conns {
		if !c.Enabled {
			continue
		}
		cfgs = append(cfgs, usenet.ServerConfig{
			Host:     c.Host,
			Port:     c.Port,
			TLS:      c.TLS,
			Username: c.Username,
			Password: c.Secret,
			MaxConns: c.MaxConns,
		})
	}
	nzb.SetSubscriptions(cfgs)
}
