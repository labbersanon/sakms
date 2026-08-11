package api

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

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
)

// fakeUpcomingTMDB serves the three endpoints the Upcoming/Movies handler
// reaches: /discover/movie twice (query A carries with_release_type, query B
// does not) and /movie/{id} once per digital item. It records every path and,
// crucially, the maximum number of /movie/{id} requests it ever saw IN FLIGHT
// AT ONCE — the assertion that the per-item resolution is sequential and never
// an errgroup fan-out.
type fakeUpcomingTMDB struct {
	mu             sync.Mutex
	paths          []string
	inFlight       int
	maxConcurrency int
	// detailBodies maps a tmdb id to the /movie/{id} body to serve. A missing
	// id gets a 500, which exercises the soft-fail path.
	detailBodies map[int]string
}

func (f *fakeUpcomingTMDB) record(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
}

func (f *fakeUpcomingTMDB) enter() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight++
	if f.inFlight > f.maxConcurrency {
		f.maxConcurrency = f.inFlight
	}
}

func (f *fakeUpcomingTMDB) leave() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight--
}

func (f *fakeUpcomingTMDB) detailCalls() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for _, p := range f.paths {
		if strings.HasPrefix(p, "/movie/") {
			id, err := strconv.Atoi(strings.TrimPrefix(p, "/movie/"))
			if err == nil {
				out = append(out, id)
			}
		}
	}
	return out
}

// newUpcomingTMDB starts the fake and returns it plus its URL. digitalBody is
// query A's /discover/movie response, plannedBody query B's.
func newUpcomingTMDB(t *testing.T, digitalBody, plannedBody string, detailBodies map[int]string) (*fakeUpcomingTMDB, string) {
	t.Helper()
	f := &fakeUpcomingTMDB{detailBodies: detailBodies}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/discover/movie" {
			if r.URL.Query().Get("with_release_type") != "" {
				w.Write([]byte(digitalBody))
				return
			}
			w.Write([]byte(plannedBody))
			return
		}
		// /movie/{id} — the per-item typed-date resolution.
		f.enter()
		defer f.leave()
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/movie/"))
		if err != nil {
			http.Error(w, "bad id", http.StatusNotFound)
			return
		}
		body, ok := f.detailBodies[id]
		if !ok {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

// detailBody builds a /movie/{id} response whose US release_dates list carries
// the given (type, date) pairs — in TMDB's real full-ISO-timestamp form, not a
// bare YYYY-MM-DD, because that difference is exactly what the handler has to
// truncate before the date can bucket into a calendar cell.
func detailBody(id int, entries ...[2]string) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, fmt.Sprintf(`{"type":%s,"release_date":"%sT00:00:00.000Z"}`, e[0], e[1]))
	}
	return fmt.Sprintf(`{"id":%d,"title":"T","release_dates":{"results":[{"iso_3166_1":"US","release_dates":[%s]}]}}`,
		id, strings.Join(parts, ","))
}

// upcomingMoviesMux wires the handler under test against a fake TMDB, with
// real (empty unless a test seeds them) grab and library stores. It returns
// those stores so a test can seed the already-requested inputs.
func upcomingMoviesMux(t *testing.T, tmdbURL string) (*http.ServeMux, *grabs.Store, *library.Store) {
	t.Helper()
	connStore, _, settingsStore, grabsStore, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	// Resets the package-level TMDB response cache as a side effect — required,
	// not optional: two tests in this process can otherwise land on the same
	// reused ephemeral port and be served each other's bodies.
	overrideFixedURL(t, "tmdb", tmdbURL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbURL, "key"); err != nil {
		t.Fatalf("seeding tmdb connection: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/calendar/upcoming/movies", upcomingMoviesHandler(testHTTPClient(), connStore, nil, settingsStore, grabsStore, libStore))
	return mux, grabsStore, libStore
}

func getUpcomingMovies(t *testing.T, srvURL, query string) (int, []apidto.UpcomingMovieEntry) {
	t.Helper()
	resp, err := http.Get(srvURL + "/api/calendar/upcoming/movies" + query)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	var entries []apidto.UpcomingMovieEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, entries
}

// TestCalendarUpcomingMovies_RequiresFromAndTo is T-4.3's movies half: from/to
// are required, 400 without them — the same contract discoverCalendarHandler
// has, and what keeps this from becoming an unbounded query.
func TestCalendarUpcomingMovies_RequiresFromAndTo(t *testing.T) {
	_, tmdbURL := newUpcomingTMDB(t, `{"results":[]}`, `{"results":[]}`, nil)
	mux, _, _ := upcomingMoviesMux(t, tmdbURL)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, q := range []string{"", "?from=2026-09-01", "?to=2026-09-30"} {
		status, _ := getUpcomingMovies(t, srv.URL, q)
		if status != http.StatusBadRequest {
			t.Errorf("query %q: expected 400, got %d", q, status)
		}
	}
}

// TestCalendarUpcomingMovies_MergeAndReleaseKindPassThrough proves the A+B
// merge and its digital/planned tagging survive the handler unchanged: an id in
// both queries is digital (A wins), a B-only id is planned, and the kind is
// NEVER re-derived from the per-item resolution (id 12 has no type-4/5 entry at
// all and still reports digital, keeping its discover date).
func TestCalendarUpcomingMovies_MergeAndReleaseKindPassThrough(t *testing.T) {
	digital := `{"results":[
		{"id":10,"title":"In Both","poster_path":"/both.jpg","release_date":"2026-06-01"},
		{"id":12,"title":"No Typed Entry","release_date":"2026-09-08"}
	]}`
	planned := `{"results":[
		{"id":10,"title":"In Both","poster_path":"/both.jpg","release_date":"2026-06-01"},
		{"id":11,"title":"Planned Only","release_date":"2026-09-20"}
	]}`
	details := map[int]string{
		// Theatrical (3) in June, Digital (4) in the requested September window:
		// the bucketing hazard §4.3 describes, in one fixture.
		10: detailBody(10, [2]string{"3", "2026-06-01"}, [2]string{"4", "2026-09-15"}),
		12: detailBody(12, [2]string{"3", "2026-09-08"}),
	}
	f, tmdbURL := newUpcomingTMDB(t, digital, planned, details)
	mux, _, _ := upcomingMoviesMux(t, tmdbURL)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, entries := getUpcomingMovies(t, srv.URL, "?from=2026-09-01&to=2026-09-30")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	byID := map[int]apidto.UpcomingMovieEntry{}
	for _, e := range entries {
		byID[e.TMDBID] = e
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 merged entries, got %d: %+v", len(entries), entries)
	}
	if got := byID[10]; got.ReleaseKind != "digital" || got.PosterPath != "/both.jpg" || got.Title != "In Both" {
		t.Errorf("id in both queries should keep A's digital tagging and fields: %+v", got)
	}
	// The whole point of building for the PRIMARY assumption: the discover
	// result said June, the typed date is September, and September is what the
	// calendar must bucket on.
	if got := byID[10].ReleaseDate; got != "2026-09-15" {
		t.Errorf("expected the typed digital date 2026-09-15 (truncated from TMDB's ISO timestamp), got %q", got)
	}
	if got := byID[11]; got.ReleaseKind != "planned" || got.ReleaseDate != "2026-09-20" {
		t.Errorf("B-only id should be planned with its discover date untouched: %+v", got)
	}
	if got := byID[12]; got.ReleaseKind != "digital" || got.ReleaseDate != "2026-09-08" {
		t.Errorf("a digital item with no type-4/5 entry must stay digital and keep its discover date: %+v", got)
	}
	// Only the DIGITAL items are resolved — a planned item's date came from a
	// primary_release_date query and is correct under either reading, so
	// resolving it would double the call budget for nothing.
	called := f.detailCalls()
	if len(called) != 2 {
		t.Errorf("expected exactly 2 per-item resolutions (the digital items), got %d: %v", len(called), called)
	}
	for _, id := range called {
		if id == 11 {
			t.Errorf("planned item 11 must not be resolved via MovieDetails")
		}
	}
}

// TestCalendarUpcomingMovies_ResolutionIsSequential is the fan-out ban, as
// executable code: the fake server tracks concurrent in-flight /movie/{id}
// requests and this asserts it never saw more than one. A future "optimisation"
// that wraps the resolution loop in an errgroup fails here.
//
// One GET only, with distinct ids: a second GET in the same test would be
// served entirely from the package-level TMDB response cache, the fake would
// record nothing, and this assertion would pass vacuously.
func TestCalendarUpcomingMovies_ResolutionIsSequential(t *testing.T) {
	var results []string
	details := map[int]string{}
	for id := 100; id < 112; id++ {
		results = append(results, fmt.Sprintf(`{"id":%d,"title":"M%d","release_date":"2026-09-02"}`, id, id))
		details[id] = detailBody(id, [2]string{"4", "2026-09-10"})
	}
	digital := `{"results":[` + strings.Join(results, ",") + `]}`
	f, tmdbURL := newUpcomingTMDB(t, digital, `{"results":[]}`, details)
	mux, _, _ := upcomingMoviesMux(t, tmdbURL)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, entries := getUpcomingMovies(t, srv.URL, "?from=2026-09-01&to=2026-09-30")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if len(entries) != 12 {
		t.Fatalf("expected 12 entries, got %d", len(entries))
	}
	if len(f.detailCalls()) != 12 {
		t.Fatalf("expected 12 resolutions (so the concurrency check observed something), got %d", len(f.detailCalls()))
	}
	f.mu.Lock()
	maxConc := f.maxConcurrency
	f.mu.Unlock()
	if maxConc != 1 {
		t.Errorf("per-item resolution must be sequential, never a fan-out: saw %d concurrent MovieDetails requests", maxConc)
	}
}

// TestCalendarUpcomingMovies_SoftFailsOneResolution proves an unresolvable
// title degrades to its discover date rather than 502-ing the whole month.
func TestCalendarUpcomingMovies_SoftFailsOneResolution(t *testing.T) {
	digital := `{"results":[
		{"id":20,"title":"Resolvable","release_date":"2026-06-01"},
		{"id":21,"title":"Details Explode","release_date":"2026-09-03"}
	]}`
	details := map[int]string{20: detailBody(20, [2]string{"4", "2026-09-12"})} // 21 absent → 500
	_, tmdbURL := newUpcomingTMDB(t, digital, `{"results":[]}`, details)
	mux, _, _ := upcomingMoviesMux(t, tmdbURL)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, entries := getUpcomingMovies(t, srv.URL, "?from=2026-09-01&to=2026-09-30")
	if status != http.StatusOK {
		t.Fatalf("one failed per-item resolution must not fail the month view; got %d", status)
	}
	byID := map[int]apidto.UpcomingMovieEntry{}
	for _, e := range entries {
		byID[e.TMDBID] = e
	}
	if byID[20].ReleaseDate != "2026-09-12" {
		t.Errorf("resolvable item should carry its typed date, got %q", byID[20].ReleaseDate)
	}
	if byID[21].ReleaseDate != "2026-09-03" || byID[21].ReleaseKind != "digital" {
		t.Errorf("unresolvable item should keep its discover date and kind: %+v", byID[21])
	}
}

// TestCalendarUpcomingMovies_TypedDateOutsideWindow pins the third resolution
// branch: a digital item whose only type-4/5 dates fall OUTSIDE the requested
// window keeps the earliest of them rather than the discover date. The
// returned date is therefore deliberately not guaranteed to be bucketable into
// the requested month — reporting the real digital date is the honest answer,
// and a month grid must drop such a row rather than assume every entry lands
// in a cell.
func TestCalendarUpcomingMovies_TypedDateOutsideWindow(t *testing.T) {
	digital := `{"results":[{"id":50,"title":"Later","release_date":"2026-09-04"}]}`
	details := map[int]string{
		50: detailBody(50, [2]string{"5", "2026-12-01"}, [2]string{"4", "2026-11-20"}),
	}
	_, tmdbURL := newUpcomingTMDB(t, digital, `{"results":[]}`, details)
	mux, _, _ := upcomingMoviesMux(t, tmdbURL)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, entries := getUpcomingMovies(t, srv.URL, "?from=2026-09-01&to=2026-09-30")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ReleaseDate != "2026-11-20" {
		t.Errorf("expected the earliest typed date 2026-11-20, got %q", entries[0].ReleaseDate)
	}
	if entries[0].ReleaseKind != "digital" {
		t.Errorf("kind must still come from which query matched, got %q", entries[0].ReleaseKind)
	}
}

// TestCalendarUpcomingMovies_AlreadyRequestedFlag is T-4.4: the flag is
// computed from the tracked library plus the grab log, and the grabs side is
// FILTERED. A failed grab must NOT mark a movie already-requested — doing so
// would permanently suppress the click-to-request affordance for a title the
// operator can no longer request any other way. A pending_retry grab (a held
// pre-release request) and an imported one both DO.
func TestCalendarUpcomingMovies_AlreadyRequestedFlag(t *testing.T) {
	ids := []int{30, 31, 32, 33, 34}
	var results []string
	details := map[int]string{}
	for _, id := range ids {
		results = append(results, fmt.Sprintf(`{"id":%d,"title":"M%d","release_date":"2026-09-05"}`, id, id))
		details[id] = detailBody(id, [2]string{"4", "2026-09-05"})
	}
	digital := `{"results":[` + strings.Join(results, ",") + `]}`
	_, tmdbURL := newUpcomingTMDB(t, digital, `{"results":[]}`, details)
	mux, grabsStore, libStore := upcomingMoviesMux(t, tmdbURL)
	ctx := context.Background()

	// 30: tracked in the library.
	if _, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 30, Title: "Tracked", FilePath: "/m/30.mkv", RootFolderPath: "/m"}); err != nil {
		t.Fatalf("seeding library item: %v", err)
	}
	// 31: a FAILED grab — must NOT count.
	failed, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Movies, Title: "Failed", TMDBID: 31})
	if err != nil {
		t.Fatalf("seeding failed grab: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, failed.ID, grabs.Failed); err != nil {
		t.Fatalf("marking grab failed: %v", err)
	}
	// 32: an IMPORTED grab — counts.
	imported, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Movies, Title: "Imported", TMDBID: 32})
	if err != nil {
		t.Fatalf("seeding imported grab: %v", err)
	}
	if err := grabsStore.UpdateStatus(ctx, imported.ID, grabs.Imported); err != nil {
		t.Fatalf("marking grab imported: %v", err)
	}
	// 33: a PENDING_RETRY grab (what a held pre-release request looks like) —
	// counts, via isActiveGrab.
	if _, err := grabsStore.Create(ctx, grabs.Grab{Mode: mode.Movies, Title: "Held", TMDBID: 33, Status: grabs.PendingRetry}); err != nil {
		t.Fatalf("seeding pending_retry grab: %v", err)
	}
	// 34: nothing at all — clickable.

	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, entries := getUpcomingMovies(t, srv.URL, "?from=2026-09-01&to=2026-09-30")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	got := map[int]bool{}
	for _, e := range entries {
		got[e.TMDBID] = e.AlreadyRequested
	}
	want := map[int]bool{30: true, 31: false, 32: true, 33: true, 34: false}
	for id, expected := range want {
		if got[id] != expected {
			t.Errorf("tmdbId %d: alreadyRequested = %v, want %v", id, got[id], expected)
		}
	}
}

// ---------------------------------------------------------------------------
// Upcoming / Series
// ---------------------------------------------------------------------------

// fakeUpcomingSeriesTMDB serves the ONE endpoint the read-path catalog sync
// reaches with discovery hardcoded false: /tv/{id}/season/{n}. /tv/{id} is
// deliberately NOT served — a request to it would mean the Calendar caller had
// somehow enabled discovery, which must never happen from a read path, so
// leaving it unregistered turns that regression into a visible 404 rather than
// a silent extra call.
type fakeUpcomingSeriesTMDB struct {
	mu       sync.Mutex
	seasons  map[int][]fakeTMDBEpisode
	seasonGE []int // every season number fetched, in order
}

func newUpcomingSeriesTMDB(t *testing.T, seasons map[int][]fakeTMDBEpisode) (*fakeUpcomingSeriesTMDB, string) {
	t.Helper()
	f := &fakeUpcomingSeriesTMDB{seasons: seasons}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tv/{id}/season/{season}", func(w http.ResponseWriter, r *http.Request) {
		season, err := strconv.Atoi(r.PathValue("season"))
		if err != nil {
			http.Error(w, "bad season", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.seasonGE = append(f.seasonGE, season)
		episodes := []map[string]any{}
		for _, e := range f.seasons[season] {
			episodes = append(episodes, map[string]any{
				"episode_number": e.Number, "name": e.Name, "air_date": e.AirDate, "runtime": 45,
			})
		}
		f.mu.Unlock()
		writeFakeTMDBJSON(w, map[string]any{"episodes": episodes})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeUpcomingSeriesTMDB) seasonFetches() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.seasonGE...)
}

// upcomingSeriesMux wires the Series handler against real stores on a temp
// database. An EMPTY tmdbURL seeds no tmdb connection at all, which is how the
// no-TMDB degradation case is exercised — mode.Build succeeds with a nil
// sess.TMDB, it does not error.
//
// The freshness reset is mandatory, not tidiness: every test builds its own
// temp database, so the first tracked series is id 1 in ALL of them, and the
// package-level cache surviving between tests would make one test's series look
// fresh to the next — the sync would silently no-op and the assertion would
// pass vacuously.
func upcomingSeriesMux(t *testing.T, tmdbURL string) (*http.ServeMux, *library.Store) {
	t.Helper()
	resetSeriesCatalogFreshness()
	t.Cleanup(resetSeriesCatalogFreshness)

	connStore, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)
	if tmdbURL != "" {
		// Resets the package-level TMDB response cache as a side effect —
		// required, not optional, for the same ephemeral-port-reuse reason the
		// movies half documents above.
		overrideFixedURL(t, "tmdb", tmdbURL)
		if err := connStore.Upsert(context.Background(), "tmdb", tmdbURL, "key"); err != nil {
			t.Fatalf("seeding tmdb connection: %v", err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/calendar/upcoming/series", upcomingSeriesHandler(testHTTPClient(), connStore, nil, settingsStore, libStore))
	return mux, libStore
}

func getUpcomingSeries(t *testing.T, srvURL, query string) (int, []apidto.UpcomingSeriesEntry) {
	t.Helper()
	resp, err := http.Get(srvURL + "/api/calendar/upcoming/series" + query)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	var entries []apidto.UpcomingSeriesEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, entries
}

// trackUpcomingSeries records one tracked series and returns it.
func trackUpcomingSeries(t *testing.T, libStore *library.Store, tmdbID int, title string) library.Series {
	t.Helper()
	series, err := libStore.UpsertSeries(context.Background(), library.Series{
		TMDBID: tmdbID, TVDBID: 4321, Title: title, RootFolderPath: "/series",
	})
	if err != nil {
		t.Fatalf("tracking %s: %v", title, err)
	}
	return series
}

// seedUpcomingEpisode records a FILELESS episode row — "TMDB says this exists,
// we don't have it" — which is exactly what MissingEpisodes returns.
func seedUpcomingEpisode(t *testing.T, libStore *library.Store, seriesID int64, season, episode int, airDate string) {
	t.Helper()
	if err := libStore.UpsertEpisodeCatalog(context.Background(), seriesID, season, episode,
		fmt.Sprintf("Ep %d", episode), airDate); err != nil {
		t.Fatalf("seeding s%02de%02d: %v", season, episode, err)
	}
}

func upcomingSeriesKeys(entries []apidto.UpcomingSeriesEntry) map[string]apidto.UpcomingSeriesEntry {
	out := map[string]apidto.UpcomingSeriesEntry{}
	for _, e := range entries {
		out[fmt.Sprintf("s%02de%02d", e.SeasonNumber, e.EpisodeNumber)] = e
	}
	return out
}

// TestCalendarUpcomingSeries_RequiresFromAndTo is T-4.3's series half: the same
// required-window contract the movies half has, enforced by the same shared
// calendarWindow helper. Without it this becomes an unbounded query.
func TestCalendarUpcomingSeries_RequiresFromAndTo(t *testing.T) {
	mux, _ := upcomingSeriesMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, q := range []string{"", "?from=2026-09-01", "?to=2026-09-30"} {
		status, _ := getUpcomingSeries(t, srv.URL, q)
		if status != http.StatusBadRequest {
			t.Errorf("query %q: expected 400, got %d", q, status)
		}
	}
}

// TestCalendarUpcomingSeries_SyncsTheCatalogBeforeReading is the smoke test for
// the always-available half of the metadata/dispatch split: the handler calls
// syncSeriesCatalogForRead before reading MissingEpisodes, so a series whose
// episodes exist only in TMDB shows up on the FIRST load rather than requiring
// the auto-grab retry cycle (which does not run at all on a default install) to
// have populated the rows first.
//
// The sync's own internals — the TTL, the cap, the sequential rule, the
// discovery=false hardcode — are covered by seriescatalog_test.go and are
// deliberately NOT re-tested here.
func TestCalendarUpcomingSeries_SyncsTheCatalogBeforeReading(t *testing.T) {
	f, tmdbURL := newUpcomingSeriesTMDB(t, map[int][]fakeTMDBEpisode{
		1: {{Number: 4, Name: "From TMDB", AirDate: "2026-09-18"}},
	})
	mux, libStore := upcomingSeriesMux(t, tmdbURL)
	series := trackUpcomingSeries(t, libStore, 777, "Synced Show")
	// One already-known episode makes season 1 a known season, which is what the
	// always-on half of the sync operates over. Its own air date is outside the
	// window, so anything the window returns came from the sync.
	seedUpcomingEpisode(t, libStore, series.ID, 1, 1, "2026-01-05")

	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, entries := getUpcomingSeries(t, srv.URL, "?from=2026-09-01&to=2026-09-30")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if got := f.seasonFetches(); len(got) == 0 {
		t.Fatalf("the handler must sync the catalog before reading; TMDB saw no season fetch")
	}
	if len(entries) != 1 {
		t.Fatalf("expected the TMDB-sourced episode only, got %d: %+v", len(entries), entries)
	}
	got := entries[0]
	if got.SeasonNumber != 1 || got.EpisodeNumber != 4 || got.AirDate != "2026-09-18" {
		t.Errorf("expected s01e04 airing 2026-09-18, got %+v", got)
	}
	if got.SeriesTitle != "Synced Show" || got.SeriesID != series.ID || got.TMDBID != 777 {
		t.Errorf("series identity fields not populated from the tracked row: %+v", got)
	}
	if got.EpisodeTitle != "From TMDB" {
		t.Errorf("episode title = %q, want the synced TMDB name", got.EpisodeTitle)
	}
}

// TestCalendarUpcomingSeries_WithoutTMDBServesTheLibrary is the graceful
// degradation requirement, and it is the reason this handler does NOT 400 on a
// nil sess.TMDB the way the movies half does: Series' result set lives in
// library_episodes, which the sync only REFRESHES. With no TMDB configured at
// all the honest answer is the rows that are already there, not an error.
func TestCalendarUpcomingSeries_WithoutTMDBServesTheLibrary(t *testing.T) {
	mux, libStore := upcomingSeriesMux(t, "") // no tmdb connection at all
	series := trackUpcomingSeries(t, libStore, 778, "Offline Show")
	seedUpcomingEpisode(t, libStore, series.ID, 2, 3, "2026-09-14")

	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, entries := getUpcomingSeries(t, srv.URL, "?from=2026-09-01&to=2026-09-30")
	if status != http.StatusOK {
		t.Fatalf("a TMDB-less install must still render the view; got %d", status)
	}
	if len(entries) != 1 || entries[0].SeasonNumber != 2 || entries[0].EpisodeNumber != 3 {
		t.Fatalf("expected the already-stored s02e03, got %+v", entries)
	}
}

// TestCalendarUpcomingSeries_UnmonitoredSeasonStillAppears is the key
// differentiator from airdatemonitor.go's eligibleEpisodes, and the test that
// fails loudly if a future session "reuses" that filter here. The season is
// EXPLICITLY unmonitored — the top-of-filter gate eligibleEpisodes applies —
// and its upcoming episode must still be visible, because monitoring gates
// SEARCHING and this is a browsing view. An operator who cannot see that an
// unmonitored season has episodes coming has no reason to ever monitor it.
func TestCalendarUpcomingSeries_UnmonitoredSeasonStillAppears(t *testing.T) {
	mux, libStore := upcomingSeriesMux(t, "")
	ctx := context.Background()
	series := trackUpcomingSeries(t, libStore, 779, "Unmonitored Show")
	if err := libStore.SetSeasonMonitored(ctx, series.ID, 3, false); err != nil {
		t.Fatalf("un-monitoring season 3: %v", err)
	}
	seedUpcomingEpisode(t, libStore, series.ID, 3, 7, "2026-09-22")

	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, entries := getUpcomingSeries(t, srv.URL, "?from=2026-09-01&to=2026-09-30")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if len(entries) != 1 || entries[0].SeasonNumber != 3 || entries[0].EpisodeNumber != 7 {
		t.Fatalf("an unmonitored season's upcoming episode must still be browsable, got %+v", entries)
	}
}

// TestCalendarUpcomingSeries_FiltersByWindowAndAirDate exercises the whole
// predicate through the route: an episode that aired before the window is out,
// an unannounced one (empty air_date) is out, one past the window is out, and
// the two inside it are in.
func TestCalendarUpcomingSeries_FiltersByWindowAndAirDate(t *testing.T) {
	mux, libStore := upcomingSeriesMux(t, "")
	series := trackUpcomingSeries(t, libStore, 780, "Filtered Show")
	// Episode 1 is out because it precedes the window's START — the view's own
	// past boundary — NOT because of any comparison against today. The handler
	// never reads the wall clock, deliberately (see upcomingEpisodes' doc), so
	// do not read this row as evidence of a now-check.
	seedUpcomingEpisode(t, libStore, series.ID, 1, 1, "2026-08-30") // before `from`
	seedUpcomingEpisode(t, libStore, series.ID, 1, 2, "2026-09-01") // window edge, in
	seedUpcomingEpisode(t, libStore, series.ID, 1, 3, "")           // unannounced
	seedUpcomingEpisode(t, libStore, series.ID, 1, 4, "2026-09-30") // window edge, in
	seedUpcomingEpisode(t, libStore, series.ID, 1, 5, "2026-10-01") // after `to`

	srv := httptest.NewServer(mux)
	defer srv.Close()

	status, entries := getUpcomingSeries(t, srv.URL, "?from=2026-09-01&to=2026-09-30")
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	got := upcomingSeriesKeys(entries)
	if len(got) != 2 || got["s01e02"].AirDate != "2026-09-01" || got["s01e04"].AirDate != "2026-09-30" {
		t.Fatalf("expected exactly s01e02 and s01e04, got %+v", entries)
	}
}

// TestCalendarUpcomingSeries_UpcomingEpisodesFilter pins the pure predicate
// directly, including the two cases the empty-air-date guard exists for: an
// unannounced episode must be DROPPED, never bucketed at the epoch (an empty
// string sorts before every real date, so without the guard it would land in
// every window whose `from` it precedes).
func TestCalendarUpcomingSeries_UpcomingEpisodesFilter(t *testing.T) {
	missing := []library.Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, AirDate: "2026-08-31"},
		{SeasonNumber: 1, EpisodeNumber: 2, AirDate: ""},
		{SeasonNumber: 1, EpisodeNumber: 3, AirDate: "2026-09-01"},
		{SeasonNumber: 1, EpisodeNumber: 4, AirDate: "2026-09-30"},
		{SeasonNumber: 1, EpisodeNumber: 5, AirDate: "2026-10-01"},
	}
	got := upcomingEpisodes(missing, "2026-09-01", "2026-09-30")
	if len(got) != 2 {
		t.Fatalf("expected 2 in-window episodes, got %d: %+v", len(got), got)
	}
	if got[0].EpisodeNumber != 3 || got[1].EpisodeNumber != 4 {
		t.Errorf("expected episodes 3 and 4 in MissingEpisodes' own order, got %+v", got)
	}
	// An empty window start would admit the unannounced episode if the
	// empty-air-date guard were ever dropped — assert it stays out even then.
	for _, ep := range upcomingEpisodes(missing, "", "2026-12-31") {
		if ep.AirDate == "" {
			t.Errorf("an unannounced episode must never be returned, got %+v", ep)
		}
	}
}

// TestCalendarUpcomingMovies_NeverQueriesProwlarr is the other half of T-4.4:
// the already-requested signal comes from the library and the grab log only.
// The mux is built with NO Prowlarr connection configured at all, and a
// Prowlarr connection pointed at a recording server confirms nothing reaches
// it — CLAUDE.md's rule that a browse surface's "do I already own this" signal
// never comes from an indexer.
func TestCalendarUpcomingMovies_NeverQueriesProwlarr(t *testing.T) {
	var prowlarrHits int
	var mu sync.Mutex
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		prowlarrHits++
		mu.Unlock()
		w.Write([]byte(`[]`))
	}))
	defer prowlarr.Close()

	digital := `{"results":[{"id":40,"title":"M","release_date":"2026-09-05"}]}`
	_, tmdbURL := newUpcomingTMDB(t, digital, `{"results":[]}`, map[int]string{40: detailBody(40, [2]string{"4", "2026-09-05"})})

	connStore, _, settingsStore, grabsStore, libStore, _, _, _, _, _ := testStores(t)
	ctx := context.Background()
	overrideFixedURL(t, "tmdb", tmdbURL)
	if err := connStore.Upsert(ctx, "tmdb", tmdbURL, "key"); err != nil {
		t.Fatalf("seeding tmdb connection: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("seeding prowlarr connection: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/calendar/upcoming/movies", upcomingMoviesHandler(testHTTPClient(), connStore, nil, settingsStore, grabsStore, libStore))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if status, _ := getUpcomingMovies(t, srv.URL, "?from=2026-09-01&to=2026-09-30"); status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	mu.Lock()
	defer mu.Unlock()
	if prowlarrHits != 0 {
		t.Errorf("Upcoming/Movies must never query Prowlarr; saw %d requests", prowlarrHits)
	}
}
