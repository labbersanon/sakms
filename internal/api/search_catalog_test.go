package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// getAdultSearch fires GET /api/modes/adult/search for one page and decodes the
// {items, hasMore} envelope. page<1 omits the param (exercises default-to-1).
func getAdultSearch(t *testing.T, base, q string, page int) (*http.Response, apidto.AdultSearchScenesPage) {
	t.Helper()
	u := base + "/api/modes/adult/search?q=" + url.QueryEscape(q)
	if page >= 1 {
		u += "&page=" + strconv.Itoa(page)
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp, apidto.AdultSearchScenesPage{}
	}
	var out apidto.AdultSearchScenesPage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		resp.Body.Close()
		t.Fatalf("decoding response: %v", err)
	}
	resp.Body.Close()
	return resp, out
}

// TestAdultSearch_PoolQueryAndOneProwlarr proves the submit path: the RSS pool
// returns the title-matching scene AND exactly ONE Prowlarr search fires. The
// pool scene surfaces as an AdultSearchScene even with no Prowlarr match.
func TestAdultSearch_PoolQueryAndOneProwlarr(t *testing.T) {
	var prowlarrCalls int32
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&prowlarrCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer prowlarr.Close()

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seedLinkedScene(t, releaseStore, "sc-nova", "Nova Rising", "Nova Studio", "https://cdn/nova.jpg", "Nova.Rising.XXX-GRP", "Nova")

	resp, page := getAdultSearch(t, srv.URL, "Nova Rising", 1)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&prowlarrCalls); got != 1 {
		t.Fatalf("expected EXACTLY ONE Prowlarr.Search on submit, got %d", got)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected the 1 pool scene, got %d (%+v)", len(page.Items), page.Items)
	}
	if page.Items[0].Scene.Title != "Nova Rising" || page.Items[0].Scene.Studio != "Nova Studio" {
		t.Errorf("expected the pool row mapped onto the scene, got %+v", page.Items[0].Scene)
	}
}

// TestAdultSearch_CollapsesProwlarrVariantsPoolLocal is the core grouping
// property via the pure-local (zero external) path: two Prowlarr quality
// variants whose raw titles match one already-identified POOL scene collapse to
// a SINGLE card, both individually selectable in that card's Releases, with the
// grab-bearing fields carried verbatim (grab-safety). No box (TPDB/StashDB) is
// configured, proving the pool-local match makes zero external calls.
func TestAdultSearch_CollapsesProwlarrVariantsPoolLocal(t *testing.T) {
	const raw1080 = "Studio.Some.Clean.Scene.Title.XXX.1080p-GROUP"
	const raw2160 = "Studio.Some.Clean.Scene.Title.XXX.2160p-GROUP"
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"guid":"1","title":"` + raw1080 + `","protocol":"torrent","size":900000000,"seeders":5,"downloadUrl":"magnet:?xt=urn:btih:AAA"},
			{"guid":"2","title":"` + raw2160 + `","protocol":"torrent","size":1900000000,"seeders":9,"downloadUrl":"magnet:?xt=urn:btih:BBB"}
		]`))
	}))
	defer prowlarr.Close()

	srv, seed, releaseStore := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seedLinkedScene(t, releaseStore, "sc-clean", "Some Clean Scene Title", "Studio", "", "", "Someone")

	resp, page := getAdultSearch(t, srv.URL, "Some Clean Scene Title", 1)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected the two quality variants COLLAPSED onto the 1 pool scene card, got %d (%+v)", len(page.Items), page.Items)
	}
	card := page.Items[0]
	if card.Scene.Title != "Some Clean Scene Title" {
		t.Errorf("expected the pool scene identity, got %q", card.Scene.Title)
	}
	if len(card.Releases) != 2 {
		t.Fatalf("expected both distinct-quality variants selectable in Releases, got %d (%+v)", len(card.Releases), card.Releases)
	}
	// Grab-safety: both carried releases keep exactly what Prowlarr returned.
	byURL := map[string]apidto.SearchReleaseResult{}
	for _, rel := range card.Releases {
		byURL[rel.DownloadURL] = rel
	}
	if r := byURL["magnet:?xt=urn:btih:AAA"]; r.Title != raw1080 || r.Protocol != "torrent" || r.Size != 900000000 {
		t.Errorf("expected the 1080p variant's grab fields unchanged, got %+v", r)
	}
	if r := byURL["magnet:?xt=urn:btih:BBB"]; r.Title != raw2160 || r.Protocol != "torrent" || r.Size != 1900000000 {
		t.Errorf("expected the 2160p variant's grab fields unchanged, got %+v", r)
	}
}

// TestAdultSearch_Page2PagesPoolZeroProwlarr proves pagination pages the RSS
// pool ONLY and fires ZERO further Prowlarr (the one-call-per-submit rule).
func TestAdultSearch_Page2PagesPoolZeroProwlarr(t *testing.T) {
	var prowlarrCalls int32
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&prowlarrCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer prowlarr.Close()

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL) // present but must never be touched on page>1

	resp, page := getAdultSearch(t, srv.URL, "Anything", 2)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&prowlarrCalls); got != 0 {
		t.Fatalf("expected ZERO Prowlarr calls on page>1, got %d", got)
	}
	if page.HasMore {
		t.Errorf("expected HasMore=false for a small pool page, got true")
	}
}

// TestAdultSearch_BoundedBoxIdentify proves the box-identification fan-out is
// BOUNDED: with no pool scene to match against, more unmatched Prowlarr raws than
// the budget are submitted, and the box (TPDB) is hit at most adultSearchMaxBoxIdentify
// times (not once per raw). Combined with the pool-local test above (zero box
// calls for pool matches), this covers the "bounded, not unbounded" invariant.
func TestAdultSearch_BoundedBoxIdentify(t *testing.T) {
	origTPDB := tpdbrest.DefaultBaseURL
	origBudget := adultSearchMaxBoxIdentify
	defer func() {
		tpdbrest.DefaultBaseURL = origTPDB
		adultSearchMaxBoxIdentify = origBudget
	}()
	adultSearchMaxBoxIdentify = 2

	var tpdbCalls int32
	tpdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tpdbCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[]}`)) // never a confident match → every raw is dropped
	}))
	defer tpdb.Close()
	tpdbrest.DefaultBaseURL = tpdb.URL

	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Five fully-distinct raws (distinct title + URL), none matching a pool scene.
		w.Write([]byte(`[
			{"guid":"1","title":"Alpha.Unrelated.One.XXX-GRP","protocol":"torrent","size":1,"downloadUrl":"magnet:?a=1"},
			{"guid":"2","title":"Bravo.Unrelated.Two.XXX-GRP","protocol":"torrent","size":1,"downloadUrl":"magnet:?a=2"},
			{"guid":"3","title":"Charlie.Unrelated.Three.XXX-GRP","protocol":"torrent","size":1,"downloadUrl":"magnet:?a=3"},
			{"guid":"4","title":"Delta.Unrelated.Four.XXX-GRP","protocol":"torrent","size":1,"downloadUrl":"magnet:?a=4"},
			{"guid":"5","title":"Echo.Unrelated.Five.XXX-GRP","protocol":"torrent","size":1,"downloadUrl":"magnet:?a=5"}
		]`))
	}))
	defer prowlarr.Close()

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)
	seed.setTPDB(t)

	resp, page := getAdultSearch(t, srv.URL, "Nothing In Pool", 1)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&tpdbCalls); got > int32(adultSearchMaxBoxIdentify) {
		t.Fatalf("expected the box hit at most %d times (bounded), got %d", adultSearchMaxBoxIdentify, got)
	}
	// All unmatched → dropped, no unmatched-card fallback.
	if len(page.Items) != 0 {
		t.Fatalf("expected all unidentified raws dropped, got %d (%+v)", len(page.Items), page.Items)
	}
}

// TestAdultSearch_NormalizesProwlarrQuery proves the Prowlarr search query is
// normalizeAdultQuery(input), not the raw input.
func TestAdultSearch_NormalizesProwlarrQuery(t *testing.T) {
	const rawInput = "Cory Chase's Big Day!"
	want := normalizeAdultQuery(rawInput)
	if want == rawInput {
		t.Fatalf("test precondition: normalizeAdultQuery must change %q to be meaningful", rawInput)
	}

	var gotQuery string
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer prowlarr.Close()

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)

	resp, _ := getAdultSearch(t, srv.URL, rawInput, 1)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotQuery != want {
		t.Errorf("expected Prowlarr query %q (normalizeAdultQuery), got %q", want, gotQuery)
	}
}

// TestAdultSearch_ZeroMatchesEmptyNoError proves a query with no pool AND no
// Prowlarr match returns empty items (200), no error, no raw fallback.
func TestAdultSearch_ZeroMatchesEmptyNoError(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer prowlarr.Close()

	srv, seed, _ := newestScenesMuxWithReleaseStore(t)
	seed.setProwlarr(t, prowlarr.URL)

	resp, page := getAdultSearch(t, srv.URL, "No Such Scene Anywhere", 1)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(page.Items) != 0 {
		t.Fatalf("expected empty items, got %d (%+v)", len(page.Items), page.Items)
	}
}

// TestAdultSearch_RequiresQuery asserts an empty q → 400.
func TestAdultSearch_RequiresQuery(t *testing.T) {
	srv, _ := newestScenesMux(t)
	resp, err := http.Get(srv.URL + "/api/modes/adult/search")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without a q param, got %d", resp.StatusCode)
	}
}

// TestAdultSearch_ProwlarrNotConfigured asserts submit (page=1) needs Prowlarr:
// with none configured it 400s (the pool query alone can't satisfy the one-shot
// submit contract).
func TestAdultSearch_ProwlarrNotConfigured(t *testing.T) {
	srv, _ := newestScenesMux(t) // no prowlarr connection seeded
	resp, err := http.Get(srv.URL + "/api/modes/adult/search?q=Anything")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when prowlarr is unconfigured, got %d", resp.StatusCode)
	}
}
