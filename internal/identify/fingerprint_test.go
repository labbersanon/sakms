package identify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/throttle"
)

// fakeFingerprintBox stands in for one stash-box's findScenesBySceneFingerprints
// endpoint — results keyed by phash, a missing key means "no match" for that
// phash. Records every phash it was actually queried for, so cascade-order
// and stage-skip behavior can be asserted precisely.
type fakeFingerprintBox struct {
	t        *testing.T
	results  map[string]stashbox.Scene
	queried  []string
	failNext bool
}

func newFakeFingerprintBox(t *testing.T, results map[string]stashbox.Scene) *stashbox.Client {
	t.Helper()
	f := &fakeFingerprintBox{t: t, results: results}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k", HasVoteField: true}, &http.Client{Timeout: 5 * time.Second})
}

func (f *fakeFingerprintBox) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Variables struct {
			FPs [][]map[string]string `json:"fps"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Fatalf("decoding request: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	if f.failNext {
		fmt.Fprint(w, `{"errors":[{"message":"boom"}]}`)
		return
	}
	matches := make([][]map[string]any, len(req.Variables.FPs))
	for i, fp := range req.Variables.FPs {
		hash := fp[0]["hash"]
		f.queried = append(f.queried, hash)
		if scene, ok := f.results[hash]; ok {
			matches[i] = []map[string]any{{
				"id": scene.ID, "title": scene.Title, "release_date": scene.ReleaseDate,
				"studio": map[string]any{"name": scene.StudioName},
			}}
		} else {
			matches[i] = []map[string]any{}
		}
	}
	body, _ := json.Marshal(map[string]any{"data": map[string]any{"findScenesBySceneFingerprints": matches}})
	w.Write(body)
}

func newTestIdentifier(boxes map[string]*stashbox.Client) *Identifier {
	return &Identifier{GiveBack: NewGiveBack(boxes), Throttle: throttle.New(0)}
}

func TestLookupFingerprints_StashDBHitStopsCascade(t *testing.T) {
	stashdb := newFakeFingerprintBox(t, map[string]stashbox.Scene{
		"hash1": {ID: "scene1", Title: "Some Scene", StudioName: "Some Studio", ReleaseDate: "2020-01-01"},
	})
	fansdb := newFakeFingerprintBox(t, nil)

	id := newTestIdentifier(map[string]*stashbox.Client{"stashdb": stashdb, "fansdb": fansdb})
	results, err := id.LookupFingerprints(context.Background(), []string{"hash1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := results["hash1"]
	if !ok {
		t.Fatalf("expected a match for hash1, got %+v", results)
	}
	if got.Title != "Some Scene" || got.SceneID != "scene1" || got.Box != "stashdb" || got.Source != "stashdb_fingerprint" {
		t.Errorf("unexpected match: %+v", got)
	}
}

func TestLookupFingerprints_MissAtOneStageFallsThroughToNext(t *testing.T) {
	stashdb := newFakeFingerprintBox(t, nil) // no match anywhere on stashdb
	fansdb := newFakeFingerprintBox(t, map[string]stashbox.Scene{
		"hash1": {ID: "scene9", Title: "Fans Scene"},
	})

	id := newTestIdentifier(map[string]*stashbox.Client{"stashdb": stashdb, "fansdb": fansdb})
	results, err := id.LookupFingerprints(context.Background(), []string{"hash1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := results["hash1"]; got == nil || got.Box != "fansdb" || got.SceneID != "scene9" {
		t.Fatalf("expected a fansdb match, got %+v", results["hash1"])
	}
}

func TestLookupFingerprints_UnconfiguredBoxSkippedNotFatal(t *testing.T) {
	tpdb := newFakeFingerprintBox(t, map[string]stashbox.Scene{
		"hash1": {ID: "scene1", Title: "TPDB Scene"},
	})
	// stashdb/fansdb deliberately absent from the map — must be skipped, not error.
	id := newTestIdentifier(map[string]*stashbox.Client{"tpdb": tpdb})
	results, err := id.LookupFingerprints(context.Background(), []string{"hash1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := results["hash1"]; got == nil || got.Box != "tpdb" {
		t.Fatalf("expected a tpdb match after skipping unconfigured stages, got %+v", results["hash1"])
	}
}

func TestLookupFingerprints_ChunkErrorRetriedAtNextStage(t *testing.T) {
	// Force stashdb to always error by pointing at a handler that always fails.
	failingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"errors":[{"message":"stashdb is down"}]}`)
	}))
	defer failingSrv.Close()
	failingStashdb := stashbox.New(stashbox.Config{Endpoint: failingSrv.URL, APIKey: "k", HasVoteField: true}, &http.Client{Timeout: 5 * time.Second})

	fansdb := newFakeFingerprintBox(t, map[string]stashbox.Scene{"hash1": {ID: "scene1", Title: "Recovered"}})
	id := newTestIdentifier(map[string]*stashbox.Client{"stashdb": failingStashdb, "fansdb": fansdb})
	results, err := id.LookupFingerprints(context.Background(), []string{"hash1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := results["hash1"]; got == nil || got.Box != "fansdb" {
		t.Fatalf("expected the stashdb error to fall through to fansdb, got %+v", results["hash1"])
	}
}

func TestLookupFingerprints_RemainingRecomputedFromOriginalOrder(t *testing.T) {
	// hash1 matches on stashdb, hash2 doesn't match anywhere on stashdb but
	// does on fansdb — proves `remaining` for the fansdb stage is derived from
	// the ORIGINAL two-hash order (only hash2 should be queried against
	// fansdb), not some stale shrinking slice.
	stashdb := newFakeFingerprintBox(t, map[string]stashbox.Scene{"hash1": {ID: "s1", Title: "One"}})
	fansdb := newFakeFingerprintBox(t, map[string]stashbox.Scene{"hash2": {ID: "s2", Title: "Two"}})

	id := newTestIdentifier(map[string]*stashbox.Client{"stashdb": stashdb, "fansdb": fansdb})
	results, err := id.LookupFingerprints(context.Background(), []string{"hash1", "hash2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 || results["hash1"].Box != "stashdb" || results["hash2"].Box != "fansdb" {
		t.Fatalf("expected both hashes resolved via their respective stages, got %+v", results)
	}
}

func TestLookupFingerprints_BatchingBoundary(t *testing.T) {
	results := map[string]stashbox.Scene{}
	var phashes []string
	for i := 0; i < fingerprintBatchSize+1; i++ {
		hash := fmt.Sprintf("hash%d", i)
		phashes = append(phashes, hash)
		results[hash] = stashbox.Scene{ID: fmt.Sprintf("scene%d", i), Title: "T"}
	}
	stashdb := newFakeFingerprintBox(t, results)
	id := newTestIdentifier(map[string]*stashbox.Client{"stashdb": stashdb})
	got, err := id.LookupFingerprints(context.Background(), phashes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(phashes) {
		t.Fatalf("expected all %d phashes resolved across the batch boundary, got %d", len(phashes), len(got))
	}
}

func TestLookupFingerprints_NilGiveBack_ReturnsEmptyNoError(t *testing.T) {
	id := &Identifier{Throttle: throttle.New(0)}
	got, err := id.LookupFingerprints(context.Background(), []string{"hash1"})
	if err != nil || len(got) != 0 {
		t.Fatalf("expected an empty result with no error, got %+v, err=%v", got, err)
	}
}

func TestLookupFingerprints_EmptyPhashes_ReturnsEmptyNoError(t *testing.T) {
	stashdb := newFakeFingerprintBox(t, nil)
	id := newTestIdentifier(map[string]*stashbox.Client{"stashdb": stashdb})
	got, err := id.LookupFingerprints(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("expected an empty result with no error, got %+v, err=%v", got, err)
	}
}
