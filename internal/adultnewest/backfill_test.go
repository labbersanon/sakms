package adultnewest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/throttle"
)

// fakeGenderBox serves the stash-box searchPerformer query used by
// PerformerGender: a term in errNames returns a GraphQL error envelope (a
// reached-but-failed box → PerformerGender returns an error → backfill must
// leave the row NULL); a term in genders returns a name-matched performer with
// that raw gender; anything else returns an empty result (a genuine
// reached-no-match → ("", nil) → backfill writes ''). hits counts every
// request so a test can assert exactly how many rows the backfill processed
// before the circuit breaker stopped it.
func fakeGenderBox(t *testing.T, hits *atomic.Int64, genders map[string]string, errNames map[string]bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		term, _ := req.Variables["term"].(string)
		w.Header().Set("Content-Type", "application/json")
		if errNames[term] {
			fmt.Fprint(w, `{"errors":[{"message":"boom"}]}`)
			return
		}
		if g, ok := genders[term]; ok {
			fmt.Fprintf(w, `{"data":{"searchPerformer":[{"id":"p1","name":%q,"gender":%q,"images":[{"url":""}]}]}}`, term, g)
			return
		}
		fmt.Fprint(w, `{"data":{"searchPerformer":[]}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// backfillIdentifier builds an Identifier whose only configured box is the
// given stash-box URL, with an unthrottled Throttle — the same reused-Identify
// shape runCycle hands backfillPerformerGenders.
func backfillIdentifier(boxURL string) *identify.Identifier {
	return &identify.Identifier{
		Boxes: identify.NewBoxSearcher(map[string]*stashbox.Client{
			"stashdb": stashbox.New(stashbox.Config{Endpoint: boxURL, APIKey: "k"}, &http.Client{Timeout: 5 * time.Second}),
		}, nil),
		Throttle: throttle.New(0),
	}
}

// seedNullPerformer inserts a RowPerformer row and then nulls its gender via
// raw SQL, reproducing a pre-migration row (Insert always writes a concrete
// string or '', never NULL, so it cannot seed the backfill queue on its own).
// Returns the row id.
func seedNullPerformer(t *testing.T, ctx context.Context, s *ReleaseStore, name string) int {
	t.Helper()
	if err := s.Insert(ctx, MatchedRelease{
		RowType: RowPerformer, EntityID: name, EntitySource: "stashdb",
		EntityTitle: name, BrowseConfirmed: true,
	}); err != nil {
		t.Fatalf("seeding performer %q: %v", name, err)
	}
	var id int
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM adult_newest_releases WHERE row_type = ? AND entity_id = ?`,
		string(RowPerformer), name).Scan(&id); err != nil {
		t.Fatalf("looking up seeded performer %q: %v", name, err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE adult_newest_releases SET gender = NULL WHERE id = ?`, id); err != nil {
		t.Fatalf("nulling gender for %q: %v", name, err)
	}
	return id
}

// rawGender reads the gender column directly (bypassing scanRelease, which
// collapses NULL→"" via sql.NullString and so cannot tell a NULL row from an
// empty-string row — the exact distinction these tests must observe). Returns
// (value, isNull).
func rawGender(t *testing.T, ctx context.Context, s *ReleaseStore, id int) (string, bool) {
	t.Helper()
	var raw *string
	if err := s.db.QueryRowContext(ctx,
		`SELECT gender FROM adult_newest_releases WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("reading gender for id=%d: %v", id, err)
	}
	if raw == nil {
		return "", true
	}
	return *raw, false
}

// TestBackfill_ErrorLeavesRowNull is the load-bearing correctness property: a
// PerformerGender call that errors (transient box outage) must NOT persist a
// negative — the row stays NULL to retry next cycle, never written to ''.
// Asserted through UngenderedPerformers (WHERE gender IS NULL) and a raw column
// read, since List/scanRelease collapse NULL→"" and could not catch a wrongly
// written empty string.
func TestBackfill_ErrorLeavesRowNull(t *testing.T) {
	_, _, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	id := seedNullPerformer(t, ctx, releaseStore, "Riley Reid")
	box := fakeGenderBox(t, nil, nil, map[string]bool{"Riley Reid": true})

	if err := backfillPerformerGenders(ctx, backfillIdentifier(box.URL), releaseStore); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if v, isNull := rawGender(t, ctx, releaseStore, id); !isNull {
		t.Fatalf("expected gender still NULL after a box error, got %q (isNull=%v)", v, isNull)
	}
	queue, err := releaseStore.UngenderedPerformers(ctx)
	if err != nil {
		t.Fatalf("UngenderedPerformers: %v", err)
	}
	if len(queue) != 1 || queue[0].ID != id {
		t.Fatalf("expected the errored row to remain in the NULL work queue, got %+v", queue)
	}
}

// TestBackfill_ReachedNoGenderWritesEmptyString: a genuinely reached box with
// no gender on file is a real negative — written as '' (NOT left NULL), so the
// row leaves the work queue and isn't re-queried forever.
func TestBackfill_ReachedNoGenderWritesEmptyString(t *testing.T) {
	_, _, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	id := seedNullPerformer(t, ctx, releaseStore, "Nobody Here")
	box := fakeGenderBox(t, nil, nil, nil) // every term → empty result (reached, no match)

	if err := backfillPerformerGenders(ctx, backfillIdentifier(box.URL), releaseStore); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	v, isNull := rawGender(t, ctx, releaseStore, id)
	if isNull {
		t.Fatalf("expected gender written as '' for a reached-no-gender row, but it is still NULL")
	}
	if v != "" {
		t.Fatalf("expected gender '', got %q", v)
	}
	queue, err := releaseStore.UngenderedPerformers(ctx)
	if err != nil {
		t.Fatalf("UngenderedPerformers: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("expected the reached row to leave the NULL work queue, got %+v", queue)
	}
}

// TestBackfill_ResolvesConcreteGender: a name-matched box answer is written as
// its normalized gender.
func TestBackfill_ResolvesConcreteGender(t *testing.T) {
	_, _, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	id := seedNullPerformer(t, ctx, releaseStore, "Riley Reid")
	box := fakeGenderBox(t, nil, map[string]string{"Riley Reid": "FEMALE"}, nil)

	if err := backfillPerformerGenders(ctx, backfillIdentifier(box.URL), releaseStore); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if v, isNull := rawGender(t, ctx, releaseStore, id); isNull || v != "female" {
		t.Fatalf("expected gender 'female', got %q (isNull=%v)", v, isNull)
	}
}

// TestBackfill_CircuitBreakerStopsAfterThreshold seeds MORE than the threshold
// of rows against an always-erroring box and asserts the box was hit EXACTLY
// genderBackfillFailureThreshold times — proving the pass actually STOPPED,
// not merely that every row stayed NULL (which would be true either way). All
// rows stay NULL (safe/resumable).
func TestBackfill_CircuitBreakerStopsAfterThreshold(t *testing.T) {
	_, _, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	total := genderBackfillFailureThreshold + 5
	errNames := map[string]bool{}
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("fail-%02d", i)
		seedNullPerformer(t, ctx, releaseStore, name)
		errNames[name] = true
	}

	var hits atomic.Int64
	box := fakeGenderBox(t, &hits, nil, errNames)

	if err := backfillPerformerGenders(ctx, backfillIdentifier(box.URL), releaseStore); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if got := hits.Load(); got != int64(genderBackfillFailureThreshold) {
		t.Fatalf("box was hit %d times, want exactly %d (the breaker must stop the pass)", got, genderBackfillFailureThreshold)
	}
	queue, err := releaseStore.UngenderedPerformers(ctx)
	if err != nil {
		t.Fatalf("UngenderedPerformers: %v", err)
	}
	if len(queue) != total {
		t.Fatalf("expected all %d rows to remain NULL after the breaker fired, got %d still queued", total, len(queue))
	}
}

// TestBackfill_SuccessResetsCounter interleaves 19 failures, one success, then
// 19 more failures (39 rows, processed in id order per UngenderedPerformers'
// ORDER BY id). The success sits at position 20, right after 19 consecutive
// failures: it resets the counter to 0, so the following 19 failures never
// reach the threshold and the pass runs to completion. WITHOUT the reset, the
// counter would carry 19 past the success and the very next failure (position
// 21) would hit 20 and abort — hitting the box only 21 times. Asserting the box
// was hit for all 39 rows proves the reset happened.
func TestBackfill_SuccessResetsCounter(t *testing.T) {
	_, _, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	fails := genderBackfillFailureThreshold - 1 // 19
	errNames := map[string]bool{}
	// 19 failures first (ids 1..19).
	for i := 0; i < fails; i++ {
		name := fmt.Sprintf("a-fail-%02d", i)
		seedNullPerformer(t, ctx, releaseStore, name)
		errNames[name] = true
	}
	// One success (id 20) — its resolve resets the consecutive-failure counter.
	successID := seedNullPerformer(t, ctx, releaseStore, "the-success")
	// 19 more failures (ids 21..39).
	for i := 0; i < fails; i++ {
		name := fmt.Sprintf("z-fail-%02d", i)
		seedNullPerformer(t, ctx, releaseStore, name)
		errNames[name] = true
	}

	total := fails*2 + 1 // 39
	var hits atomic.Int64
	box := fakeGenderBox(t, &hits, map[string]string{"the-success": "MALE"}, errNames)

	if err := backfillPerformerGenders(ctx, backfillIdentifier(box.URL), releaseStore); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if got := hits.Load(); got != int64(total) {
		t.Fatalf("box was hit %d times, want %d (the success must reset the counter so the pass never aborts)", got, total)
	}
	if v, isNull := rawGender(t, ctx, releaseStore, successID); isNull || v != "male" {
		t.Fatalf("expected the success row resolved to 'male', got %q (isNull=%v)", v, isNull)
	}
	// The 38 failures stay NULL (resumable next cycle).
	queue, err := releaseStore.UngenderedPerformers(ctx)
	if err != nil {
		t.Fatalf("UngenderedPerformers: %v", err)
	}
	if len(queue) != fails*2 {
		t.Fatalf("expected %d failed rows still NULL, got %d", fails*2, len(queue))
	}
}

// TestBackfill_IdempotentAfterDrain: once the queue is fully drained a second
// run is a cheap no-op — UngenderedPerformers is empty and the box is never
// touched again.
func TestBackfill_IdempotentAfterDrain(t *testing.T) {
	_, _, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	seedNullPerformer(t, ctx, releaseStore, "Riley Reid")
	var hits atomic.Int64
	box := fakeGenderBox(t, &hits, map[string]string{"Riley Reid": "FEMALE"}, nil)
	id := backfillIdentifier(box.URL)

	if err := backfillPerformerGenders(ctx, id, releaseStore); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	first := hits.Load()
	if first != 1 {
		t.Fatalf("first run hit the box %d times, want 1", first)
	}

	if err := backfillPerformerGenders(ctx, id, releaseStore); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if got := hits.Load(); got != first {
		t.Fatalf("second run hit the box %d more time(s) — expected a no-op after full drain", got-first)
	}
}

// TestBackfill_ResumesAfterCircuitBreaker: a first pass tripped by a down box
// leaves every row NULL; when the box recovers, the next pass drains them all.
// Proves the NULL-queue design is resumable across cycles.
func TestBackfill_ResumesAfterCircuitBreaker(t *testing.T) {
	_, _, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	total := genderBackfillFailureThreshold + 5
	names := make([]string, total)
	for i := 0; i < total; i++ {
		names[i] = fmt.Sprintf("perf-%02d", i)
		seedNullPerformer(t, ctx, releaseStore, names[i])
	}

	// Cycle 1: box down for every name → breaker fires, everything stays NULL.
	downErr := map[string]bool{}
	for _, n := range names {
		downErr[n] = true
	}
	downBox := fakeGenderBox(t, nil, nil, downErr)
	if err := backfillPerformerGenders(ctx, backfillIdentifier(downBox.URL), releaseStore); err != nil {
		t.Fatalf("cycle 1 backfill: %v", err)
	}
	queue, err := releaseStore.UngenderedPerformers(ctx)
	if err != nil {
		t.Fatalf("UngenderedPerformers after cycle 1: %v", err)
	}
	if len(queue) != total {
		t.Fatalf("expected all %d rows still NULL after the breaker, got %d queued", total, len(queue))
	}

	// Cycle 2: box healthy for every name → the whole queue drains.
	upGenders := map[string]string{}
	for _, n := range names {
		upGenders[n] = "FEMALE"
	}
	upBox := fakeGenderBox(t, nil, upGenders, nil)
	if err := backfillPerformerGenders(ctx, backfillIdentifier(upBox.URL), releaseStore); err != nil {
		t.Fatalf("cycle 2 backfill: %v", err)
	}
	queue, err = releaseStore.UngenderedPerformers(ctx)
	if err != nil {
		t.Fatalf("UngenderedPerformers after cycle 2: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("expected the queue fully drained after the box recovered, got %d still NULL", len(queue))
	}
}

func seedUntaggedScene(t *testing.T, ctx context.Context, releaseStore *ReleaseStore, entityID string) int {
	t.Helper()
	if err := releaseStore.Insert(ctx, MatchedRelease{
		RowType:      RowScene,
		EntityID:     entityID,
		EntitySource: "stashdb",
		EntityTitle:  "Tagged Scene",
	}); err != nil {
		t.Fatalf("seeding untagged scene: %v", err)
	}
	list, err := releaseStore.UntaggedScenes(ctx, 0)
	if err != nil {
		t.Fatalf("UntaggedScenes: %v", err)
	}
	for _, row := range list {
		if row.EntityID == entityID {
			return row.ID
		}
	}
	t.Fatalf("seeded scene %q not found in untagged queue", entityID)
	return 0
}

func fakeSceneTagBox(t *testing.T, tags map[string][]string, errIDs map[string]bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		id, _ := req.Variables["id"].(string)
		w.Header().Set("Content-Type", "application/json")
		if errIDs[id] {
			fmt.Fprint(w, `{"errors":[{"message":"boom"}]}`)
			return
		}
		tagList, ok := tags[id]
		if !ok {
			fmt.Fprint(w, `{"data":{"findScene":null}}`)
			return
		}
		tagJSON := ""
		for i, name := range tagList {
			if i > 0 {
				tagJSON += ","
			}
			tagJSON += fmt.Sprintf(`{"name":%q}`, name)
		}
		fmt.Fprintf(w, `{"data":{"findScene":{"id":%q,"title":"T","release_date":"2024-01-01","studio":{"name":"S","parent":null},"tags":[%s]}}}`,
			id, tagJSON)
	}))
}

func TestBackfill_ResolvesSceneTags(t *testing.T) {
	_, _, releaseStore := newTestScanStores(t)
	ctx := context.Background()

	id := seedUntaggedScene(t, ctx, releaseStore, "scene-uuid-1")
	box := fakeSceneTagBox(t, map[string][]string{"scene-uuid-1": {"Anal", "Blonde"}}, nil)

	if err := backfillSceneTags(ctx, backfillIdentifier(box.URL), releaseStore); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	got := findByID(t, releaseStore, RowScene, "scene-uuid-1")
	if len(got.Genres) != 2 || got.Genres[0] != "Anal" || got.Genres[1] != "Blonde" {
		t.Fatalf("expected genres resolved, got %+v", got.Genres)
	}
	queue, err := releaseStore.UntaggedScenes(ctx, 0)
	if err != nil {
		t.Fatalf("UntaggedScenes: %v", err)
	}
	for _, row := range queue {
		if row.ID == id {
			t.Fatalf("expected row %d to leave the untagged queue", id)
		}
	}
}
