package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/adultmerge"
	"github.com/labbersanon/sakms/internal/adultmergecache"
	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/db"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// --- Unit: mergePerformers / mergeStudios ---

func TestMergePerformers_NearExactPairCollapses(t *testing.T) {
	// Identical names → collapse to one displayed name, StashDB-first, no
	// divergence flag, no AltName, both ids set, Source "merged".
	tpdb := []tpdbrest.Performer{{ID: "t1", Name: "Riley Reid", Image: "t.jpg"}}
	stash := []stashbox.Performer{{ID: "s1", Name: "Riley Reid", ImageURL: "s.jpg"}}
	out := adultmerge.MergePerformers(tpdb, stash)
	if len(out) != 1 {
		t.Fatalf("expected 1 collapsed card, got %d: %+v", len(out), out)
	}
	c := out[0]
	if c.Source != "merged" || c.TPDBID != "t1" || c.StashDBID != "s1" {
		t.Errorf("expected a merged card with both ids, got %+v", c)
	}
	if c.NamesDiverged || c.AltName != "" {
		t.Errorf("expected no divergence on a near-exact pair, got %+v", c)
	}
	if c.Name != "Riley Reid" {
		t.Errorf("expected collapsed Name %q, got %q", "Riley Reid", c.Name)
	}
	if c.Image != "s.jpg" {
		t.Errorf("expected StashDB-first image, got %q", c.Image)
	}
}

func TestMergePerformers_DivergentPairSurfacesBothNames(t *testing.T) {
	// 4-token names sharing 3 tokens → maxSimilarity 0.75: pairs (>=0.6) but is
	// NOT near-exact (<0.9), so BOTH names must be surfaced.
	tpdb := []tpdbrest.Performer{{ID: "t1", Name: "Anna Bella Rose West", Image: "t.jpg"}}
	stash := []stashbox.Performer{{ID: "s1", Name: "Anna Bella Rose East", ImageURL: "s.jpg"}}
	out := adultmerge.MergePerformers(tpdb, stash)
	if len(out) != 1 {
		t.Fatalf("expected 1 merged card, got %d: %+v", len(out), out)
	}
	c := out[0]
	if c.Source != "merged" || c.TPDBID != "t1" || c.StashDBID != "s1" {
		t.Errorf("expected a merged card with both ids, got %+v", c)
	}
	if !c.NamesDiverged {
		t.Errorf("expected NamesDiverged=true for a fuzzy-but-not-exact pair, got %+v", c)
	}
	if c.Name != "Anna Bella Rose East" { // StashDB canonical
		t.Errorf("expected Name to be the StashDB name, got %q", c.Name)
	}
	if c.AltName != "Anna Bella Rose West" { // TPDB name
		t.Errorf("expected AltName to be the TPDB name, got %q", c.AltName)
	}
	if c.Image != "s.jpg" {
		t.Errorf("expected StashDB-first image, got %q", c.Image)
	}
}

func TestMergePerformers_PairedImageFallsBackToTPDBWhenStashEmpty(t *testing.T) {
	tpdb := []tpdbrest.Performer{{ID: "t1", Name: "Riley Reid", Image: "t.jpg"}}
	stash := []stashbox.Performer{{ID: "s1", Name: "Riley Reid", ImageURL: ""}}
	out := adultmerge.MergePerformers(tpdb, stash)
	if len(out) != 1 || out[0].Image != "t.jpg" {
		t.Errorf("expected TPDB image fallback when StashDB image is empty, got %+v", out)
	}
}

func TestMergePerformers_UnpairedBothSidesSurviveAndSpineOrder(t *testing.T) {
	// T1 pairs with S1; T2 is TPDB-exclusive; S3 is StashDB-exclusive.
	// Order must be: merged(T1,S1), tpdb(T2), then stashdb(S3).
	tpdb := []tpdbrest.Performer{
		{ID: "t1", Name: "Riley Reid", Image: "t1.jpg"},
		{ID: "t2", Name: "Only Tpdb Person", Image: "t2.jpg"},
	}
	stash := []stashbox.Performer{
		{ID: "s1", Name: "Riley Reid", ImageURL: "s1.jpg"},
		{ID: "s3", Name: "Only Stashdb Person", ImageURL: "s3.jpg"},
	}
	out := adultmerge.MergePerformers(tpdb, stash)
	if len(out) != 3 {
		t.Fatalf("expected 3 cards (1 merged + 2 exclusives), got %d: %+v", len(out), out)
	}
	if out[0].Source != "merged" || out[0].TPDBID != "t1" || out[0].StashDBID != "s1" {
		t.Errorf("card[0] should be the merged pair, got %+v", out[0])
	}
	if out[1].Source != "tpdb" || out[1].TPDBID != "t2" || out[1].StashDBID != "" || out[1].Name != "Only Tpdb Person" {
		t.Errorf("card[1] should be the TPDB exclusive (spine order), got %+v", out[1])
	}
	if out[2].Source != "stashdb" || out[2].StashDBID != "s3" || out[2].TPDBID != "" || out[2].Name != "Only Stashdb Person" {
		t.Errorf("card[2] should be the StashDB exclusive appended last, got %+v", out[2])
	}
}

func TestMergePerformers_NilTPDBPurePassthrough(t *testing.T) {
	// The clamp-mitigation steady state: TPDB leg is empty (not errored), so the
	// merged row degrades to a pure StashDB passthrough of unpaired cards.
	stash := []stashbox.Performer{
		{ID: "s1", Name: "Alpha", ImageURL: "a.jpg"},
		{ID: "s2", Name: "Beta", ImageURL: "b.jpg"},
	}
	out := adultmerge.MergePerformers(nil, stash)
	if len(out) != 2 {
		t.Fatalf("expected 2 pure-StashDB cards, got %d: %+v", len(out), out)
	}
	for i, c := range out {
		if c.Source != "stashdb" || c.TPDBID != "" || c.StashDBID != stash[i].ID {
			t.Errorf("card[%d] should be a StashDB-only card, got %+v", i, c)
		}
	}
}

func TestMergePerformers_NilStashPurePassthrough(t *testing.T) {
	tpdb := []tpdbrest.Performer{{ID: "t1", Name: "Alpha", Image: "a.jpg"}}
	out := adultmerge.MergePerformers(tpdb, nil)
	if len(out) != 1 || out[0].Source != "tpdb" || out[0].TPDBID != "t1" || out[0].StashDBID != "" {
		t.Errorf("expected a pure TPDB-only passthrough, got %+v", out)
	}
}

func TestMergePerformers_EmptyBothNoPanic(t *testing.T) {
	out := adultmerge.MergePerformers(nil, nil)
	if len(out) != 0 {
		t.Errorf("expected empty result for two empty legs, got %+v", out)
	}
}

func TestMergeStudios_DivergentNearExactAndPassthrough(t *testing.T) {
	// Near-exact collapse (identical names) + a StashDB-exclusive + verify the
	// Site.Image (vs StashDB Studio.ImageURL) field mapping is StashDB-first.
	tpdb := []tpdbrest.Site{{ID: "t1", Name: "Vixen", Image: "t.png"}}
	stash := []stashbox.Studio{
		{ID: "s1", Name: "Vixen", ImageURL: "s.png"},
		{ID: "s2", Name: "Exclusive Studio", ImageURL: "ex.png"},
	}
	out := adultmerge.MergeStudios(tpdb, stash)
	if len(out) != 2 {
		t.Fatalf("expected 2 cards (1 merged + 1 exclusive), got %d: %+v", len(out), out)
	}
	if out[0].Source != "merged" || out[0].Name != "Vixen" || out[0].NamesDiverged || out[0].Image != "s.png" {
		t.Errorf("card[0] should be a collapsed merged studio with StashDB image, got %+v", out[0])
	}
	if out[1].Source != "stashdb" || out[1].StashDBID != "s2" {
		t.Errorf("card[1] should be the StashDB-exclusive studio, got %+v", out[1])
	}

	// Divergent-studio case (4-token, share 3).
	dt := []tpdbrest.Site{{ID: "t9", Name: "Big City Films West"}}
	ds := []stashbox.Studio{{ID: "s9", Name: "Big City Films East"}}
	dout := adultmerge.MergeStudios(dt, ds)
	if len(dout) != 1 || !dout[0].NamesDiverged || dout[0].Name != "Big City Films East" || dout[0].AltName != "Big City Films West" {
		t.Errorf("expected a divergent-name merged studio surfacing both names, got %+v", dout)
	}

	// Nil-leg passthrough.
	if got := adultmerge.MergeStudios(nil, ds); len(got) != 1 || got[0].Source != "stashdb" {
		t.Errorf("expected pure StashDB studio passthrough on a nil TPDB leg, got %+v", got)
	}
}

// --- Unit: dedupeStashScenesByTPDBHashes ---

func TestDedupeStashScenesByTPDBHashes(t *testing.T) {
	tpdb := []tpdbrest.Scene{
		{ID: "t1", Title: "T One", Site: "S", Hashes: []string{"h1"}},
		{ID: "t2", Title: "T Two", Site: "S", Hashes: nil}, // no hashes → can't mask anything
	}
	stash := []stashbox.Scene{
		{ID: "sx", Title: "Dup", PHashes: []string{"h1"}},  // shares h1 → dropped
		{ID: "sy", Title: "Keep", PHashes: []string{"h2"}}, // no overlap → kept
	}
	out := dedupeStashScenesByTPDBHashes(tpdb, stash)
	if len(out) != 3 {
		t.Fatalf("expected 3 scenes (2 tpdb + 1 non-dup stash), got %d: %+v", len(out), out)
	}
	// Every emitted scene must carry a Source (the per-scene badge chain).
	wantSources := []string{"tpdb", "tpdb", "stashdb"}
	wantIDs := []string{"t1", "t2", "sy"}
	for i, s := range out {
		if s.Source != wantSources[i] {
			t.Errorf("scene[%d] source = %q, want %q", i, s.Source, wantSources[i])
		}
		if s.ID != wantIDs[i] {
			t.Errorf("scene[%d] id = %q, want %q", i, s.ID, wantIDs[i])
		}
		if s.Source == "" {
			t.Errorf("scene[%d] has an empty Source", i)
		}
	}
}

// --- Handler / integration ---

// tpdbPerformerPage renders a TPDB /performers list envelope with the given
// (id,name) rows and echoed meta.current_page. Images are set non-empty so
// backfillMissingImages never fires a detail fetch in these tests.
func tpdbPerformerPage(currentPage int, rows ...[2]string) string {
	items := ""
	for i, r := range rows {
		if i > 0 {
			items += ","
		}
		items += fmt.Sprintf(`{"_id":%q,"name":%q,"image":"http://cdn/%s.jpg"}`, r[0], r[1], r[0])
	}
	return fmt.Sprintf(`{"data":[%s],"meta":{"current_page":%d}}`, items, currentPage)
}

// stashPerformerPage renders a StashDB queryPerformers response with the given rows.
func stashPerformerPage(rows ...[2]string) string {
	items := ""
	for i, r := range rows {
		if i > 0 {
			items += ","
		}
		items += fmt.Sprintf(`{"id":%q,"name":%q,"images":[{"url":"http://cdn/%s.jpg"}]}`, r[0], r[1], r[0])
	}
	return fmt.Sprintf(`{"data":{"queryPerformers":{"performers":[%s]}}}`, items)
}

// stashPageParam pulls variables.input.page out of a stash-box GraphQL POST.
func stashPageParam(t *testing.T, r *http.Request) int {
	t.Helper()
	var req struct {
		Variables struct {
			Input struct {
				Page int `json:"page"`
			} `json:"input"`
		} `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	return req.Variables.Input.Page
}

func TestPerformersMerged_400WhenTPDBAbsent(t *testing.T) {
	srv := httptest.NewServer(newAdultMux(t, map[string]string{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/modes/adult/performers-merged")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when tpdb isn't configured, got %d", resp.StatusCode)
	}
}

func TestPerformersMerged_DegradesToTPDBOnlyWhenStashDBErrors(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(tpdbPerformerPage(1, [2]string{"t1", "Riley Reid"})))
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"errors":[{"message":"boom"}]}`))
	})
	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}))
	defer srv.Close()

	var page apidto.MergedPerformerPage
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged?perPage=5", &page)
	cards := page.Items
	if len(cards) != 1 || cards[0].Source != "tpdb" || cards[0].TPDBID != "t1" {
		t.Errorf("expected TPDB-only degradation (never a request error) when StashDB errors, got %+v", cards)
	}
}

// TestPerformersMerged_UnevenPaginationExhaustion_TPDBShallowerClamp pins the
// hazardous direction: TPDB is the SHALLOWER source (1 real page), StashDB is
// deeper (2 real pages). Past TPDB's final page the mock TPDB clamp-repeats
// (HTTP 200 + same items + stuck meta.current_page) — the REAL live-verified
// behavior. The merged row must deliver ALL StashDB items with none dropped,
// show NO stale TPDB repeat past its final page, and only exhaust once StashDB
// drains.
func TestPerformersMerged_UnevenPaginationExhaustion_TPDBShallowerClamp(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		// One real page of 2 items. Any page > 1 clamps meta.current_page back
		// to 1 while repeating page 1's items — TPDB's observed out-of-range mode.
		w.Write([]byte(tpdbPerformerPage(1, [2]string{"t1", "Tpdb One"}, [2]string{"t2", "Tpdb Two"})))
		_ = page
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		page := stashPageParam(t, r)
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case 1:
			w.Write([]byte(stashPerformerPage([2]string{"s1", "Stash One"}, [2]string{"s2", "Stash Two"})))
		case 2:
			w.Write([]byte(stashPerformerPage([2]string{"s3", "Stash Three"}, [2]string{"s4", "Stash Four"})))
		default:
			w.Write([]byte(stashPerformerPage())) // empty-200 past the final page
		}
	})
	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}))
	defer srv.Close()

	const perPage = 2
	seenTPDB := map[string]bool{}
	seenStash := map[string]int{}
	exhaustedAt := 0
	for page := 1; page <= 5; page++ {
		var pageResp apidto.MergedPerformerPage
		getJSON(t, fmt.Sprintf("%s/api/modes/adult/performers-merged?page=%d&perPage=%d", srv.URL, page, perPage), &pageResp)
		cards := pageResp.Items
		for _, c := range cards {
			switch c.Source {
			case "tpdb":
				seenTPDB[c.TPDBID] = true
				if page > 1 {
					t.Errorf("page %d: stale TPDB repeat leaked into merged output: %+v", page, c)
				}
			case "stashdb":
				seenStash[c.StashDBID]++
			default:
				t.Errorf("page %d: unexpected card source %q: %+v", page, c.Source, c)
			}
		}
		if len(cards) < perPage {
			exhaustedAt = page
			break
		}
	}

	// All 4 StashDB items delivered exactly once — none dropped, none repeated.
	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		if seenStash[id] != 1 {
			t.Errorf("StashDB %s delivered %d times, want exactly 1", id, seenStash[id])
		}
	}
	if len(seenTPDB) != 2 {
		t.Errorf("expected both TPDB items once (page 1 only), saw %v", seenTPDB)
	}
	// page1: 2 tpdb + 2 stash = 4; page2: 2 stash (tpdb clamped→empty) = 2;
	// page3: 0 → exhaust. Row must NOT exhaust before StashDB drains.
	if exhaustedAt != 3 {
		t.Errorf("expected exhaustion at page 3 (once StashDB drained), got %d", exhaustedAt)
	}
}

// TestPerformersMerged_HardError4xxReturns502 documents the OTHER (hard-error)
// P.2 mode: a source returning a non-2xx on an out-of-range page. TPDB's leg is
// required, so tpdbErr != nil → 502. This is the case the plan's G10/Task-0
// route to Option 1b — which is deliberately NOT built here, because live
// verification (2026-07-26, the addendum) established TPDB /performers CLAMPS
// (HTTP 200) rather than 4xxs, making this path unreachable in production. The
// assertion documents current behavior (a loud 502, never a silent tail-drop),
// it does not claim the row degrades.
func TestPerformersMerged_HardError4xxReturns502(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "out of range", http.StatusBadRequest)
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(stashPerformerPage([2]string{"s1", "Stash One"})))
	})
	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/performers-merged?page=2&perPage=2")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 on a hard TPDB error (documented hard-error P.2 limitation), got %d", resp.StatusCode)
	}
}

func TestStudiosMerged_MergesBothSources(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/sites/t1/scenes" {
			// filterZeroSceneSites' check (2026-07-26) — t1 has a scene, so it
			// survives the zero-scene filter.
			w.Write([]byte(`{"data":[{"_id":"sc1","title":"A Scene"}]}`))
			return
		}
		// BrowseSites now walks internally (2026-07-26, junk-network filter) —
		// page 1 has the one real item, page 2+ must be empty so the walk
		// terminates at the true end of the (tiny, fixture) catalog instead of
		// looping to its cap and returning duplicate "Vixen" entries.
		if r.URL.Query().Get("page") == "1" {
			w.Write([]byte(`{"data":[{"uuid":"t1","name":"Vixen","logo":"http://cdn/t.png"}],"meta":{"current_page":1}}`))
			return
		}
		w.Write([]byte(`{"data":[]}`))
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryStudios":{"studios":[` +
			`{"id":"s1","name":"Vixen","images":[{"url":"http://cdn/s.png"}]},` +
			`{"id":"s2","name":"Exclusive Studio","images":[]}]}}}`))
	})
	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}))
	defer srv.Close()

	// The endpoint returns a MergedStudioPage envelope ({items, hasMore}), not
	// a bare array (2026-07-26, see that DTO's doc comment) -- Performers-merged
	// mirrors this with its own MergedPerformerPage envelope (see the tests above).
	var page apidto.MergedStudioPage
	getJSON(t, srv.URL+"/api/modes/adult/studios-merged?perPage=5", &page)
	cards := page.Items
	if len(cards) != 2 {
		t.Fatalf("expected 2 merged studio cards, got %d: %+v", len(cards), cards)
	}
	if cards[0].Source != "merged" || cards[0].Name != "Vixen" || cards[0].Image != "http://cdn/s.png" {
		t.Errorf("card[0] should be the collapsed merged Vixen with StashDB image, got %+v", cards[0])
	}
	// Both ids must be set on a merged card (the DTO's own documented
	// invariant) -- guards a real live-verified bug (2026-07-26): rawSiteEntry
	// decoded a nonexistent "_id" field (SiteResource has no "_id", only
	// "uuid") so every TPDBID silently stayed empty. This assertion was
	// missing before the bug was found live; a regression that reintroduces
	// it now fails here instead of only in production.
	if cards[0].TPDBID != "t1" || cards[0].StashDBID != "s1" {
		t.Errorf("card[0] should carry both source ids, got %+v", cards[0])
	}
	if cards[1].Source != "stashdb" || cards[1].StashDBID != "s2" {
		t.Errorf("card[1] should be the StashDB-exclusive studio, got %+v", cards[1])
	}
	// Both fixture legs are smaller than perPage (5): TPDB has 1 real item,
	// StashDB has 2 -- neither leg delivered a full page, so the row is
	// genuinely exhausted and HasMore must be false.
	if page.HasMore {
		t.Errorf("expected HasMore=false when both legs are smaller than perPage, got true")
	}
}

// TestStudiosMerged_HasMoreSurvivesZeroSceneFiltering is the definitive
// regression guard for the whole zero-scene-filter fix chain (2026-07-26):
// TPDB returns a full page (BrowseSites' own hasMore, computed BEFORE
// filtering, is true), but every one of those items has zero scenes, so
// filterZeroSceneSites drops all of them. StashDB (small fixture, no more
// pages) alone would signal exhausted. HasMore must still be TRUE, sourced
// from tpdbHasMore -- NOT re-derived from the post-filter (now-empty) merged
// result, which is exactly the bug this feature exists to prevent: a page
// with zero real items showing wrongly ends the row when the real TPDB
// catalog is not actually exhausted.
func TestStudiosMerged_HasMoreSurvivesZeroSceneFiltering(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/scenes") {
			w.Write([]byte(`{"data":[]}`)) // every site has zero scenes
			return
		}
		if r.URL.Query().Get("page") == "1" {
			w.Write([]byte(`{"data":[{"uuid":"t1","name":"Orphan One"},{"uuid":"t2","name":"Orphan Two"}]}`))
			return
		}
		w.Write([]byte(`{"data":[]}`))
	})
	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL}))
	defer srv.Close()

	var page apidto.MergedStudioPage
	// perPage=2 matches the fixture's 2-item page exactly, so
	// tpdbHasMore = len(accum) >= needed = 2 >= 2 = true.
	getJSON(t, srv.URL+"/api/modes/adult/studios-merged?perPage=2", &page)
	if len(page.Items) != 0 {
		t.Fatalf("expected 0 items (both zero-scene entries dropped), got %+v", page.Items)
	}
	if !page.HasMore {
		t.Errorf("expected HasMore=true (sourced pre-filter) even though this page's items were all dropped, got false")
	}
}

// --- Unit: filterZeroSceneSites ---

// TestFilterZeroSceneSites_DropsZeroSceneEntries proves the core behavior:
// a site with zero scenes is dropped, one with scenes is kept, and original
// order is preserved among survivors.
func TestFilterZeroSceneSites_DropsZeroSceneEntries(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sites/has-scenes/scenes":
			w.Write([]byte(`{"data":[{"_id":"sc1","title":"A Scene"}]}`))
		case "/sites/zero-scenes/scenes":
			w.Write([]byte(`{"data":[]}`))
		default:
			t.Errorf("unexpected scenes request path %q", r.URL.Path)
		}
	})
	client := tpdbrest.New(tpdb.URL, "testkey", &http.Client{})
	sites := []tpdbrest.Site{
		{ID: "zero-scenes", Name: "Orphan Placeholder"},
		{ID: "has-scenes", Name: "Real Studio"},
	}
	out := adultmerge.FilterZeroSceneSites(context.Background(), client, sites)
	if len(out) != 1 || out[0].ID != "has-scenes" {
		t.Fatalf("expected only the has-scenes entry to survive, got %+v", out)
	}
}

// TestFilterZeroSceneSites_FailsOpenOnError proves a per-item ScenesBySite
// error keeps the item rather than dropping it — one transient TPDB hiccup
// must not silently hide a real studio.
func TestFilterZeroSceneSites_FailsOpenOnError(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	client := tpdbrest.New(tpdb.URL, "testkey", &http.Client{})
	sites := []tpdbrest.Site{{ID: "s1", Name: "Real Studio"}}
	out := adultmerge.FilterZeroSceneSites(context.Background(), client, sites)
	if len(out) != 1 || out[0].ID != "s1" {
		t.Fatalf("expected the item to survive a ScenesBySite error (fail-open), got %+v", out)
	}
}

// --- Merged drill-down scenes ---

func TestMergedScenes_400WhenBothIdsAbsent(t *testing.T) {
	srv := httptest.NewServer(newAdultMux(t, map[string]string{}))
	defer srv.Close()
	for _, path := range []string{
		"/api/modes/adult/discover/performers-merged/scenes",
		"/api/modes/adult/discover/studios-merged/scenes",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		func() {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s: expected 400 when both ids absent, got %d", path, resp.StatusCode)
			}
		}()
	}
}

func TestMergedScenes_SingleSourceWhenOnlyTPDBId(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"tsc1","title":"T Scene","site":{"name":"Vixen"}}]}`))
	})
	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL}))
	defer srv.Close()

	var scenes []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/performers-merged/scenes?tpdbId=pf1", &scenes)
	if len(scenes) != 1 || scenes[0].ID != "tsc1" || scenes[0].Source != "tpdb" {
		t.Errorf("expected a single TPDB-sourced scene, got %+v", scenes)
	}
}

func TestMergedScenes_PhashMergedAndSourceStamped(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"_id":"tsc1","title":"T Scene","site":{"name":"Vixen"},` +
			`"hashes":[{"hash":"h1","type":"phash"}]}]}`))
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// One duplicate (shares h1) → dropped; one unique (h2) → kept.
		w.Write([]byte(`{"data":{"queryScenes":{"scenes":[` +
			`{"id":"dup","title":"Dup","release_date":"2024-01-01","studio":{"name":"Vixen"},"fingerprints":[{"hash":"h1","algorithm":"PHASH"}]},` +
			`{"id":"keep","title":"Keep","release_date":"2024-02-02","studio":{"name":"Vixen"},"fingerprints":[{"hash":"h2","algorithm":"PHASH"}]}]}}}`))
	})
	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}))
	defer srv.Close()

	var scenes []adultScene
	getJSON(t, srv.URL+"/api/modes/adult/discover/performers-merged/scenes?tpdbId=pf1&stashdbId=sp1", &scenes)
	if len(scenes) != 2 {
		t.Fatalf("expected 2 scenes (tpdb + non-dup stash, dup dropped), got %d: %+v", len(scenes), scenes)
	}
	if scenes[0].ID != "tsc1" || scenes[0].Source != "tpdb" {
		t.Errorf("scene[0] should be the TPDB scene, got %+v", scenes[0])
	}
	if scenes[1].ID != "keep" || scenes[1].Source != "stashdb" {
		t.Errorf("scene[1] should be the non-duplicate StashDB scene stamped stashdb, got %+v", scenes[1])
	}
	for _, s := range scenes {
		if s.Source == "" {
			t.Errorf("every merged scene must carry a Source, got %+v", s)
		}
	}
}

// --- Unit + integration: Option B availability hard filter ---
//
// .omc/plans/ralplan-adult-discover-performers-availability-gate.md's Testing
// section (Decision DB) is the source of truth for this section's coverage —
// see that file for the full rationale each test below encodes.

// newAdultMuxWithPool is newAdultMux (TPDB/StashDB connections wired to fake
// servers) plus newAdultPoolServer's pool-seeding/injectable-FeedHealth
// (adultdiscover_stashbox_test.go) combined into one helper — the availability
// hard filter needs both a live merged browse (TPDB/StashDB) AND a seeded
// adult_newest_releases pool to exercise end-to-end.
func newAdultMuxWithPool(t *testing.T, conns map[string]string, fh *adultnewest.FeedHealth, seed []adultnewest.MatchedRelease) *http.ServeMux {
	t.Helper()
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	for service, u := range conns {
		if err := connStore.Upsert(context.Background(), service, u, "key"); err != nil {
			t.Fatalf("upserting %s: %v", service, err)
		}
		overrideFixedURL(t, service, u)
	}
	for _, m := range seed {
		if err := adultNewestReleaseStore.Insert(context.Background(), m); err != nil {
			t.Fatalf("seeding release %q: %v", m.EntityID, err)
		}
	}
	// A fresh migrated DB backs the merged-row precompute cache. Left empty here,
	// so every merged-handler request is a cache miss that falls through to the
	// live merge path — preserving these tests' existing live-path assertions.
	cacheDB, err := db.Open(filepath.Join(t.TempDir(), "mergecache.db"))
	if err != nil {
		t.Fatalf("opening merge-cache db: %v", err)
	}
	t.Cleanup(func() { cacheDB.Close() })
	adultMergeCacheStore := adultmergecache.New(cacheDB)
	return NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, fh, adultMergeCacheStore, rssFeedsStore, nil, nil, nil, nil, nil)
}

// seedReleaseStore inserts rows into releaseStore, failing the test on error.
func seedReleaseStore(t *testing.T, releaseStore *adultnewest.ReleaseStore, rows ...adultnewest.MatchedRelease) {
	t.Helper()
	for _, m := range rows {
		if err := releaseStore.Insert(context.Background(), m); err != nil {
			t.Fatalf("seeding release %q: %v", m.EntityID, err)
		}
	}
}

// TestAvailableNameSets_RollsUpSceneCreditsOnly is the DB rollup mandatory
// test: Scene/Movie rows contribute their Performers[]/EntityStudio; Performer/
// Studio rows contribute nothing (they're written BrowseConfirmed=true
// unconditionally by the scan job, so including them would turn this into a
// catalog-presence signal, not a grabbable one — see availableNameSets' doc).
func TestAvailableNameSets_RollsUpSceneCreditsOnly(t *testing.T) {
	_, _, _, _, _, _, _, _, _, releaseStore, _ := testStores(t)
	seedReleaseStore(t, releaseStore,
		adultnewest.MatchedRelease{RowType: adultnewest.RowScene, EntityID: "sc1", EntitySource: "tpdb",
			EntityTitle: "A Scene", EntityStudio: "Vixen", Performers: []string{"Riley Reid", "Jane Doe"}, BrowseConfirmed: true},
		// These are unconditionally BrowseConfirmed=true by scan.go — must NOT
		// be read here, or the availability signal degenerates into
		// catalog-presence (plan's Decision DB rationale).
		adultnewest.MatchedRelease{RowType: adultnewest.RowPerformer, EntityID: "pf1", EntitySource: "tpdb",
			EntityTitle: "Should Not Appear", BrowseConfirmed: true},
		adultnewest.MatchedRelease{RowType: adultnewest.RowStudio, EntityID: "st1", EntitySource: "tpdb",
			EntityTitle: "Should Not Appear Studio", BrowseConfirmed: true},
	)

	performerNames, studioNames := availableNameSets(context.Background(), releaseStore, adultnewest.NewFeedHealth())
	got := map[string]bool{}
	for _, n := range performerNames {
		got[n] = true
	}
	if len(performerNames) != 2 || !got["Riley Reid"] || !got["Jane Doe"] {
		t.Errorf("expected exactly the scene's 2 credited performers, got %+v", performerNames)
	}
	if len(studioNames) != 1 || studioNames[0] != "Vixen" {
		t.Errorf("expected exactly the scene's studio, got %+v", studioNames)
	}
}

// TestAvailableNameSets_FeedOnlyGatedByFeedHealth proves a feed-only credit
// (BrowseConfirmed=false) is included iff its feed is currently fresh — driving
// FeedHealth healthy/stale via SetHealthy, mirroring
// TestAdultMergedRecent_GatesOutFeedOnlyWhenFeedNotFresh's established pattern.
func TestAvailableNameSets_FeedOnlyGatedByFeedHealth(t *testing.T) {
	now := time.Now()
	seedFeedOnlyRow := func(t *testing.T, releaseStore *adultnewest.ReleaseStore) {
		t.Helper()
		seedReleaseStore(t, releaseStore, adultnewest.MatchedRelease{
			RowType: adultnewest.RowScene, EntityID: "feedonly", EntitySource: "tpdb", EntityTitle: "Feed Only Scene",
			Performers: []string{"Feed Person"}, EntityStudio: "Feed Studio",
			FeedID: 7, FeedItemKey: "http://feed/x.torrent", LastConfirmedSeen: now.Unix(),
		})
	}

	t.Run("stale feed excludes the credit", func(t *testing.T) {
		_, _, _, _, _, _, _, _, _, releaseStore, _ := testStores(t)
		seedFeedOnlyRow(t, releaseStore)
		performerNames, studioNames := availableNameSets(context.Background(), releaseStore, adultnewest.NewFeedHealth())
		if len(performerNames) != 0 || len(studioNames) != 0 {
			t.Errorf("expected the feed-only credit excluded while its feed is unhealthy, got performers=%+v studios=%+v", performerNames, studioNames)
		}
	})

	t.Run("fresh feed includes the credit", func(t *testing.T) {
		_, _, _, _, _, _, _, _, _, releaseStore, _ := testStores(t)
		seedFeedOnlyRow(t, releaseStore)
		fh := adultnewest.NewFeedHealth()
		fh.SetHealthy(7, now)
		performerNames, studioNames := availableNameSets(context.Background(), releaseStore, fh)
		if len(performerNames) != 1 || performerNames[0] != "Feed Person" {
			t.Errorf("expected the feed-only performer credit included once its feed is healthy, got %+v", performerNames)
		}
		if len(studioNames) != 1 || studioNames[0] != "Feed Studio" {
			t.Errorf("expected the feed-only studio credit included once its feed is healthy, got %+v", studioNames)
		}
	})
}

// TestAvailableNameSets_EmptyNameHygiene is the mandatory empty-name hygiene
// test (Critic MINOR): blank/whitespace-only Performers[] entries and an
// all-whitespace EntityStudio must never pollute the available-name sets with
// an empty-string key.
func TestAvailableNameSets_EmptyNameHygiene(t *testing.T) {
	_, _, _, _, _, _, _, _, _, releaseStore, _ := testStores(t)
	seedReleaseStore(t, releaseStore, adultnewest.MatchedRelease{
		RowType: adultnewest.RowScene, EntityID: "sc1", EntitySource: "tpdb", EntityTitle: "Blank Studio Scene",
		EntityStudio: "   ", Performers: []string{"", "  ", "Real Name"}, BrowseConfirmed: true,
	})

	performerNames, studioNames := availableNameSets(context.Background(), releaseStore, adultnewest.NewFeedHealth())
	if len(performerNames) != 1 || performerNames[0] != "Real Name" {
		t.Errorf("expected blank/whitespace performer entries excluded, only the real name kept, got %+v", performerNames)
	}
	if len(studioNames) != 0 {
		t.Errorf("expected the whitespace-only studio excluded entirely (no empty-string key), got %+v", studioNames)
	}
	// A card whose own name normalizes to empty must never be spuriously kept,
	// even against a hygiene-failure set.
	if nameAvailable("", performerNames) {
		t.Errorf("an empty card name must never match")
	}
}

// TestNameAvailable_EmptyNameNeverMatches guards nameAvailable's own defensive
// empty-name check, independent of availableNameSets' build-time hygiene.
func TestNameAvailable_EmptyNameNeverMatches(t *testing.T) {
	if nameAvailable("", []string{"Real Name"}) {
		t.Error("an empty name must never match a populated set")
	}
	if nameAvailable("", []string{""}) {
		t.Error("an empty name must never match, even against a blank set entry")
	}
}

// TestCardAvailable_ChecksBothNameAndAltName is the mandatory diverged-card
// predicate test (Architect #1 / Critic MINOR-1): cardAvailable must match via
// EITHER Name or AltName, not Name alone.
func TestCardAvailable_ChecksBothNameAndAltName(t *testing.T) {
	if !cardAvailable("Name Only", "", []string{"Name Only"}) {
		t.Error("expected a match via Name")
	}
	if !cardAvailable("Stash Spelling", "Tpdb Spelling", []string{"Tpdb Spelling"}) {
		t.Error("expected a match via AltName alone")
	}
	if cardAvailable("Stash Spelling", "Tpdb Spelling", []string{"Something Else"}) {
		t.Error("expected no match when neither Name nor AltName is in the set")
	}
}

// TestFilterAvailablePerformers_DivergedCardKeptViaAltNameMatch is the
// mandatory diverged-card test at the filter's own boundary (not just the
// cardAvailable predicate in isolation) — mirrors
// TestMergePerformers_DivergentPairSurfacesBothNames' fixture: Name is
// StashDB's canonical spelling, AltName is TPDB's. The pool credits this
// scene under the TPDB spelling (AltName) — proving the filter does not
// false-negative on a diverged card.
func TestFilterAvailablePerformers_DivergedCardKeptViaAltNameMatch(t *testing.T) {
	cards := []apidto.MergedPerformerCard{
		{Name: "Anna Bella Rose East", AltName: "Anna Bella Rose West", NamesDiverged: true},
	}
	out := filterAvailablePerformers(cards, []string{"Anna Bella Rose West"})
	if len(out) != 1 {
		t.Fatalf("expected the diverged card kept via its AltName match, got %+v", out)
	}
}

// TestFilterAvailablePerformers_DropsUnavailableKeepsAvailable is the basic DB
// hard-filter contract: a card whose name is in the available set is kept; a
// card whose name is absent is dropped.
func TestFilterAvailablePerformers_DropsUnavailableKeepsAvailable(t *testing.T) {
	cards := []apidto.MergedPerformerCard{{Name: "Riley Reid"}, {Name: "Unknown Person"}}
	out := filterAvailablePerformers(cards, []string{"Riley Reid"})
	if len(out) != 1 || out[0].Name != "Riley Reid" {
		t.Fatalf("expected only the available card kept, got %+v", out)
	}
}

// TestFilterAvailablePerformers_EmptyAvailableFailsOpen is the mandatory
// empty-pool floor test: with zero available names, every card is kept
// unfiltered — an infrastructure gap (pool never scanned, adultnewest
// disabled, or a pool-read error) must never silently empty the row.
func TestFilterAvailablePerformers_EmptyAvailableFailsOpen(t *testing.T) {
	cards := []apidto.MergedPerformerCard{{Name: "Anyone"}, {Name: "Someone Else"}}
	out := filterAvailablePerformers(cards, nil)
	if len(out) != 2 {
		t.Fatalf("expected fail-open (all cards kept) when available is empty, got %+v", out)
	}
}

// TestFilterAvailableStudios_DropsUnavailableKeepsAvailable is
// filterAvailablePerformers' studio sibling of the basic hard-filter contract.
func TestFilterAvailableStudios_DropsUnavailableKeepsAvailable(t *testing.T) {
	cards := []apidto.MergedStudioCard{{Name: "Vixen"}, {Name: "Unknown Studio"}}
	out := filterAvailableStudios(cards, []string{"Vixen"})
	if len(out) != 1 || out[0].Name != "Vixen" {
		t.Fatalf("expected only the available studio kept, got %+v", out)
	}
}

// TestFilterAvailablePerformers_DescriptorCoCreditHonestyWitness is the DC
// regression witness (honesty guard — KEEP verbatim in force, per the plan).
// "Huge Tits" is a body-part descriptor TPDB miscredits as a scene performer
// alongside real names (live-verified 2026-07-26 investigation, see the
// investigation PRD in memory). This encodes, as an executable expectation,
// that the availability hard filter does NOT and CANNOT distinguish a
// descriptor from a real person — it only checks catalog presence, so a
// descriptor co-credited on an available scene is retained exactly like a
// real performer sharing that scene. Any future change that claims to "fix
// descriptors via availability" must confront this test.
func TestFilterAvailablePerformers_DescriptorCoCreditHonestyWitness(t *testing.T) {
	cards := []apidto.MergedPerformerCard{
		{Name: "Huge Tits"},        // the descriptor miscredit
		{Name: "Karin Kitty"},      // the real performer sharing the scene
		{Name: "Not In Any Scene"}, // an unrelated card, correctly dropped
	}
	out := filterAvailablePerformers(cards, []string{"Huge Tits", "Karin Kitty"})
	if len(out) != 2 {
		t.Fatalf("expected both co-credited names (descriptor AND real performer) retained, only the uncredited name dropped, got %+v", out)
	}
	kept := map[string]bool{}
	for _, c := range out {
		kept[c.Name] = true
	}
	if !kept["Huge Tits"] {
		t.Errorf("the availability filter must not filter out a descriptor co-credit -- it has no signal to distinguish it from a real performer, got %+v", out)
	}
}

// TestFilterAvailablePerformers_LogsDropCountButNeverLeaksToResponse is the
// mandatory diagnostic-drop-count test: the filter emits a server-log-only
// kept/dropped line, and that count never leaks into the MergedPerformerPage
// DTO/response shape.
func TestFilterAvailablePerformers_LogsDropCountButNeverLeaksToResponse(t *testing.T) {
	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prevOut)

	cards := []apidto.MergedPerformerCard{{Name: "Riley Reid"}, {Name: "Unknown Person"}}
	out := filterAvailablePerformers(cards, []string{"Riley Reid"})
	if len(out) != 1 {
		t.Fatalf("expected only the available card kept, got %+v", out)
	}
	if !strings.Contains(buf.String(), "kept 1, dropped 1 of 2") {
		t.Errorf("expected a server-log-only kept/dropped diagnostic line, got %q", buf.String())
	}

	body, err := json.Marshal(apidto.MergedPerformerPage{Items: out, HasMore: false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(body, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for k := range generic {
		if k != "items" && k != "hasMore" {
			t.Errorf("unexpected key %q leaked into MergedPerformerPage JSON -- the drop count must stay server-log-only", k)
		}
	}
}

// TestPerformersMerged_EmptyPoolFailsOpenShowsAllCards is the empty-pool floor
// mandatory test at the HTTP/handler level: with zero scene/movie pool rows,
// the row returns unfiltered.
func TestPerformersMerged_EmptyPoolFailsOpenShowsAllCards(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(tpdbPerformerPage(1, [2]string{"t1", "Anyone At All"})))
	})
	mux := newAdultMuxWithPool(t, map[string]string{"tpdb": tpdb.URL}, adultnewest.NewFeedHealth(), nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var page apidto.MergedPerformerPage
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged", &page)
	if len(page.Items) != 1 || page.Items[0].Name != "Anyone At All" {
		t.Fatalf("expected the card kept unfiltered on an empty pool (fail-open -- an infrastructure gap must never silently empty the row), got %+v", page.Items)
	}
}

// TestPerformersMerged_AvailabilityFilterDropsAllButHasMoreStaysTrue is the
// mandatory hasMore-integrity test: MergedPerformerPage.HasMore is computed
// from PRE-filter source lengths -- a page whose items are ALL dropped by the
// availability filter still reports HasMore=true when a full source page was
// fetched (the pool here is populated but matches neither returned card, so
// the filter genuinely engages rather than failing open).
func TestPerformersMerged_AvailabilityFilterDropsAllButHasMoreStaysTrue(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(tpdbPerformerPage(1, [2]string{"t1", "Not In Pool One"}, [2]string{"t2", "Not In Pool Two"})))
	})
	seed := []adultnewest.MatchedRelease{
		{RowType: adultnewest.RowScene, EntityID: "sc1", EntitySource: "tpdb", EntityTitle: "Some Scene",
			Performers: []string{"Somebody Else Entirely"}, BrowseConfirmed: true},
	}
	mux := newAdultMuxWithPool(t, map[string]string{"tpdb": tpdb.URL}, adultnewest.NewFeedHealth(), seed)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var page apidto.MergedPerformerPage
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged?perPage=2", &page)
	if len(page.Items) != 0 {
		t.Fatalf("expected both cards dropped (neither matches the pool's sole credit), got %+v", page.Items)
	}
	if !page.HasMore {
		t.Errorf("expected HasMore=true (sourced pre-filter) even though every item on this page was dropped, got false")
	}
}

// TestStudiosMerged_AvailabilityFilterAppliesAfterZeroSceneLayer is the
// mandatory Studios composition guard: layer 2 (filterZeroSceneSites) runs
// pre-merge on the TPDB leg only and removes a zero-scene TPDB site
// regardless of pool availability; layer 3 (filterAvailableStudios) runs
// post-merge and is the ONLY layer that can touch a StashDB-exclusive studio,
// since filterZeroSceneSites never sees it.
func TestStudiosMerged_AvailabilityFilterAppliesAfterZeroSceneLayer(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/scenes") {
			w.Write([]byte(`{"data":[]}`)) // every TPDB site has zero scenes
			return
		}
		w.Write([]byte(`{"data":[{"uuid":"orphan","name":"Zero Scene Site"}]}`))
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryStudios":{"studios":[{"id":"s1","name":"Stash Only Studio","images":[]}]}}}`))
	})
	// "Stash Only Studio" is pool-available; "Zero Scene Site" is not in the
	// pool at all -- irrelevant either way, since layer 2 removes it before
	// layer 3 ever runs.
	seed := []adultnewest.MatchedRelease{
		{RowType: adultnewest.RowScene, EntityID: "sc1", EntitySource: "stashdb", EntityTitle: "A Scene",
			EntityStudio: "Stash Only Studio", BrowseConfirmed: true},
	}
	mux := newAdultMuxWithPool(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}, adultnewest.NewFeedHealth(), seed)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var page apidto.MergedStudioPage
	getJSON(t, srv.URL+"/api/modes/adult/studios-merged", &page)
	if len(page.Items) != 1 || page.Items[0].Name != "Stash Only Studio" {
		t.Fatalf("expected only the StashDB-exclusive, pool-available studio to survive both layers, got %+v", page.Items)
	}
}

// --- Read-cache path (internal/adultmergecache) ---

// newAdultMuxWithCache is newAdultMuxWithPool that ALSO returns the merged-row
// cache store, so a test can pre-populate it and then exercise the handler's
// cache read path. fh is the caller's FeedHealth (so an availability flip can be
// driven between requests); seed pre-loads the adult_newest_releases pool.
func newAdultMuxWithCache(t *testing.T, conns map[string]string, fh *adultnewest.FeedHealth, seed []adultnewest.MatchedRelease) (*http.ServeMux, *adultmergecache.Store) {
	t.Helper()
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	for service, u := range conns {
		if err := connStore.Upsert(context.Background(), service, u, "key"); err != nil {
			t.Fatalf("upserting %s: %v", service, err)
		}
		overrideFixedURL(t, service, u)
	}
	for _, m := range seed {
		if err := adultNewestReleaseStore.Insert(context.Background(), m); err != nil {
			t.Fatalf("seeding release %q: %v", m.EntityID, err)
		}
	}
	cacheDB, err := db.Open(filepath.Join(t.TempDir(), "mergecache.db"))
	if err != nil {
		t.Fatalf("opening merge-cache db: %v", err)
	}
	t.Cleanup(func() { cacheDB.Close() })
	cacheStore := adultmergecache.New(cacheDB)
	mux := NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, fh, cacheStore, rssFeedsStore, nil, nil, nil, nil, nil)
	return mux, cacheStore
}

func putPerformerCache(t *testing.T, store *adultmergecache.Store, page int, hasMore bool, cards []apidto.MergedPerformerCard) {
	t.Helper()
	payload, err := json.Marshal(cards)
	if err != nil {
		t.Fatalf("marshal performer cache payload: %v", err)
	}
	if err := store.Put(context.Background(), "performers", page, 20, payload, hasMore, "2026-07-28T00:00:00Z"); err != nil {
		t.Fatalf("performer cache Put: %v", err)
	}
}

func putStudioCache(t *testing.T, store *adultmergecache.Store, page int, hasMore bool, cards []apidto.MergedStudioCard) {
	t.Helper()
	payload, err := json.Marshal(cards)
	if err != nil {
		t.Fatalf("marshal studio cache payload: %v", err)
	}
	if err := store.Put(context.Background(), "studios", page, 20, payload, hasMore, "2026-07-28T00:00:00Z"); err != nil {
		t.Fatalf("studio cache Put: %v", err)
	}
}

// TestPerformersMerged_CacheHitServesWithoutUpstream proves an eligible request
// is served from the cache with NO live TPDB/StashDB call. TPDB is deliberately
// left unconfigured: the live path 400s when TPDB is absent, so a 200 with the
// cached cards can only mean the handler never fell through to the live fetch.
func TestPerformersMerged_CacheHitServesWithoutUpstream(t *testing.T) {
	fh := adultnewest.NewFeedHealth()
	mux, store := newAdultMuxWithCache(t, map[string]string{}, fh, nil)
	putPerformerCache(t, store, 1, true, []apidto.MergedPerformerCard{
		{Name: "Riley Reid", Source: "tpdb", TPDBID: "t1"},
		{Name: "Jane Doe", Source: "tpdb", TPDBID: "t2"},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var page apidto.MergedPerformerPage
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged?page=1&perPage=20", &page)
	// Empty pool → availability filter fails open → both cached cards returned.
	if len(page.Items) != 2 {
		t.Fatalf("expected both cached cards served from cache (would 400 if the live path ran with TPDB absent), got %+v", page.Items)
	}
	if !page.HasMore {
		t.Errorf("expected HasMore served from the cached pre-filter value (true), got false")
	}
}

// TestPerformersMerged_CacheMissFallsBackToLive proves an empty cache falls
// through to today's exact live-merge behavior.
func TestPerformersMerged_CacheMissFallsBackToLive(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(tpdbPerformerPage(999, [2]string{"live", "Live Performer"})))
	})
	fh := adultnewest.NewFeedHealth()
	mux, _ := newAdultMuxWithCache(t, map[string]string{"tpdb": tpdb.URL}, fh, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var page apidto.MergedPerformerPage
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged?page=1&perPage=20", &page)
	if len(page.Items) != 1 || page.Items[0].Name != "Live Performer" {
		t.Fatalf("expected the live-merge result on a cache miss, got %+v", page.Items)
	}
}

// TestPerformersMerged_IneligibleRequestsBypassCache proves page>PrecomputePages
// and perPage!=20 requests always go live, even when a cache row happens to exist.
func TestPerformersMerged_IneligibleRequestsBypassCache(t *testing.T) {
	tpdb := fakeTPDB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// meta.current_page=999 so no requested page (1 or 4) is ever treated as
		// clamped — the live path returns "Live Performer" for any page.
		w.Write([]byte(tpdbPerformerPage(999, [2]string{"live", "Live Performer"})))
	})
	fh := adultnewest.NewFeedHealth()
	mux, store := newAdultMuxWithCache(t, map[string]string{"tpdb": tpdb.URL}, fh, nil)
	sentinel := []apidto.MergedPerformerCard{{Name: "CACHED SENTINEL", Source: "tpdb", TPDBID: "sent"}}
	putPerformerCache(t, store, 1, false, sentinel)
	putPerformerCache(t, store, 4, false, sentinel) // a page-4 row the gate must ignore
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Eligible: page<=3 && perPage==20 → served from cache (the sentinel).
	var hit apidto.MergedPerformerPage
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged?page=1&perPage=20", &hit)
	if len(hit.Items) != 1 || hit.Items[0].Name != "CACHED SENTINEL" {
		t.Fatalf("an eligible request should be served from cache, got %+v", hit.Items)
	}
	// perPage != 20 → bypass the cache → live.
	var byPerPage apidto.MergedPerformerPage
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged?page=1&perPage=10", &byPerPage)
	if len(byPerPage.Items) != 1 || byPerPage.Items[0].Name != "Live Performer" {
		t.Fatalf("perPage!=20 must bypass the cache and go live, got %+v", byPerPage.Items)
	}
	// page > PrecomputePages → bypass the cache even though a page-4 row exists → live.
	var byPage apidto.MergedPerformerPage
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged?page=4&perPage=20", &byPage)
	if len(byPage.Items) != 1 || byPage.Items[0].Name != "Live Performer" {
		t.Fatalf("page>PrecomputePages must bypass the cache (never serve the page-4 sentinel) and go live, got %+v", byPage.Items)
	}
}

// TestPerformersMerged_CacheHitHasMoreIsPreFilterStored is the AC4 guard on the
// cache-hit path: HasMore is the stored pre-filter value, never recomputed from
// the post-filter item count. The pool credits a name matching NEITHER cached
// card, so the filter engages and drops both — yet HasMore must stay the stored
// true, not become false off a now-zero count.
func TestPerformersMerged_CacheHitHasMoreIsPreFilterStored(t *testing.T) {
	fh := adultnewest.NewFeedHealth()
	seed := []adultnewest.MatchedRelease{
		{RowType: adultnewest.RowScene, EntityID: "sc1", EntitySource: "tpdb", EntityTitle: "Some Scene",
			Performers: []string{"Nobody At All"}, BrowseConfirmed: true},
	}
	mux, store := newAdultMuxWithCache(t, map[string]string{}, fh, seed)
	putPerformerCache(t, store, 1, true, []apidto.MergedPerformerCard{{Name: "Riley Reid"}, {Name: "Jane Doe"}})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var page apidto.MergedPerformerPage
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged?page=1&perPage=20", &page)
	if len(page.Items) != 0 {
		t.Fatalf("expected both cached cards dropped by the live availability filter, got %+v", page.Items)
	}
	if !page.HasMore {
		t.Errorf("HasMore must be the stored pre-filter value (true) even when every item was dropped, got false")
	}
}

// TestPerformersMerged_CacheHitFeedHealthFlipNoPrecompute is the AC3 guard: a
// feed-health flip changes which cached cards survive on the NEXT request, with
// NO precompute run in between — proving the availability filter runs live
// against the cached PRE-filter payload, not a baked-in filtered snapshot.
func TestPerformersMerged_CacheHitFeedHealthFlipNoPrecompute(t *testing.T) {
	now := time.Now()
	fh := adultnewest.NewFeedHealth()
	seed := []adultnewest.MatchedRelease{
		// Riley: browse-confirmed → always available (keeps the pool non-empty so
		// the filter never fails open).
		{RowType: adultnewest.RowScene, EntityID: "sc1", EntitySource: "tpdb", EntityTitle: "Riley Scene",
			Performers: []string{"Riley Reid"}, BrowseConfirmed: true},
		// Jane: feed-only (feed 7) → available iff feed 7 is currently fresh.
		{RowType: adultnewest.RowScene, EntityID: "sc2", EntitySource: "tpdb", EntityTitle: "Jane Scene",
			Performers: []string{"Jane Doe"}, FeedID: 7, FeedItemKey: "http://feed/x.torrent", LastConfirmedSeen: now.Unix()},
	}
	mux, store := newAdultMuxWithCache(t, map[string]string{}, fh, seed)
	putPerformerCache(t, store, 1, false, []apidto.MergedPerformerCard{{Name: "Riley Reid"}, {Name: "Jane Doe"}})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Feed 7 fresh → pool = {Riley, Jane} → both cached cards survive.
	fh.SetHealthy(7, now)
	var healthy apidto.MergedPerformerPage
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged?page=1&perPage=20", &healthy)
	if len(healthy.Items) != 2 {
		t.Fatalf("with feed 7 fresh, expected both cached cards available, got %+v", healthy.Items)
	}

	// Flip feed 7 stale — NO precompute runs. Same cached payload, re-filtered live.
	fh.MarkUnhealthy(7)
	var stale apidto.MergedPerformerPage
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged?page=1&perPage=20", &stale)
	if len(stale.Items) != 1 || stale.Items[0].Name != "Riley Reid" {
		t.Fatalf("after the feed-health flip, expected only Riley (browse-confirmed) to survive, got %+v", stale.Items)
	}
}

// TestPerformersMerged_CacheHitEmptyPoolFailsOpen proves the empty-pool fail-open
// floor still applies on the cache-hit path: with no pool rows, every cached card
// is returned unfiltered.
func TestPerformersMerged_CacheHitEmptyPoolFailsOpen(t *testing.T) {
	fh := adultnewest.NewFeedHealth()
	mux, store := newAdultMuxWithCache(t, map[string]string{}, fh, nil)
	putPerformerCache(t, store, 1, false, []apidto.MergedPerformerCard{
		{Name: "Alpha"}, {Name: "Beta"}, {Name: "Gamma"},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var page apidto.MergedPerformerPage
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged?page=1&perPage=20", &page)
	if len(page.Items) != 3 {
		t.Fatalf("empty pool must fail open on the cache-hit path (all cards kept), got %+v", page.Items)
	}
}

// TestStudiosMerged_CacheHitServesWithoutUpstream is the studios-row parity of
// the performers cache-hit test.
func TestStudiosMerged_CacheHitServesWithoutUpstream(t *testing.T) {
	fh := adultnewest.NewFeedHealth()
	mux, store := newAdultMuxWithCache(t, map[string]string{}, fh, nil)
	putStudioCache(t, store, 1, true, []apidto.MergedStudioCard{
		{Name: "Vixen", Source: "merged", TPDBID: "t1", StashDBID: "s1"},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var page apidto.MergedStudioPage
	getJSON(t, srv.URL+"/api/modes/adult/studios-merged?page=1&perPage=20", &page)
	if len(page.Items) != 1 || page.Items[0].Name != "Vixen" {
		t.Fatalf("expected the cached studio served from cache (TPDB absent, so live would 400), got %+v", page.Items)
	}
	if !page.HasMore {
		t.Errorf("expected HasMore served from the cached value (true), got false")
	}
}
