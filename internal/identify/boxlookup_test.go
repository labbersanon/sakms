package identify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

func TestIsFansiteHinted(t *testing.T) {
	if !IsFansiteHinted("some OnlyFans clip") {
		t.Error("expected OnlyFans to be hinted")
	}
	if !IsFansiteHinted("", "ManyVids export") {
		t.Error("expected ManyVids to be hinted across multiple texts")
	}
	if IsFansiteHinted("Tushy - Some Scene", "Tushy") {
		t.Error("expected a normal studio scene to NOT be fansite-hinted")
	}
}

func newBoxSearcherWithFakes(t *testing.T, stashboxHandler, tpdbHandler http.HandlerFunc) *BoxSearcher {
	t.Helper()
	boxes := map[string]*stashbox.Client{}
	if stashboxHandler != nil {
		srv := httptest.NewServer(stashboxHandler)
		t.Cleanup(srv.Close)
		boxes["stashdb"] = stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k"}, &http.Client{Timeout: 5 * time.Second})
	}
	var tpdb *tpdbrest.Client
	if tpdbHandler != nil {
		srv := httptest.NewServer(tpdbHandler)
		t.Cleanup(srv.Close)
		tpdb = tpdbrest.New(srv.URL, "k", &http.Client{Timeout: 5 * time.Second})
	}
	return NewBoxSearcher(boxes, tpdb)
}

func TestSearchStashBox_StudioMismatchRejected(t *testing.T) {
	calls := 0
	b := newBoxSearcherWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"searchScene":[
			{"id":"1","title":"Some Title Match","release_date":"2020-01-01","studio":{"name":"Brazzers","parent":null}}
		]}}}`))
	}, nil)

	// Genuinely zero token overlap between "Tushy" and "Brazzers".
	got, err := b.SearchStashBox(context.Background(), "stashdb", "Some Title Match", "Tushy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected zero-overlap studio mismatch to be rejected, got %+v", got)
	}
}

func TestSearchStashBox_StudioOverlapAccepted(t *testing.T) {
	b := newBoxSearcherWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"searchScene":[
			{"id":"1","title":"Gaping Anal With Adriana","release_date":"2022-09-05","studio":{"name":"TeamSkeet X Evil Angel","parent":null}}
		]}}}`))
	}, nil)

	// "TeamSkeet" shares a token with "TeamSkeet X Evil Angel" — should be accepted.
	got, err := b.SearchStashBox(context.Background(), "stashdb", "Gaping Anal With Adriana", "TeamSkeet")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected a match when studio shares token overlap")
	}
}

func TestSearchStashBox_LowSimilarityRejected(t *testing.T) {
	b := newBoxSearcherWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"searchScene":[
			{"id":"1","title":"Completely Different Content","release_date":"2020-01-01","studio":{"name":"Some Studio","parent":null}}
		]}}}`))
	}, nil)

	got, err := b.SearchStashBox(context.Background(), "stashdb", "My Original Title Words", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected low title similarity to be rejected, got %+v", got)
	}
}

func TestSearchStashBox_UnconfiguredBoxReturnsNilNoError(t *testing.T) {
	b := newBoxSearcherWithFakes(t, nil, nil) // "fansdb" never configured
	got, err := b.SearchStashBox(context.Background(), "fansdb", "Anything", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for an unconfigured box, got %+v", got)
	}
}

func TestSearchStashBox_CachesRepeatedCalls(t *testing.T) {
	calls := 0
	b := newBoxSearcherWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"searchScene":[
			{"id":"1","title":"Some Title","release_date":"2024-01-01","studio":{"name":"","parent":null}}
		]}}}`))
	}, nil)

	ctx := context.Background()
	r1, _ := b.SearchStashBox(ctx, "stashdb", "Some Title", "")
	r1.Source = "web+" + r1.Source // mutate the returned copy
	r2, _ := b.SearchStashBox(ctx, "stashdb", "Some Title", "")

	if calls != 1 {
		t.Fatalf("expected only 1 real HTTP call (second should hit cache), got %d", calls)
	}
	if r2.Source != "stashdb_text" {
		t.Fatalf("expected mutation of r1 not to leak into r2's cached value, got %q", r2.Source)
	}
}

// TPDB text search does NOT apply a studio-overlap gate (studio narrows
// server-side via the REST "site" param instead), unlike SearchStashBox's
// client-side gate.
func TestSearchTPDB_NoStudioGate(t *testing.T) {
	b := newBoxSearcherWithFakes(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"Matching Title Words","date":"2024-01-01","site":{"name":"Some Unrelated Site"}}]}`))
	})

	got, err := b.SearchTPDB(context.Background(), "Matching Title Words", "A Totally Different Studio Guess")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected TPDB text search to accept a title match regardless of studio mismatch")
	}
	if got.Source != "tpdb_text" || got.Box != "tpdb" {
		t.Fatalf("got %+v", got)
	}
}

// TestSearchTPDB_PopulatesPerformersFromOwnSceneData proves MatchResult.
// Performers is sourced from the matched TPDB scene's own performer list
// (comma-joined), the same authoritative-sourcing convention as Tags — not
// left empty and not sourced from anywhere else.
func TestSearchTPDB_PopulatesPerformersFromOwnSceneData(t *testing.T) {
	b := newBoxSearcherWithFakes(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"Matching Title Words","date":"2024-01-01","site":{"name":"Some Site"},"performers":[{"name":"Jane Doe"},{"name":"John Roe"}]}]}`))
	})

	got, err := b.SearchTPDB(context.Background(), "Matching Title Words", "Some Site")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected a match")
	}
	if got.Performers != "Jane Doe,John Roe" {
		t.Errorf("Performers = %q, want %q", got.Performers, "Jane Doe,John Roe")
	}
}

func TestSearchTPDB_ZeroDurationFallsBackToByIDRefetch(t *testing.T) {
	// Regression: TPDB's search endpoint sometimes returns duration:0 for a
	// scene that genuinely has a real duration on file (found live
	// 2026-07-15 — 46 of 51 cached scenes had entity_duration_seconds=0
	// despite TPDB's own site showing a real duration). SearchTPDB must
	// confirm via GET /scenes/{id} rather than trusting the search result.
	byIDCalls := 0
	b := newBoxSearcherWithFakes(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/scenes/11034171" {
			byIDCalls++
			_, _ = w.Write([]byte(`{"data":{"_id":"11034171","title":"Matching Title Words","date":"2024-01-01","site":{"name":"Some Site"},"duration":1863}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"_id":"11034171","title":"Matching Title Words","date":"2024-01-01","site":{"name":"Some Site"},"duration":0}]}`))
	})

	got, err := b.SearchTPDB(context.Background(), "Matching Title Words", "Some Site")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected a match")
	}
	if got.RuntimeSeconds != 1863 {
		t.Errorf("RuntimeSeconds = %d, want 1863 (from the by-id re-fetch)", got.RuntimeSeconds)
	}
	if byIDCalls != 1 {
		t.Errorf("expected exactly 1 by-id re-fetch call, got %d", byIDCalls)
	}
}

func TestSearchTPDB_NonZeroDurationSkipsByIDRefetch(t *testing.T) {
	byIDCalls := 0
	b := newBoxSearcherWithFakes(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/scenes/11034171" {
			byIDCalls++
			_, _ = w.Write([]byte(`{"data":{"_id":"11034171","title":"Matching Title Words","date":"2024-01-01","site":{"name":"Some Site"},"duration":9999}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"_id":"11034171","title":"Matching Title Words","date":"2024-01-01","site":{"name":"Some Site"},"duration":1863}]}`))
	})

	got, err := b.SearchTPDB(context.Background(), "Matching Title Words", "Some Site")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.RuntimeSeconds != 1863 {
		t.Fatalf("expected the search result's own duration (1863) to be trusted, got %+v", got)
	}
	if byIDCalls != 0 {
		t.Errorf("expected no by-id re-fetch when search duration is already non-zero, got %d calls", byIDCalls)
	}
}

func TestSceneByID_NotFound(t *testing.T) {
	b := newBoxSearcherWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScene":null}}`))
	}, nil)

	got, err := b.SceneByID(context.Background(), "stashdb", "some-uuid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for not-found scene, got %+v", got)
	}
}

func TestSceneByID_Found(t *testing.T) {
	b := newBoxSearcherWithFakes(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScene":{"id":"uuid1","title":"T","release_date":"2020-01-01","studio":{"name":"S","parent":null}}}}`))
	}, nil)

	got, err := b.SceneByID(context.Background(), "stashdb", "uuid1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Source != "stashdb_id" || got.SceneID != "uuid1" {
		t.Fatalf("got %+v", got)
	}
}

// TestMatchResult_RuntimeSecondsThreadedThrough is the regression test for a
// live bug: MatchResult.RuntimeSeconds wasn't populated by ANY lookup path
// until this fix, so every Adult grab request built from a cached match had
// no real runtime — Adult's bitrate-quality-floor scorer never re-fetches
// one itself (unlike Movies/Series), so every candidate silently landed in
// the manual fallback pick list. Covers both the stash-box and TPDB text
// search paths — SceneByID/SearchTPDBMovies share the same decode plumbing,
// not independently re-tested here.
func TestMatchResult_RuntimeSecondsThreadedThrough(t *testing.T) {
	b := newBoxSearcherWithFakes(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"searchScene":[
				{"id":"1","title":"Some Scene","release_date":"2020-01-01","studio":{"name":"Vixen","parent":null},"duration":1800}
			]}}}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"Some Scene","date":"2020-01-01","site":{"name":"Vixen"},"duration":2400}]}`))
		},
	)

	stashGot, err := b.SearchStashBox(context.Background(), "stashdb", "Some Scene", "Vixen")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stashGot == nil || stashGot.RuntimeSeconds != 1800 {
		t.Fatalf("SearchStashBox: got %+v, want RuntimeSeconds=1800", stashGot)
	}

	tpdbGot, err := b.SearchTPDB(context.Background(), "Some Scene", "Vixen")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tpdbGot == nil || tpdbGot.RuntimeSeconds != 2400 {
		t.Fatalf("SearchTPDB: got %+v, want RuntimeSeconds=2400", tpdbGot)
	}
}
