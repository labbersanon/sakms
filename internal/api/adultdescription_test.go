package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// getAdultDescription fires GET /api/modes/adult/discover/description with the
// given params (a param with an empty value is omitted entirely, so a test can
// exercise the absent-param path) and returns the response plus its RAW body —
// raw, because TestAdultDescriptionNoDataReturns200Empty asserts the exact
// on-the-wire shape, which a decode would hide.
func getAdultDescription(t *testing.T, base string, params map[string]string) (*http.Response, string) {
	t.Helper()
	q := url.Values{}
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	resp, err := http.Get(base + "/api/modes/adult/discover/description?" + q.Encode())
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp, string(body)
}

// decodeDescription decodes the AdultDescription DTO out of a raw body.
func decodeDescription(t *testing.T, body string) apidto.AdultDescription {
	t.Helper()
	var out apidto.AdultDescription
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	return out
}

// descTPDBFake routes every TPDB REST endpoint this endpoint can reach, tallying
// each one so a test can assert the exact upstream call count (§2.2's structural
// bound) rather than merely "it worked":
//
//	sceneCalls     GET /scenes/{id}      (SceneDescription, box=tpdb)
//	sitesCalls     GET /sites            (studio exact-name resolve)
//	siteByIDCalls  GET /sites/{id}       (studio detail → Description)
//	perfCalls      GET /performers       (performer exact-name resolve)
//	perfByIDCalls  GET /performers/{id}  (performer detail → Bio)
//
// Every counter is nil-safe. The names it resolves are fixed ("Brazzers" /
// "Riley Reid"), matching resolveEntityInBox's exact-name rule.
type descTPDBCounters struct {
	sceneCalls    int32
	sitesCalls    int32
	siteByIDCalls int32
	perfCalls     int32
	perfByIDCalls int32
}

func (c *descTPDBCounters) total() int32 {
	return atomic.LoadInt32(&c.sceneCalls) + atomic.LoadInt32(&c.sitesCalls) +
		atomic.LoadInt32(&c.siteByIDCalls) + atomic.LoadInt32(&c.perfCalls) +
		atomic.LoadInt32(&c.perfByIDCalls)
}

func descTPDBFake(c *descTPDBCounters) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(p, "/scenes/"):
			atomic.AddInt32(&c.sceneCalls, 1)
			w.Write([]byte(`{"data":{"_id":"sc1","title":"Some Scene","description":"a tpdb scene description"}}`))
		case p == "/sites":
			atomic.AddInt32(&c.sitesCalls, 1)
			w.Write([]byte(`{"data":[{"uuid":"site1","name":"Brazzers"}]}`))
		case strings.HasPrefix(p, "/sites/"):
			atomic.AddInt32(&c.siteByIDCalls, 1)
			w.Write([]byte(`{"data":{"uuid":"site1","name":"Brazzers","description":"a tpdb studio description"}}`))
		case p == "/performers":
			atomic.AddInt32(&c.perfCalls, 1)
			w.Write([]byte(`{"data":[{"_id":"p1","name":"Riley Reid"}]}`))
		case strings.HasPrefix(p, "/performers/"):
			atomic.AddInt32(&c.perfByIDCalls, 1)
			w.Write([]byte(`{"data":{"_id":"p1","name":"Riley Reid","bio":"a tpdb performer bio"}}`))
		default:
			w.Write([]byte(`{"data":[]}`))
		}
	}
}

// startDescTPDB points tpdbrest.DefaultBaseURL at a counting fake for one test
// and restores it afterwards.
func startDescTPDB(t *testing.T) *descTPDBCounters {
	t.Helper()
	orig := tpdbrest.DefaultBaseURL
	t.Cleanup(func() { tpdbrest.DefaultBaseURL = orig })
	counters := &descTPDBCounters{}
	srv := httptest.NewServer(descTPDBFake(counters))
	t.Cleanup(srv.Close)
	tpdbrest.DefaultBaseURL = srv.URL
	return counters
}

// startDescStashDB points stashbox.StashDBURL at a counting GraphQL fake
// answering findScene, and restores it afterwards.
func startDescStashDB(t *testing.T) *int32 {
	t.Helper()
	orig := stashbox.StashDBURL
	t.Cleanup(func() { stashbox.StashDBURL = orig })
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"findScene":{"id":"uuid-1","title":"Some Scene","details":"a stash-box scene detail"}}}`))
	}))
	t.Cleanup(srv.Close)
	stashbox.StashDBURL = srv.URL
	return &calls
}

// seedPerformerPoolBox is seedStudioPoolBox's performer sibling — a pool row so
// EntityBox(performer, name) resolves to entity_source.
func seedPerformerPoolBox(t *testing.T, store *adultnewest.ReleaseStore, name, source string) {
	t.Helper()
	if err := store.Insert(context.Background(), adultnewest.MatchedRelease{
		RowType:         adultnewest.RowPerformer,
		EntityID:        name,
		EntitySource:    source,
		EntityTitle:     name,
		BrowseConfirmed: true,
	}); err != nil {
		t.Fatalf("seeding performer pool box: %v", err)
	}
}

// --- kind validation: the ONE and ONLY 400 -----------------------------------

// TestAdultDescriptionBadKind pins that a malformed or absent kind is the sole
// non-200 this endpoint ever returns. Every other bad input is a no-data
// condition (see the 200-empty tests below), because the frontend has no
// ErrorBoundary and a non-2xx poisons the calling Solid resource.
func TestAdultDescriptionBadKind(t *testing.T) {
	srv, _, _ := newestScenesMuxWithReleaseStore(t)

	for _, kind := range []string{"", "movie", "Scene", "performers", "prowlarr"} {
		resp, _ := getAdultDescription(t, srv.URL, map[string]string{"kind": kind, "name": "Brazzers"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("kind=%q: expected 400, got %d", kind, resp.StatusCode)
		}
	}
}

// --- scene kind ---------------------------------------------------------------

// TestAdultDescriptionSceneTPDB is the happy path for a TPDB-sourced scene:
// 200, the description, source echoing the box actually consulted, and EXACTLY
// ONE upstream call.
func TestAdultDescriptionSceneTPDB(t *testing.T) {
	tpdb := startDescTPDB(t)
	stashCalls := startDescStashDB(t)

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)
	seed.setStashdb(t)

	resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "scene", "source": "tpdb", "id": "sc1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, body)
	}
	got := decodeDescription(t, body)
	if got.Text != "a tpdb scene description" || got.Source != "tpdb" {
		t.Errorf("expected the TPDB description + source tpdb, got %+v", got)
	}
	if n := atomic.LoadInt32(&tpdb.sceneCalls); n != 1 {
		t.Errorf("expected EXACTLY ONE GET /scenes/{id}, got %d", n)
	}
	if n := tpdb.total(); n != 1 {
		t.Errorf("expected exactly one TPDB call in total (no fan-out), got %d", n)
	}
	// AC4: the OTHER configured box must never be consulted.
	if n := atomic.LoadInt32(stashCalls); n != 0 {
		t.Errorf("expected zero stash-box calls for a tpdb-sourced scene, got %d", n)
	}
}

// TestAdultDescriptionSceneStashBox is the stash-box sibling: findScene's
// details, source stashdb, and the TPDB fake untouched.
func TestAdultDescriptionSceneStashBox(t *testing.T) {
	tpdb := startDescTPDB(t)
	stashCalls := startDescStashDB(t)

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)
	seed.setStashdb(t)

	resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "scene", "source": "stashdb", "id": "uuid-1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, body)
	}
	got := decodeDescription(t, body)
	if got.Text != "a stash-box scene detail" || got.Source != "stashdb" {
		t.Errorf("expected the stash-box details + source stashdb, got %+v", got)
	}
	if n := atomic.LoadInt32(stashCalls); n != 1 {
		t.Errorf("expected EXACTLY ONE findScene, got %d", n)
	}
	if n := tpdb.total(); n != 0 {
		t.Errorf("expected zero TPDB calls for a stashdb-sourced scene, got %d", n)
	}
}

// TestAdultDescriptionUnknownSceneSourceReturns200Empty is the Critic C-2
// regression guard, and the single most load-bearing test in this file.
//
// A Show More drill-down scene is emitted with Source "prowlarr" and no ID at
// all (adultdiscover_newest_scenes.go's page>1 mapping), and those items open
// DetailPopup through this exact endpoint. The original design specified 400 for
// an unrecognised source — which would have fired on EVERY Show More scene's
// popup open, silently swallowed by the frontend's catch. It must be 200-empty,
// with ZERO upstream calls.
func TestAdultDescriptionUnknownSceneSourceReturns200Empty(t *testing.T) {
	tpdb := startDescTPDB(t)
	stashCalls := startDescStashDB(t)

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)
	seed.setStashdb(t)

	// "prowlarr" is the real, shipped value; the others are the same class of
	// input arriving malformed.
	for _, source := range []string{"prowlarr", "rss", "TPDB", "tpdb "} {
		resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "scene", "source": source, "id": "sc1"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("source=%q: expected 200 (never 400), got %d (%s)", source, resp.StatusCode, body)
		}
		if strings.TrimSpace(body) != `{"text":"","source":""}` {
			t.Errorf("source=%q: expected exactly {\"text\":\"\",\"source\":\"\"}, got %s", source, body)
		}
	}
	if n := tpdb.total(); n != 0 {
		t.Errorf("expected ZERO TPDB calls for an unrecognised source, got %d", n)
	}
	if n := atomic.LoadInt32(stashCalls); n != 0 {
		t.Errorf("expected ZERO stash-box calls for an unrecognised source, got %d", n)
	}
}

// TestAdultDescriptionEmptySceneIdReturns200Empty — a Show More scene carries no
// ID either, so the missing-id path must be a no-data condition too.
func TestAdultDescriptionEmptySceneIdReturns200Empty(t *testing.T) {
	tpdb := startDescTPDB(t)

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)

	resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "scene", "source": "tpdb"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, body)
	}
	if strings.TrimSpace(body) != `{"text":"","source":""}` {
		t.Errorf("expected an empty DTO, got %s", body)
	}
	if n := tpdb.total(); n != 0 {
		t.Errorf("expected zero upstream calls for an empty id, got %d", n)
	}
}

// TestAdultDescriptionSceneMissingSource — kind=scene with no source at all is
// 200-empty, NOT 400, for the same reason as the unrecognised-source case.
func TestAdultDescriptionSceneMissingSource(t *testing.T) {
	tpdb := startDescTPDB(t)

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)

	resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "scene", "id": "sc1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, body)
	}
	if strings.TrimSpace(body) != `{"text":"","source":""}` {
		t.Errorf("expected an empty DTO, got %s", body)
	}
	if n := tpdb.total(); n != 0 {
		t.Errorf("expected zero upstream calls for an absent source, got %d", n)
	}
}

// TestAdultDescriptionNoDataReturns200Empty is AC5: a recognised box that simply
// holds nothing for this scene answers 200 with the exact wire shape
// {"text":"","source":""} — never 404, never 5xx, and never a source echo with
// no text behind it.
func TestAdultDescriptionNoDataReturns200Empty(t *testing.T) {
	orig := tpdbrest.DefaultBaseURL
	defer func() { tpdbrest.DefaultBaseURL = orig }()
	// A box that answers, but with no description — the ordinary "catalog has
	// the scene, has no prose for it" case.
	tpdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"_id":"sc1","title":"Some Scene"}}`))
	}))
	defer tpdb.Close()
	tpdbrest.DefaultBaseURL = tpdb.URL

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)

	resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "scene", "source": "tpdb", "id": "sc1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, body)
	}
	if strings.TrimSpace(body) != `{"text":"","source":""}` {
		t.Errorf("expected exactly {\"text\":\"\",\"source\":\"\"}, got %s", body)
	}
}

// TestAdultDescriptionSourceBlankWheneverTextBlank pins the R2-3 rule across
// every no-data shape at once: Source is blank whenever Text is, so a caller can
// never see a provenance claim with nothing behind it.
func TestAdultDescriptionSourceBlankWheneverTextBlank(t *testing.T) {
	startDescTPDB(t)
	startDescStashDB(t)

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)
	seed.setStashdb(t)
	// A pool row whose box is real, but for an entity TPDB won't resolve.
	seedStudioPoolBox(t, releaseStore, "Unknown Studio", "tpdb")

	cases := []map[string]string{
		{"kind": "scene", "source": "prowlarr", "id": "sc1"},
		{"kind": "scene", "source": "tpdb"},
		{"kind": "performer"},
		{"kind": "studio", "name": "Not In The Pool"},
		{"kind": "studio", "name": "Unknown Studio"},
	}
	for _, params := range cases {
		resp, body := getAdultDescription(t, srv.URL, params)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%v: expected 200, got %d (%s)", params, resp.StatusCode, body)
		}
		got := decodeDescription(t, body)
		if got.Text == "" && got.Source != "" {
			t.Errorf("%v: Source must be blank whenever Text is, got %+v", params, got)
		}
	}
}

// --- entity kinds --------------------------------------------------------------

// TestAdultDescriptionEntityByName is the newest-row drill variant: no source
// hint, so the handler resolves the box from the matched-entity pool, then
// fetches the studio description. Source reports the box actually consulted.
func TestAdultDescriptionEntityByName(t *testing.T) {
	tpdb := startDescTPDB(t)

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)
	seedStudioPoolBox(t, releaseStore, "Brazzers", "tpdb")

	resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "studio", "name": "Brazzers"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, body)
	}
	got := decodeDescription(t, body)
	if got.Text != "a tpdb studio description" || got.Source != "tpdb" {
		t.Errorf("expected the studio description + source tpdb, got %+v", got)
	}
	if n := atomic.LoadInt32(&tpdb.sitesCalls); n != 1 {
		t.Errorf("expected exactly one exact-name resolve, got %d", n)
	}
	if n := atomic.LoadInt32(&tpdb.siteByIDCalls); n != 1 {
		t.Errorf("expected exactly one site detail fetch, got %d", n)
	}
}

// TestAdultDescriptionEntityPerformerBio is the performer half of the entity
// path — resolve by exact name, then GetPerformerByID's Bio.
func TestAdultDescriptionEntityPerformerBio(t *testing.T) {
	tpdb := startDescTPDB(t)

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)
	seedPerformerPoolBox(t, releaseStore, "Riley Reid", "tpdb")

	resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "performer", "name": "Riley Reid"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, body)
	}
	got := decodeDescription(t, body)
	if got.Text != "a tpdb performer bio" || got.Source != "tpdb" {
		t.Errorf("expected the performer bio + source tpdb, got %+v", got)
	}
	if n := atomic.LoadInt32(&tpdb.perfCalls); n != 1 {
		t.Errorf("expected exactly one performer resolve, got %d", n)
	}
	if n := atomic.LoadInt32(&tpdb.perfByIDCalls); n != 1 {
		t.Errorf("expected exactly one performer detail fetch, got %d", n)
	}
}

// TestAdultDescriptionEntityNoPoolRow — no source hint and no pool row means no
// box to ask: 200-empty, zero upstream calls, short-circuited before any client
// is built.
func TestAdultDescriptionEntityNoPoolRow(t *testing.T) {
	tpdb := startDescTPDB(t)

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)

	resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "studio", "name": "Never Pooled"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, body)
	}
	if strings.TrimSpace(body) != `{"text":"","source":""}` {
		t.Errorf("expected an empty DTO, got %s", body)
	}
	if n := tpdb.total(); n != 0 {
		t.Errorf("expected zero upstream calls with no pool row, got %d", n)
	}
}

// TestAdultDescriptionEntityWithExplicitSource is the R2-4 test pinning the
// source-override branch the C-1 contract rewrite introduced: the stash-box
// drill shape, kind=performer&name=X&source=stashdb.
//
// This is the ONE case whose expectation is GATE-0-sensitive, and Wade chose
// Option A (2026-08-02): a bio is always resolved via TPDB regardless of the
// entity's own catalog source, because live introspection proves no stash-box
// endpoint has a bio/description field to query at all. So the stash-box hint is
// coerced away — TPDB is resolved and fetched, the stash-box fake receives ZERO
// requests, and Source honestly reports "tpdb" rather than echoing the caller's
// hint. (Under the rejected Option B this same case would assert 200-empty with
// zero calls to either box.)
func TestAdultDescriptionEntityWithExplicitSource(t *testing.T) {
	tpdb := startDescTPDB(t)
	stashCalls := startDescStashDB(t)

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)
	seed.setStashdb(t)
	// Deliberately NO pool row: the source hint is what supplies the box here,
	// which is exactly what this test exists to pin.

	resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "performer", "name": "Riley Reid", "source": "stashdb"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, body)
	}
	got := decodeDescription(t, body)
	if got.Text != "a tpdb performer bio" {
		t.Errorf("expected the TPDB bio despite the stashdb hint (Option A), got %+v", got)
	}
	if got.Source != "tpdb" {
		t.Errorf("expected Source to report the box ACTUALLY consulted (tpdb), not the hint, got %q", got.Source)
	}
	if n := atomic.LoadInt32(stashCalls); n != 0 {
		t.Errorf("expected ZERO stash-box requests — there is no bio field to query, got %d", n)
	}
	if n := tpdb.total(); n != 2 {
		t.Errorf("expected exactly two TPDB calls (resolve + detail) — the price Option A pays for a stash-box-sourced entity, got %d", n)
	}
}

// TestAdultDescriptionEntityMissingName — an entity kind with no name is
// 200-empty with zero upstream calls, not 400. Only kind is validated.
func TestAdultDescriptionEntityMissingName(t *testing.T) {
	tpdb := startDescTPDB(t)

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)

	for _, kind := range []string{"performer", "studio"} {
		resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": kind, "source": "tpdb"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("kind=%s: expected 200, got %d (%s)", kind, resp.StatusCode, body)
		}
		if strings.TrimSpace(body) != `{"text":"","source":""}` {
			t.Errorf("kind=%s: expected an empty DTO, got %s", kind, body)
		}
	}
	if n := tpdb.total(); n != 0 {
		t.Errorf("expected zero upstream calls with no name, got %d", n)
	}
}

// --- structural bounds ----------------------------------------------------------

// TestAdultDescriptionOneUpstreamCall pins §2.2's call budget with EXACT counts,
// so a future fan-out is caught rather than merely tolerated: one call for a
// scene, two for an entity (an exact-name resolve, then a detail fetch — the
// count Option A pays even for a stash-box-sourced entity).
func TestAdultDescriptionOneUpstreamCall(t *testing.T) {
	t.Run("scene", func(t *testing.T) {
		tpdb := startDescTPDB(t)
		srv, seed, _ := newestScenesMuxWithReleaseStore(t)
		seed.setTPDB(t)

		resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "scene", "source": "tpdb", "id": "sc1"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, body)
		}
		if n := tpdb.total(); n != 1 {
			t.Errorf("expected EXACTLY ONE upstream catalog call for a scene, got %d", n)
		}
	})

	t.Run("entity", func(t *testing.T) {
		tpdb := startDescTPDB(t)
		srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
		seed.setTPDB(t)
		seedStudioPoolBox(t, releaseStore, "Brazzers", "tpdb")

		resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "studio", "name": "Brazzers"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, body)
		}
		if n := tpdb.total(); n != 2 {
			t.Errorf("expected EXACTLY TWO upstream catalog calls for an entity (resolve + detail), got %d", n)
		}
	})
}

// TestAdultDescriptionMakesNoProwlarrCall pins the carve-out this endpoint is
// narrower than: zero Prowlarr calls, on ANY input. This is a behavioral
// guarantee, not a structural one — mode.Build still allocates sess.Prowlarr
// from the connection store when one is configured (a plain struct
// constructor, no I/O), it is simply never read or called on any path through
// this handler. Do not "verify" this rule by asserting sess.Prowlarr == nil;
// that assertion is false. This test is what actually enforces the guarantee:
// a Prowlarr connection is configured and pointed at a counting fake, then
// every reachable shape of request is fired — the counter must stay at zero
// throughout.
func TestAdultDescriptionMakesNoProwlarrCall(t *testing.T) {
	startDescTPDB(t)
	startDescStashDB(t)

	var prowlarrCalls int32
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&prowlarrCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer prowlarr.Close()

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setTPDB(t)
	seed.setStashdb(t)
	seedStudioPoolBox(t, releaseStore, "Brazzers", "tpdb")
	seedPerformerPoolBox(t, releaseStore, "Riley Reid", "stashdb")

	for _, params := range []map[string]string{
		{"kind": "scene", "source": "tpdb", "id": "sc1"},
		{"kind": "scene", "source": "stashdb", "id": "uuid-1"},
		{"kind": "scene", "source": "prowlarr", "id": "sc1"},
		{"kind": "studio", "name": "Brazzers"},
		{"kind": "performer", "name": "Riley Reid"},
		{"kind": "performer", "name": "Riley Reid", "source": "fansdb"},
	} {
		resp, body := getAdultDescription(t, srv.URL, params)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%v: expected 200, got %d (%s)", params, resp.StatusCode, body)
		}
	}
	if n := atomic.LoadInt32(&prowlarrCalls); n != 0 {
		t.Errorf("this endpoint must NEVER reach Prowlarr, got %d requests", n)
	}
}

// TestAdultDescriptionNoIdentifier — an Adult install with no catalog configured
// at all answers 200-empty rather than erroring. sess.Identify's box clients are
// all nil there, and every one of this endpoint's paths treats that as no data.
func TestAdultDescriptionNoIdentifier(t *testing.T) {
	srv, _, releaseStore := newestScenesMuxWithReleaseStore(t)
	seedStudioPoolBox(t, releaseStore, "Brazzers", "tpdb")

	for _, params := range []map[string]string{
		{"kind": "scene", "source": "tpdb", "id": "sc1"},
		{"kind": "scene", "source": "stashdb", "id": "uuid-1"},
		{"kind": "studio", "name": "Brazzers"},
	} {
		resp, body := getAdultDescription(t, srv.URL, params)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%v: expected 200 with no catalogs configured, got %d (%s)", params, resp.StatusCode, body)
		}
		if strings.TrimSpace(body) != `{"text":"","source":""}` {
			t.Errorf("%v: expected an empty DTO, got %s", params, body)
		}
	}
}

// TestAdultDescriptionOwnClientTimeoutBounds proves the handler's timeout is its
// own, not inherited: descriptionTimeout is shrunk to 300ms while the shared
// test client's own Timeout stays at 5s (testHTTPClient), and the catalog never
// responds. If the handler reused the shared client — or bounded only the
// context while a longer client Timeout stayed in place — this would block for
// seconds. It must instead return 200-empty inside the shrunk bound.
//
// Shrinking is exactly why descriptionTimeout is a var and not a const, the same
// convention newestScenesOutboundTimeout documents.
//
// "Not shared" is additionally structural rather than only asserted here:
// adultDescriptionHandler takes no *http.Client parameter at all, so unlike
// every other handler in this package there is no app-wide client in scope for
// it to reuse — it can only build its own. Giving it one would mean changing its
// signature, which this test's sibling call sites would not compile against.
func TestAdultDescriptionOwnClientTimeoutBounds(t *testing.T) {
	orig := descriptionTimeout
	defer func() { descriptionTimeout = orig }()
	descriptionTimeout = 300 * time.Millisecond

	origBase := tpdbrest.DefaultBaseURL
	defer func() { tpdbrest.DefaultBaseURL = origBase }()
	block := make(chan struct{})
	defer close(block)
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer hung.Close()
	tpdbrest.DefaultBaseURL = hung.URL

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setTPDB(t)

	start := time.Now()
	resp, body := getAdultDescription(t, srv.URL, map[string]string{"kind": "scene", "source": "tpdb", "id": "sc1"})
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200-empty when the catalog hangs, got %d (%s)", resp.StatusCode, body)
	}
	if strings.TrimSpace(body) != `{"text":"","source":""}` {
		t.Errorf("expected an empty DTO, got %s", body)
	}
	if elapsed > 3*time.Second {
		t.Errorf("handler did not return within its OWN 300ms bound (shared client Timeout is 5s): took %s", elapsed)
	}
}
