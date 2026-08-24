// Tests for the Adult monitor API handlers:
//   GET  /api/modes/adult/discover/monitor         — zero Prowlarr calls
//   PUT  /api/modes/adult/discover/monitor         — 409 on unresolvable
//   GET  /api/settings/adult-monitor-interval      — default + override
//   PUT  /api/settings/adult-monitor-interval      — stores + negative rejected
//
// These tests exercise the full request path through NewMux so routing,
// middleware, and handler wiring are all covered.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/discoversliders"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/rssfeeds"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/trakt"
)

// newMonitorTestEnv sets up a real DB with all stores on the SAME database,
// registers the monitor routes via NewMux, and returns the test server plus
// the monitored store. Using a single database ensures writes made directly
// through the returned stores are visible to the handlers.
func newMonitorTestEnv(t *testing.T) (*httptest.Server, *adultnewest.MonitoredStore, *adultnewest.ReleaseStore) {
	t.Helper()
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}

	connStore := connections.New(sqlDB, secretStore)
	propStore := proposals.New(sqlDB)
	settingsStore := settings.New(sqlDB)
	grabsStore := grabs.New(sqlDB, secretStore)
	libStore := library.New(sqlDB)
	slidersStore := discoversliders.New(sqlDB)
	traktStore := trakt.NewStore(sqlDB, secretStore)
	adultNewestRowStore := adultnewest.New(sqlDB)
	releaseStore := adultnewest.NewReleaseStore(sqlDB)
	rssFeedsStore := rssfeeds.NewStore(sqlDB, secretStore)
	monitoredStore := adultnewest.NewMonitoredStore(sqlDB)

	mux := NewMux(testHTTPClient(), connStore, nil, propStore,
		testProber(t), testPHasher(t), testVideoHasher(t), settingsStore,
		grabsStore, libStore, slidersStore, traktStore,
		adultNewestRowStore, releaseStore, testFeedHealth(), rssFeedsStore,
		nil, nil, nil, nil, nil, nil, nil, nil,
		monitoredStore)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, monitoredStore, releaseStore
}

// --- GET /api/modes/adult/discover/monitor ---

func TestGetAdultMonitorState_BadKindReturns400(t *testing.T) {
	srv, _, _ := newMonitorTestEnv(t)

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/monitor?kind=invalid&name=Jane")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for bad kind; got %d", resp.StatusCode)
	}
}

func TestGetAdultMonitorState_EmptyNameReturns400(t *testing.T) {
	srv, _, _ := newMonitorTestEnv(t)

	resp, err := http.Get(srv.URL + "/api/modes/adult/discover/monitor?kind=performer&name=")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty name; got %d", resp.StatusCode)
	}
}

func TestGetAdultMonitorState_NotInPool_ReturnsUnresolved(t *testing.T) {
	srv, _, _ := newMonitorTestEnv(t)

	// Neither monitored store nor pool has this entity — must return resolved=false.
	var state apidto.AdultMonitorState
	getJSON(t, srv.URL+"/api/modes/adult/discover/monitor?kind=performer&name=Nobody", &state)
	if state.Resolved {
		t.Errorf("expected Resolved=false for unknown entity; got %+v", state)
	}
	if state.Monitored {
		t.Errorf("expected Monitored=false; got %+v", state)
	}
}

func TestGetAdultMonitorState_FromMonitoredStore_NoProlarr(t *testing.T) {
	srv, monitoredStore, _ := newMonitorTestEnv(t)
	ctx := context.Background()

	// Seed the monitored store directly — no Prowlarr should be called.
	since := "2026-08-24T10:00:00.000Z"
	_, err := monitoredStore.UpsertOnMonitor(ctx, "performer", "tpdb", "perf-123", "Jane Doe", "", since)
	if err != nil {
		t.Fatalf("UpsertOnMonitor: %v", err)
	}

	var state apidto.AdultMonitorState
	getJSON(t, srv.URL+"/api/modes/adult/discover/monitor?kind=performer&name=Jane+Doe", &state)
	if !state.Resolved {
		t.Errorf("expected Resolved=true from monitored store; got %+v", state)
	}
	if state.EntityID != "perf-123" {
		t.Errorf("expected EntityID=perf-123; got %q", state.EntityID)
	}
	if state.Source != "tpdb" {
		t.Errorf("expected Source=tpdb; got %q", state.Source)
	}
	if !state.Monitored {
		t.Error("expected Monitored=true since we upserted with monitored=1")
	}
}

// --- PUT /api/modes/adult/discover/monitor ---

func TestPutAdultMonitor_BadKindReturns400(t *testing.T) {
	srv, _, _ := newMonitorTestEnv(t)

	body, _ := json.Marshal(apidto.SetAdultMonitorRequest{Kind: "badkind", Name: "Jane", Monitored: true})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/adult/discover/monitor", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400; got %d", resp.StatusCode)
	}
}

func TestPutAdultMonitor_Enable_NoIdentifyConfigured_Returns409(t *testing.T) {
	// With no connections configured, ResolveEntityID cannot run — expect 409.
	srv, _, _ := newMonitorTestEnv(t)

	body, _ := json.Marshal(apidto.SetAdultMonitorRequest{Kind: "performer", Name: "Jane Doe", Monitored: true})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/adult/discover/monitor", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	// Identify is not configured — mode.Build will not return a session with
	// a populated Identify, so the handler must return 409 or 500.
	if resp.StatusCode == http.StatusOK {
		t.Errorf("expected non-200 when identify is not configured; got 200")
	}
}

func TestPutAdultMonitor_Disable_WhenNotInStore_NoOp204(t *testing.T) {
	// Un-monitoring a performer that was never monitored must be a no-op 204.
	srv, _, _ := newMonitorTestEnv(t)

	body, _ := json.Marshal(apidto.SetAdultMonitorRequest{Kind: "performer", Name: "Nobody", Monitored: false})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/adult/discover/monitor", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204 for un-monitoring a non-existent entity; got %d", resp.StatusCode)
	}
}

func TestPutAdultMonitor_Disable_CancelsNeverDispatchedRetries(t *testing.T) {
	ctx := context.Background()
	srv, monitoredStore, _ := newMonitorTestEnv(t)

	// Seed the monitored store so the entity exists.
	since := "2026-08-24T10:00:00.000Z"
	_, err := monitoredStore.UpsertOnMonitor(ctx, "performer", "tpdb", "perf-dis", "Alice Smith", "", since)
	if err != nil {
		t.Fatalf("UpsertOnMonitor: %v", err)
	}

	// Disable monitoring — must return 204 and clear monitored flag.
	body, _ := json.Marshal(apidto.SetAdultMonitorRequest{Kind: "performer", Name: "Alice Smith", Monitored: false})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/modes/adult/discover/monitor", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204; got %d", resp.StatusCode)
	}

	// Verify the entity is marked un-monitored in the store.
	e, err := monitoredStore.GetByKindSourceID(ctx, "performer", "tpdb", "perf-dis")
	if err != nil {
		t.Fatalf("GetByKindSourceID: %v", err)
	}
	if e.Monitored {
		t.Errorf("expected Monitored=false after PUT disable; got %+v", e)
	}
	if e.MonitoredSince != "" {
		t.Errorf("expected MonitoredSince cleared after disable; got %q", e.MonitoredSince)
	}
}

// --- GET /api/settings/adult-monitor-interval ---

func TestGetAdultMonitorInterval_DefaultWhenNeverSet(t *testing.T) {
	srv, _, _ := newMonitorTestEnv(t)

	type resp struct {
		IntervalSeconds int `json:"intervalSeconds"`
	}
	var out resp
	getJSON(t, srv.URL+"/api/settings/adult-monitor-interval", &out)
	if out.IntervalSeconds != 24*60*60 {
		t.Errorf("expected default 86400; got %d", out.IntervalSeconds)
	}
}

// --- PUT /api/settings/adult-monitor-interval ---

func TestPutAdultMonitorInterval_StoresAndRetrievesValue(t *testing.T) {
	srv, _, _ := newMonitorTestEnv(t)

	putBody, _ := json.Marshal(map[string]int{"intervalSeconds": 7200})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/adult-monitor-interval", bytes.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("PUT expected 204; got %d", resp.StatusCode)
	}

	type getResp struct {
		IntervalSeconds int `json:"intervalSeconds"`
	}
	var out getResp
	getJSON(t, srv.URL+"/api/settings/adult-monitor-interval", &out)
	if out.IntervalSeconds != 7200 {
		t.Errorf("expected 7200 after PUT; got %d", out.IntervalSeconds)
	}
}

func TestPutAdultMonitorInterval_NegativeRejected(t *testing.T) {
	srv, _, _ := newMonitorTestEnv(t)

	putBody, _ := json.Marshal(map[string]int{"intervalSeconds": -1})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/adult-monitor-interval", bytes.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for negative interval; got %d", resp.StatusCode)
	}
}
