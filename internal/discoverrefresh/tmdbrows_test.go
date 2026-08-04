package discoverrefresh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/labbersanon/sakms/internal/tmdb"
)

// tmdbRowsFake is a stand-in TMDB serving three real pages of Movies Trending
// where every ODD id has no US release, plus an empty page 4 (genuine end of
// data). Every other endpoint the six-target sweep touches answers with an
// empty result set, so the run is bounded and the interesting row is isolated.
//
// It counts requests per path+page, which is how the test proves no raw page is
// consumed twice — the property that makes the flat accumulated list
// duplicate-free by construction rather than by assertion.
type tmdbRowsFake struct {
	mu     sync.Mutex
	calls  map[string]int
	server *httptest.Server
}

// tmdbRowsTrendingPages is how many populated pages the fake serves before going
// empty. Deliberately below carouselCachedPages*tmdbPageSize worth of
// SURVIVORS (3 pages × 20 items, half filtered out = 30 < 60), so accumulation
// stops on the empty page rather than on the depth target — which is what makes
// exhausted true and pins the raw-page-boundary behaviour.
const tmdbRowsTrendingPages = 3

func newTMDBRowsFake(t *testing.T) *tmdbRowsFake {
	t.Helper()
	f := &tmdbRowsFake{calls: map[string]int{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/trending/movie/week", func(w http.ResponseWriter, r *http.Request) {
		page := f.record(r)
		if page > tmdbRowsTrendingPages {
			writeTMDBRowsResults(w, nil)
			return
		}
		// Ids are globally unique across pages (1-20, 21-40, 41-60), so a
		// duplicate in the stored payload is detectable by id alone.
		ids := make([]int, 0, tmdbPageSize)
		for i := range tmdbPageSize {
			ids = append(ids, (page-1)*tmdbPageSize+i+1)
		}
		writeTMDBRowsResults(w, ids)
	})
	for _, empty := range []string{"/movie/popular", "/movie/upcoming", "/trending/tv/week", "/tv/popular", "/tv/on_the_air"} {
		mux.HandleFunc(empty, func(w http.ResponseWriter, r *http.Request) {
			f.record(r)
			writeTMDBRowsResults(w, nil)
		})
	}
	// Odd ids have no US release at all; even ids have a past digital one.
	mux.HandleFunc("/movie/{id}/release_dates", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if id%2 != 0 {
			w.Write([]byte(`{"results":[]}`))
			return
		}
		w.Write([]byte(`{"results":[{"iso_3166_1":"US","release_dates":[{"type":4,"release_date":"2020-01-01T00:00:00.000Z"}]}]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected TMDB request: %s", r.URL.Path)
		http.Error(w, "unexpected path", http.StatusNotFound)
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *tmdbRowsFake) record(r *http.Request) int {
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[fmt.Sprintf("%s?page=%d", r.URL.Path, page)]++
	return page
}

func (f *tmdbRowsFake) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[key]
}

func writeTMDBRowsResults(w http.ResponseWriter, ids []int) {
	results := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		results = append(results, map[string]any{"id": id, "title": fmt.Sprintf("Movie %d", id)})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"results": results})
}

// newTMDBRowsFakeClient builds the client refreshTMDBCategories is given in
// production: pointed at the fake, with BypassCache set. The bypass is
// load-bearing here, not cosmetic — internal/tmdb's response cache is a
// process-level singleton, so without it one test's bodies leak into another's
// through a reused ephemeral port.
func newTMDBRowsFakeClient(f *tmdbRowsFake) *tmdb.Client {
	return tmdb.New(tmdb.Config{BaseURL: f.server.URL, APIKey: "test-key", BypassCache: true}, f.server.Client())
}

// T-6 — TMDB accumulate + filter.
func TestRefreshTMDBCategories_AccumulatesSurvivorsOnce(t *testing.T) {
	f := newTMDBRowsFake(t)
	s := newTestStore(t)
	ctx := context.Background()

	refreshTMDBCategories(ctx, Deps{Cache: s}, newTMDBRowsFakeClient(f))

	entry, err := s.Get(ctx, "tmdb", "movies:trending")
	if err != nil {
		t.Fatalf("Get(tmdb/movies:trending): %v", err)
	}

	// Only survivors are stored: the write path filters, so the read path makes
	// zero HasUSRelease calls (§0.4). 3 pages × 20 items, odd ids dropped = 30.
	wantSurvivors := tmdbRowsTrendingPages * tmdbPageSize / 2
	if len(entry.Payload) != wantSurvivors {
		t.Fatalf("payload holds %d items, want %d survivors", len(entry.Payload), wantSurvivors)
	}
	seen := map[int]bool{}
	for i, raw := range entry.Payload {
		var item tmdb.Item
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("payload[%d] is not one item body (%s): %v", i, raw, err)
		}
		if item.ID%2 != 0 {
			t.Errorf("payload[%d] is id %d, which has no US release and must have been filtered out", i, item.ID)
		}
		if seen[item.ID] {
			t.Errorf("payload[%d] repeats id %d — the flat list must contain no duplicates", i, item.ID)
		}
		seen[item.ID] = true
	}

	// Accumulation stopped on a RAW page boundary, and rawPages counts pages
	// consumed — not len(payload)/pageSize, which is 2 here (30/20 rounded up).
	// The fall-through past the window therefore asks for raw page 5, not 3.
	if entry.RawPages != tmdbRowsTrendingPages+1 {
		t.Errorf("rawPages = %d, want %d (three populated pages plus the empty one that ended it)", entry.RawPages, tmdbRowsTrendingPages+1)
	}
	if !entry.Exhausted {
		t.Error("exhausted = false, want true — the upstream itself returned an empty page")
	}
	if entry.PageSize != tmdbPageSize {
		t.Errorf("pageSize = %d, want %d", entry.PageSize, tmdbPageSize)
	}
	if entry.ItemCount != wantSurvivors {
		t.Errorf("itemCount = %d, want %d", entry.ItemCount, wantSurvivors)
	}
	if entry.RefreshedAt == "" || entry.LastError != "" {
		t.Errorf("refreshedAt = %q, lastError = %q — want a stamped success", entry.RefreshedAt, entry.LastError)
	}

	// Every raw page was fetched exactly once. A page fetched twice is how a
	// duplicate would enter the flat list in the first place.
	for page := 1; page <= tmdbRowsTrendingPages+1; page++ {
		key := fmt.Sprintf("/trending/movie/week?page=%d", page)
		if got := f.count(key); got != 1 {
			t.Errorf("%s fetched %d times, want exactly 1", key, got)
		}
	}
	if got := f.count(fmt.Sprintf("/trending/movie/week?page=%d", tmdbRowsTrendingPages+2)); got != 0 {
		t.Errorf("kept fetching past the empty page (%d times), want 0", got)
	}

	// The row is written ONCE — one row per (source, cache_key), never one per
	// page. This is what the migration's "DELIBERATELY NO page COLUMN" note
	// exists to protect, and the key list doubles as executable documentation
	// of exactly which six categories are cached (§0.3).
	rows, err := s.db.QueryContext(ctx, `SELECT cache_key FROM discover_row_cache WHERE source = 'tmdb' ORDER BY cache_key`)
	if err != nil {
		t.Fatalf("listing cached tmdb rows: %v", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scanning cache_key: %v", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating cached tmdb rows: %v", err)
	}
	want := "movies:popular,movies:trending,movies:upcoming,series:popular,series:trending,series:upcoming"
	if strings.Join(keys, ",") != want {
		t.Errorf("cached tmdb keys = %v, want exactly the six fixed rows [%s]", keys, want)
	}
}

// The five non-Trending targets all served empty first pages, which must cache
// as an exhausted zero-item row rather than as no row at all: source='tmdb' may
// legitimately cache an empty list (normalizeAll returns a non-nil slice), and
// caching it is what keeps the read path off the network for those rows.
func TestRefreshTMDBCategories_EmptyUpstreamCachesExhaustedRow(t *testing.T) {
	f := newTMDBRowsFake(t)
	s := newTestStore(t)
	ctx := context.Background()

	refreshTMDBCategories(ctx, Deps{Cache: s}, newTMDBRowsFakeClient(f))

	entry, err := s.Get(ctx, "tmdb", "series:popular")
	if err != nil {
		t.Fatalf("Get(tmdb/series:popular): %v", err)
	}
	if len(entry.Payload) != 0 || entry.RawPages != 1 || !entry.Exhausted {
		t.Errorf("entry = %d items/rawPages %d/exhausted %v, want 0/1/true", len(entry.Payload), entry.RawPages, entry.Exhausted)
	}
	items, hit, liveRawPage := entry.Slice(1)
	if !hit || len(items) != 0 || liveRawPage != 0 {
		t.Errorf("Slice(1) = %d items/hit %v/liveRawPage %d, want 0/true/0 — an exhausted row serves [] with zero external calls", len(items), hit, liveRawPage)
	}
}

// An upstream failure must abandon THAT KEY only, leaving any previous payload
// intact (MarkFailure is an UPDATE, never an upsert) and never writing a
// partially accumulated list.
func TestRefreshTMDBCategories_UpstreamFailureKeepsPreviousPayload(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	previous := []json.RawMessage{json.RawMessage(`{"id":7}`)}
	if err := s.Put(ctx, "tmdb", "movies:trending", previous, tmdbPageSize, 1, true); err != nil {
		t.Fatalf("seeding the previous payload: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/trending/movie/") {
			http.Error(w, "upstream is down", http.StatusBadGateway)
			return
		}
		writeTMDBRowsResults(w, nil)
	}))
	defer srv.Close()
	client := tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key", BypassCache: true}, srv.Client())

	refreshTMDBCategories(ctx, Deps{Cache: s}, client)

	entry, err := s.Get(ctx, "tmdb", "movies:trending")
	if err != nil {
		t.Fatalf("Get(tmdb/movies:trending): %v", err)
	}
	if len(entry.Payload) != 1 || string(entry.Payload[0]) != `{"id":7}` {
		t.Errorf("payload = %v, want the previous one untouched — a failed refresh must never blank a row", entry.Payload)
	}
	if entry.LastError == "" {
		t.Error("lastError is empty, want the failure recorded")
	}
	// The other five keys are unaffected by one key's failure.
	if _, err := s.Get(ctx, "tmdb", "series:popular"); err != nil {
		t.Errorf("Get(tmdb/series:popular): %v — one key's failure must not abandon the sweep", err)
	}
}
