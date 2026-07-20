package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/nodekeys"
	"github.com/labbersanon/sakms/internal/nodes"
	"github.com/labbersanon/sakms/internal/nodesettings"
	"github.com/labbersanon/sakms/internal/settings"
)

// testNodesMux builds a real NewNodesMux backed by real stores sharing one
// sqlite DB (mirroring cmd/sakms/main.go's wiring), plus the API key needed
// to hit its operator-authenticated routes. Distinct from testNodeMux
// (nodes_test.go), which has a known pre-existing bug (its reg param is
// never actually passed into NewMux) — this helper builds the mux this
// story's new routes are actually registered on.
func testNodesMux(t *testing.T) (mux *http.ServeMux, reg *nodes.Registry, sqlDB *sql.DB, settingsStore *settings.Store, nodeSettingsStore *nodesettings.Store, nodeKeyStore *nodekeys.Store, apiKey string) {
	t.Helper()
	authStore, tokenEnc, db := testAuthStoreWithDB(t)

	reg = nodes.New()
	pairingReg := nodes.NewPairingRegistry()
	nodeKeyStore = nodekeys.New(db)
	settingsStore = settings.New(db)
	nodeSettingsStore = nodesettings.New(db)

	key, err := authStore.EnsureAPIKey(context.Background())
	if err != nil {
		t.Fatalf("EnsureAPIKey: %v", err)
	}

	mux = NewNodesMux(reg, pairingReg, nodeKeyStore, tokenEnc, authStore, settingsStore, nodeSettingsStore)
	return mux, reg, db, settingsStore, nodeSettingsStore, nodeKeyStore, key
}

func TestNodePathMappings_Unauthenticated_401(t *testing.T) {
	mux, _, _, _, _, _, _ := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/nodes/some-node/path-mappings")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without credentials, got %d", resp.StatusCode)
	}
}

func TestNodeBrowse_Unauthenticated_401(t *testing.T) {
	mux, _, _, _, _, _, _ := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/nodes/some-node/browse?path=/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without credentials, got %d", resp.StatusCode)
	}
}

func TestNodeBrowseResult_NoNodeBearerKey_401(t *testing.T) {
	mux, _, _, _, _, _, apiKey := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// A valid OPERATOR key must not substitute for a node's bearer key on the
	// node-callback route.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/nodes/browse/req-1/result", nil)
	req.Header.Set("X-Api-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a node bearer key, got %d", resp.StatusCode)
	}
}

func TestNodePathMappings_FixedFiveRows_ConfiguredAndPersistedValues(t *testing.T) {
	mux, _, _, settingsStore, nodeSettingsStore, _, apiKey := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := context.Background()
	if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), "/data/movies"); err != nil {
		t.Fatalf("settingsStore.Set: %v", err)
	}
	// Adult root deliberately left unset, to confirm it renders as
	// Configured=false rather than being omitted.

	if err := nodeSettingsStore.Set(ctx, "node-a", nodesettings.Settings{
		PathMappings: []nodesettings.PathMappingEntry{
			{LibraryPathKey: string(apidto.LibraryPathMoviesRoot), NodePath: "/mnt/movies"},
		},
		MaxJobs: 2,
	}); err != nil {
		t.Fatalf("nodeSettingsStore.Set: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/nodes/node-a/path-mappings", nil)
	req.Header.Set("X-Api-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var got apidto.NodePathMappingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(got.Entries) != 5 {
		t.Fatalf("expected exactly 5 fixed rows, got %d: %+v", len(got.Entries), got.Entries)
	}

	byKey := make(map[apidto.LibraryPathKey]apidto.NodePathMappingEntry, len(got.Entries))
	for _, e := range got.Entries {
		byKey[e.Key] = e
	}

	movies := byKey[apidto.LibraryPathMoviesRoot]
	if !movies.Configured || movies.ServerPath != "/data/movies" || movies.NodePath != "/mnt/movies" {
		t.Errorf("movies row: got %+v, want Configured=true ServerPath=/data/movies NodePath=/mnt/movies", movies)
	}

	adult := byKey[apidto.LibraryPathAdultRoot]
	if adult.Configured || adult.ServerPath != "" {
		t.Errorf("adult row: expected disabled/unconfigured (unset library path), got %+v", adult)
	}
}

// TestResolvePathMap_BlankNodePathSkipped locks in the fix for a real
// divergence bug found during review: resolvePathMap (the live-save push) and
// pushPersistedNodeSettings (the reconnect push, internal/nodesettings) must
// agree on what a blank NodePath means. Before this fix, resolvePathMap
// pushed Local: "" for a configured-but-blank row, which cmd/sakms-node's
// mergePathMap would apply as an explicit overwrite (wiping that key's
// existing node-local value) on save, while pushPersistedNodeSettings skips
// empty-NodePath rows entirely on reconnect — the exact same key would be
// wiped on save but left untouched on the very next reconnect. Blank must
// mean "leave it alone" on every push path, consistently.
func TestResolvePathMap_BlankNodePathSkipped(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer sqlDB.Close()
	settingsStore := settings.New(sqlDB)

	ctx := context.Background()
	if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), "/data/movies"); err != nil {
		t.Fatalf("settingsStore.Set: %v", err)
	}

	got := resolvePathMap(ctx, settingsStore, []apidto.NodePathMappingInput{
		{Key: apidto.LibraryPathMoviesRoot, NodePath: ""}, // configured library path, blank node path
	})
	if len(got) != 0 {
		t.Fatalf("expected a blank NodePath to be skipped (not pushed as Local: \"\"), got %+v", got)
	}
}

func TestResolvePathMap_UnconfiguredLibraryPathSkipped(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer sqlDB.Close()
	settingsStore := settings.New(sqlDB)

	got := resolvePathMap(context.Background(), settingsStore, []apidto.NodePathMappingInput{
		{Key: apidto.LibraryPathAdultRoot, NodePath: "/mnt/adult"}, // library path never configured
	})
	if len(got) != 0 {
		t.Fatalf("expected an unconfigured library path to be skipped, got %+v", got)
	}
}

func TestResolvePathMap_ConfiguredWithNodePath_Included(t *testing.T) {
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	defer sqlDB.Close()
	settingsStore := settings.New(sqlDB)

	ctx := context.Background()
	if err := settingsStore.Set(ctx, string(apidto.LibraryPathMoviesRoot), "/data/movies"); err != nil {
		t.Fatalf("settingsStore.Set: %v", err)
	}

	got := resolvePathMap(ctx, settingsStore, []apidto.NodePathMappingInput{
		{Key: apidto.LibraryPathMoviesRoot, NodePath: "/mnt/movies"},
	})
	if len(got) != 1 || got[0].Server != "/data/movies" || got[0].Local != "/mnt/movies" {
		t.Fatalf("expected the fully-specified row to be included, got %+v", got)
	}
}

func TestNodeBrowse_NodeNotConnected_ClearError(t *testing.T) {
	mux, _, _, _, _, _, apiKey := testNodesMux(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/nodes/never-connected/browse?path=/", nil)
	req.Header.Set("X-Api-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected a clear 502 for a non-connected node, got %d", resp.StatusCode)
	}
}
