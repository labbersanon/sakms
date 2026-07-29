package adultmergecache

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/imageproxy"
	"github.com/labbersanon/sakms/internal/secrets"
)

// newWarmStores builds a connections store, a cache Store, and a durable image
// store all over one fresh migrated SQLite DB (the durable store needs migration
// 0046's adult_image_cache table, which db.Open applies). It returns the durable
// store too so a test can pre-seed / inspect it.
func newWarmStores(t *testing.T) (*connections.Store, *Store, *imageproxy.DurableStore) {
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
	durable, err := imageproxy.NewDurableStore(sqlDB, filepath.Join(t.TempDir(), "imagecache"))
	if err != nil {
		t.Fatalf("building durable store: %v", err)
	}
	return connections.New(sqlDB, sec), New(sqlDB), durable
}

// fakeTPDBPerformerImages serves a /performers browse whose page 1 carries the
// given image URLs (one performer each) and whose later pages are empty, plus
// the /sites + /sites/{id}/scenes endpoints the studios pipeline would touch (so
// the same fake is reusable). Only page 1 has cards, so image warming is
// exercised on exactly the URLs under test.
func fakeTPDBPerformerImages(t *testing.T, page1Images []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/scenes"):
			w.Write([]byte(`{"data":[{"_id":"sc","title":"S"}]}`))
		case path == "/sites":
			w.Write([]byte(`{"data":[]}`)) // no studios in these performer-focused tests
		case strings.HasPrefix(path, "/performers"):
			p, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if p != 1 {
				fmt.Fprintf(w, `{"data":[],"meta":{"current_page":%d}}`, p)
				return
			}
			var b strings.Builder
			b.WriteString(`{"data":[`)
			for i, img := range page1Images {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, `{"_id":"tp%d","name":"Perf %d","image":%q}`, i, i, img)
			}
			b.WriteString(`],"meta":{"current_page":1}}`)
			w.Write([]byte(b.String()))
		default:
			t.Errorf("unexpected TPDB path %q", path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPrecompute_NilProxyMetadataOnly is the nil-tolerance guard (AC: passing a
// nil proxy reproduces exactly today's metadata-only behavior). Both precompute
// functions must cache their pages without panicking when no proxy is wired.
func TestPrecompute_NilProxyMetadataOnly(t *testing.T) {
	connStore, store, _ := newWarmStores(t)
	ctx := context.Background()

	tpdb := fakeTPDBServer(t, 0)
	stash := fakeStashServer(t)
	overrideFixedURLs(t, tpdb.URL, stash.URL)
	mustUpsert(t, connStore, "tpdb", tpdb.URL)
	mustUpsert(t, connStore, "stashdb", stash.URL)

	if err := precomputePerformers(ctx, &http.Client{}, connStore, store, nil); err != nil {
		t.Fatalf("precomputePerformers(nil proxy): %v", err)
	}
	if err := precomputeStudios(ctx, &http.Client{}, connStore, store, nil); err != nil {
		t.Fatalf("precomputeStudios(nil proxy): %v", err)
	}

	for _, tc := range []struct {
		rowType string
		pages   int
	}{{"performers", PrecomputePerformerPages}, {"studios", PrecomputeStudioPages}} {
		for page := 1; page <= tc.pages; page++ {
			if _, found, err := store.Get(ctx, tc.rowType, page, cachePerPage); err != nil || !found {
				t.Errorf("nil-proxy metadata-only: %s page %d not cached (found=%v err=%v)", tc.rowType, page, found, err)
			}
		}
	}
}

// TestPrecompute_ImageWarmSingleFailureIsolated proves per-item failure
// isolation: within one page, one card's image warm fails while another's
// succeeds, and the failure neither fails the page, the entity type, nor the
// cycle — the metadata is still cached and the succeeding card's image is still
// persisted.
//
// The failure is simulated by an SSRF-rejected loopback URL (127.0.0.1) rather
// than a 404-returning CDN: imageproxy's test-only allowPrivateHosts escape
// hatch is unexported, so a package-external test cannot point FetchAndPersist
// at a 127.0.0.1 httptest CDN and have it pass validation. The isolation
// behavior is identical either way — FetchAndPersist returns an error, and
// precompute logs+skips+counts it the same — so the SSRF rejection is a faithful
// stand-in for a CDN failure at this layer. (The full httptest-CDN 404 path is
// US-9's integration story.) The success is a pre-seeded, already-persisted
// canonical public URL, which FetchAndPersist Has-skips (returns nil, zero
// network) — so this test makes no real outbound call at all.
func TestPrecompute_ImageWarmSingleFailureIsolated(t *testing.T) {
	connStore, store, durable := newWarmStores(t)
	ctx := context.Background()

	// goodURL: a canonical public IP-literal https URL (no DNS, not blocked by
	// isBlockedIP), pre-seeded so FetchAndPersist Has-skips it (success, no fetch).
	const goodURL = "https://203.0.113.10/good.jpg"
	// badURL: a loopback URL FetchAndPersist rejects via the SSRF guard (failure).
	const badURL = "https://127.0.0.1/bad.jpg"
	if err := durable.Put(ctx, goodURL, "image/jpeg", []byte("seed-bytes")); err != nil {
		t.Fatalf("pre-seeding goodURL: %v", err)
	}

	tpdb := fakeTPDBPerformerImages(t, []string{goodURL, badURL})
	overrideFixedURLs(t, tpdb.URL, "http://stashdb.unused.invalid")
	mustUpsert(t, connStore, "tpdb", tpdb.URL) // TPDB-only: no stashdb wired

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	proxy := imageproxy.NewWithDurable(&http.Client{}, durable)
	if err := precomputePerformers(ctx, &http.Client{}, connStore, store, proxy); err != nil {
		t.Fatalf("precomputePerformers must not propagate a single card's warm failure: %v", err)
	}

	// badURL's warm actually ran and errored (proves this isn't a vacuous pass
	// where warming never fired) — only a real FetchAndPersist failure logs this.
	// With just 2 URLs the breaker never trips, so no abort line races in.
	if !strings.Contains(buf.String(), "image warm "+badURL) {
		t.Errorf("expected badURL's warm failure to be logged (proves warming ran on it); got:\n%s", buf.String())
	}
	// The page's metadata is still cached despite badURL's warm failure.
	if _, found, err := store.Get(ctx, "performers", 1, cachePerPage); err != nil || !found {
		t.Fatalf("performers page 1 metadata must be cached despite a warm failure (found=%v err=%v)", found, err)
	}
	// The succeeding card's image is retained; the failing card's is not.
	if has, err := durable.Has(ctx, goodURL); err != nil || !has {
		t.Errorf("goodURL image must remain persisted (has=%v err=%v)", has, err)
	}
	if has, err := durable.Has(ctx, badURL); err != nil || has {
		t.Errorf("badURL image must NOT be persisted after its warm failure (has=%v err=%v)", has, err)
	}
}

// TestPrecompute_ImageWarmCircuitBreaker proves the per-cycle circuit breaker:
// after imageWarmFailureThreshold (20) consecutive FetchAndPersist failures, image
// warming aborts for the rest of the cycle and logs the abort exactly once, while
// metadata warming is unaffected. 21 distinct loopback URLs (all SSRF-rejected,
// no network) guarantee the counter crosses the threshold with no intervening
// success to reset it.
func TestPrecompute_ImageWarmCircuitBreaker(t *testing.T) {
	connStore, store, durable := newWarmStores(t)
	ctx := context.Background()

	imgs := make([]string, 21)
	for i := range imgs {
		imgs[i] = fmt.Sprintf("https://127.0.0.1/bad%d.jpg", i)
	}
	tpdb := fakeTPDBPerformerImages(t, imgs)
	overrideFixedURLs(t, tpdb.URL, "http://stashdb.unused.invalid")
	mustUpsert(t, connStore, "tpdb", tpdb.URL)

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	proxy := imageproxy.NewWithDurable(&http.Client{}, durable)
	if err := precomputePerformers(ctx, &http.Client{}, connStore, store, proxy); err != nil {
		t.Fatalf("precomputePerformers must not fail on mass warm failure: %v", err)
	}

	if got := strings.Count(buf.String(), "image warming aborted for the rest of this cycle"); got != 1 {
		t.Errorf("expected the circuit-breaker abort to be logged exactly once, got %d", got)
	}
	// Metadata warming is unaffected by the image circuit breaker.
	if _, found, err := store.Get(ctx, "performers", 1, cachePerPage); err != nil || !found {
		t.Errorf("performers page 1 metadata must still be cached (breaker only stops image warming) (found=%v err=%v)", found, err)
	}
}

// newCycleStores builds the connections/cache/durable trio over one migrated DB
// like newWarmStores, but ALSO returns the durable store's on-disk image dir so a
// runCycle-level test can seed/observe write-side drift (stray temps) and disk
// usage directly. Kept separate from newWarmStores to leave that helper's three
// existing callers untouched.
func newCycleStores(t *testing.T) (*connections.Store, *Store, *imageproxy.DurableStore, string) {
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
	imageDir := filepath.Join(t.TempDir(), "imagecache")
	durable, err := imageproxy.NewDurableStore(sqlDB, imageDir)
	if err != nil {
		t.Fatalf("building durable store: %v", err)
	}
	return connections.New(sqlDB, sec), New(sqlDB), durable, imageDir
}

// TestRunCycle_ReconcilesEveryCycle proves the every-cycle Reconcile wiring (US-5
// AC): a stray *.tmp-* file seeded before EACH runCycle is reconciled away by that
// cycle — not just the first — because runCycle calls MaintainDurable (which
// Reconciles) at the start of every cycle, and Run drives runCycle on both the
// boot poll and every subsequent tick.
func TestRunCycle_ReconcilesEveryCycle(t *testing.T) {
	connStore, store, durable, imageDir := newCycleStores(t)
	ctx := context.Background()

	tpdb := fakeTPDBServer(t, 0)
	stash := fakeStashServer(t)
	overrideFixedURLs(t, tpdb.URL, stash.URL)
	mustUpsert(t, connStore, "tpdb", tpdb.URL)
	mustUpsert(t, connStore, "stashdb", stash.URL)
	proxy := imageproxy.NewWithDurable(&http.Client{}, durable)

	countStrayTemps := func() int {
		n := 0
		_ = filepath.WalkDir(imageDir, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.Contains(d.Name(), ".tmp-") {
				n++
			}
			return nil
		})
		return n
	}
	seedStrayTemp := func() {
		shard := filepath.Join(imageDir, "ff")
		if err := os.MkdirAll(shard, 0o755); err != nil {
			t.Fatalf("mkdir stray shard: %v", err)
		}
		if err := os.WriteFile(filepath.Join(shard, strings.Repeat("f", 64)+".tmp-stray"), []byte("partial"), 0o644); err != nil {
			t.Fatalf("seeding stray temp: %v", err)
		}
	}

	// Cycle 1 (the boot-poll equivalent): reconcile clears the stray temp.
	seedStrayTemp()
	runCycle(ctx, &http.Client{}, connStore, store, proxy, defaultImageCacheMaxBytes)
	if got := countStrayTemps(); got != 0 {
		t.Fatalf("cycle 1 must reconcile away the seeded stray temp, still found %d", got)
	}
	// Cycle 2 (a subsequent ticker tick): reconcile runs AGAIN — the proof this is
	// every-cycle, not a one-shot boot call.
	seedStrayTemp()
	runCycle(ctx, &http.Client{}, connStore, store, proxy, defaultImageCacheMaxBytes)
	if got := countStrayTemps(); got != 0 {
		t.Fatalf("cycle 2 (tick) must ALSO reconcile away the stray temp, still found %d", got)
	}
}

// fakeCDNTransport is a real net/http.RoundTripper that lets a test's
// imageproxy.Proxy make a genuine network round-trip to an httptest CDN
// server, while the card.Image URL under test still declares a public,
// non-blocked host.
//
// imageproxy's SSRF guard (isBlockedIP) requires https + a host that does not
// resolve to loopback/RFC1918/link-local/multicast/unspecified. A bare
// httptest.Server listens on 127.0.0.1, which the guard always rejects, and
// there is no cross-package way to reach imageproxy.Proxy's unexported
// allowPrivateHosts test escape hatch from this package (it's only settable
// from imageproxy's own _test.go files — see TestPrecompute_ImageWarmSingleFailureIsolated's
// doc comment above for the same constraint). So card image URLs here use
// TEST-NET-3 (203.0.113.0/24) IP literals: reserved for documentation and
// genuinely non-routable, but NOT in any range isBlockedIP checks — validate()
// accepts them as a normal "public" host exactly like a real CDN IP. This
// transport then redirects the actual TCP dial to the real httptest server, so
// the full production SSRF-then-fetch code path (validate -> fetchUpstream ->
// status/content-type checks) runs for real against a real CDN response,
// rather than standing in an SSRF rejection for "a failure" as the
// SingleFailureIsolated test above does.
type fakeCDNTransport struct {
	mu     sync.Mutex
	counts map[string]int // request URL string -> times actually round-tripped
	real   string         // e.g. "127.0.0.1:54321" -- the httptest CDN's real address
}

func (t *fakeCDNTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.counts[req.URL.String()]++
	t.mu.Unlock()
	dial := req.Clone(req.Context())
	dial.URL.Scheme = "http"
	dial.URL.Host = t.real
	dial.Host = ""
	return http.DefaultTransport.RoundTrip(dial)
}

// requestCount returns how many times rawURL was actually round-tripped to
// the real CDN — used to assert incremental skip (a second cycle must not
// increase this).
func (t *fakeCDNTransport) requestCount(rawURL string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts[rawURL]
}

// newFakeCDN starts an httptest CDN server serving image/jpeg bytes for each
// path in body; any other path (e.g. a deliberately-omitted "missing" URL)
// gets a real 404 from the real server -- not a simulated one. Returns the
// transport a Proxy's http.Client should use (see fakeCDNTransport's doc).
func newFakeCDN(t *testing.T, body map[string][]byte) *fakeCDNTransport {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := body[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return &fakeCDNTransport{counts: make(map[string]int), real: strings.TrimPrefix(srv.URL, "http://")}
}

// fakeTPDBTwoPerformerPagesWithSites serves /performers pages 1 and 2 with the
// given image URLs (one performer per URL, later pages empty), plus a real
// /sites page 1 (one site) + /scenes stub so the studios pipeline in the same
// runCycle also completes normally -- this is what lets a test prove a
// performers image failure doesn't abort the rest of the cycle.
func fakeTPDBTwoPerformerPagesWithSites(t *testing.T, page1Images, page2Images []string) *httptest.Server {
	t.Helper()
	pages := map[int][]string{1: page1Images, 2: page2Images}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/scenes"):
			w.Write([]byte(`{"data":[{"_id":"sc","title":"S"}]}`))
		case path == "/sites":
			if r.URL.Query().Get("page") == "1" {
				w.Write([]byte(`{"data":[{"uuid":"site1","name":"Vixen"}],"meta":{"current_page":1}}`))
				return
			}
			w.Write([]byte(`{"data":[]}`))
		case strings.HasPrefix(path, "/performers"):
			p, _ := strconv.Atoi(r.URL.Query().Get("page"))
			imgs := pages[p]
			var b strings.Builder
			b.WriteString(`{"data":[`)
			for i, img := range imgs {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, `{"_id":"tp%d_%d","name":"Perf %d-%d","image":%q}`, p, i, p, i, img)
			}
			fmt.Fprintf(&b, `],"meta":{"current_page":%d}}`, p)
			w.Write([]byte(b.String()))
		default:
			t.Errorf("unexpected TPDB path %q", path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRunCycle_PopulatesMetadataAndDurableImages_ThenIncrementalSkip is US-9's
// headline integration test (AC #1 + #6 / plan §4 bullets 1-2): a runCycle
// against a real httptest TPDB + a real httptest CDN (see fakeCDNTransport)
// populates BOTH the metadata cache table AND the durable image store for the
// covered page; a second runCycle over the exact same fixtures then issues
// ZERO new CDN fetches for those already-warmed URLs (incremental skip). This
// is distinct from TestPrecompute_ImageWarmSingleFailureIsolated /
// TestPrecompute_ImageWarmCircuitBreaker above, which only ever run ONE cycle
// and use a pre-seeded Has-hit or an SSRF rejection as a success/failure
// stand-in -- neither proves a real CDN round-trip actually populated the
// durable store, nor that a genuine second cycle makes no new CDN calls.
func TestRunCycle_PopulatesMetadataAndDurableImages_ThenIncrementalSkip(t *testing.T) {
	connStore, store, durable, _ := newCycleStores(t)
	ctx := context.Background()

	const goodURL1 = "https://203.0.113.10/imgs/perf0.jpg"
	const goodURL2 = "https://203.0.113.10/imgs/perf1.jpg"

	transport := newFakeCDN(t, map[string][]byte{
		"/imgs/perf0.jpg": []byte("perf0-bytes"),
		"/imgs/perf1.jpg": []byte("perf1-bytes"),
	})

	tpdb := fakeTPDBPerformerImages(t, []string{goodURL1, goodURL2})
	overrideFixedURLs(t, tpdb.URL, "http://stashdb.unused.invalid")
	mustUpsert(t, connStore, "tpdb", tpdb.URL) // TPDB-only, same convention as the tests above

	proxy := imageproxy.NewWithDurable(&http.Client{Transport: transport}, durable)

	// Cycle 1 (cold): both the metadata table AND the durable image store must
	// be populated for the covered page.
	runCycle(ctx, &http.Client{}, connStore, store, proxy, defaultImageCacheMaxBytes)

	if _, found, err := store.Get(ctx, "performers", 1, cachePerPage); err != nil || !found {
		t.Fatalf("performers page 1 metadata must be cached after cycle 1 (found=%v err=%v)", found, err)
	}
	for _, u := range []string{goodURL1, goodURL2} {
		if has, err := durable.Has(ctx, u); err != nil || !has {
			t.Errorf("expected %s to be durably persisted after cycle 1 (has=%v err=%v)", u, has, err)
		}
	}
	body1, ct1, found1, err := durable.Get(ctx, goodURL1)
	if err != nil || !found1 {
		t.Fatalf("durable.Get(%s) after cycle 1: found=%v err=%v", goodURL1, found1, err)
	}
	if string(body1) != "perf0-bytes" || ct1 != "image/jpeg" {
		t.Errorf("durable.Get(%s) = body %q ct %q, want \"perf0-bytes\"/\"image/jpeg\"", goodURL1, body1, ct1)
	}
	if got := transport.requestCount(goodURL1); got != 1 {
		t.Fatalf("expected exactly 1 real CDN fetch for %s after cycle 1, got %d", goodURL1, got)
	}

	// Cycle 2 (unchanged fixtures): the durable store already holds both
	// images, so FetchAndPersist's internal Has-skip must fire -- zero NEW CDN
	// round-trips for either URL.
	runCycle(ctx, &http.Client{}, connStore, store, proxy, defaultImageCacheMaxBytes)

	for _, u := range []string{goodURL1, goodURL2} {
		if got := transport.requestCount(u); got != 1 {
			t.Errorf("expected %s to still have exactly 1 TOTAL CDN fetch after cycle 2 (incremental skip must issue zero new CDN fetches), got %d", u, got)
		}
	}
}

// TestRunCycle_ImageFetch404DoesNotFailPageEntityOrCycle is US-9's AC #3 test:
// a real httptest CDN 404 for one card's image -- NOT an SSRF-rejected
// loopback URL standing in for "a failure" (see
// TestPrecompute_ImageWarmSingleFailureIsolated's doc comment, which
// explicitly defers this exact case to US-9) -- must not fail the page
// containing it, must not abort the rest of that entity type's precompute
// (a later page's metadata+image still land), and must not abort the cycle
// (the independent studios pipeline still completes).
func TestRunCycle_ImageFetch404DoesNotFailPageEntityOrCycle(t *testing.T) {
	connStore, store, durable, _ := newCycleStores(t)
	ctx := context.Background()

	const goodURL = "https://203.0.113.20/imgs/good.jpg"
	const badURL = "https://203.0.113.20/imgs/missing.jpg" // real CDN 404, not SSRF-rejected
	const page2URL = "https://203.0.113.20/imgs/page2.jpg"

	transport := newFakeCDN(t, map[string][]byte{
		"/imgs/good.jpg":  []byte("good-bytes"),
		"/imgs/page2.jpg": []byte("page2-bytes"),
		// "/imgs/missing.jpg" deliberately absent -> a genuine 404 from the real CDN server.
	})

	tpdb := fakeTPDBTwoPerformerPagesWithSites(t, []string{goodURL, badURL}, []string{page2URL})
	overrideFixedURLs(t, tpdb.URL, "http://stashdb.unused.invalid")
	mustUpsert(t, connStore, "tpdb", tpdb.URL)

	proxy := imageproxy.NewWithDurable(&http.Client{Transport: transport}, durable)

	runCycle(ctx, &http.Client{}, connStore, store, proxy, defaultImageCacheMaxBytes)

	// The page containing the failing image is still cached -- the page itself
	// did not fail.
	if _, found, err := store.Get(ctx, "performers", 1, cachePerPage); err != nil || !found {
		t.Fatalf("performers page 1 metadata must be cached despite one image 404 (found=%v err=%v)", found, err)
	}
	// The OTHER image on the same page still persists.
	if has, err := durable.Has(ctx, goodURL); err != nil || !has {
		t.Errorf("goodURL must remain persisted after badURL's real 404 (has=%v err=%v)", has, err)
	}
	// The failing image itself is never persisted.
	if has, err := durable.Has(ctx, badURL); err != nil || has {
		t.Errorf("badURL must NOT be persisted after a real 404 (has=%v err=%v)", has, err)
	}
	// The entity type's precompute kept going past page 1's failure: page 2's
	// metadata AND its own (unrelated, healthy) image are still cached.
	if _, found, err := store.Get(ctx, "performers", 2, cachePerPage); err != nil || !found {
		t.Fatalf("performers page 2 metadata must be cached -- the entity type's precompute must not abort on page 1's image failure (found=%v err=%v)", found, err)
	}
	if has, err := durable.Has(ctx, page2URL); err != nil || !has {
		t.Errorf("page 2's image must be persisted -- the cycle must not abort on page 1's image failure (has=%v err=%v)", has, err)
	}
	// The cycle overall completed: studios' independent pipeline still ran.
	for page := 1; page <= PrecomputeStudioPages; page++ {
		if _, found, err := store.Get(ctx, "studios", page, cachePerPage); err != nil || !found {
			t.Errorf("studios page %d metadata must be cached -- the cycle must not abort on the performers image failure (found=%v err=%v)", page, found, err)
		}
	}
}

// TestPrecomputeStudios_NoWastedRawPagesPastCatalogEnd is US-9's coverage-cap
// test for studios: it confirms the studios cap (PrecomputeStudioPages=5)
// coincides with tpdbrest.BrowseSites' own catalog-end detection (an empty
// raw page), rather than wastefully walking deep raw pages that are already
// known-empty on every one of the 5 merged-page calls. The fixture has
// exactly one real site on raw page 1 and an empty raw page 2 onward, so every
// merged-page call (BrowseSites re-walks from raw page 1 each time, see that
// method's doc) should hit raw pages 1 and 2 exactly once each -- never a raw
// page beyond 2, and never anywhere close to tpdbrest's own
// maxSitePagesPerRequest=15 structural ceiling.
func TestPrecomputeStudios_NoWastedRawPagesPastCatalogEnd(t *testing.T) {
	connStore, _, store := newSchedulerStores(t)
	ctx := context.Background()

	var mu sync.Mutex
	sitesRequests := map[int]int{}

	tpdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/scenes"):
			w.Write([]byte(`{"data":[{"_id":"sc","title":"S"}]}`))
		case r.URL.Path == "/sites":
			n, _ := strconv.Atoi(r.URL.Query().Get("page"))
			mu.Lock()
			sitesRequests[n]++
			mu.Unlock()
			if n == 1 {
				w.Write([]byte(`{"data":[{"uuid":"site1","name":"Vixen"}],"meta":{"current_page":1}}`))
				return
			}
			w.Write([]byte(`{"data":[]}`))
		default:
			t.Errorf("unexpected TPDB path %q", r.URL.Path)
		}
	}))
	t.Cleanup(tpdb.Close)

	overrideFixedURLs(t, tpdb.URL, "http://stashdb.unused.invalid")
	mustUpsert(t, connStore, "tpdb", tpdb.URL)

	if err := precomputeStudios(ctx, &http.Client{}, connStore, store, nil); err != nil {
		t.Fatalf("precomputeStudios: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := sitesRequests[1]; got != PrecomputeStudioPages {
		t.Errorf("raw /sites page 1 requested %d times, want exactly %d (once per merged studios page)", got, PrecomputeStudioPages)
	}
	if got := sitesRequests[2]; got != PrecomputeStudioPages {
		t.Errorf("raw /sites page 2 (the empty, catalog-ending page) requested %d times, want exactly %d", got, PrecomputeStudioPages)
	}
	for raw := 3; raw <= 15; raw++ {
		if got := sitesRequests[raw]; got != 0 {
			t.Errorf("raw /sites page %d must NEVER be requested (the catalog ended at raw page 2) -- got %d requests; the studios cap is wasting deep pages past what's actually available", raw, got)
		}
	}
}

// TestRunCycle_EnforcesDiskCap proves the disk-cap circuit breaker is wired into
// runCycle (US-5 AC): a durable store pre-seeded OVER a low cap is pruned back
// under the cap by the cycle's MaintainDurable call, while metadata still caches
// normally (the cap only pauses image byte warming, never metadata).
func TestRunCycle_EnforcesDiskCap(t *testing.T) {
	connStore, store, durable, _ := newCycleStores(t)
	ctx := context.Background()

	// Pre-seed 5×100 = 500 bytes of durable image data (unrelated to the fixtures).
	body := make([]byte, 100)
	for i := 0; i < 5; i++ {
		if err := durable.Put(ctx, fmt.Sprintf("https://cdn.example/seed%d.jpg", i), "image/jpeg", body); err != nil {
			t.Fatalf("pre-seeding durable entry %d: %v", i, err)
		}
	}
	if total, _ := durable.TotalBytes(ctx); total != 500 {
		t.Fatalf("pre-seed usage = %d bytes, want 500", total)
	}

	tpdb := fakeTPDBServer(t, 0)
	stash := fakeStashServer(t)
	overrideFixedURLs(t, tpdb.URL, stash.URL)
	mustUpsert(t, connStore, "tpdb", tpdb.URL)
	mustUpsert(t, connStore, "stashdb", stash.URL)
	proxy := imageproxy.NewWithDurable(&http.Client{}, durable)

	const capBytes = 200
	runCycle(ctx, &http.Client{}, connStore, store, proxy, capBytes)

	total, err := durable.TotalBytes(ctx)
	if err != nil {
		t.Fatalf("TotalBytes after cycle: %v", err)
	}
	if total > capBytes {
		t.Fatalf("durable usage after a capped cycle = %d bytes, must be pruned to <= %d", total, capBytes)
	}
	if total >= 500 {
		t.Fatalf("durable usage = %d bytes — nothing was pruned, the cap never engaged", total)
	}
	// Metadata caching is completely unaffected by the disk cap.
	if _, found, err := store.Get(ctx, "performers", 1, cachePerPage); err != nil || !found {
		t.Errorf("performers page 1 metadata must still be cached under a disk cap (found=%v err=%v)", found, err)
	}
}
