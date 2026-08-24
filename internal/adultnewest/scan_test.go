package adultnewest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/throttle"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// overrideTPDBBaseURL points tpdbrest.DefaultBaseURL at u for the test's
// duration, restoring it on cleanup. mode.Build now constructs the TPDB REST
// client from this hardcoded package var, not the stored Connection.URL, so a
// test that stands up a fake TPDB must redirect the var, not just store its URL.
func overrideTPDBBaseURL(t *testing.T, u string) {
	t.Helper()
	prev := tpdbrest.DefaultBaseURL
	tpdbrest.DefaultBaseURL = u
	t.Cleanup(func() { tpdbrest.DefaultBaseURL = prev })
}

// newTestScanStores builds a connections.Store and settings.Store backed by
// the same freshly-migrated SQLite file, plus a standalone ReleaseStore —
// real SQL and real encryption, no mocks, matching internal/recheck's own
// store-test convention (see recheck_test.go's newTestStores).
func newTestScanStores(t *testing.T) (*connections.Store, *settings.Store, *ReleaseStore) {
	t.Helper()
	sqlDB := dbtest.New(t)

	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	return connections.New(sqlDB, secretStore), settings.New(sqlDB), NewReleaseStore(sqlDB)
}

// fakeProwlarr serves Prowlarr's /api/v1/search, returning body verbatim (a
// JSON array of releaseResource objects) for any query — mirrors
// internal/recheck/recheck_test.go's fakeProwlarr exactly.
func fakeProwlarr(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeOllama serves Ollama's /api/chat with a fixed JSON extraction result,
// regardless of prompt — enough to make mode.Build populate sess.Identify
// (buildIdentifier only requires a non-nil AI client; StashDB/FansDB/TPDB
// are all optional, see mode.go's buildIdentifier). content is the raw JSON
// string ParseFilename expects to decode (studio/title/performers keys).
func fakeOllama(t *testing.T, content string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"message": map[string]any{"content": content}}
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeTPDB serves ThePornDB's REST API for exactly the confirmations named
// in sites/performers (keyed by exact search term) — /scenes and /movies
// always return empty (out of scope for this test, keeps the scene/movie
// path from interfering), /sites and /performers return a match with a
// real-looking image URL only for a name present in the map, empty
// otherwise. Used to prove StudioImage/PerformerImage's "only cache a
// confirmed entity" behavior against something other than a live network
// call.
func fakeTPDB(t *testing.T, sites, performers map[string]bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("q")
		switch r.URL.Path {
		case "/sites":
			if sites[q] {
				fmt.Fprintf(w, `{"data":[{"_id":1,"name":%q,"logo":"https://cdn.theporndb.net/sites/fake-logo.png"}]}`, q)
				return
			}
			fmt.Fprint(w, `{"data":[]}`)
		case "/performers":
			if performers[q] {
				fmt.Fprintf(w, `{"data":[{"_id":1,"name":%q,"image":"https://cdn.theporndb.net/performer/fake.jpg"}]}`, q)
				return
			}
			fmt.Fprint(w, `{"data":[]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeStashBox serves the two stash-box GraphQL queries identifyStudioPerformers'
// StudioImage/PerformerImage calls issue — findStudio (matched on the "name"
// variable) and searchPerformer (matched on the "term" variable) — routed by a
// substring check on the outgoing query text (same "distinguish by query shape"
// approach fakeTPDB uses via URL path). Used to prove US-3's gender threading
// end-to-end through identifyStudioPerformers without a live network call.
func fakeStashBox(t *testing.T, studioImages, performerGenders map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "findStudio"):
			name, _ := req.Variables["name"].(string)
			if img, ok := studioImages[name]; ok {
				fmt.Fprintf(w, `{"data":{"findStudio":{"id":"s1","name":%q,"images":[{"url":%q}]}}}`, name, img)
				return
			}
			fmt.Fprint(w, `{"data":{"findStudio":null}}`)
		case strings.Contains(req.Query, "searchPerformer"):
			term, _ := req.Variables["term"].(string)
			if gender, ok := performerGenders[term]; ok {
				fmt.Fprintf(w, `{"data":{"searchPerformer":[{"id":"p1","name":%q,"gender":%q,"images":[{"url":"https://cdn/performer.jpg"}]}]}}`, term, gender)
				return
			}
			fmt.Fprint(w, `{"data":{"searchPerformer":[]}}`)
		default:
			fmt.Fprint(w, `{"data":{}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestIdentifyStudioPerformers_ThreadsGenderOnPerformerRowsOnly proves US-3's
// gender threading (plan §2.4): a RowPerformer cache row carries the
// normalized gender PerformerImage resolved, while its sibling RowStudio row
// carries an empty gender (studios have no gender concept — StudioImage is
// unchanged). identifyStudioPerformers is the SHARED function both the
// browse pass (matchRelease) and the feed pass (processFeedItem) call
// unmodified (F6) — testing it directly here proves both passes get this
// behavior without duplicating the test against two callers.
func TestIdentifyStudioPerformers_ThreadsGenderOnPerformerRowsOnly(t *testing.T) {
	srv := fakeStashBox(t,
		map[string]string{"Tushy": "https://cdn/tushy.jpg"},
		map[string]string{"Riley Reid": "FEMALE"},
	)
	id := &identify.Identifier{
		Boxes: identify.NewBoxSearcher(map[string]*stashbox.Client{
			"stashdb": stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k"}, &http.Client{Timeout: 5 * time.Second}),
		}, nil),
		Throttle: throttle.New(0),
	}

	detail := identify.DetailedMatch{StudioName: "Tushy", Performers: []string{"Riley Reid"}}
	out := identifyStudioPerformers(context.Background(), id, detail)

	if len(out) != 2 {
		t.Fatalf("expected 1 studio row + 1 performer row, got %d: %+v", len(out), out)
	}
	var studioRow, performerRow *MatchedRelease
	for i := range out {
		switch out[i].RowType {
		case RowStudio:
			studioRow = &out[i]
		case RowPerformer:
			performerRow = &out[i]
		}
	}
	if studioRow == nil || studioRow.Gender != "" {
		t.Fatalf("expected RowStudio's Gender to be empty, got %+v", studioRow)
	}
	if performerRow == nil || performerRow.Gender != "female" {
		t.Fatalf("expected RowPerformer's Gender to be the normalized 'female', got %+v", performerRow)
	}
}

// TestRun_BrowseBootPollFiresBeforeInterval proves the browse pass now fires an
// immediate boot poll — the deliberate reversal of its earlier "no-initial-tick"
// shape. With a long browse interval and the feed pass off, Prowlarr is still
// queried at t≈0, before the first tick would ever elapse. Without the boot poll
// the browse pass effectively never ran in this fast-redeploy deployment (a 24h
// ticker never survives to fire between redeploys), leaving the entity DB its
// match quality depends on empty. Mirrors TestRun_FeedBootPollFiresBeforeInterval.
func TestRun_BrowseBootPollFiresBeforeInterval(t *testing.T) {
	connStore, settingsStore, releaseStore := newTestScanStores(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ollama := fakeOllama(t, `{"studio":null,"title":null,"performers":null}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	hit := make(chan struct{}, 1)
	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search" {
			http.NotFound(w, r)
			return
		}
		select {
		case hit <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(prow.Close)
	if err := connStore.Upsert(ctx, "prowlarr", prow.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	// Feed pass explicitly off, so only the browse boot poll can query Prowlarr.
	if err := settingsStore.Set(ctx, FeedIntervalSettingKey, "0"); err != nil {
		t.Fatalf("disabling feed pass: %v", err)
	}

	// A one-hour browse interval: if the boot poll didn't fire, Prowlarr would
	// not be queried for an hour — the test would time out at 2s instead.
	go Run(ctx, time.Hour, connStore, nil, settingsStore, releaseStore, nil, nil, NewFeedHealth(), nil)

	select {
	case <-hit:
		// Boot poll fired — success.
	case <-time.After(2 * time.Second):
		t.Fatal("prowlarr was not queried within 2s — the browse boot poll did not fire before the interval")
	}
}

func configureAI(t *testing.T, ctx context.Context, connStore *connections.Store, settingsStore *settings.Store, ollamaURL string) {
	t.Helper()
	if err := connStore.Upsert(ctx, "ollama", ollamaURL, ""); err != nil {
		t.Fatalf("configuring ollama: %v", err)
	}
	if err := settingsStore.Set(ctx, mode.AIModelKey, "test-model"); err != nil {
		t.Fatalf("configuring ai model: %v", err)
	}
	if err := settingsStore.Set(ctx, mode.AIFallbackEnabledKey, "true"); err != nil {
		t.Fatalf("configuring ai fallback enabled: %v", err)
	}
}

func TestLoadInterval_UnsetDefaultsTo24Hours(t *testing.T) {
	_, settingsStore, _ := newTestScanStores(t)
	want := 24 * time.Hour
	if got := LoadInterval(context.Background(), settingsStore); got != want {
		t.Errorf("expected %v (defaultIntervalHours) for a never-set interval, got %v", want, got)
	}
}

func TestLoadInterval_ExplicitZeroIsOffNotDefault(t *testing.T) {
	_, settingsStore, _ := newTestScanStores(t)
	ctx := context.Background()
	// An operator explicitly saving "0" via Settings must mean off, not fall
	// back to the 24h default — this is the exact distinction
	// settings.ErrNotFound vs. a stored non-positive value exists to make.
	if err := settingsStore.Set(ctx, IntervalSettingKey, "0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := LoadInterval(ctx, settingsStore); got != 0 {
		t.Errorf("expected 0 for an explicitly-saved 0, got %v", got)
	}
}

func TestLoadInterval_StoredValueRoundTrips(t *testing.T) {
	_, settingsStore, _ := newTestScanStores(t)
	ctx := context.Background()
	if err := settingsStore.Set(ctx, IntervalSettingKey, "1800"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := LoadInterval(ctx, settingsStore); got != 1800*time.Second {
		t.Errorf("expected 1800s, got %v", got)
	}
}

func TestLoadInterval_NonPositiveIsZero(t *testing.T) {
	_, settingsStore, _ := newTestScanStores(t)
	ctx := context.Background()
	for _, v := range []string{"0", "-5", "not-a-number", ""} {
		if err := settingsStore.Set(ctx, IntervalSettingKey, v); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := LoadInterval(ctx, settingsStore); got != 0 {
			t.Errorf("value %q: expected 0, got %v", v, got)
		}
	}
}

func TestToMatchedRelease_SceneVsMovieTypeDispatch(t *testing.T) {
	scene := identify.MatchResult{Title: "A Scene", Type: "scene", SceneID: "1", Box: "tpdb"}
	if got := toMatchedRelease(RowScene, scene, "raw release title"); got.RowType != RowScene {
		t.Errorf("expected RowScene, got %v", got.RowType)
	}

	movie := identify.MatchResult{Title: "A Movie", Type: "movie", SceneID: "2", Box: "tpdb"}
	if got := toMatchedRelease(RowMovie, movie, "raw release title"); got.RowType != RowMovie {
		t.Errorf("expected RowMovie, got %v", got.RowType)
	}
}

// TestToMatchedRelease_MapsRuntimeSeconds is the regression test for the
// live "no available downloads" bug: RuntimeSeconds wasn't mapped onto
// EntityDurationSeconds at all until this fix, so every cached entity
// carried a 0 duration regardless of what the identify pipeline actually
// found.
func TestToMatchedRelease_MapsRuntimeSeconds(t *testing.T) {
	m := identify.MatchResult{Title: "A Scene", RuntimeSeconds: 1800}
	got := toMatchedRelease(RowScene, m, "raw release title")
	if got.EntityDurationSeconds != 1800 {
		t.Errorf("EntityDurationSeconds = %d, want 1800", got.EntityDurationSeconds)
	}
}

// TestToMatchedRelease_MapsFirstSeenReleaseTitle is the regression test for
// the live "no available downloads" bug's second cause: a Grab-time search
// reconstructed from TPDB's own studio+title metadata could legitimately
// find zero raw Prowlarr results even when the release that triggered the
// match was real, since TPDB's title text includes tokens (e.g. "S6:E10")
// real indexer filenames never contain. Storing the raw release title that
// actually matched, and reusing it as the Grab-time query, closes that gap.
func TestToMatchedRelease_MapsFirstSeenReleaseTitle(t *testing.T) {
	m := identify.MatchResult{Title: "A Scene"}
	got := toMatchedRelease(RowScene, m, "Studio.23.04.22.Performer.Scene.Title.XXX.1080p-GROUP")
	if got.FirstSeenReleaseTitle != "Studio.23.04.22.Performer.Scene.Title.XXX.1080p-GROUP" {
		t.Errorf("FirstSeenReleaseTitle = %q, want the raw release title", got.FirstSeenReleaseTitle)
	}
}

func TestToMatchedRelease_SplitsCommaJoinedTags(t *testing.T) {
	m := identify.MatchResult{Title: "T", Tags: "Anal Fetish,MILF,Goth"}
	got := toMatchedRelease(RowScene, m, "raw release title")
	want := []string{"Anal Fetish", "MILF", "Goth"}
	if len(got.Genres) != len(want) {
		t.Fatalf("expected %v, got %v", want, got.Genres)
	}
	for i, g := range want {
		if got.Genres[i] != g {
			t.Errorf("expected genres %v, got %v", want, got.Genres)
			break
		}
	}
}

func TestToMatchedRelease_EmptyTagsYieldsNilGenres(t *testing.T) {
	m := identify.MatchResult{Title: "T"}
	got := toMatchedRelease(RowScene, m, "raw release title")
	if len(got.Genres) != 0 {
		t.Errorf("expected no genres, got %v", got.Genres)
	}
}

func TestToMatchedRelease_SplitsCommaJoinedPerformers(t *testing.T) {
	m := identify.MatchResult{Title: "T", Performers: "Jane Doe,John Roe"}
	got := toMatchedRelease(RowScene, m, "raw release title")
	want := []string{"Jane Doe", "John Roe"}
	if len(got.Performers) != len(want) {
		t.Fatalf("expected %v, got %v", want, got.Performers)
	}
	for i, p := range want {
		if got.Performers[i] != p {
			t.Errorf("expected performers %v, got %v", want, got.Performers)
			break
		}
	}
}

func TestToMatchedRelease_EmptyPerformersYieldsNilPerformers(t *testing.T) {
	m := identify.MatchResult{Title: "T"}
	got := toMatchedRelease(RowScene, m, "raw release title")
	if len(got.Performers) != 0 {
		t.Errorf("expected no performers, got %v", got.Performers)
	}
}

// TestRunCycle_NoProwlarrConfigured_SkipsCleanly mirrors runCycle's own
// documented fault-isolation contract: with nothing configured at all, the
// cycle must return without error and without writing anything.
func TestRunCycle_NoProwlarrConfigured_SkipsCleanly(t *testing.T) {
	connStore, settingsStore, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	runCycle(ctx, &http.Client{Timeout: time.Second}, connStore, nil, settingsStore, releaseStore, nil)

	list, err := releaseStore.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected no matched entities with no prowlarr configured, got %+v", list)
	}
}

// TestRunCycle_ProwlarrConfiguredButNoAI_SkipsCleanly confirms sess.Identify
// being nil (no AI provider configured) skips the whole cycle rather than
// panicking on a nil pipeline — the same fault-isolation shape as the
// no-Prowlarr case.
func TestRunCycle_ProwlarrConfiguredButNoAI_SkipsCleanly(t *testing.T) {
	connStore, settingsStore, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	prow := fakeProwlarr(t, `[{"guid":"g1","title":"Some.Studio.Some.Scene.XXX.1080p","protocol":"torrent","seeders":5}]`)
	if err := connStore.Upsert(ctx, "prowlarr", prow.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	runCycle(ctx, prow.Client(), connStore, nil, settingsStore, releaseStore, nil)

	list, err := releaseStore.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected no matched entities with no AI configured, got %+v", list)
	}
	// Nothing should be marked seen either — the cycle bailed before ever
	// reaching a release.
	seen, err := releaseStore.SeenGUIDs(ctx, []string{"g1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seen["g1"] {
		t.Errorf("expected g1 not to be marked seen when the cycle skipped before processing")
	}
}

// TestRunCycle_UnmatchedReleaseIsMarkedSeenButNotCached is the core
// dedup-without-a-cache-row contract this package's schema doc comment
// describes: a release the AI can't parse a title from is marked seen (so
// it's never retried) but produces no adult_newest_releases row (so it never
// appears on Discover).
func TestRunCycle_UnmatchedReleaseIsMarkedSeenButNotCached(t *testing.T) {
	connStore, settingsStore, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	prow := fakeProwlarr(t, `[{"guid":"g-unmatched","title":"garbage","protocol":"torrent","seeders":5}]`)
	ollama := fakeOllama(t, `{"studio":null,"title":null,"performers":null}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)
	if err := connStore.Upsert(ctx, "prowlarr", prow.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	runCycle(ctx, prow.Client(), connStore, nil, settingsStore, releaseStore, nil)

	list, err := releaseStore.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected no cache row for an unmatched release, got %+v", list)
	}

	seen, err := releaseStore.SeenGUIDs(ctx, []string{"g-unmatched"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !seen["g-unmatched"] {
		t.Errorf("expected the unmatched release to be marked seen so it isn't retried")
	}
}

// TestRunCycle_SeenReleaseIsNotReprocessed proves the dedup gate: a release
// already in adult_newest_seen from a prior cycle is skipped entirely on the
// next cycle, even though it's still present in Prowlarr's result set.
func TestRunCycle_SeenReleaseIsNotReprocessed(t *testing.T) {
	connStore, settingsStore, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	if err := releaseStore.MarkSeen(ctx, "g-already-seen"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Ollama would fail this test (by t.Fatal-ing the whole process is too
	// harsh for a background job's fault isolation) if it were ever hit —
	// use a server that fails the request, proving runCycle never reaches
	// the identify pipeline for a release it's already seen.
	failingOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failingOllama.Close)
	configureAI(t, ctx, connStore, settingsStore, failingOllama.URL)

	prow := fakeProwlarr(t, `[{"guid":"g-already-seen","title":"Some Title","protocol":"torrent","seeders":5}]`)
	if err := connStore.Upsert(ctx, "prowlarr", prow.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	// Must not panic or error out despite the identify pipeline being
	// configured to fail every call — the seen release should never reach it.
	runCycle(ctx, prow.Client(), connStore, nil, settingsStore, releaseStore, nil)

	list, err := releaseStore.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected no cache rows, got %+v", list)
	}
}

// rawEntityTitles reads entity_title values straight from the table for a row
// type, ORDER BY entity_title — bypassing List's US-4 linked-scene filter so a
// US-1 test can assert exactly which performer/studio ROWS physically exist,
// independent of whether they'd currently list. s.db is reachable in-package.
func rawEntityTitles(t *testing.T, s *ReleaseStore, rowType RowType) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT entity_title FROM adult_newest_releases WHERE row_type = ? ORDER BY entity_title`, string(rowType))
	if err != nil {
		t.Fatalf("reading raw %s rows: %v", rowType, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatalf("scanning raw %s row: %v", rowType, err)
		}
		out = append(out, title)
	}
	return out
}

// tpdbSceneStudioPerformerFake serves a TPDB REST instance that returns a scene
// for the given scene title (so a scene ROW is created), confirms each name in
// sites/performers, and returns empty /movies. Used by the US-1 tests that need
// the identify pipeline to actually persist a scene row so the studio/performer
// append path fires.
func tpdbSceneStudioPerformerFake(t *testing.T, sceneTitle string, sites, performers map[string]bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("q")
		switch r.URL.Path {
		case "/scenes":
			if q == sceneTitle {
				fmt.Fprintf(w, `{"data":[{"_id":"scene1","title":%q,"site":{"name":"Real Studio"},"date":"2020-01-01","duration":1800}]}`, sceneTitle)
				return
			}
			fmt.Fprint(w, `{"data":[]}`)
		case "/sites":
			if sites[q] {
				fmt.Fprintf(w, `{"data":[{"_id":1,"name":%q,"logo":"https://cdn.theporndb.net/sites/fake-logo.png"}]}`, q)
				return
			}
			fmt.Fprint(w, `{"data":[]}`)
		case "/performers":
			if performers[q] {
				fmt.Fprintf(w, `{"data":[{"_id":1,"name":%q,"image":"https://cdn.theporndb.net/performer/fake.jpg"}]}`, q)
				return
			}
			fmt.Fprint(w, `{"data":[]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRunCycle_UnconfirmedStudioAndPerformerGuessesAreSkipped is the
// regression test for a real bug caught live in production during this
// feature's own deploy verification: a real scan produced Studio/Performer
// cards for "And", "Clouds", and a full raw scene title mis-parsed as a
// "studio" — none of them real entities, all AI extraction artifacts that
// verifyStudio/verifyPerformers fell back to returning uncorrected (a
// pre-existing, deliberate choice there — see StudioName/PerformerImage's
// doc comments) because nothing in any configured database confirmed them.
// Only a name StudioImage/PerformerImage can actually confirm (i.e. finds a
// real image for) should ever become a cached Studio/Performer row.
//
// Post-US-1 this test also proves the working case is unregressed: a scene IS
// matched here (so the studio/performer append path fires), and the confirmed
// studio/performer are still cached exactly as before. Asserted via the raw
// table (rawEntityTitles) so the check is about which rows exist, decoupled from
// US-4's list-time linked-scene filter.
func TestRunCycle_UnconfirmedStudioAndPerformerGuessesAreSkipped(t *testing.T) {
	connStore, settingsStore, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	tpdb := tpdbSceneStudioPerformerFake(t, "Some Scene Title",
		map[string]bool{"Real Studio": true},
		map[string]bool{"Real Performer": true},
	)
	overrideTPDBBaseURL(t, tpdb.URL)
	if err := connStore.Upsert(ctx, "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("configuring tpdb: %v", err)
	}

	ollama := fakeOllama(t, `{"studio":"Real Studio","title":"Some Scene Title","performers":["Real Performer","Garbage Fragment"]}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	prow := fakeProwlarr(t, `[{"guid":"g-mixed","title":"Real.Studio.Some.Scene.Title.Real.Performer.And.Garbage.Fragment.XXX","protocol":"torrent","seeders":5}]`)
	if err := connStore.Upsert(ctx, "prowlarr", prow.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	runCycle(ctx, prow.Client(), connStore, nil, settingsStore, releaseStore, nil)

	// A scene row was persisted, so the studio/performer append path ran.
	if got := rawEntityTitles(t, releaseStore, RowScene); len(got) != 1 {
		t.Fatalf("expected the confirmed scene to be cached, got %v", got)
	}
	if got := rawEntityTitles(t, releaseStore, RowStudio); len(got) != 1 || got[0] != "Real Studio" {
		t.Errorf("expected only the confirmed studio to be cached, got %v", got)
	}
	if got := rawEntityTitles(t, releaseStore, RowPerformer); len(got) != 1 || got[0] != "Real Performer" {
		t.Errorf("expected only the confirmed performer (Garbage Fragment skipped), got %v", got)
	}
}

// TestMatchRelease_NoSceneRow_SkipsStudioPerformer is US-1's core orphan-
// prevention proof for the browse pass: when the identify pipeline confirms a
// studio+performer but NO scene/movie row is persisted (no scene match and no
// movie-catalog fallback), zero performer/studio rows are created — even though
// identifyStudioPerformers itself would have found a valid image for both. Before
// the fix these were appended unconditionally, producing the orphaned cards this
// whole effort exists to eliminate.
func TestMatchRelease_NoSceneRow_SkipsStudioPerformer(t *testing.T) {
	connStore, settingsStore, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	// /scenes and /movies always empty → no scene/movie row is ever persisted —
	// but /sites and /performers DO confirm the names, so identifyStudioPerformers
	// would (pre-fix) have produced orphaned rows.
	tpdb := fakeTPDB(t,
		map[string]bool{"Real Studio": true},
		map[string]bool{"Real Performer": true},
	)
	overrideTPDBBaseURL(t, tpdb.URL)
	if err := connStore.Upsert(ctx, "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("configuring tpdb: %v", err)
	}

	ollama := fakeOllama(t, `{"studio":"Real Studio","title":"Some Scene Title","performers":["Real Performer"]}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	prow := fakeProwlarr(t, `[{"guid":"g-noscene","title":"Real.Studio.Some.Scene.Title.Real.Performer.XXX","protocol":"torrent","seeders":5}]`)
	if err := connStore.Upsert(ctx, "prowlarr", prow.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	runCycle(ctx, prow.Client(), connStore, nil, settingsStore, releaseStore, nil)

	if got := rawEntityTitles(t, releaseStore, RowScene); len(got) != 0 {
		t.Errorf("expected no scene row, got %v", got)
	}
	if got := rawEntityTitles(t, releaseStore, RowMovie); len(got) != 0 {
		t.Errorf("expected no movie row, got %v", got)
	}
	if got := rawEntityTitles(t, releaseStore, RowStudio); len(got) != 0 {
		t.Errorf("expected NO studio row when no scene/movie was persisted (US-1), got %v", got)
	}
	if got := rawEntityTitles(t, releaseStore, RowPerformer); len(got) != 0 {
		t.Errorf("expected NO performer row when no scene/movie was persisted (US-1), got %v", got)
	}
}

// TestMatchRelease_SceneMatchWithNoConfirmedRelease_IsNotCached is the
// regression test for a real gap found live in production, 2026-07-15: a
// release can genuinely fuzzy-match a real TPDB scene (IdentifyDetailed
// succeeds) while that scene's CANONICAL title+studio finds zero results on
// a live, literal Prowlarr search — e.g. a studio whose content is only
// ever released as multi-scene compilation packs, never as the single scene
// TPDB catalogs separately. Caching that scene produced a Discover card
// Grab could never fulfill. confirmAvailable must run the same search a
// later Grab click would run and skip caching when it finds nothing.
func TestMatchRelease_SceneMatchWithNoConfirmedRelease_IsNotCached(t *testing.T) {
	connStore, settingsStore, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	tpdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("q")
		switch r.URL.Path {
		case "/sites":
			if q == "Some Studio" {
				fmt.Fprint(w, `{"data":[{"_id":1,"name":"Some Studio","logo":"https://cdn.theporndb.net/logo.png"}]}`)
				return
			}
			fmt.Fprint(w, `{"data":[]}`)
		case "/scenes":
			if q == "Some Scene Title" {
				fmt.Fprint(w, `{"data":[{"_id":"scene1","title":"Some Scene Title","site":{"name":"Some Studio"},"date":"2020-01-01"}]}`)
				return
			}
			fmt.Fprint(w, `{"data":[]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	t.Cleanup(tpdb.Close)
	overrideTPDBBaseURL(t, tpdb.URL)
	if err := connStore.Upsert(ctx, "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("configuring tpdb: %v", err)
	}

	ollama := fakeOllama(t, `{"studio":"Some Studio","title":"Some Scene Title","performers":[]}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	// Prowlarr: the bare-browse "newest releases" call (query="") returns
	// one raw release — that's how IdentifyDetailed gets a title to parse
	// at all. The confirmAvailable search (a real, non-empty normalized
	// query) returns EMPTY — the exact asymmetry this fix closes.
	var prowlarrQueries []string
	prowlarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		prowlarrQueries = append(prowlarrQueries, q)
		w.Header().Set("Content-Type", "application/json")
		if q == "" {
			fmt.Fprint(w, `[{"guid":"g1","title":"Raw.Release.Title.That.Fuzzy.Matched","protocol":"torrent","seeders":5}]`)
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(prowlarrSrv.Close)
	if err := connStore.Upsert(ctx, "prowlarr", prowlarrSrv.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	runCycle(ctx, prowlarrSrv.Client(), connStore, nil, settingsStore, releaseStore, nil)

	scenes, err := releaseStore.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scenes) != 0 {
		t.Errorf("expected no scene cached (canonical title found nothing on the confirmation search), got %+v", scenes)
	}

	confirmSearchHappened := false
	for _, q := range prowlarrQueries {
		if q != "" {
			confirmSearchHappened = true
		}
	}
	if !confirmSearchHappened {
		t.Error("expected a second, normalized-query confirmation search to have actually been made")
	}
}

// TestMatchRelease_SceneMatchWithConfirmedRelease_IsCached is the positive
// counterpart to the test above — when the canonical title+studio search
// DOES find a release, the scene is cached normally.
func TestMatchRelease_SceneMatchWithConfirmedRelease_IsCached(t *testing.T) {
	connStore, settingsStore, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	tpdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("q")
		switch r.URL.Path {
		case "/sites":
			if q == "Some Studio" {
				fmt.Fprint(w, `{"data":[{"_id":1,"name":"Some Studio","logo":"https://cdn.theporndb.net/logo.png"}]}`)
				return
			}
			fmt.Fprint(w, `{"data":[]}`)
		case "/scenes":
			if q == "Some Scene Title" {
				fmt.Fprint(w, `{"data":[{"_id":"scene1","title":"Some Scene Title","site":{"name":"Some Studio"},"date":"2020-01-01","duration":1800}]}`)
				return
			}
			fmt.Fprint(w, `{"data":[]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	t.Cleanup(tpdb.Close)
	overrideTPDBBaseURL(t, tpdb.URL)
	if err := connStore.Upsert(ctx, "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("configuring tpdb: %v", err)
	}

	ollama := fakeOllama(t, `{"studio":"Some Studio","title":"Some Scene Title","performers":[]}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	prowlarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Both the bare browse AND the confirmation search return a real
		// release here — the confirmation search doesn't need to find the
		// SAME release, just something.
		fmt.Fprint(w, `[{"guid":"g1","title":"Some.Studio.Some.Scene.Title.XXX.1080p","protocol":"torrent","seeders":5}]`)
	}))
	t.Cleanup(prowlarrSrv.Close)
	if err := connStore.Upsert(ctx, "prowlarr", prowlarrSrv.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	runCycle(ctx, prowlarrSrv.Client(), connStore, nil, settingsStore, releaseStore, nil)

	scenes, err := releaseStore.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scenes) != 1 || scenes[0].EntityTitle != "Some Scene Title" {
		t.Errorf("expected the confirmed scene to be cached, got %+v", scenes)
	}
	if scenes[0].FirstSeenReleaseTitle != "Some.Studio.Some.Scene.Title.XXX.1080p" {
		t.Errorf("FirstSeenReleaseTitle = %q, want the raw release title that triggered the match", scenes[0].FirstSeenReleaseTitle)
	}
}

// TestMatchRelease_CaseDivergentStudioName_StillLinksToScene is the
// end-to-end proof (real matchRelease pipeline, not hand-constructed
// predicate inputs) for the case/whitespace-insensitive join hardening in
// performerLinkPredicate/studioLinkPredicate (releases.go) and migration
// 0051. A scene row's entity_studio and a studio row's entity_id come from
// two genuinely different sources inside the SAME identify pipeline run:
//   - the matched scene object's own site name (toMatchedRelease's m.Studio,
//     here "Vixen Media", from a live-shaped TPDB /scenes response) — see
//     identify.MatchResult.Studio.
//   - verifyStudio's independent normalized-fallback guess (detail.StudioName,
//     here lowercase "vixen media") when TPDB's /sites search finds no match
//     for the AI's raw guess — the exact divergence
//     TestIdentifyDetailed_SceneMatchAlsoReturnsCorrectedStudioAndPerformers
//     (internal/identify/identify_test.go) proves happens for real.
//
// Neither IdentifyDetailed nor matchRelease reconciles the casing between
// these two sources, so before the TRIM(LOWER(...)) hardening, a
// byte-exact join would treat the studio row as unlinked from its own
// triggering scene — silently hiding it from the US-4 browse-strip filter
// and from the US-2 drill-down (ScenesLinkedToEntity). This test drives the
// full chain (AI parse -> verifyStudio -> TPDB scene search ->
// confirmAvailable -> identifyStudioPerformers) via runCycle and asserts the
// join actually holds despite the casing difference. If
// studioLinkPredicate/performerLinkPredicate are reverted to a byte-exact
// `=` comparison, this test fails.
func TestMatchRelease_CaseDivergentStudioName_StillLinksToScene(t *testing.T) {
	connStore, settingsStore, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	const sceneTitle = "Some Scene Title"
	const sceneStudio = "Vixen Media"    // scene's own site name -> toMatchedRelease's EntityStudio
	const fallbackStudio = "vixen media" // verifyStudio's normalized-fallback -> detail.StudioName

	// The TPDB fake distinguishes verifyStudio's OWN /sites lookup for
	// fallbackStudio (must come back empty, forcing the normalized-fallback
	// path rather than a box-canonical name) from identifyStudioPerformers'
	// LATER StudioImage confirmation lookup for that same (already-fallback)
	// name, which must succeed — otherwise no studio row is ever cached at
	// all, and this test couldn't prove anything about its linkage. Both
	// calls query the identical string ("vixen media"), so a call counter is
	// what distinguishes them, matching the real pipeline's call order.
	var sitesCallsMu sync.Mutex
	sitesCalls := 0
	tpdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query().Get("q")
		switch r.URL.Path {
		case "/scenes":
			if q == sceneTitle {
				fmt.Fprintf(w, `{"data":[{"_id":"scene1","title":%q,"site":{"name":%q},"date":"2020-01-01","duration":1800}]}`, sceneTitle, sceneStudio)
				return
			}
			fmt.Fprint(w, `{"data":[]}`)
		case "/sites":
			if q != fallbackStudio {
				fmt.Fprint(w, `{"data":[]}`)
				return
			}
			sitesCallsMu.Lock()
			sitesCalls++
			call := sitesCalls
			sitesCallsMu.Unlock()
			if call == 1 {
				// verifyStudio's own lookup: no match, so it falls through
				// to rejectNonStudioGuess(cleaned) instead of a box-canonical
				// name.
				fmt.Fprint(w, `{"data":[]}`)
				return
			}
			// identifyStudioPerformers' later StudioImage confirmation call:
			// DOES find an entity for the fallback name — this is what lets
			// a studio row get cached at all, carrying the divergent casing.
			fmt.Fprintf(w, `{"data":[{"_id":1,"name":%q,"logo":"https://cdn.theporndb.net/sites/fake-logo.png"}]}`, fallbackStudio)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	t.Cleanup(tpdb.Close)
	overrideTPDBBaseURL(t, tpdb.URL)
	if err := connStore.Upsert(ctx, "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("configuring tpdb: %v", err)
	}

	ollama := fakeOllama(t, `{"studio":"vixen.media","title":"Some Scene Title","performers":[]}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	prow := fakeProwlarr(t, `[{"guid":"g-case","title":"Vixen.Media.Some.Scene.Title.XXX.1080p","protocol":"torrent","seeders":5}]`)
	if err := connStore.Upsert(ctx, "prowlarr", prow.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	runCycle(ctx, prow.Client(), connStore, nil, settingsStore, releaseStore, nil)

	scenes, err := releaseStore.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scenes) != 1 || scenes[0].EntityStudio != sceneStudio {
		t.Fatalf("expected one scene cached with EntityStudio %q, got %+v", sceneStudio, scenes)
	}

	// Bypass the US-4 linkage filter to confirm the studio ROW itself exists
	// with the divergent-case entity_id, independent of whether it currently
	// links/lists.
	if got := rawEntityTitles(t, releaseStore, RowStudio); len(got) != 1 || got[0] != fallbackStudio {
		t.Fatalf("expected one studio row with the divergent-case fallback name %q, got %v", fallbackStudio, got)
	}

	// The critical assertion: the case-divergent join actually links the
	// studio to its triggering scene.
	linked, err := releaseStore.ScenesLinkedToEntity(ctx, RowStudio, fallbackStudio)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(linked) != 1 || linked[0].EntityStudio != sceneStudio {
		t.Fatalf("expected ScenesLinkedToEntity(%q) to return the scene despite the casing difference, got %+v", fallbackStudio, linked)
	}

	// And the US-4 browse-strip listing (List, not the raw-table bypass
	// above) must include the studio row too — proving it isn't hidden by
	// the live linkage filter despite the casing difference.
	studios, err := releaseStore.List(ctx, RowStudio, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(studios) != 1 || studios[0].EntityID != fallbackStudio {
		t.Fatalf("expected the studio to survive the US-4 browse-strip linkage filter despite the casing difference, got %+v", studios)
	}
}
