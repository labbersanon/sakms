package adultmergecache

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"encoding/json"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// newSchedulerStores builds a connections/settings store pair plus a cache
// Store, all over one fresh migrated SQLite DB.
func newSchedulerStores(t *testing.T) (*connections.Store, *settings.Store, *Store) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	sec, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return connections.New(sqlDB, sec), settings.New(sqlDB), New(sqlDB)
}

func mustUpsert(t *testing.T, connStore *connections.Store, service, url string) {
	t.Helper()
	if err := connStore.Upsert(context.Background(), service, url, "key"); err != nil {
		t.Fatalf("upserting %s: %v", service, err)
	}
}

func mustSet(t *testing.T, settingsStore *settings.Store, key, val string) {
	t.Helper()
	if err := settingsStore.Set(context.Background(), key, val); err != nil {
		t.Fatalf("setting %s=%s: %v", key, val, err)
	}
}

// overrideFixedURLs points the fixed-URL package vars the precompute clients are
// built from (tpdbrest.DefaultBaseURL, stashbox.StashDBURL) at the fake servers
// for the test's duration — the precompute path uses these constants, not
// Connection.URL, exactly like the live handlers do. Restored on cleanup.
func overrideFixedURLs(t *testing.T, tpdbURL, stashURL string) {
	t.Helper()
	prevT := tpdbrest.DefaultBaseURL
	prevS := stashbox.StashDBURL
	tpdbrest.DefaultBaseURL = tpdbURL
	stashbox.StashDBURL = stashURL
	t.Cleanup(func() {
		tpdbrest.DefaultBaseURL = prevT
		stashbox.StashDBURL = prevS
	})
}

// fakeTPDBServer serves the three TPDB REST endpoints the precompute cycle
// touches: /performers (BrowsePerformers), /sites (BrowseSites), and
// /sites/{id}/scenes (ScenesBySite). If failPerformersPage != 0, that one
// /performers page 500s (to exercise per-page fault isolation).
func fakeTPDBServer(t *testing.T, failPerformersPage int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/scenes"):
			// A site's scene-count probe — return one scene so the site survives
			// FilterZeroSceneSites.
			w.Write([]byte(`{"data":[{"_id":"sc","title":"S"}]}`))
		case path == "/sites":
			// One real site on rawPage 1, empty afterwards so BrowseSites' walk
			// terminates at the true catalog end.
			if r.URL.Query().Get("page") == "1" {
				w.Write([]byte(`{"data":[{"uuid":"site1","name":"Vixen"}],"meta":{"current_page":1}}`))
				return
			}
			w.Write([]byte(`{"data":[]}`))
		case strings.HasPrefix(path, "/performers"):
			p, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if failPerformersPage != 0 && p == failPerformersPage {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			// Echo current_page == requested so BrowsePerformers treats it as a
			// real (non-clamped) page.
			fmt.Fprintf(w, `{"data":[{"_id":"tp%d","name":"Perf %d","image":"http://cdn/p%d.jpg"}],"meta":{"current_page":%d}}`, p, p, p, p)
		default:
			t.Errorf("unexpected TPDB path %q", path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeStashServer answers the two stash-box GraphQL queries the precompute cycle
// issues, discriminated by the operation name in the POST body.
func fakeStashServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "queryStudios") {
			w.Write([]byte(`{"data":{"queryStudios":{"studios":[{"id":"ss1","name":"Vixen","images":[]}]}}}`))
			return
		}
		w.Write([]byte(`{"data":{"queryPerformers":{"performers":[{"id":"sp1","name":"Stash Perf","images":[]}]}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLoadInterval(t *testing.T) {
	_, settingsStore, _ := newSchedulerStores(t)
	ctx := context.Background()

	// Genuinely unset → the 24h on-by-default value (NOT off).
	if got := LoadInterval(ctx, settingsStore); got != defaultIntervalHours*time.Hour {
		t.Errorf("unset key: got %v, want %v", got, defaultIntervalHours*time.Hour)
	}
	// Explicit "0" → off.
	mustSet(t, settingsStore, IntervalSettingKey, "0")
	if got := LoadInterval(ctx, settingsStore); got != 0 {
		t.Errorf(`explicit "0": got %v, want 0 (off)`, got)
	}
	// Unparseable value → off (degrade safely, never a guessed default).
	mustSet(t, settingsStore, IntervalSettingKey, "not-a-number")
	if got := LoadInterval(ctx, settingsStore); got != 0 {
		t.Errorf("garbage value: got %v, want 0", got)
	}
	// Negative → off.
	mustSet(t, settingsStore, IntervalSettingKey, "-5")
	if got := LoadInterval(ctx, settingsStore); got != 0 {
		t.Errorf("negative value: got %v, want 0", got)
	}
	// A real positive value parses (seconds).
	mustSet(t, settingsStore, IntervalSettingKey, "3600")
	if got := LoadInterval(ctx, settingsStore); got != time.Hour {
		t.Errorf("3600s: got %v, want %v", got, time.Hour)
	}
}

func TestLoadImageCacheMaxBytes(t *testing.T) {
	_, settingsStore, _ := newSchedulerStores(t)
	ctx := context.Background()

	// Genuinely unset → the 512 MB safe default (never "no cap").
	if got := LoadImageCacheMaxBytes(ctx, settingsStore); got != defaultImageCacheMaxBytes {
		t.Errorf("unset key: got %d, want %d", got, defaultImageCacheMaxBytes)
	}
	// A real positive integer overrides.
	mustSet(t, settingsStore, ImageCacheMaxBytesSettingKey, "1048576")
	if got := LoadImageCacheMaxBytes(ctx, settingsStore); got != 1048576 {
		t.Errorf("1048576: got %d, want 1048576", got)
	}
	// Malformed → safe default (NOT off — a cap must never degrade to unbounded).
	mustSet(t, settingsStore, ImageCacheMaxBytesSettingKey, "not-a-number")
	if got := LoadImageCacheMaxBytes(ctx, settingsStore); got != defaultImageCacheMaxBytes {
		t.Errorf("garbage value: got %d, want %d", got, defaultImageCacheMaxBytes)
	}
	// Zero/negative → safe default (a 0/negative cap would be nonsensical).
	for _, v := range []string{"0", "-5"} {
		mustSet(t, settingsStore, ImageCacheMaxBytesSettingKey, v)
		if got := LoadImageCacheMaxBytes(ctx, settingsStore); got != defaultImageCacheMaxBytes {
			t.Errorf("%q: got %d, want %d (safe default)", v, got, defaultImageCacheMaxBytes)
		}
	}
}

func TestRunCycle_WritesPrecomputePagesPerRowType(t *testing.T) {
	connStore, _, store := newSchedulerStores(t)
	ctx := context.Background()

	tpdb := fakeTPDBServer(t, 0)
	stash := fakeStashServer(t)
	overrideFixedURLs(t, tpdb.URL, stash.URL)
	mustUpsert(t, connStore, "tpdb", tpdb.URL)
	mustUpsert(t, connStore, "stashdb", stash.URL)

	runCycle(ctx, &http.Client{}, connStore, store, nil, 0)

	for _, tc := range []struct {
		rowType string
		pages   int
	}{{"performers", PrecomputePerformerPages}, {"studios", PrecomputeStudioPages}} {
		for page := 1; page <= tc.pages; page++ {
			_, found, err := store.Get(ctx, tc.rowType, page, cachePerPage)
			if err != nil {
				t.Fatalf("Get(%s,%d): %v", tc.rowType, page, err)
			}
			if !found {
				t.Errorf("expected %s page %d to be cached, got a miss", tc.rowType, page)
			}
		}
		// Nothing beyond the per-entity cap is precomputed.
		if _, found, _ := store.Get(ctx, tc.rowType, tc.pages+1, cachePerPage); found {
			t.Errorf("expected %s page %d NOT cached (beyond its per-entity cap), got a hit", tc.rowType, tc.pages+1)
		}
	}

	// Sanity: the stored performers page-1 payload decodes to real merged cards.
	got, _, _ := store.Get(ctx, "performers", 1, cachePerPage)
	var cards []apidto.MergedPerformerCard
	if err := json.Unmarshal(got.Items, &cards); err != nil {
		t.Fatalf("decoding cached performers page 1: %v", err)
	}
	if len(cards) == 0 {
		t.Errorf("expected the cached performers page 1 to contain merged cards, got empty")
	}
}

func TestRunCycle_PerPageFaultIsolated(t *testing.T) {
	connStore, _, store := newSchedulerStores(t)
	ctx := context.Background()

	tpdb := fakeTPDBServer(t, 2) // /performers page 2 → 500
	stash := fakeStashServer(t)
	overrideFixedURLs(t, tpdb.URL, stash.URL)
	mustUpsert(t, connStore, "tpdb", tpdb.URL)
	mustUpsert(t, connStore, "stashdb", stash.URL)

	runCycle(ctx, &http.Client{}, connStore, store, nil, 0)

	// The failed page is skipped, not cached...
	if _, found, _ := store.Get(ctx, "performers", 2, cachePerPage); found {
		t.Errorf("performers page 2 fetch failed — it must NOT be cached this cycle")
	}
	// ...but the OTHER performers pages still are (the cycle never aborts wholesale).
	for _, page := range []int{1, 3} {
		if _, found, _ := store.Get(ctx, "performers", page, cachePerPage); !found {
			t.Errorf("performers page %d must still be cached despite page 2's failure", page)
		}
	}
	// Studios are an independent pipeline — unaffected by the performers fault.
	for page := 1; page <= PrecomputeStudioPages; page++ {
		if _, found, _ := store.Get(ctx, "studios", page, cachePerPage); !found {
			t.Errorf("studios page %d must be cached (independent of the performers failure)", page)
		}
	}
}

// TestPrecomputeStudios_ScenesBySiteBoundedConcurrency is the AC8 regression
// guard. It instruments a fake TPDB whose /sites/{id}/scenes endpoint (the
// ScenesBySite call FilterZeroSceneSites fans out) records the peak number of
// concurrently in-flight calls across a full precomputeStudios cycle
// (PrecomputeStudioPages). The fixture returns a FULL page (20 sites) for raw
// pages 1..3 and empty afterward, so each of those pages' FilterZeroSceneSites
// genuinely fans out ScenesBySite bounded to 5 (later pages have no sites, hence
// no fan-out). Because pages are precomputed SEQUENTIALLY, the peak across the
// whole cycle must stay ≤5. If the page loop were ever changed to run pages
// concurrently, the peak would exceed 5 and this test fails. The peak-≥2 floor
// guards against a vacuous pass (a fixture where the fan-out never overlaps could
// not catch a concurrency regression).
func TestPrecomputeStudios_ScenesBySiteBoundedConcurrency(t *testing.T) {
	connStore, _, store := newSchedulerStores(t)
	ctx := context.Background()

	var mu sync.Mutex
	var cur, peak int
	sites := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/scenes") {
			mu.Lock()
			cur++
			if cur > peak {
				peak = cur
			}
			mu.Unlock()
			// Hold the call open long enough that concurrent in-flight calls
			// genuinely overlap within the test's window.
			time.Sleep(15 * time.Millisecond)
			mu.Lock()
			cur--
			mu.Unlock()
			w.Write([]byte(`{"data":[{"_id":"sc","title":"S"}]}`))
			return
		}
		if r.URL.Path == "/sites" {
			n, _ := strconv.Atoi(r.URL.Query().Get("page"))
			// A full page of 20 sites for each of the 3 raw pages the walk fetches
			// to satisfy pages 1..3; empty afterwards so the walk terminates.
			if n < 1 || n > 3 {
				w.Write([]byte(`{"data":[]}`))
				return
			}
			var b strings.Builder
			b.WriteString(`{"data":[`)
			for i := 0; i < 20; i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, `{"uuid":"r%ds%d","name":"Site %d-%d"}`, n, i, n, i)
			}
			b.WriteString(`]}`)
			w.Write([]byte(b.String()))
			return
		}
		t.Errorf("unexpected TPDB path %q", r.URL.Path)
	}))
	t.Cleanup(sites.Close)

	overrideFixedURLs(t, sites.URL, "http://stashdb.unused.invalid")
	mustUpsert(t, connStore, "tpdb", sites.URL)
	// No stashdb connection is configured → studios precompute runs TPDB-only,
	// isolating the ScenesBySite fan-out under test.

	if err := precomputeStudios(ctx, &http.Client{}, connStore, store, nil); err != nil {
		t.Fatalf("precomputeStudios: %v", err)
	}

	mu.Lock()
	gotPeak := peak
	mu.Unlock()
	if gotPeak > 5 {
		t.Fatalf("peak concurrent ScenesBySite = %d, exceeds the ≤5 bound (AC8) — did the sequential page loop become concurrent?", gotPeak)
	}
	if gotPeak < 2 {
		t.Fatalf("peak concurrent ScenesBySite = %d — the fan-out never overlapped, so this test can't catch a concurrency regression; strengthen the fixture", gotPeak)
	}
}

// TestPackageImportsNoProwlarr is the runtime structural counterpart to the AC5
// grep: it parses this package's AND internal/adultmerge's non-test source (the
// sibling package the precompute path fetches/merges through, per the
// import-cycle fix — see adultmerge's package doc) and fails if any file in
// either imports a prowlarr package. The precompute cycle is TPDB/StashDB-only,
// and this guard fails to pass the moment someone adds a prowlarr import to
// either package — scoping it to only adultmergecache would miss a prowlarr
// import landing in adultmerge, where the actual TPDB/StashDB fetch code lives.
func TestPackageImportsNoProwlarr(t *testing.T) {
	for _, dir := range []string{".", "../adultmerge"} {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing package source in %s: %v", dir, err)
		}
		for _, pkg := range pkgs {
			for name, file := range pkg.Files {
				for _, imp := range file.Imports {
					if strings.Contains(imp.Path.Value, "prowlarr") {
						t.Errorf("%s imports %s — %s must be TPDB/StashDB-only, never Prowlarr (AC5)", filepath.Base(name), imp.Path.Value, dir)
					}
				}
			}
		}
	}
}
