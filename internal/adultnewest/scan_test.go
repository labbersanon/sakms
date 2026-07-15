package adultnewest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/db"
	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/secrets"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

// newTestScanStores builds a connections.Store and settings.Store backed by
// the same freshly-migrated SQLite file, plus a standalone ReleaseStore —
// real SQL and real encryption, no mocks, matching internal/recheck's own
// store-test convention (see recheck_test.go's newTestStores).
func newTestScanStores(t *testing.T) (*connections.Store, *settings.Store, *ReleaseStore) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "sakms.db"))
	if err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

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

func configureAI(t *testing.T, ctx context.Context, connStore *connections.Store, settingsStore *settings.Store, ollamaURL string) {
	t.Helper()
	if err := connStore.Upsert(ctx, "ollama", ollamaURL, ""); err != nil {
		t.Fatalf("configuring ollama: %v", err)
	}
	if err := settingsStore.Set(ctx, mode.AIModelKey, "test-model"); err != nil {
		t.Fatalf("configuring ai model: %v", err)
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
	if got := toMatchedRelease(RowScene, scene); got.RowType != RowScene {
		t.Errorf("expected RowScene, got %v", got.RowType)
	}

	movie := identify.MatchResult{Title: "A Movie", Type: "movie", SceneID: "2", Box: "tpdb"}
	if got := toMatchedRelease(RowMovie, movie); got.RowType != RowMovie {
		t.Errorf("expected RowMovie, got %v", got.RowType)
	}
}

func TestToMatchedRelease_SplitsCommaJoinedTags(t *testing.T) {
	m := identify.MatchResult{Title: "T", Tags: "Anal Fetish,MILF,Goth"}
	got := toMatchedRelease(RowScene, m)
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
	got := toMatchedRelease(RowScene, m)
	if len(got.Genres) != 0 {
		t.Errorf("expected no genres, got %v", got.Genres)
	}
}

// TestRunCycle_NoProwlarrConfigured_SkipsCleanly mirrors runCycle's own
// documented fault-isolation contract: with nothing configured at all, the
// cycle must return without error and without writing anything.
func TestRunCycle_NoProwlarrConfigured_SkipsCleanly(t *testing.T) {
	connStore, settingsStore, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	runCycle(ctx, &http.Client{Timeout: time.Second}, connStore, settingsStore, releaseStore)

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

	runCycle(ctx, prow.Client(), connStore, settingsStore, releaseStore)

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

	runCycle(ctx, prow.Client(), connStore, settingsStore, releaseStore)

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
	runCycle(ctx, prow.Client(), connStore, settingsStore, releaseStore)

	list, err := releaseStore.List(ctx, RowScene, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected no cache rows, got %+v", list)
	}
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
func TestRunCycle_UnconfirmedStudioAndPerformerGuessesAreSkipped(t *testing.T) {
	connStore, settingsStore, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	tpdb := fakeTPDB(t,
		map[string]bool{"Real Studio": true},
		map[string]bool{"Real Performer": true},
	)
	if err := connStore.Upsert(ctx, "tpdb", tpdb.URL, "key"); err != nil {
		t.Fatalf("configuring tpdb: %v", err)
	}

	ollama := fakeOllama(t, `{"studio":"Real Studio","title":"Some Scene Title","performers":["Real Performer","Garbage Fragment"]}`)
	configureAI(t, ctx, connStore, settingsStore, ollama.URL)

	prow := fakeProwlarr(t, `[{"guid":"g-mixed","title":"Real.Studio.Some.Scene.Title.Real.Performer.And.Garbage.Fragment.XXX","protocol":"torrent","seeders":5}]`)
	if err := connStore.Upsert(ctx, "prowlarr", prow.URL, "key"); err != nil {
		t.Fatalf("configuring prowlarr: %v", err)
	}

	runCycle(ctx, prow.Client(), connStore, settingsStore, releaseStore)

	studios, err := releaseStore.List(ctx, RowStudio, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(studios) != 1 || studios[0].EntityTitle != "Real Studio" {
		t.Errorf("expected only the confirmed studio to be cached, got %+v", studios)
	}

	performers, err := releaseStore.List(ctx, RowPerformer, "", 1, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(performers) != 1 || performers[0].EntityTitle != "Real Performer" {
		t.Errorf("expected only the confirmed performer to be cached (Garbage Fragment must be skipped), got %+v", performers)
	}
}
