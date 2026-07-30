package identify

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/throttle"
)

// stashboxCounts tallies which stash-box GraphQL operation each fake request
// carried, routed by the query text (all ops POST to the one endpoint).
type stashboxCounts struct {
	findStudio            int32
	searchPerformer       int32
	queryScenesByStudio   int32 // queryScenes with a "studios" filter in the body
	queryScenesByPerformer int32 // queryScenes with a "performers" filter in the body
}

// tpdbCounts tallies which TPDB REST endpoint each fake request hit.
type tpdbCounts struct {
	searchSites       int32
	searchPerformers  int32
	scenesBySite      int32
	scenesByPerformer int32
	sceneByID         int32 // GET /scenes/{id} — resolveTPDBDuration's re-fetch; must stay 0
}

// stashboxFake returns a handler that answers FindStudio/SearchPerformer/
// queryScenes (studio- or performer-filtered), counting each, and writes the
// supplied JSON payloads.
func stashboxFake(counts *stashboxCounts, studioJSON, performerJSON, scenesJSON string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(s, "findStudio"):
			atomic.AddInt32(&counts.findStudio, 1)
			_, _ = w.Write([]byte(studioJSON))
		case strings.Contains(s, "searchPerformer"):
			atomic.AddInt32(&counts.searchPerformer, 1)
			_, _ = w.Write([]byte(performerJSON))
		case strings.Contains(s, "queryScenes"):
			// The catalog fetch embeds either a "studios" or "performers" filter
			// key in the variables — route on that to prove the studio drill
			// used QueryScenesByStudio and the performer drill used
			// QueryScenesByPerformer.
			if strings.Contains(s, "\"studios\"") {
				atomic.AddInt32(&counts.queryScenesByStudio, 1)
			} else if strings.Contains(s, "\"performers\"") {
				atomic.AddInt32(&counts.queryScenesByPerformer, 1)
			}
			_, _ = w.Write([]byte(scenesJSON))
		default:
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}
}

// tpdbFake returns a handler routing on the REST path, counting each endpoint,
// and writing the supplied JSON payloads.
func tpdbFake(counts *tpdbCounts, sitesJSON, performersJSON, scenesJSON string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case (strings.HasPrefix(p, "/sites/") || strings.HasPrefix(p, "/performers/")) && strings.HasSuffix(p, "/scenes"):
			if strings.HasPrefix(p, "/sites/") {
				atomic.AddInt32(&counts.scenesBySite, 1)
			} else {
				atomic.AddInt32(&counts.scenesByPerformer, 1)
			}
			_, _ = w.Write([]byte(scenesJSON))
		case strings.HasPrefix(p, "/scenes/"):
			atomic.AddInt32(&counts.sceneByID, 1)
			_, _ = w.Write([]byte(`{"data":{}}`))
		case p == "/sites":
			atomic.AddInt32(&counts.searchSites, 1)
			_, _ = w.Write([]byte(sitesJSON))
		case p == "/performers":
			atomic.AddInt32(&counts.searchPerformers, 1)
			_, _ = w.Write([]byte(performersJSON))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}
}

func newEnricher(t *testing.T, interval time.Duration, stashboxHandler, tpdbHandler http.HandlerFunc) *Identifier {
	t.Helper()
	return &Identifier{
		Boxes:    newBoxSearcherWithFakes(t, stashboxHandler, tpdbHandler),
		Throttle: throttle.New(interval),
	}
}

const (
	studioSceneJSON = `{"data":{"queryScenes":{"scenes":[
		{"id":"sc1","title":"Some Scene Title","release_date":"2023-01-01","studio":{"name":"Brazzers"},"images":[{"url":"http://cdn/sc1.jpg"}],"duration":1200}
	]}}}`
	tpdbStudioSceneJSON = `{"data":[
		{"_id":"tsc1","title":"Some Scene Title","date":"2023-01-01","site":{"name":"Brazzers"},"image":"http://cdn/tsc1.jpg","duration":0,"tags":[{"id":1,"name":"Anal"},{"id":2,"name":"Blonde"}],"performers":[{"name":"Riley Reid"},{"name":"Jane Doe"}]}
	]}`
)

// TestEnrichNewestScenes_ThrottlesBeforeEveryCall proves every upstream call is
// throttled: a single-box studio drill makes exactly two same-host calls
// (FindStudio + QueryScenesByStudio), so with a non-zero interval the second is
// spaced by ~interval behind the first. A missing Throttle.Wait on either call
// would collapse that spacing to ~0.
func TestEnrichNewestScenes_ThrottlesBeforeEveryCall(t *testing.T) {
	const interval = 60 * time.Millisecond
	var counts stashboxCounts
	id := newEnricher(t, interval,
		stashboxFake(&counts, `{"data":{"findStudio":{"id":"s1","name":"Brazzers"}}}`, "", studioSceneJSON),
		nil,
	)

	start := time.Now()
	got, err := id.EnrichNewestScenes(context.Background(), "studio", "Brazzers", []string{"Brazzers.Some.Scene.Title.XXX.1080p-GRP"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&counts.findStudio) != 1 || atomic.LoadInt32(&counts.queryScenesByStudio) != 1 {
		t.Fatalf("expected exactly 1 FindStudio + 1 QueryScenesByStudio, got %+v", counts)
	}
	if elapsed < interval-10*time.Millisecond {
		t.Fatalf("expected the two same-host calls spaced by ~%s (proving both were throttled), took only %s", interval, elapsed)
	}
	if len(got) != 1 {
		t.Fatalf("expected the one release to match the catalog scene, got %d matches", len(got))
	}
}

// TestEnrichNewestScenes_OboxesCallAccounting is the load-bearing budget
// invariant: many release titles must still call each box's resolve + catalog
// method AT MOST ONCE per drill — O(boxes), never O(items).
func TestEnrichNewestScenes_OboxesCallAccounting(t *testing.T) {
	var sc stashboxCounts
	var tc tpdbCounts
	id := newEnricher(t, 0,
		stashboxFake(&sc, `{"data":{"findStudio":{"id":"s1","name":"Brazzers"}}}`, "", studioSceneJSON),
		tpdbFake(&tc, `{"data":[{"uuid":"site1","name":"Brazzers"}]}`, "", tpdbStudioSceneJSON),
	)

	titles := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		titles = append(titles, "Brazzers.Some.Scene.Title.XXX.1080p-GRP")
	}
	if _, err := id.EnrichNewestScenes(context.Background(), "studio", "Brazzers", titles); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := atomic.LoadInt32(&sc.findStudio); got != 1 {
		t.Errorf("FindStudio: expected 1 call for 25 items, got %d", got)
	}
	if got := atomic.LoadInt32(&sc.queryScenesByStudio); got != 1 {
		t.Errorf("QueryScenesByStudio: expected 1 call for 25 items, got %d", got)
	}
	if got := atomic.LoadInt32(&tc.searchSites); got != 1 {
		t.Errorf("SearchSites: expected 1 call for 25 items, got %d", got)
	}
	if got := atomic.LoadInt32(&tc.scenesBySite); got != 1 {
		t.Errorf("ScenesBySite: expected 1 call for 25 items, got %d", got)
	}
}

// TestEnrichNewestScenes_NearTieDeclines proves the ambiguity guard: two
// candidates BOTH clearing 0.6 within nameTieDelta of each other cause the
// resolve to decline (no id), so that box fetches no catalog and the item stays
// raw. Distinct from the low-score case below — here the best candidate is
// ABOVE threshold; only the near-tie declines it.
func TestEnrichNewestScenes_NearTieDeclines(t *testing.T) {
	var sc stashboxCounts
	// Drill "Riley Reid"; two candidates both fully contain it → both score 1.0
	// (containment), a genuine delta-0 tie above the 0.6 bar.
	id := newEnricher(t, 0,
		stashboxFake(&sc, "",
			`{"data":{"searchPerformer":[{"id":"p1","name":"Riley Reid X"},{"id":"p2","name":"Riley Reid Y"}]}}`,
			studioSceneJSON),
		nil,
	)

	got, err := id.EnrichNewestScenes(context.Background(), "performer", "Riley Reid", []string{"Riley.Reid.Some.Scene.XXX-GRP"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected a near-tie to decline resolution (no matches), got %d", len(got))
	}
	if atomic.LoadInt32(&sc.searchPerformer) != 1 {
		t.Errorf("expected the resolve search to still fire once, got %d", sc.searchPerformer)
	}
	if got := atomic.LoadInt32(&sc.queryScenesByPerformer); got != 0 {
		t.Errorf("expected NO catalog fetch after an ambiguous resolve, got %d", got)
	}
}

// TestEnrichNewestScenes_LowScoreDeclines proves the below-threshold guard: a
// single candidate scoring < 0.6 is rejected, so no catalog is fetched. Kept
// separate from the near-tie test — this one exercises the low-score branch.
func TestEnrichNewestScenes_LowScoreDeclines(t *testing.T) {
	var sc stashboxCounts
	// "Riley Reid" vs "Riley Smith": 1 token overlap → jaccard 1/3 ≈ 0.33 < 0.6.
	id := newEnricher(t, 0,
		stashboxFake(&sc, "",
			`{"data":{"searchPerformer":[{"id":"p1","name":"Riley Smith"}]}}`,
			studioSceneJSON),
		nil,
	)

	got, err := id.EnrichNewestScenes(context.Background(), "performer", "Riley Reid", []string{"Riley.Reid.Some.Scene.XXX-GRP"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected a low-scoring resolve to decline (no matches), got %d", len(got))
	}
	if got := atomic.LoadInt32(&sc.queryScenesByPerformer); got != 0 {
		t.Errorf("expected NO catalog fetch after a low-score resolve, got %d", got)
	}
}

// TestResolveCandidateID covers the object-keyed resolve helper's three
// outcomes directly, including the score arithmetic the near-tie branch relies
// on.
func TestResolveCandidateID(t *testing.T) {
	// Accepted: one clear winner above threshold, no near-tie.
	id, name, _, outcome := resolveCandidateID("riley reid",
		[]namedCandidate{{name: "Riley Reid", id: "p1"}, {name: "Totally Different", id: "p2"}}, performerMatchThreshold)
	if id != "p1" || name != "Riley Reid" || outcome != "accepted" {
		t.Errorf("accepted case: got id=%q name=%q outcome=%q", id, name, outcome)
	}

	// Ambiguous: two candidates both >= 0.6 within nameTieDelta.
	id, _, _, outcome = resolveCandidateID("riley reid",
		[]namedCandidate{{name: "Riley Reid X", id: "p1"}, {name: "Riley Reid Y", id: "p2"}}, performerMatchThreshold)
	if id != "" || outcome != "declined-ambiguous" {
		t.Errorf("ambiguous case: got id=%q outcome=%q", id, outcome)
	}

	// Low score: single candidate below threshold.
	id, _, _, outcome = resolveCandidateID("riley reid",
		[]namedCandidate{{name: "Riley Smith", id: "p1"}}, performerMatchThreshold)
	if id != "" || outcome != "declined-low-score" {
		t.Errorf("low-score case: got id=%q outcome=%q", id, outcome)
	}

	// No candidates: declines low-score with a zero score.
	id, _, _, outcome = resolveCandidateID("riley reid", nil, performerMatchThreshold)
	if id != "" || outcome != "declined-low-score" {
		t.Errorf("empty case: got id=%q outcome=%q", id, outcome)
	}
}

// TestMatchReleaseTitles_Containment proves a raw release string with studio/
// quality/group cruft matches a clean catalog title via maxSimilarity
// containment, while an unrelated title does not.
func TestMatchReleaseTitles_Containment(t *testing.T) {
	catalog := []*MatchResult{
		{Title: "Some Scene Title", Box: "stashdb"},
		{Title: "Completely Unrelated Feature", Box: "stashdb"},
	}
	titles := []string{
		"Studio.Name.Some.Scene.Title.XXX.1080p-GROUP", // should match "Some Scene Title"
		"Nothing.In.Common.Here.XXX-GRP",               // should match nothing
	}
	got := matchReleaseTitles(titles, catalog)
	if m, ok := got[0]; !ok || m.Title != "Some Scene Title" {
		t.Errorf("expected release 0 to match 'Some Scene Title' via containment, got %+v (ok=%v)", got[0], ok)
	}
	if _, ok := got[1]; ok {
		t.Errorf("expected release 1 (unrelated) to match nothing, got %+v", got[1])
	}
}

// TestEnrichNewestScenes_NoResolveTPDBDuration proves the code path never calls
// resolveTPDBDuration/GetSceneByID (GET /scenes/{id}): a TPDB catalog scene with
// duration:0 must be accepted as-is (RuntimeSeconds==0), with zero /scenes/{id}
// re-fetches.
func TestEnrichNewestScenes_NoResolveTPDBDuration(t *testing.T) {
	var tc tpdbCounts
	id := &Identifier{
		Boxes: newBoxSearcherWithFakes(t, nil,
			tpdbFake(&tc, `{"data":[{"uuid":"site1","name":"Brazzers"}]}`, "", tpdbStudioSceneJSON)),
		Throttle: throttle.New(0),
	}

	got, err := id.EnrichNewestScenes(context.Background(), "studio", "Brazzers", []string{"Brazzers.Some.Scene.Title.XXX-GRP"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&tc.sceneByID) != 0 {
		t.Fatalf("resolveTPDBDuration/GetSceneByID must NEVER be called, got %d /scenes/{id} requests", tc.sceneByID)
	}
	m, ok := got[0]
	if !ok {
		t.Fatalf("expected the release to match the TPDB catalog scene")
	}
	if m.RuntimeSeconds != 0 {
		t.Errorf("expected the catalog scene's duration (0) accepted as-is, got %d", m.RuntimeSeconds)
	}
	// The TPDB leg carries Tags + Performers (comma-joined, no space).
	if m.Tags != "Anal,Blonde" {
		t.Errorf("expected Tags comma-joined no-space, got %q", m.Tags)
	}
	if m.Performers != "Riley Reid,Jane Doe" {
		t.Errorf("expected Performers comma-joined no-space, got %q", m.Performers)
	}
}

// TestEnrichNewestScenes_NoBoxesConfigured proves a clean no-op (empty map, no
// error, no panic) when nothing is configured.
func TestEnrichNewestScenes_NoBoxesConfigured(t *testing.T) {
	id := &Identifier{Boxes: NewBoxSearcher(map[string]*stashbox.Client{}, nil), Throttle: throttle.New(0)}
	got, err := id.EnrichNewestScenes(context.Background(), "studio", "Brazzers", []string{"Anything-GRP"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty result with no boxes configured, got %+v", got)
	}
}

// TestEnrichNewestScenes_StudioVsPerformerPath proves the studio drill uses the
// studio resolve+catalog methods and the performer drill uses the performer
// ones — the same drill name routed two ways must hit different endpoints.
func TestEnrichNewestScenes_StudioVsPerformerPath(t *testing.T) {
	t.Run("studio", func(t *testing.T) {
		var sc stashboxCounts
		id := newEnricher(t, 0,
			stashboxFake(&sc, `{"data":{"findStudio":{"id":"s1","name":"Brazzers"}}}`,
				`{"data":{"searchPerformer":[]}}`, studioSceneJSON),
			nil,
		)
		if _, err := id.EnrichNewestScenes(context.Background(), "studio", "Brazzers", []string{"x-GRP"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if atomic.LoadInt32(&sc.findStudio) != 1 {
			t.Errorf("studio drill must call FindStudio, got %d", sc.findStudio)
		}
		if atomic.LoadInt32(&sc.searchPerformer) != 0 {
			t.Errorf("studio drill must NOT call SearchPerformer, got %d", sc.searchPerformer)
		}
	})
	t.Run("performer", func(t *testing.T) {
		var sc stashboxCounts
		id := newEnricher(t, 0,
			stashboxFake(&sc, `{"data":{"findStudio":null}}`,
				`{"data":{"searchPerformer":[{"id":"p1","name":"Riley Reid"}]}}`, studioSceneJSON),
			nil,
		)
		if _, err := id.EnrichNewestScenes(context.Background(), "performer", "Riley Reid", []string{"x-GRP"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if atomic.LoadInt32(&sc.searchPerformer) != 1 {
			t.Errorf("performer drill must call SearchPerformer, got %d", sc.searchPerformer)
		}
		if atomic.LoadInt32(&sc.findStudio) != 0 {
			t.Errorf("performer drill must NOT call FindStudio, got %d", sc.findStudio)
		}
		if atomic.LoadInt32(&sc.queryScenesByPerformer) != 1 {
			t.Errorf("performer drill must call QueryScenesByPerformer, got %d", sc.queryScenesByPerformer)
		}
	})
}

// TestEnrichNewestScenes_CrossBoxPrefersStashDB proves the deterministic
// StashDB > TPDB merge: when a release matches in both boxes, the StashDB match
// wins.
func TestEnrichNewestScenes_CrossBoxPrefersStashDB(t *testing.T) {
	var sc stashboxCounts
	var tc tpdbCounts
	id := newEnricher(t, 0,
		stashboxFake(&sc, `{"data":{"findStudio":{"id":"s1","name":"Brazzers"}}}`, "", studioSceneJSON),
		tpdbFake(&tc, `{"data":[{"uuid":"site1","name":"Brazzers"}]}`, "", tpdbStudioSceneJSON),
	)

	got, err := id.EnrichNewestScenes(context.Background(), "studio", "Brazzers", []string{"Brazzers.Some.Scene.Title.XXX-GRP"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := got[0]
	if !ok {
		t.Fatalf("expected a match")
	}
	if m.Box != "stashdb" {
		t.Errorf("expected StashDB to win the cross-box merge, got box=%q", m.Box)
	}
}

// TestEnrichNewestScenes_BoxErrorNeverFails proves best-effort: a box that
// errors on resolve contributes no catalog but never fails the call (and a
// sibling box still enriches).
func TestEnrichNewestScenes_BoxErrorNeverFails(t *testing.T) {
	var tc tpdbCounts
	id := newEnricher(t, 0,
		func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) },
		tpdbFake(&tc, `{"data":[{"uuid":"site1","name":"Brazzers"}]}`, "", tpdbStudioSceneJSON),
	)

	got, err := id.EnrichNewestScenes(context.Background(), "studio", "Brazzers", []string{"Brazzers.Some.Scene.Title.XXX-GRP"})
	if err != nil {
		t.Fatalf("a failing box must never fail the whole call, got %v", err)
	}
	// StashDB errored → TPDB still enriches.
	if m, ok := got[0]; !ok || m.Box != "tpdb" {
		t.Errorf("expected the surviving TPDB match, got %+v (ok=%v)", got[0], ok)
	}
}
