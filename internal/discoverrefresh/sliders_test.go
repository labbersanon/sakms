package discoverrefresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/discoversliders"
	"github.com/labbersanon/sakms/internal/tmdb"
)

// tmdbFixture serves TMDB's list shape for the three endpoints a slider refresh
// can reach in these tests, with ids that identify both the MEDIA TYPE and the
// PAGE they came from — that is what makes an item-for-item comparison between
// the cached slice and the live call meaningful rather than a length check.
//
// pageItems is how many results each page carries (20, TMDB's real width) or 0
// to serve an empty catalog. tvPages, when positive, caps how deep the TV
// catalog goes — that is how a mixed slider whose two catalogs have UNEQUAL
// depth is simulated.
type tmdbFixture struct {
	pageItems int
	tvPages   int
}

func (f tmdbFixture) results(base, page int) string {
	out := make([]string, 0, f.pageItems)
	for i := range f.pageItems {
		id := base + (page-1)*100 + i
		out = append(out, fmt.Sprintf(
			`{"id":%d,"title":"m%d","name":"t%d","release_date":"2020-01-01","first_air_date":"2020-01-01"}`,
			id, id, id))
	}
	body := "["
	for i, r := range out {
		if i > 0 {
			body += ","
		}
		body += r
	}
	return body + "]"
}

// page defaults to 1 because tmdb.pageQuery OMITS the param entirely for page
// <= 1 — a fixture that read a missing param as 0 would generate page-1 ids
// from the wrong base and every comparison here would be consistently wrong but
// green.
func fixturePage(r *http.Request) int {
	p, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

func (f tmdbFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, base, page int) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"results":%s}`, f.results(base, page))
	}
	// Distinct bases per endpoint, so a concatenated mixed page is visibly
	// movies-then-TV and a studio page is visibly neither.
	mux.HandleFunc("/trending/movie/week", func(w http.ResponseWriter, r *http.Request) { write(w, 1000, fixturePage(r)) })
	mux.HandleFunc("/trending/tv/week", func(w http.ResponseWriter, r *http.Request) { write(w, 2000, fixturePage(r)) })
	mux.HandleFunc("/discover/movie", func(w http.ResponseWriter, r *http.Request) { write(w, 3000, fixturePage(r)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// sliderTestEnv wires a slider store and a cache store over ONE database, plus
// a TMDB client pointed at the fixture. BypassCache is set for the same reason
// the production client sets it — and it also keeps these tests clear of
// internal/tmdb's package-level LRU entirely.
func sliderTestEnv(t *testing.T, f tmdbFixture) (Deps, *tmdb.Client, *discoversliders.Store) {
	t.Helper()
	sqlDB := dbtest.New(t)

	srv := f.server(t)
	client := tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key", BypassCache: true}, srv.Client())
	slidersStore := discoversliders.New(sqlDB)
	return Deps{SlidersStore: slidersStore, Cache: NewStore(sqlDB)}, client, slidersStore
}

// decodeItems turns a cached slice back into tmdb.Items so it can be compared
// against a live ResolveSlider result field for field.
func decodeItems(t *testing.T, raw []json.RawMessage) []tmdb.Item {
	t.Helper()
	out := make([]tmdb.Item, 0, len(raw))
	for i, body := range raw {
		var it tmdb.Item
		if err := json.Unmarshal(body, &it); err != nil {
			t.Fatalf("decoding cached item %d (%s): %v", i, body, err)
		}
		out = append(out, it)
	}
	return out
}

func assertSamePage(t *testing.T, label string, cached []tmdb.Item, live []tmdb.Item) {
	t.Helper()
	if len(cached) != len(live) {
		t.Fatalf("%s: cached %d items, live returned %d", label, len(cached), len(live))
	}
	for i := range live {
		if cached[i] != live[i] {
			t.Errorf("%s: item %d = %+v, live has %+v", label, i, cached[i], live[i])
		}
	}
}

// T-5b case (a) — a mixed-target FIXED-FEED slider has a logical page size of
// 40, because fetchFixedFeed concatenates a 20-item movie page with a 20-item
// TV page.
func TestRefreshSliders_MixedTrending_PageSize40(t *testing.T) {
	ctx := context.Background()
	d, client, slidersStore := sliderTestEnv(t, tmdbFixture{pageItems: 20})

	sl, err := slidersStore.Create(ctx, "Trending Everything", discoversliders.FilterTrending, "", discoversliders.TargetMixed, true)
	if err != nil {
		t.Fatalf("creating slider: %v", err)
	}
	if err := refreshSliders(ctx, d, client); err != nil {
		t.Fatalf("refreshSliders: %v", err)
	}

	entry, err := d.Cache.Get(ctx, sliderSource, strconv.Itoa(sl.ID))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// The tightest single assertion: the stored page size IS what one live call
	// returns. A target-only predicate still says 40 here, so this passes — case
	// (b) is what catches that.
	live1, err := ResolveSlider(ctx, client, *sl, 1)
	if err != nil {
		t.Fatalf("live page 1: %v", err)
	}
	if entry.PageSize != len(live1) {
		t.Errorf("PageSize = %d, want %d (one live page)", entry.PageSize, len(live1))
	}
	if entry.PageSize != 40 {
		t.Errorf("PageSize = %d, want 40 for a mixed fixed-feed slider", entry.PageSize)
	}

	live2, err := ResolveSlider(ctx, client, *sl, 2)
	if err != nil {
		t.Fatalf("live page 2: %v", err)
	}
	cached2, hit, _ := entry.Slice(2)
	if !hit {
		t.Fatal("page 2 should be inside the cached window")
	}
	assertSamePage(t, "mixed trending page 2", decodeItems(t, cached2), live2)
}

// T-5b case (b) — THE REGRESSION NET. A mixed-target STUDIO slider has a
// logical page size of 20, not 40: ResolveSlider degrades it to the movie
// catalog alone rather than concatenating. Case (a) passes green against a
// target-only predicate; only this case fails, and it fails on page 1.
func TestRefreshSliders_MixedStudio_PageSize20(t *testing.T) {
	ctx := context.Background()
	d, client, slidersStore := sliderTestEnv(t, tmdbFixture{pageItems: 20})

	sl, err := slidersStore.Create(ctx, "A24, Everything", discoversliders.FilterStudio, "41077", discoversliders.TargetMixed, true)
	if err != nil {
		t.Fatalf("creating slider: %v", err)
	}
	if err := refreshSliders(ctx, d, client); err != nil {
		t.Fatalf("refreshSliders: %v", err)
	}

	entry, err := d.Cache.Get(ctx, sliderSource, strconv.Itoa(sl.ID))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	live1, err := ResolveSlider(ctx, client, *sl, 1)
	if err != nil {
		t.Fatalf("live page 1: %v", err)
	}
	if entry.PageSize != len(live1) {
		t.Errorf("PageSize = %d, want %d (one live page)", entry.PageSize, len(live1))
	}
	if entry.PageSize != 20 {
		t.Errorf("PageSize = %d, want 20 — a mixed studio slider degrades to one catalog", entry.PageSize)
	}

	cached1, hit, _ := entry.Slice(1)
	if !hit {
		t.Fatal("page 1 should be inside the cached window")
	}
	assertSamePage(t, "mixed studio page 1", decodeItems(t, cached1), live1)

	// Page 2 as well: a wrong page size corrupts every page after the first, so
	// asserting only one page would leave half the failure mode uncovered.
	live2, err := ResolveSlider(ctx, client, *sl, 2)
	if err != nil {
		t.Fatalf("live page 2: %v", err)
	}
	cached2, hit, _ := entry.Slice(2)
	if !hit {
		t.Fatal("page 2 should be inside the cached window")
	}
	assertSamePage(t, "mixed studio page 2", decodeItems(t, cached2), live2)
}

// T-12f (slider half) — a zero-item slider accumulation writes NO ROW and marks
// NO FAILURE, so the read path keeps falling through live and keeps reproducing
// today's `null`. This rule is source-specific: trakt and stashbox DO cache an
// empty list (their halves of T-12f live with those refreshers).
func TestRefreshSliders_EmptyResultWritesNothing(t *testing.T) {
	ctx := context.Background()
	d, client, slidersStore := sliderTestEnv(t, tmdbFixture{pageItems: 0})

	sl, err := slidersStore.Create(ctx, "Empty Slider", discoversliders.FilterTrending, "", discoversliders.TargetMixed, true)
	if err != nil {
		t.Fatalf("creating slider: %v", err)
	}
	if err := refreshSliders(ctx, d, client); err != nil {
		t.Fatalf("refreshSliders: %v", err)
	}

	// No row at all — not an empty payload, and not a row carrying a last_error.
	// Both of those would stop the read path falling through.
	if _, err := d.Cache.Get(ctx, sliderSource, strconv.Itoa(sl.ID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after an empty accumulation = %v, want ErrNotFound (no row written)", err)
	}
}

// A misconfigured slider is marked failed and left uncached — the read path
// still falls through live and still returns the 400 that tells the operator to
// edit it, rather than serving a frozen wrong answer.
func TestRefreshSliders_MisconfiguredSliderIsNotCached(t *testing.T) {
	ctx := context.Background()
	d, client, slidersStore := sliderTestEnv(t, tmdbFixture{pageItems: 20})

	sl, err := slidersStore.Create(ctx, "Studio On TV", discoversliders.FilterStudio, "41077", discoversliders.TargetTV, true)
	if err != nil {
		t.Fatalf("creating slider: %v", err)
	}
	if err := refreshSliders(ctx, d, client); err != nil {
		t.Fatalf("refreshSliders: %v", err)
	}

	if _, err := d.Cache.Get(ctx, sliderSource, strconv.Itoa(sl.ID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get for a misconfigured slider = %v, want ErrNotFound", err)
	}
	if _, err := ResolveSlider(ctx, client, *sl, 1); !errors.Is(err, ErrSliderMisconfigured) {
		t.Fatalf("ResolveSlider error = %v, want ErrSliderMisconfigured", err)
	}
}

// A disabled slider is never refreshed, and refreshSliders' orphan sweep
// removes any row it left behind when it was still enabled.
func TestRefreshSliders_DisabledSliderIsSweptNotRefreshed(t *testing.T) {
	ctx := context.Background()
	d, client, slidersStore := sliderTestEnv(t, tmdbFixture{pageItems: 20})

	sl, err := slidersStore.Create(ctx, "Trending Everything", discoversliders.FilterTrending, "", discoversliders.TargetMixed, true)
	if err != nil {
		t.Fatalf("creating slider: %v", err)
	}
	if err := refreshSliders(ctx, d, client); err != nil {
		t.Fatalf("refreshSliders: %v", err)
	}
	if _, err := d.Cache.Get(ctx, sliderSource, strconv.Itoa(sl.ID)); err != nil {
		t.Fatalf("Get while enabled: %v", err)
	}

	if _, err := slidersStore.Update(ctx, sl.ID, sl.Title, sl.FilterType, sl.FilterValue, sl.Target, false); err != nil {
		t.Fatalf("disabling slider: %v", err)
	}
	if err := refreshSliders(ctx, d, client); err != nil {
		t.Fatalf("refreshSliders after disable: %v", err)
	}
	if _, err := d.Cache.Get(ctx, sliderSource, strconv.Itoa(sl.ID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after disable = %v, want ErrNotFound (orphan swept)", err)
	}
}
