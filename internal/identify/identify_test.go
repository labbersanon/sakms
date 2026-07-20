package identify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/bravesearch"
	"github.com/labbersanon/sakms/internal/ollama"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/throttle"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// testEnv wires up an Identifier with fake servers standing in for every
// external service, so the full pipeline can be exercised end-to-end without
// any real network calls.
type testEnv struct {
	t              *testing.T
	stashdbHits    int
	fansdbHits     int
	tpdbHits       int
	ollamaCalls    []string // prompts, in call order
	ollamaResponse func(callIndex int, prompt string) string
	braveHits      int
	braveResponse  func(query string) []bravesearch.Result

	stashdbSearchScene func(term string) []stashbox.Scene
	tpdbSearch         func(q, site string) []tpdbrest.Scene
}

func newTestEnv(t *testing.T) *testEnv {
	e := &testEnv{t: t}
	return e
}

func (e *testEnv) identifier(withFansdb, withBrave bool) *Identifier {
	httpClient := &http.Client{Timeout: 5 * time.Second}

	stashdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.stashdbHits++
		e.handleStashbox(w, r)
	}))
	e.t.Cleanup(stashdbSrv.Close)

	boxes := map[string]*stashbox.Client{
		"stashdb": stashbox.New(stashbox.Config{Endpoint: stashdbSrv.URL, APIKey: "k"}, httpClient),
	}
	if withFansdb {
		fansdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			e.fansdbHits++
			e.handleStashbox(w, r)
		}))
		e.t.Cleanup(fansdbSrv.Close)
		boxes["fansdb"] = stashbox.New(stashbox.Config{Endpoint: fansdbSrv.URL, APIKey: "k"}, httpClient)
	}

	tpdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.tpdbHits++
		q := r.URL.Query().Get("q")
		site := r.URL.Query().Get("site")
		var scenes []tpdbrest.Scene
		if e.tpdbSearch != nil {
			scenes = e.tpdbSearch(q, site)
		}
		writeJSON(w, tpdbSceneResponse(scenes))
	}))
	e.t.Cleanup(tpdbSrv.Close)
	tpdb := tpdbrest.New(tpdbSrv.URL, "k", httpClient)

	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := ""
		if len(req.Messages) > 0 {
			prompt = req.Messages[0].Content
		}
		idx := len(e.ollamaCalls)
		e.ollamaCalls = append(e.ollamaCalls, prompt)
		content := "{}"
		if e.ollamaResponse != nil {
			content = e.ollamaResponse(idx, prompt)
		}
		writeJSON(w, map[string]any{"message": map[string]any{"content": content}})
	}))
	e.t.Cleanup(ollamaSrv.Close)
	ollamaClient := ollama.New(ollamaSrv.URL, "test-model", httpClient)

	var braveClient *bravesearch.Client
	if withBrave {
		braveSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			e.braveHits++
			query := r.URL.Query().Get("q")
			var results []bravesearch.Result
			if e.braveResponse != nil {
				results = e.braveResponse(query)
			}
			writeJSON(w, braveResultsResponse(results))
		}))
		e.t.Cleanup(braveSrv.Close)
		braveClient = bravesearch.New(braveSrv.URL, "k", httpClient)
	}

	return &Identifier{
		Boxes:    NewBoxSearcher(boxes, tpdb),
		AI:       ollamaClient,
		Brave:    braveClient,
		Throttle: throttle.New(0), // no artificial delay in tests
	}
}

func (e *testEnv) handleStashbox(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string `json:"query"`
		Variables struct {
			Term string `json:"term"`
			ID   string `json:"id"`
		} `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.Variables.ID != "" {
		// findScene by id — not exercised by these tests, return null.
		writeJSON(w, map[string]any{"data": map[string]any{"findScene": nil}})
		return
	}

	var scenes []stashbox.Scene
	if e.stashdbSearchScene != nil {
		scenes = e.stashdbSearchScene(req.Variables.Term)
	}
	raw := make([]map[string]any, len(scenes))
	for i, s := range scenes {
		raw[i] = map[string]any{
			"id": s.ID, "title": s.Title, "release_date": s.ReleaseDate,
			"studio": map[string]any{"name": s.StudioName, "parent": nil},
		}
	}
	writeJSON(w, map[string]any{"data": map[string]any{"searchScene": raw}})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func tpdbSceneResponse(scenes []tpdbrest.Scene) map[string]any {
	raw := make([]map[string]any, len(scenes))
	for i, s := range scenes {
		raw[i] = map[string]any{
			"_id": s.ID, "title": s.Title, "date": s.Date,
			"site": map[string]any{"name": s.Site},
		}
	}
	return map[string]any{"data": raw}
}

func braveResultsResponse(results []bravesearch.Result) map[string]any {
	raw := make([]map[string]any, len(results))
	for i, r := range results {
		raw[i] = map[string]any{"title": r.Title, "description": r.Description, "url": r.URL}
	}
	return map[string]any{"web": map[string]any{"results": raw}}
}

func TestIdentify_UUIDDirectLookupSkipsEverythingElse(t *testing.T) {
	e := newTestEnv(t)
	id := &Identifier{
		Boxes: NewBoxSearcher(map[string]*stashbox.Client{
			"stashdb": stashboxWithFindScene(t, "a29768db-b3cd-4a71-a75e-4294373207bb", stashbox.Scene{
				ID: "a29768db-b3cd-4a71-a75e-4294373207bb", Title: "Direct Match", ReleaseDate: "2021-01-01", StudioName: "Hoby Buchanon",
			}),
		}, nil),
		AI:       e.identifier(false, false).AI, // present but must never be called
		Brave:    nil,
		Throttle: throttle.New(0),
	}

	result, err := id.Identify(context.Background(), "some-file-a29768db-b3cd-4a71-a75e-4294373207bb", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Source != "stashdb_id" || result.Title != "Direct Match" {
		t.Fatalf("got %+v", result)
	}
	if len(e.ollamaCalls) != 0 {
		t.Fatal("Ollama should not be called when UUID lookup succeeds")
	}
}

func stashboxWithFindScene(t *testing.T, wantID string, scene stashbox.Scene) *stashbox.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				ID string `json:"id"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Variables.ID != wantID {
			writeJSON(w, map[string]any{"data": map[string]any{"findScene": nil}})
			return
		}
		writeJSON(w, map[string]any{"data": map[string]any{"findScene": map[string]any{
			"id": scene.ID, "title": scene.Title, "release_date": scene.ReleaseDate,
			"studio": map[string]any{"name": scene.StudioName, "parent": nil},
		}}})
	}))
	t.Cleanup(srv.Close)
	return stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k"}, &http.Client{Timeout: 5 * time.Second})
}

func TestIdentify_QwenParseThenStashDBMatch_BackfillsYear(t *testing.T) {
	e := newTestEnv(t)
	e.ollamaResponse = func(idx int, prompt string) string {
		return `{"studio":"Tushy","title":"Some Scene","performers":null}`
	}
	e.stashdbSearchScene = func(term string) []stashbox.Scene {
		return []stashbox.Scene{{ID: "1", Title: "Some Scene", StudioName: "Tushy"}} // no release_date
	}
	id := e.identifier(false, false)

	// The year is no longer trusted from the LLM (see ExtractYearFromToken) —
	// it must come from a real date token in the stem for backfill to have
	// anything to work with.
	result, err := id.Identify(context.Background(), "tushy.19.05.20.some.scene", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Source != "stashdb_text" {
		t.Fatalf("got %+v", result)
	}
	if result.Date != "2019" {
		t.Fatalf("expected date backfilled from the deterministic year extraction, got %q", result.Date)
	}
}

func TestIdentify_FansiteHintGatesFansDB(t *testing.T) {
	e := newTestEnv(t)
	e.ollamaResponse = func(idx int, prompt string) string {
		return `{"studio":"SomeCreator","title":"A Clip","year":null,"performers":null}`
	}
	// stashdb never matches, forcing fansdb to be consulted if hinted
	e.stashdbSearchScene = func(term string) []stashbox.Scene { return nil }

	t.Run("not hinted, fansdb never called", func(t *testing.T) {
		id := e.identifier(true, false)
		_, _ = id.Identify(context.Background(), "some-clip-no-hint", "")
		if e.fansdbHits != 0 {
			t.Fatalf("expected fansdb to be skipped without a fansite hint, got %d hits", e.fansdbHits)
		}
	})
}

func TestIdentify_FansiteHintTriggersFansDB(t *testing.T) {
	e := newTestEnv(t)
	e.ollamaResponse = func(idx int, prompt string) string {
		return `{"studio":"SomeCreator","title":"A Clip","year":null,"performers":null}`
	}
	e.stashdbSearchScene = func(term string) []stashbox.Scene { return nil }
	id := e.identifier(true, false)

	_, _ = id.Identify(context.Background(), "onlyfans-some-clip", "")
	if e.fansdbHits == 0 {
		t.Fatal("expected fansdb to be consulted when the filename hints at fansite content")
	}
}

func TestIdentify_NoTitleFromQwen_ReturnsNilEarly(t *testing.T) {
	e := newTestEnv(t)
	e.ollamaResponse = func(idx int, prompt string) string {
		return `{"studio":null,"title":null,"year":null,"performers":null}`
	}
	e.stashdbSearchScene = func(term string) []stashbox.Scene {
		t.Fatal("should not search when Qwen extracted no title")
		return nil
	}
	id := e.identifier(false, false)

	result, err := id.Identify(context.Background(), "totally-unparseable", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %+v", result)
	}
}

func TestIdentify_NoBraveClient_StopsAfterInternalDBMiss(t *testing.T) {
	e := newTestEnv(t)
	e.ollamaResponse = func(idx int, prompt string) string {
		return `{"studio":"S","title":"T","year":null,"performers":null}`
	}
	e.stashdbSearchScene = func(term string) []stashbox.Scene { return nil }
	e.tpdbSearch = func(q, site string) []tpdbrest.Scene { return nil }
	id := e.identifier(false, false) // withBrave=false

	result, err := id.Identify(context.Background(), "no-brave-file", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil when no Brave client is configured and internal DBs miss, got %+v", result)
	}
}

func TestIdentify_FullWebSearchFallback_GroundedReSearchMatchesStashDB(t *testing.T) {
	e := newTestEnv(t)
	callCount := 0
	e.ollamaResponse = func(idx int, prompt string) string {
		callCount++
		if callCount == 1 {
			// qwen_parse_filename
			return `{"studio":"Vague Guess","title":"Ambiguous Title","year":null,"performers":null}`
		}
		// qwen_extract_from_search (grounded)
		return `{"studio":"Exposed Latinas","title":"Threesome With The Wife And Friend Scene 1","year":"2023"}`
	}
	e.stashdbSearchScene = func(term string) []stashbox.Scene {
		// First round (ungrounded guess) misses; grounded re-search hits.
		if term == "Exposed Latinas Threesome With The Wife And Friend Scene 1" || term == "Threesome With The Wife And Friend Scene 1 Exposed Latinas" {
			return []stashbox.Scene{{ID: "42", Title: "Threesome With The Wife And Friend Scene 1", StudioName: "Exposed Latinas", ReleaseDate: "2023-06-01"}}
		}
		return nil
	}
	e.braveResponse = func(query string) []bravesearch.Result {
		return []bravesearch.Result{{Title: "Exposed Latinas - Threesome Scene 1", Description: "desc", URL: "https://x"}}
	}
	id := e.identifier(false, true)

	result, err := id.Identify(context.Background(), "Exposed Latinas Threesome With The Wife And Friend Scene 1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected a match via grounded re-search")
	}
	if result.Source != "web+stashdb_text" {
		t.Fatalf("expected source prefixed web+ for a StashDB grounded re-search match, got %q", result.Source)
	}
	if result.SceneID != "42" {
		t.Fatalf("got %+v", result)
	}
}

func TestIdentify_GroundedReSearch_TPDBMatchDoesNotGetWebPrefix(t *testing.T) {
	// A TPDB match after grounding does NOT get "web+" prepended, unlike a
	// StashDB/FansDB match — see identify.go's reSearchAfterGrounding comment.
	//
	// The grounded title deliberately shares real tokens with the original
	// stem ("Rebel Rhyder Anal Scene") since a grounded result with ZERO
	// overlap is correctly rejected by ExtractFromSearch's own similarity
	// sanity gate.
	e := newTestEnv(t)
	callCount := 0
	e.ollamaResponse = func(idx int, prompt string) string {
		callCount++
		if callCount == 1 {
			return `{"studio":null,"title":"Rebel Rhyder Anal Scene","year":null,"performers":null}`
		}
		return `{"studio":"Real Studio","title":"Rebel Rhyder Anal Scene","year":"2022"}`
	}
	e.stashdbSearchScene = func(term string) []stashbox.Scene { return nil }
	e.tpdbSearch = func(q, site string) []tpdbrest.Scene {
		if q == "Rebel Rhyder Anal Scene" {
			return []tpdbrest.Scene{{ID: "99", Title: "Rebel Rhyder Anal Scene", Site: "Real Studio", Date: "2022-01-01"}}
		}
		return nil
	}
	e.braveResponse = func(query string) []bravesearch.Result {
		return []bravesearch.Result{{Title: "Real Studio - Rebel Rhyder Anal Scene", Description: "d", URL: "https://x"}}
	}
	id := e.identifier(false, true)

	result, err := id.Identify(context.Background(), "Rebel Rhyder Anal Scene", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected a TPDB match via grounded re-search")
	}
	if result.Source != "tpdb_text" {
		t.Fatalf("expected plain 'tpdb_text' (no web+ prefix, matching Python's asymmetric behavior), got %q", result.Source)
	}
}

func TestIdentify_NothingMatchesAnywhere_BareWebSearchResult(t *testing.T) {
	// Grounded title shares real tokens with the stem — see the comment on
	// TestIdentify_GroundedReSearch_TPDBMatchDoesNotGetWebPrefix for why a
	// zero-overlap fixture is unrealistic and gets correctly rejected by the
	// similarity sanity gate.
	e := newTestEnv(t)
	callCount := 0
	e.ollamaResponse = func(idx int, prompt string) string {
		callCount++
		if callCount == 1 {
			return `{"studio":null,"title":"Brand New Scene Title","year":null,"performers":null}`
		}
		return `{"studio":"Brand New Studio","title":"Brand New Scene Title","year":"2024"}`
	}
	e.stashdbSearchScene = func(term string) []stashbox.Scene { return nil }
	e.tpdbSearch = func(q, site string) []tpdbrest.Scene { return nil }
	e.braveResponse = func(query string) []bravesearch.Result {
		return []bravesearch.Result{{Title: "Brand New Studio - Brand New Scene Title", Description: "d", URL: "https://x"}}
	}
	id := e.identifier(false, true)

	result, err := id.Identify(context.Background(), "Brand New Scene Title", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected a bare web_search result when nothing exists in any database")
	}
	if result.Source != "web_search" || result.SceneID != "" {
		t.Fatalf("got %+v", result)
	}
	if result.Title != "Brand New Scene Title" || result.Studio != "Brand New Studio" {
		t.Fatalf("got %+v", result)
	}
}

func TestIdentifyDetailed_SceneMatchAlsoReturnsCorrectedStudioAndPerformers(t *testing.T) {
	e := newTestEnv(t)
	// Dotted guesses prove IdentifyDetailed surfaces verifyStudio/
	// verifyPerformers' post-correction output (separator-normalized), not the
	// raw AI guess. With the existing mock, findStudio/searchPerformer fall
	// through to no-match, so the normalized-fallback value is what's returned.
	e.ollamaResponse = func(idx int, prompt string) string {
		return `{"studio":"vixen.media","title":"Some Scene","performers":["riley.reid","jane.doe"]}`
	}
	e.stashdbSearchScene = func(term string) []stashbox.Scene {
		return []stashbox.Scene{{ID: "7", Title: "Some Scene", StudioName: "Vixen Media", ReleaseDate: "2023-01-01"}}
	}
	id := e.identifier(false, false)

	detail, err := id.IdentifyDetailed(context.Background(), "vixen.media.some.scene", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Scene == nil || detail.Scene.Source != "stashdb_text" || detail.Scene.SceneID != "7" {
		t.Fatalf("expected a stashdb scene match, got %+v", detail.Scene)
	}
	if detail.StudioName != "vixen media" {
		t.Fatalf("expected normalized corrected studio, got %q", detail.StudioName)
	}
	if len(detail.Performers) != 2 || detail.Performers[0] != "riley reid" || detail.Performers[1] != "jane doe" {
		t.Fatalf("expected normalized corrected performers, got %+v", detail.Performers)
	}
}

func TestIdentifyDetailed_NoSceneMatchStillReturnsStudioAndPerformers(t *testing.T) {
	// The new background job wants Studio/Performer rows even for releases that
	// don't cleanly resolve to a specific scene, so a nil Scene must NOT suppress
	// the corrected identities.
	e := newTestEnv(t)
	e.ollamaResponse = func(idx int, prompt string) string {
		return `{"studio":"exposed.latinas","title":"A Scene","performers":["some.performer"]}`
	}
	e.stashdbSearchScene = func(term string) []stashbox.Scene { return nil }
	e.tpdbSearch = func(q, site string) []tpdbrest.Scene { return nil }
	id := e.identifier(false, false) // withBrave=false — pipeline stops after internal-DB miss

	detail, err := id.IdentifyDetailed(context.Background(), "exposed.latinas.a.scene", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Scene != nil {
		t.Fatalf("expected no scene match, got %+v", detail.Scene)
	}
	if detail.StudioName != "exposed latinas" {
		t.Fatalf("expected corrected studio even with no scene, got %q", detail.StudioName)
	}
	if len(detail.Performers) != 1 || detail.Performers[0] != "some performer" {
		t.Fatalf("expected corrected performers even with no scene, got %+v", detail.Performers)
	}
}

func TestIdentifyDetailed_UUIDPathHasSceneButNoDerivedIdentities(t *testing.T) {
	// The direct-UUID branch never runs verifyStudio/verifyPerformers, so
	// StudioName/Performers are empty by design — faithful to "exactly what the
	// pipeline derived," never backfilled from the scene's own studio.
	e := newTestEnv(t)
	id := &Identifier{
		Boxes: NewBoxSearcher(map[string]*stashbox.Client{
			"stashdb": stashboxWithFindScene(t, "a29768db-b3cd-4a71-a75e-4294373207bb", stashbox.Scene{
				ID: "a29768db-b3cd-4a71-a75e-4294373207bb", Title: "Direct Match", ReleaseDate: "2021-01-01", StudioName: "Hoby Buchanon",
			}),
		}, nil),
		AI:       e.identifier(false, false).AI,
		Brave:    nil,
		Throttle: throttle.New(0),
	}

	detail, err := id.IdentifyDetailed(context.Background(), "some-file-a29768db-b3cd-4a71-a75e-4294373207bb", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Scene == nil || detail.Scene.Title != "Direct Match" {
		t.Fatalf("expected the UUID scene match, got %+v", detail.Scene)
	}
	if detail.StudioName != "" || len(detail.Performers) != 0 {
		t.Fatalf("expected no derived identities on the UUID path, got studio=%q performers=%+v", detail.StudioName, detail.Performers)
	}
	if len(e.ollamaCalls) != 0 {
		t.Fatal("Ollama should not be called when UUID lookup succeeds")
	}
}

func TestIdentifyDetailed_RejectsReleaseGroupTagAsStudio(t *testing.T) {
	// The studio false-positive guard (rejectNonStudioGuess/looksLikeReleaseGroupTag)
	// is inherited automatically because IdentifyDetailed calls the same
	// verifyStudio — a release-group-shaped tag yields an empty StudioName.
	e := newTestEnv(t)
	e.ollamaResponse = func(idx int, prompt string) string {
		return `{"studio":"WRB","title":"A Scene","performers":null}`
	}
	e.stashdbSearchScene = func(term string) []stashbox.Scene { return nil }
	e.tpdbSearch = func(q, site string) []tpdbrest.Scene { return nil }
	id := e.identifier(false, false)

	detail, err := id.IdentifyDetailed(context.Background(), "evilangel.25.11.11.rebel.rhyder.xxx.2160p.mp4-wrb", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.StudioName != "" {
		t.Fatalf("expected release-group tag rejected to empty studio, got %q", detail.StudioName)
	}
}

func TestIdentify_ParentFolderNameSkippedWhenGenericFolder(t *testing.T) {
	e := newTestEnv(t)
	var seenParent string
	e.ollamaResponse = func(idx int, prompt string) string {
		seenParent = prompt
		return `{"studio":null,"title":null,"year":null,"performers":null}`
	}
	id := e.identifier(false, false)

	_, _ = id.Identify(context.Background(), "some-file", "_unmatched")
	if len(e.ollamaCalls) == 0 {
		t.Fatal("expected Ollama to be called")
	}
	// The generic "_unmatched" folder name should NOT appear as context.
	if strings.Contains(seenParent, "parent folder named: '_unmatched'") {
		t.Fatal("expected generic parent folder names to be filtered out of context")
	}
}
