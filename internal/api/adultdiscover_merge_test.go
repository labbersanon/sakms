package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// --- Unit: mergePerformers / mergeStudios ---

func TestMergePerformers_NearExactPairCollapses(t *testing.T) {
	// Identical names → collapse to one displayed name, StashDB-first, no
	// divergence flag, no AltName, both ids set, Source "merged".
	tpdb := []tpdbrest.Performer{{ID: "t1", Name: "Riley Reid", Image: "t.jpg"}}
	stash := []stashbox.Performer{{ID: "s1", Name: "Riley Reid", ImageURL: "s.jpg"}}
	out := mergePerformers(tpdb, stash)
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
	out := mergePerformers(tpdb, stash)
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
	out := mergePerformers(tpdb, stash)
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
	out := mergePerformers(tpdb, stash)
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
	out := mergePerformers(nil, stash)
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
	out := mergePerformers(tpdb, nil)
	if len(out) != 1 || out[0].Source != "tpdb" || out[0].TPDBID != "t1" || out[0].StashDBID != "" {
		t.Errorf("expected a pure TPDB-only passthrough, got %+v", out)
	}
}

func TestMergePerformers_EmptyBothNoPanic(t *testing.T) {
	out := mergePerformers(nil, nil)
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
	out := mergeStudios(tpdb, stash)
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
	dout := mergeStudios(dt, ds)
	if len(dout) != 1 || !dout[0].NamesDiverged || dout[0].Name != "Big City Films East" || dout[0].AltName != "Big City Films West" {
		t.Errorf("expected a divergent-name merged studio surfacing both names, got %+v", dout)
	}

	// Nil-leg passthrough.
	if got := mergeStudios(nil, ds); len(got) != 1 || got[0].Source != "stashdb" {
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
		{ID: "sx", Title: "Dup", PHashes: []string{"h1"}}, // shares h1 → dropped
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

	var cards []apidto.MergedPerformerCard
	getJSON(t, srv.URL+"/api/modes/adult/performers-merged?perPage=5", &cards)
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
		var cards []apidto.MergedPerformerCard
		getJSON(t, fmt.Sprintf("%s/api/modes/adult/performers-merged?page=%d&perPage=%d", srv.URL, page, perPage), &cards)
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
		w.Write([]byte(`{"data":[{"uuid":"t1","name":"Vixen","logo":"http://cdn/t.png"}],"meta":{"current_page":1}}`))
	})
	stash := fakeStashBox(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"queryStudios":{"studios":[` +
			`{"id":"s1","name":"Vixen","images":[{"url":"http://cdn/s.png"}]},` +
			`{"id":"s2","name":"Exclusive Studio","images":[]}]}}}`))
	})
	srv := httptest.NewServer(newAdultMux(t, map[string]string{"tpdb": tpdb.URL, "stashdb": stash.URL}))
	defer srv.Close()

	var cards []apidto.MergedStudioCard
	getJSON(t, srv.URL+"/api/modes/adult/studios-merged?perPage=5", &cards)
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
