package api

// Claude 2026-08-04: new file — HTTP coverage for the stash-box database
// registry routes (Stage 5 Wave 2, plan
// .omc/plans/autopilot-impl-stage5-stashboxdb-ui.md).
// Reason: driven through the REAL mux (template: pruning_rules_test.go) so the
// route patterns registered in handler.go are exercised too — a handler that
// works but was never registered is the failure mode a direct call hides. That
// matters more than usual here because the store is DERIVED from connStore
// inside NewMux rather than passed in, so "is it wired at all" is exactly what
// these tests have to prove.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/allowlist"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
)

// stashBoxEnv is a mux wired with a REAL connections store over a real
// database — the registry store NewMux builds internally comes off that same
// handle, so nothing here can pass with the wiring broken.
type stashBoxEnv struct {
	srv       *httptest.Server
	connStore *connections.Store
	sqlDB     *sql.DB
}

func newStashBoxEnv(t *testing.T) *stashBoxEnv {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	key, err := secrets.LoadOrCreateKey(filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatalf("creating secret key: %v", err)
	}
	enc, err := secrets.New(key)
	if err != nil {
		t.Fatalf("building encryptor: %v", err)
	}
	connStore := connections.New(sqlDB, enc)

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, proposals.New(sqlDB), allowlist.New(sqlDB),
		testProber(t), testPHasher(t), testVideoHasher(t), settings.New(sqlDB), nil, library.New(sqlDB),
		nil, nil, nil, nil, testFeedHealth(), nil, nil, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)

	return &stashBoxEnv{srv: srv, connStore: connStore, sqlDB: sqlDB}
}

func (e *stashBoxEnv) do(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshalling body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, reader)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (e *stashBoxEnv) list(t *testing.T) []apidto.StashBoxDatabase {
	t.Helper()
	code, body := e.do(t, http.MethodGet, "/api/stashbox-databases", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/stashbox-databases = %d, want 200 (%s)", code, body)
	}
	var got []apidto.StashBoxDatabase
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding list: %v (%s)", err, body)
	}
	return got
}

func decodeStashBox(t *testing.T, body []byte) apidto.StashBoxDatabase {
	t.Helper()
	var got apidto.StashBoxDatabase
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding row: %v (%s)", err, body)
	}
	return got
}

// TestStashBoxDatabases_ListSeeded is the "is it wired" test: a mux built with
// nothing but a connections store must already serve the two rows migration
// 0061 seeded, in cascade order, with their endpoints and fansite flags.
func TestStashBoxDatabases_ListSeeded(t *testing.T) {
	env := newStashBoxEnv(t)
	got := env.list(t)

	if len(got) != 2 {
		t.Fatalf("seeded rows = %d, want 2: %+v", len(got), got)
	}
	if got[0].Name != "stashdb" || got[1].Name != "fansdb" {
		t.Fatalf("cascade order = %q,%q, want stashdb,fansdb", got[0].Name, got[1].Name)
	}
	if got[0].Endpoint != "https://stashdb.org/graphql" {
		t.Errorf("stashdb endpoint = %q", got[0].Endpoint)
	}
	if got[1].Endpoint != "https://fansdb.cc/graphql" {
		t.Errorf("fansdb endpoint = %q", got[1].Endpoint)
	}
	if got[0].FansiteOnly {
		t.Error("stashdb should not be fansite-only")
	}
	if !got[1].FansiteOnly {
		t.Error("fansdb should be fansite-only")
	}
	// No key stored yet, so both must report hasApiKey=false rather than
	// erroring on the missing `connections` row.
	if got[0].HasAPIKey || got[1].HasAPIKey {
		t.Errorf("seeded rows report a key with no connections rows: %+v", got)
	}
}

// TestStashBoxDatabases_SeededKeyResolvesFromConnections is AC12/decision 5:
// a key already stored under the `connections` service must surface on the
// registry row with NO re-entry, masked to its last 4 characters.
func TestStashBoxDatabases_SeededKeyResolvesFromConnections(t *testing.T) {
	env := newStashBoxEnv(t)
	ctx := context.Background()
	if err := env.connStore.Upsert(ctx, "stashdb", "", "sb-secret-key-9876"); err != nil {
		t.Fatalf("seeding connections key: %v", err)
	}

	got := env.list(t)
	if !got[0].HasAPIKey {
		t.Fatal("stashdb row does not see the connections key")
	}
	if got[0].KeySuffix != "9876" {
		t.Errorf("keySuffix = %q, want 9876", got[0].KeySuffix)
	}
	// The redaction contract: the real secret must not appear anywhere in the
	// serialized response.
	_, body := env.do(t, http.MethodGet, "/api/stashbox-databases", nil)
	if bytes.Contains(body, []byte("sb-secret-key-9876")) {
		t.Fatalf("raw secret leaked in list response: %s", body)
	}
}

// TestStashBoxDatabases_HiddenFromConnectionsList is AC13: the two registry
// services must no longer appear in GET /api/connections, or Settings would
// list them twice with two different edit paths.
func TestStashBoxDatabases_HiddenFromConnectionsList(t *testing.T) {
	env := newStashBoxEnv(t)
	ctx := context.Background()
	for _, svc := range []string{"stashdb", "fansdb", "tpdb"} {
		if err := env.connStore.Upsert(ctx, svc, "", "key-"+svc); err != nil {
			t.Fatalf("seeding %s: %v", svc, err)
		}
	}

	code, body := env.do(t, http.MethodGet, "/api/connections", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/connections = %d (%s)", code, body)
	}
	var list []connections.Summary
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decoding connections: %v (%s)", err, body)
	}
	seen := map[string]bool{}
	for _, c := range list {
		seen[c.Service] = true
	}
	if seen["stashdb"] || seen["fansdb"] {
		t.Errorf("registry services still listed in /api/connections: %+v", list)
	}
	// TPDB stays (locked decision 4) — it is a plain connection, not a
	// registry row, and hiding it would strand its Metadata-sources UI.
	if !seen["tpdb"] {
		t.Error("tpdb was filtered out of /api/connections but must remain")
	}
}

// TestStashBoxDatabases_CreateAppendsToCascade covers POST: a new row lands at
// the END of the cascade so adding a database never silently reprioritises the
// existing ones.
func TestStashBoxDatabases_CreateAppendsToCascade(t *testing.T) {
	env := newStashBoxEnv(t)

	code, body := env.do(t, http.MethodPost, "/api/stashbox-databases",
		apidto.StashBoxDatabaseCreateRequest{
			Name:     "pmvstash",
			Endpoint: "https://pmvstash.org/graphql",
			APIKey:   "pmv-key-4321",
		})
	if code != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (%s)", code, body)
	}
	created := decodeStashBox(t, body)
	if created.Name != "pmvstash" || created.Priority != 2 {
		t.Errorf("created = %+v, want name=pmvstash priority=2", created)
	}
	if !created.HasAPIKey || created.KeySuffix != "4321" {
		t.Errorf("created key not reported: %+v", created)
	}
	if bytes.Contains(body, []byte("pmv-key-4321")) {
		t.Fatalf("raw secret echoed by create: %s", body)
	}

	got := env.list(t)
	if len(got) != 3 || got[2].Name != "pmvstash" {
		t.Fatalf("cascade after create = %+v", got)
	}
}

// TestStashBoxDatabases_CreateRejects covers the four Create guards the store
// owns, asserting each surfaces as a 400 (an operator-fixable input problem)
// rather than a 500.
func TestStashBoxDatabases_CreateRejects(t *testing.T) {
	env := newStashBoxEnv(t)

	cases := []struct {
		name string
		req  apidto.StashBoxDatabaseCreateRequest
	}{
		{"reserved name", apidto.StashBoxDatabaseCreateRequest{Name: "tpdb", Endpoint: "https://x.test/graphql", APIKey: "k"}},
		{"duplicate name", apidto.StashBoxDatabaseCreateRequest{Name: "stashdb", Endpoint: "https://x.test/graphql", APIKey: "k"}},
		{"blank name", apidto.StashBoxDatabaseCreateRequest{Name: "  ", Endpoint: "https://x.test/graphql", APIKey: "k"}},
		{"bad endpoint", apidto.StashBoxDatabaseCreateRequest{Name: "ok", Endpoint: "not-a-url", APIKey: "k"}},
		{"no key", apidto.StashBoxDatabaseCreateRequest{Name: "ok", Endpoint: "https://x.test/graphql"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := env.do(t, http.MethodPost, "/api/stashbox-databases", tc.req)
			if code != http.StatusBadRequest {
				t.Fatalf("POST = %d, want 400 (%s)", code, body)
			}
		})
	}
}

// TestStashBoxDatabases_CreateHitsCap is AC16: the sixth create is refused.
func TestStashBoxDatabases_CreateHitsCap(t *testing.T) {
	env := newStashBoxEnv(t)

	// Two are seeded; three more fill the cap of five.
	for _, name := range []string{"three", "four", "five"} {
		code, body := env.do(t, http.MethodPost, "/api/stashbox-databases",
			apidto.StashBoxDatabaseCreateRequest{Name: name, Endpoint: "https://" + name + ".test/graphql", APIKey: "k"})
		if code != http.StatusOK {
			t.Fatalf("creating %s = %d (%s)", name, code, body)
		}
	}
	code, body := env.do(t, http.MethodPost, "/api/stashbox-databases",
		apidto.StashBoxDatabaseCreateRequest{Name: "six", Endpoint: "https://six.test/graphql", APIKey: "k"})
	if code != http.StatusBadRequest {
		t.Fatalf("sixth create = %d, want 400 (%s)", code, body)
	}
	if len(env.list(t)) != 5 {
		t.Fatalf("cap breached: %+v", env.list(t))
	}
}

// TestStashBoxDatabases_SeededRowsAreFullyEditable is AC11: there is no
// reserved tier — the seeded StashDB row can be renamed, re-endpointed,
// reprioritised, disabled and fansite-flagged like any other.
func TestStashBoxDatabases_SeededRowsAreFullyEditable(t *testing.T) {
	env := newStashBoxEnv(t)
	ctx := context.Background()
	if err := env.connStore.Upsert(ctx, "stashdb", "", "orig-key-1111"); err != nil {
		t.Fatalf("seeding key: %v", err)
	}
	id := env.list(t)[0].ID

	newName, newEndpoint, fansite, enabled := "stashdb-mirror", "https://mirror.test/graphql", true, false
	code, body := env.do(t, http.MethodPut, "/api/stashbox-databases/"+strconv.FormatInt(id, 10),
		apidto.StashBoxDatabaseUpdateRequest{
			Name: &newName, Endpoint: &newEndpoint, FansiteOnly: &fansite, Enabled: &enabled,
		})
	if code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (%s)", code, body)
	}
	updated := decodeStashBox(t, body)
	if updated.Name != newName || updated.Endpoint != newEndpoint || !updated.FansiteOnly || updated.Enabled {
		t.Fatalf("edits not applied: %+v", updated)
	}
	// The key is untouched by an update that never mentioned it — the
	// three-state rule's "absent = preserve" leg, and the one that silently
	// wipes secrets when it regresses.
	if !updated.HasAPIKey || updated.KeySuffix != "1111" {
		t.Fatalf("renaming the row dropped its key: %+v", updated)
	}
}

// TestStashBoxDatabases_UpdateKeyThreeStates walks all three legs of the
// secret rule on a SEEDED row, where the write has to land in `connections`
// rather than inline — the routing the API never exposes.
func TestStashBoxDatabases_UpdateKeyThreeStates(t *testing.T) {
	env := newStashBoxEnv(t)
	ctx := context.Background()
	if err := env.connStore.Upsert(ctx, "fansdb", "", "first-key-1111"); err != nil {
		t.Fatalf("seeding key: %v", err)
	}
	id := env.list(t)[1].ID
	path := "/api/stashbox-databases/" + strconv.FormatInt(id, 10)

	// absent → preserve
	prio := 1
	code, body := env.do(t, http.MethodPut, path, apidto.StashBoxDatabaseUpdateRequest{Priority: &prio})
	if code != http.StatusOK {
		t.Fatalf("preserve PUT = %d (%s)", code, body)
	}
	if got := decodeStashBox(t, body); got.KeySuffix != "1111" {
		t.Fatalf("absent apiKey did not preserve: %+v", got)
	}

	// non-empty → replace, and it must land in `connections`, not inline
	replacement := "second-key-2222"
	code, body = env.do(t, http.MethodPut, path, apidto.StashBoxDatabaseUpdateRequest{APIKey: &replacement})
	if code != http.StatusOK {
		t.Fatalf("replace PUT = %d (%s)", code, body)
	}
	if got := decodeStashBox(t, body); got.KeySuffix != "2222" {
		t.Fatalf("replacement not applied: %+v", got)
	}
	conn, err := env.connStore.Get(ctx, "fansdb")
	if err != nil {
		t.Fatalf("reading connections after replace: %v", err)
	}
	if conn.APIKey != replacement {
		t.Fatalf("replacement went somewhere other than connections: %q", conn.APIKey)
	}

	// "" → clear
	blank := ""
	code, body = env.do(t, http.MethodPut, path, apidto.StashBoxDatabaseUpdateRequest{APIKey: &blank})
	if code != http.StatusOK {
		t.Fatalf("clear PUT = %d (%s)", code, body)
	}
	if got := decodeStashBox(t, body); got.HasAPIKey {
		t.Fatalf("empty apiKey did not clear: %+v", got)
	}
}

// TestStashBoxDatabases_Reorder covers the drag-and-drop persist path plus its
// full-set contract.
func TestStashBoxDatabases_Reorder(t *testing.T) {
	env := newStashBoxEnv(t)
	before := env.list(t)
	flipped := []int64{before[1].ID, before[0].ID}

	code, body := env.do(t, http.MethodPut, "/api/stashbox-databases/reorder",
		apidto.StashBoxDatabaseReorderRequest{IDs: flipped})
	if code != http.StatusOK {
		t.Fatalf("reorder = %d, want 200 (%s)", code, body)
	}
	var got []apidto.StashBoxDatabase
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding reorder response: %v", err)
	}
	if got[0].Name != "fansdb" || got[1].Name != "stashdb" {
		t.Fatalf("reorder response order = %q,%q", got[0].Name, got[1].Name)
	}
	if persisted := env.list(t); persisted[0].Name != "fansdb" {
		t.Fatalf("reorder did not persist: %+v", persisted)
	}

	// A partial list is refused rather than leaving the unlisted row at a
	// stale priority.
	code, body = env.do(t, http.MethodPut, "/api/stashbox-databases/reorder",
		apidto.StashBoxDatabaseReorderRequest{IDs: []int64{before[0].ID}})
	if code != http.StatusBadRequest {
		t.Fatalf("partial reorder = %d, want 400 (%s)", code, body)
	}
}

// TestStashBoxDatabases_Delete covers DELETE, including the seeded-row case
// where the paired `connections` secret is cleared so none is orphaned.
func TestStashBoxDatabases_Delete(t *testing.T) {
	env := newStashBoxEnv(t)
	ctx := context.Background()
	if err := env.connStore.Upsert(ctx, "fansdb", "", "doomed-key-1111"); err != nil {
		t.Fatalf("seeding key: %v", err)
	}
	id := env.list(t)[1].ID

	code, body := env.do(t, http.MethodDelete, "/api/stashbox-databases/"+strconv.FormatInt(id, 10), nil)
	if code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204 (%s)", code, body)
	}
	got := env.list(t)
	if len(got) != 1 || got[0].Name != "stashdb" {
		t.Fatalf("after delete = %+v", got)
	}
	if _, err := env.connStore.Get(ctx, "fansdb"); err == nil {
		t.Fatal("deleting the row left its connections secret behind")
	}

	// Deleting it again is a 404, not a silent success.
	if code, _ := env.do(t, http.MethodDelete, "/api/stashbox-databases/"+strconv.FormatInt(id, 10), nil); code != http.StatusNotFound {
		t.Fatalf("second DELETE = %d, want 404", code)
	}
}

// TestStashBoxDatabases_BadID asserts a non-numeric id is a 400 rather than a
// panic or a 404.
func TestStashBoxDatabases_BadID(t *testing.T) {
	env := newStashBoxEnv(t)
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		code, body := env.do(t, method, "/api/stashbox-databases/abc",
			apidto.StashBoxDatabaseUpdateRequest{})
		if code != http.StatusBadRequest {
			t.Errorf("%s /abc = %d, want 400 (%s)", method, code, body)
		}
	}
}

// TestStashBoxDatabases_TestStoredHidesDetail is the security contract carried
// over from connectionsTestStoredHandler: a failed stored test reports a fixed
// message, never the raw client error, because that error echoes the row's
// endpoint — stored operator config the client may not read back.
func TestStashBoxDatabases_TestStoredHidesDetail(t *testing.T) {
	env := newStashBoxEnv(t)
	// A row pointed at an unroutable endpoint so the test fails at dial time,
	// which is exactly the error shape that would otherwise leak the host.
	endpoint := "http://127.0.0.1:1/graphql"
	id := env.list(t)[0].ID
	key := "k"
	if code, body := env.do(t, http.MethodPut, "/api/stashbox-databases/"+strconv.FormatInt(id, 10),
		apidto.StashBoxDatabaseUpdateRequest{Endpoint: &endpoint, APIKey: &key}); code != http.StatusOK {
		t.Fatalf("pointing row at dead endpoint = %d (%s)", code, body)
	}

	code, body := env.do(t, http.MethodPost, "/api/stashbox-databases/"+strconv.FormatInt(id, 10)+"/test-stored", nil)
	if code != http.StatusOK {
		t.Fatalf("test-stored = %d, want 200 with ok:false (%s)", code, body)
	}
	var result ConnectionTestResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decoding result: %v (%s)", err, body)
	}
	if result.OK {
		t.Fatal("test against a dead endpoint reported success")
	}
	if result.Error != "connection test failed" {
		t.Fatalf("stored test leaked detail: %q", result.Error)
	}
	if bytes.Contains(body, []byte("127.0.0.1")) {
		t.Fatalf("stored test echoed the endpoint: %s", body)
	}
}

// TestStashBoxDatabases_Unavailable asserts the nil-store path answers 503
// rather than panicking — every mux built without a connections store (most
// of this package's other tests) has no registry to serve.
func TestStashBoxDatabases_Unavailable(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	srv := httptest.NewServer(NewMux(testHTTPClient(), nil, nil, proposals.New(sqlDB), allowlist.New(sqlDB),
		testProber(t), testPHasher(t), testVideoHasher(t), settings.New(sqlDB), nil, library.New(sqlDB),
		nil, nil, nil, nil, testFeedHealth(), nil, nil, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/stashbox-databases")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("nil-store GET = %d, want 503", resp.StatusCode)
	}
}
