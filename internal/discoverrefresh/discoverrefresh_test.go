package discoverrefresh

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/discoversliders"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/tmdb"
	"github.com/labbersanon/sakms/internal/trakt"
)

// newTestSettingsStore is this file's minimal single-purpose db+settings
// wiring — deliberately smaller than newTraktDeps/sliderTestEnv (trakt_test.go,
// sliders_test.go), which build a whole Deps. LoadInterval only ever touches
// a *settings.Store.
func newTestSettingsStore(t *testing.T) *settings.Store {
	t.Helper()
	sqlDB := dbtest.New(t)
	return settings.New(sqlDB)
}

// T-1 — LoadInterval's four cases, mirroring adultnewest's own
// TestLoadInterval_* suite (internal/adultnewest/scan_test.go:267-311)
// one-for-one against this package's copy.

func TestLoadInterval_UnsetDefaultsTo24Hours(t *testing.T) {
	store := newTestSettingsStore(t)
	want := defaultIntervalHours * time.Hour
	if got := LoadInterval(context.Background(), store); got != want {
		t.Errorf("unset interval = %v, want %v (on by default)", got, want)
	}
}

func TestLoadInterval_ExplicitZeroIsOffNotDefault(t *testing.T) {
	store := newTestSettingsStore(t)
	ctx := context.Background()
	if err := store.Set(ctx, IntervalSettingKey, "0"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := LoadInterval(ctx, store); got != 0 {
		t.Errorf("explicit 0 = %v, want 0 — an operator turning it off must stay off", got)
	}
}

func TestLoadInterval_BlankNonIntegerOrNegativeIsZero(t *testing.T) {
	store := newTestSettingsStore(t)
	ctx := context.Background()
	for _, v := range []string{"abc", "", "-5"} {
		if err := store.Set(ctx, IntervalSettingKey, v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
		if got := LoadInterval(ctx, store); got != 0 {
			t.Errorf("stored %q = %v, want 0", v, got)
		}
	}
}

func TestLoadInterval_ValidPositiveRoundTrips(t *testing.T) {
	store := newTestSettingsStore(t)
	ctx := context.Background()
	if err := store.Set(ctx, IntervalSettingKey, "3600"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := LoadInterval(ctx, store); got != time.Hour {
		t.Errorf("stored 3600 = %v, want 1h", got)
	}
}

// fullDeps wires ConnStore, SettingsStore, SlidersStore, TraktStore and Cache
// over ONE migrated sqlite file, matching how cmd/sakms/main.go constructs a
// single Deps over the app's one *sql.DB. Every store is real (never nil),
// so "unconfigured" in these tests means "no connection/credentials saved,"
// the same production meaning §3.7 documents — not a nil-pointer shortcut.
func fullDeps(t *testing.T) (Deps, *sql.DB) {
	t.Helper()
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	d := Deps{
		HTTPClient:    &http.Client{},
		ConnStore:     connections.New(sqlDB, secretStore),
		SettingsStore: settings.New(sqlDB),
		SlidersStore:  discoversliders.New(sqlDB),
		TraktStore:    trakt.NewStore(sqlDB, secretStore),
		TraktBaseURL:  trakt.DefaultBaseURL,
		Cache:         NewStore(sqlDB),
	}
	return d, sqlDB
}

func cacheRowCount(t *testing.T, sqlDB *sql.DB) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM discover_row_cache`).Scan(&n); err != nil {
		t.Fatalf("counting discover_row_cache rows: %v", err)
	}
	return n
}

// emptyTMDBListServer answers every TMDB list path with a genuinely empty
// results page, so any of the six refreshTMDBCategoriesDue targets that
// reaches it accumulates zero items and stops (exhausted) on its first raw
// page — one call per target reached, nothing more, and never a
// HasUSRelease follow-up (that only fires on a non-empty batch).
func emptyTMDBListServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// pointTMDBAt swaps the package-level tmdb.DefaultBaseURL for the duration
// of the test — the seam that var exists for (client.go's own doc comment;
// see e.g. internal/mode/mode_test.go's overrideAIProviderBaseURL for the
// same technique against other providers). Required because
// buildBypassedTMDBClient (sliders.go) hardcodes tmdb.DefaultBaseURL rather
// than reading it from the stored connection.
func pointTMDBAt(t *testing.T, url string) {
	t.Helper()
	prev := tmdb.DefaultBaseURL
	tmdb.DefaultBaseURL = url
	t.Cleanup(func() { tmdb.DefaultBaseURL = prev })
}

// T-19 — unconfigured sources are silent: no TMDB connection, no Trakt
// credentials, no sliders. Every store is real; nothing is SAVED to it. Must
// produce no rows, no panic, and (implicitly, by never standing up a fake
// server) no external call of any kind.
func TestRefreshAll_UnconfiguredSourcesAreSilent(t *testing.T) {
	d, sqlDB := fullDeps(t)
	ctx := context.Background()

	RefreshAll(ctx, d, false)
	RefreshAll(ctx, d, true)

	if n := cacheRowCount(t, sqlDB); n != 0 {
		t.Errorf("cache has %d rows after refreshing three unconfigured sources, want 0", n)
	}
}

// T-17 — the due-check. Seeds every key this Deps can reach (six tmdb
// categories, the one trakt key — SlidersStore has no sliders, so that
// source is a no-op either way) with refreshed_at = now, proves
// RefreshAll(force=false) makes zero upstream calls, ages every row past the
// interval, proves the SAME call makes calls once stale, then proves
// force=true refreshes regardless of freshness.
func TestRefreshAll_DueCheck(t *testing.T) {
	d, sqlDB := fullDeps(t)
	ctx := context.Background()

	if err := d.ConnStore.Upsert(ctx, "tmdb", "", "test-key"); err != nil {
		t.Fatalf("configuring tmdb connection: %v", err)
	}
	linkTrakt(t, d.TraktStore)
	if err := d.SettingsStore.Set(ctx, IntervalSettingKey, "86400"); err != nil {
		t.Fatalf("setting interval: %v", err)
	}

	var tmdbCalls, traktCalls atomic.Int32
	tmdbSrv := emptyTMDBListServer(t, &tmdbCalls)
	pointTMDBAt(t, tmdbSrv.URL)

	traktSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traktCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(traktSrv.Close)
	d.TraktBaseURL = traktSrv.URL
	d.HTTPClient = tmdbSrv.Client()

	// Seed every reachable key as freshly refreshed.
	for _, target := range tmdbTargets {
		if err := d.Cache.Put(ctx, "tmdb", target.cacheKey(), items(1), tmdbPageSize, 1, true); err != nil {
			t.Fatalf("seeding tmdb/%s: %v", target.cacheKey(), err)
		}
	}
	if err := d.Cache.Put(ctx, sourceTrakt, traktCacheKey, items(1), 1, 1, true); err != nil {
		t.Fatalf("seeding trakt: %v", err)
	}

	RefreshAll(ctx, d, false)
	if got := tmdbCalls.Load(); got != 0 {
		t.Errorf("tmdb calls with every row fresh = %d, want 0", got)
	}
	if got := traktCalls.Load(); got != 0 {
		t.Errorf("trakt calls with the row fresh = %d, want 0", got)
	}

	// Age every row past the 24h interval directly via SQL — Put always
	// stamps refreshed_at = now, so this is the only way to seed a stale row.
	stale := time.Now().Add(-25 * time.Hour).UTC().Format(timeLayout)
	if _, err := sqlDB.ExecContext(ctx, `UPDATE discover_row_cache SET refreshed_at = ?`, stale); err != nil {
		t.Fatalf("aging cache rows: %v", err)
	}

	RefreshAll(ctx, d, false)
	if got := tmdbCalls.Load(); got == 0 {
		t.Error("tmdb calls with every row stale = 0, want > 0 — a stale row must be refreshed")
	}
	if got := traktCalls.Load(); got == 0 {
		t.Error("trakt calls with the row stale = 0, want > 0")
	}

	tmdbCalls.Store(0)
	traktCalls.Store(0)
	RefreshAll(ctx, d, true)
	if got := tmdbCalls.Load(); got == 0 {
		t.Error("tmdb calls with force=true and every row freshly refreshed = 0, want > 0 — force must ignore refreshed_at")
	}
	if got := traktCalls.Load(); got == 0 {
		t.Error("trakt calls with force=true = 0, want > 0")
	}
}

// T-12c — interval <= 0 purges the whole cache at boot, rather than freezing
// it (Critic finding H4). Run's interval<=0 branch is synchronous (purge,
// return — no ticker is ever started), so this exercises Run directly with
// no goroutine/timing involved.
func TestRun_ZeroIntervalPurgesCacheAtBoot(t *testing.T) {
	d, sqlDB := fullDeps(t)
	ctx := context.Background()

	if err := d.Cache.Put(ctx, "tmdb", "movies:trending", items(3), tmdbPageSize, 1, true); err != nil {
		t.Fatalf("seeding tmdb row: %v", err)
	}
	if err := d.Cache.Put(ctx, sourceTrakt, traktCacheKey, items(2), 2, 1, true); err != nil {
		t.Fatalf("seeding trakt row: %v", err)
	}
	if n := cacheRowCount(t, sqlDB); n != 2 {
		t.Fatalf("seeded %d rows, want 2", n)
	}

	Run(ctx, 0, d)

	if n := cacheRowCount(t, sqlDB); n != 0 {
		t.Errorf("cache has %d rows after Run with interval<=0, want 0 — interval-off must restore live behaviour, not freeze the cache", n)
	}
}

// T-12 — cycle-scope single-flight: two concurrent TriggerOnce calls, the
// second returns false while the first is still running. Made deterministic
// by blocking the fake TMDB server's handler on a channel until the test has
// observed the request arrive and asserted the second call's result.
func TestTriggerOnce_CycleScope_SecondCallReturnsFalse(t *testing.T) {
	d, _ := fullDeps(t)
	ctx := context.Background()

	if err := d.ConnStore.Upsert(ctx, "tmdb", "", "test-key"); err != nil {
		t.Fatalf("configuring tmdb connection: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(srv.Close)
	pointTMDBAt(t, srv.URL)
	d.HTTPClient = srv.Client()

	firstResult := make(chan bool, 1)
	go func() { firstResult <- TriggerOnce(context.Background(), d) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first TriggerOnce cycle never reached the fake TMDB server")
	}

	if got := TriggerOnce(context.Background(), d); got {
		t.Error("second concurrent TriggerOnce returned true, want false — a cycle was already running")
	}

	close(release)

	select {
	case got := <-firstResult:
		if !got {
			t.Error("first TriggerOnce returned false, want true — it should have run uncontested")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first TriggerOnce cycle never completed after being released")
	}

	if cycleRunning.Load() {
		t.Error("cycleRunning left true after both calls finished")
	}
}

// TestTriggerAsync_ReturnsImmediatelyAndCompletesInBackground is BE-15's own
// version of T-12: unlike TriggerOnce (deliberately synchronous, see the
// test above), TriggerAsync must claim cycleRunning and return WITHOUT
// waiting for the forced cycle to finish — the shape
// triggerDiscoverRefreshHandler needs to answer 202 immediately (plan
// §5.1). Made deterministic the same way: the fake TMDB server blocks on a
// channel until the test has already observed TriggerAsync return.
func TestTriggerAsync_ReturnsImmediatelyAndCompletesInBackground(t *testing.T) {
	d, sqlDB := fullDeps(t)
	ctx := context.Background()

	if err := d.ConnStore.Upsert(ctx, "tmdb", "", "test-key"); err != nil {
		t.Fatalf("configuring tmdb connection: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(srv.Close)
	pointTMDBAt(t, srv.URL)
	d.HTTPClient = srv.Client()

	returned := make(chan bool, 1)
	go func() { returned <- TriggerAsync(ctx, d) }()

	select {
	case got := <-returned:
		if !got {
			t.Error("TriggerAsync returned false, want true — no cycle was running yet")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TriggerAsync blocked instead of returning immediately")
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("TriggerAsync's background cycle never reached the fake TMDB server")
	}

	if !cycleRunning.Load() {
		t.Error("cycleRunning = false while TriggerAsync's background cycle is still running")
	}

	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for cycleRunning.Load() {
		if time.Now().After(deadline) {
			t.Fatal("cycleRunning never cleared after the background cycle finished")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if n := cacheRowCount(t, sqlDB); n == 0 {
		t.Error("cache has 0 rows after TriggerAsync's background cycle finished, want at least one — the forced cycle should have run")
	}
}

// TestTriggerAsync_BusyDuringACycleReturnsFalse confirms TriggerAsync shares
// the SAME cycleRunning flag as TriggerOnce/Run (plan §5.1) — a call while
// another cycle (started by TriggerOnce here, to reuse its blocking
// contract for a deterministic test) is in flight returns false immediately
// and starts nothing.
func TestTriggerAsync_BusyDuringACycleReturnsFalse(t *testing.T) {
	d, _ := fullDeps(t)
	ctx := context.Background()

	if err := d.ConnStore.Upsert(ctx, "tmdb", "", "test-key"); err != nil {
		t.Fatalf("configuring tmdb connection: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(srv.Close)
	pointTMDBAt(t, srv.URL)
	d.HTTPClient = srv.Client()

	firstResult := make(chan bool, 1)
	go func() { firstResult <- TriggerOnce(ctx, d) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first cycle never reached the fake TMDB server")
	}

	if got := TriggerAsync(ctx, d); got {
		t.Error("TriggerAsync returned true while a TriggerOnce cycle was running, want false — no second cycle should have started")
	}

	close(release)

	select {
	case got := <-firstResult:
		if !got {
			t.Error("first TriggerOnce returned false, want true — it should have run uncontested")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first cycle never completed after being released")
	}
}
