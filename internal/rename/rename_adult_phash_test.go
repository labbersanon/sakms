package rename

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/mediainfo"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/servarr"
	"github.com/curtiswtaylorjr/sakms/internal/stashbox"
	"github.com/curtiswtaylorjr/sakms/internal/throttle"
)

// countingAI counts ChatJSON calls and always returns resp — lets tests
// assert whether the legacy AI/text pipeline actually ran.
type countingAI struct {
	calls int
	resp  map[string]any
}

func (a *countingAI) ChatJSON(ctx context.Context, prompt string) (map[string]any, error) {
	a.calls++
	return a.resp, nil
}

// fakeHasher stands in for the videophash hasher: a canned hash per path, or a
// forced error for paths in errs (proving per-file fail-open to the legacy
// pipeline). Satisfies the rename-local PHasher interface.
type fakeHasher struct {
	hashes map[string]string
	errs   map[string]bool
}

func (f *fakeHasher) Hash(_ context.Context, path string) (string, error) {
	if f.errs[path] {
		return "", fmt.Errorf("boom hashing %s", path)
	}
	return f.hashes[path], nil
}

// fakeProber stands in for the mediainfo prober, supplying a canned duration
// (seconds) per path — the source of a proposal's DurationSeconds now that it
// no longer rides in on a Stash read. Satisfies the rename-local Prober interface.
type fakeProber struct {
	durations map[string]float64
}

func (f *fakeProber) Probe(_ context.Context, path string) (*mediainfo.Probe, error) {
	return &mediainfo.Probe{Duration: f.durations[path]}, nil
}

// giveBackRecord captures what a fake stash-box saw at fingerprint give-back,
// so the give-back-through-Apply test can assert the duration actually flowed
// (the guard against the silent duration-coupling regression).
type giveBackRecord struct {
	submitted bool
	sceneID   string
	hash      string
	duration  int
}

// textMatchScene is the canned searchScene (text-search) response newFakeAdultBox
// serves when textMatch is non-nil — the counterpart to the fingerprint-cascade
// results map, for tests exercising the legacy text/AI fallback path
// (identify.BoxSearcher.SearchStashBox -> stashbox.Client.SearchScene).
type textMatchScene struct {
	id, title, studio string
}

// newFakeAdultBox stands in for one stash-box's fingerprint + text-search
// endpoints. It serves the cascade lookup (findScenesBySceneFingerprints, keyed
// by phash — a missing key means no match); when rec is non-nil, the give-back
// submitFingerprint mutation, recording the submitted duration; and, when
// textMatch is non-nil, the searchScene text-search query used by the legacy
// AI/text identification fallback. Reimplemented here (rather than shared with
// internal/identify's own fingerprint test fake) since that one is unexported
// to its own package.
func newFakeAdultBox(t *testing.T, results map[string]struct{ id, title string }, rec *giveBackRecord, textMatch *textMatchScene) *stashbox.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string          `json:"query"`
			Variables json.RawMessage `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(req.Query, "SubmitFingerprint"):
			var v struct {
				Input struct {
					SceneID     string `json:"scene_id"`
					Fingerprint struct {
						Hash     string `json:"hash"`
						Duration int    `json:"duration"`
					} `json:"fingerprint"`
				} `json:"input"`
			}
			json.Unmarshal(req.Variables, &v)
			if rec != nil {
				rec.submitted = true
				rec.sceneID = v.Input.SceneID
				rec.hash = v.Input.Fingerprint.Hash
				rec.duration = v.Input.Fingerprint.Duration
			}
			fmt.Fprint(w, `{"data":{"submitFingerprint":true}}`)
		case strings.Contains(req.Query, "SearchScene"):
			scenes := []any{}
			if textMatch != nil {
				scenes = append(scenes, map[string]any{
					"id": textMatch.id, "title": textMatch.title, "release_date": "",
					"studio": map[string]any{"name": textMatch.studio},
				})
			}
			body, _ := json.Marshal(map[string]any{"data": map[string]any{"searchScene": scenes}})
			w.Write(body)
		default:
			var v struct {
				FPs [][]map[string]string `json:"fps"`
			}
			json.Unmarshal(req.Variables, &v)
			matches := make([][]map[string]any, len(v.FPs))
			for i, fp := range v.FPs {
				hash := fp[0]["hash"]
				if scene, ok := results[hash]; ok {
					matches[i] = []map[string]any{{"id": scene.id, "title": scene.title, "release_date": "", "studio": map[string]any{"name": ""}}}
				} else {
					matches[i] = []map[string]any{}
				}
			}
			body, _ := json.Marshal(map[string]any{"data": map[string]any{"findScenesBySceneFingerprints": matches}})
			w.Write(body)
		}
	}))
	t.Cleanup(srv.Close)
	return stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k", HasVoteField: true}, srv.Client())
}

// adultTestSession builds a Whisparr *mode.Session wired for the phash-first
// pipeline. The fake Servarr handler fails the test if it's ever called —
// scanAdultPhashFirst and its legacy fallback (proposeOneAdult) never touch
// the *arr app; that's Apply's job, not Scan's.
func adultTestSession(t *testing.T, ai *countingAI, boxes map[string]*stashbox.Client) *mode.Session {
	t.Helper()
	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("must never call the *arr app during Scan, got %s %s", r.Method, r.URL.Path)
	})
	var aiClient identify.AIClient
	if ai != nil {
		aiClient = ai
	}
	sess.Identify = &identify.Identifier{
		AI:       aiClient,
		GiveBack: identify.NewGiveBack(boxes),
		Boxes:    identify.NewBoxSearcher(boxes, nil),
		Throttle: throttle.New(0),
	}
	return sess
}

func TestScanAdultPhashFirst_CascadeHit_SkipsAIEntirely(t *testing.T) {
	path := "/media/Adult/scene1.mp4"
	hasher := &fakeHasher{hashes: map[string]string{path: "hash1"}}
	prober := &fakeProber{durations: map[string]float64{path: 1800}}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		"hash1": {id: "box-scene-1", title: "Cascade Scene"},
	}, nil, nil)
	ai := &countingAI{}
	sess := adultTestSession(t, ai, map[string]*stashbox.Client{"stashdb": stashdb})

	candidates := []adultCandidate{{
		root: servarr.RootFolder{Path: "/media/Adult"},
		uf:   servarr.UnmappedFolder{Name: "scene1.mp4", Path: path},
	}}
	out := scanAdultPhashFirst(context.Background(), sess, hasher, prober, candidates, nil, []servarr.QualityProfile{{ID: 4}})
	if len(out) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(out), out)
	}
	p := out[0]
	if p.Status != proposals.Pending || p.Title != "Cascade Scene" || p.ForeignID != "box-scene-1" {
		t.Fatalf("expected a fingerprint-cascade hit, got %+v", p)
	}
	if p.GiveBackBox != "stashdb" || p.GiveBackSceneID != "box-scene-1" {
		t.Errorf("expected give-back target captured from the cascade match, got box=%q scene=%q", p.GiveBackBox, p.GiveBackSceneID)
	}
	if p.PHash != "hash1" || p.DurationSeconds != 1800 {
		t.Errorf("expected phash from the hasher and duration from the prober, got phash=%q duration=%d", p.PHash, p.DurationSeconds)
	}
	if ai.calls != 0 {
		t.Errorf("expected the AI/text pipeline to never run on a cascade hit, got %d calls", ai.calls)
	}
}

func TestScanAdultPhashFirst_CascadeMiss_FallsThroughToProposeOneAdult(t *testing.T) {
	path := "/media/Adult/scene1.mp4"
	hasher := &fakeHasher{hashes: map[string]string{path: "hash1"}}
	prober := &fakeProber{}
	stashdb := newFakeAdultBox(t, nil, nil, nil) // no match anywhere
	ai := &countingAI{resp: map[string]any{"studio": nil, "title": nil, "year": nil, "performers": nil}}
	sess := adultTestSession(t, ai, map[string]*stashbox.Client{"stashdb": stashdb})

	candidates := []adultCandidate{{
		root: servarr.RootFolder{Path: "/media/Adult"},
		uf:   servarr.UnmappedFolder{Name: "scene1.mp4", Path: path},
	}}
	out := scanAdultPhashFirst(context.Background(), sess, hasher, prober, candidates, nil, nil)
	if len(out) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(out), out)
	}
	if out[0].Status != proposals.Unmatched {
		t.Fatalf("expected a cascade miss to fall through to the legacy pipeline and end up Unmatched, got %+v", out[0])
	}
	if ai.calls == 0 {
		t.Error("expected the legacy AI/text pipeline to actually run on a cascade miss")
	}
}

// TestScanAdultPhashFirst_HashError_PerFileFallsOpenToLegacy proves the
// fail-open is per-file, not all-or-nothing: candidate A hashes and matches the
// cascade (Pending), while candidate B's Hash errors and routes ONLY B to the
// legacy pipeline. Replaces the old Stash-load-error test — a batched Stash
// read (and its all-or-nothing failure mode) no longer exists.
func TestScanAdultPhashFirst_HashError_PerFileFallsOpenToLegacy(t *testing.T) {
	pathA := "/media/Adult/a.mp4"
	pathB := "/media/Adult/b.mp4"
	hasher := &fakeHasher{
		hashes: map[string]string{pathA: "hashA"},
		errs:   map[string]bool{pathB: true},
	}
	prober := &fakeProber{durations: map[string]float64{pathA: 1800}}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		"hashA": {id: "box-a", title: "Scene A"},
	}, nil, nil)
	ai := &countingAI{resp: map[string]any{"studio": nil, "title": nil, "year": nil, "performers": nil}}
	sess := adultTestSession(t, ai, map[string]*stashbox.Client{"stashdb": stashdb})

	candidates := []adultCandidate{
		{root: servarr.RootFolder{Path: "/media/Adult"}, uf: servarr.UnmappedFolder{Name: "a.mp4", Path: pathA}},
		{root: servarr.RootFolder{Path: "/media/Adult"}, uf: servarr.UnmappedFolder{Name: "b.mp4", Path: pathB}},
	}
	out := scanAdultPhashFirst(context.Background(), sess, hasher, prober, candidates, nil, []servarr.QualityProfile{{ID: 4}})
	if len(out) != 2 {
		t.Fatalf("expected 2 proposals (one cascade hit, one legacy), got %d: %+v", len(out), out)
	}
	// Order-preserved build: candidate-index order (A before B), regardless
	// of whether each one resolved via cascade hit or legacy fallback.
	if out[0].Status != proposals.Pending || out[0].Title != "Scene A" || out[0].PHash != "hashA" {
		t.Errorf("expected candidate A to resolve via the cascade despite B erroring, got %+v", out[0])
	}
	if out[1].SourcePath != pathB || out[1].Status != proposals.Unmatched {
		t.Errorf("expected candidate B (hash error) to fall through to the legacy pipeline, got %+v", out[1])
	}
	if ai.calls != 1 {
		t.Errorf("expected the legacy pipeline to run for exactly the one errored candidate, got %d AI calls", ai.calls)
	}
}

// TestScanAdultPhashFirst_GiveBackFiresWithProberDuration is the NON-NEGOTIABLE
// guard against the silent duration-coupling regression: it carries a
// cascade-hit proposal all the way through rename.Apply and asserts that
// fingerprint give-back actually FIRED with the prober-sourced duration. The
// stamping check in the cascade-hit test alone does NOT catch this, because
// submitFingerprintGiveBack fails open (returns false, no error) on a
// non-positive DurationSeconds — so a duration that never made it in would look
// identical to a give-back that simply wasn't configured.
func TestScanAdultPhashFirst_GiveBackFiresWithProberDuration(t *testing.T) {
	path := "/media/Adult/scene1.mp4"
	rec := &giveBackRecord{}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		"hash1": {id: "box-scene-1", title: "Cascade Scene"},
	}, rec, nil)
	hasher := &fakeHasher{hashes: map[string]string{path: "hash1"}}
	prober := &fakeProber{durations: map[string]float64{path: 1800}}

	// A session whose Whisparr accepts Apply's Add + downloaded-scan (Scan never
	// touches the *arr app; Apply does), and whose give-back routes to the
	// recording box.
	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodPost:
			json.NewEncoder(w).Encode(map[string]any{"id": 77})
		case r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected *arr call during Apply: %s %s", r.Method, r.URL.Path)
		}
	})
	sess.Identify = &identify.Identifier{
		GiveBack: identify.NewGiveBack(map[string]*stashbox.Client{"stashdb": stashdb}),
		Throttle: throttle.New(0),
	}

	candidates := []adultCandidate{{
		root: servarr.RootFolder{Path: "/media/Adult"},
		uf:   servarr.UnmappedFolder{Name: "scene1.mp4", Path: path},
	}}
	out := scanAdultPhashFirst(context.Background(), sess, hasher, prober, candidates, nil, []servarr.QualityProfile{{ID: 4}})
	if len(out) != 1 || out[0].Status != proposals.Pending {
		t.Fatalf("expected one Pending cascade-hit proposal to carry into Apply, got %+v", out)
	}
	if out[0].PHash != "hash1" || out[0].DurationSeconds != 1800 {
		t.Fatalf("expected the scanned proposal to carry phash+prober duration into Apply, got phash=%q duration=%d", out[0].PHash, out[0].DurationSeconds)
	}

	_, submitted, err := Apply(context.Background(), sess, out[0])
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}
	if !submitted {
		t.Fatal("expected Apply to report fingerprint give-back as submitted")
	}
	if !rec.submitted {
		t.Fatal("expected the stash-box to have received a give-back submission end-to-end through Apply")
	}
	if rec.duration != 1800 {
		t.Errorf("expected give-back to carry the prober's duration 1800 (the duration-coupling guard), got %d", rec.duration)
	}
	if rec.hash != "hash1" || rec.sceneID != "box-scene-1" {
		t.Errorf("expected give-back to carry the scanned phash/scene, got hash=%q scene=%q", rec.hash, rec.sceneID)
	}
}

// TestScanAdultPhashFirst_TextMatchFallback_GivesBackAtApplyStashFree is the
// Part 1 regression guard: a candidate that hashes fine but MISSES the
// fingerprint cascade and instead resolves via the legacy AI/text pipeline
// (searchInternalDBs -> BoxSearcher.SearchStashBox) must still carry its
// LOCAL phash+prober duration into its proposal, so fingerprint give-back
// fires at Apply — Stash-free. Before the Part 1 fix, only cascade hits
// stamped PHash/DurationSeconds, so this exact scenario reached Apply with
// GiveBackBox set but PHash == "", and submitFingerprintGiveBack silently
// no-op'd; recovery required the now-retired SubmitFingerprintRetry.
func TestScanAdultPhashFirst_TextMatchFallback_GivesBackAtApplyStashFree(t *testing.T) {
	path := "/media/Adult/scene1.mp4"
	rec := &giveBackRecord{}
	stashdb := newFakeAdultBox(t, nil, rec, &textMatchScene{
		id: "box-scene-1", title: "Text Match Scene", studio: "Test Studio",
	}) // cascade miss (no fingerprint results), text search resolves via textMatch
	hasher := &fakeHasher{hashes: map[string]string{path: "hash1"}}
	prober := &fakeProber{durations: map[string]float64{path: 1800}}
	ai := &countingAI{resp: map[string]any{
		"studio": "Test Studio", "title": "Text Match Scene", "year": nil, "performers": nil,
	}}

	// A session whose Whisparr accepts Apply's Add + downloaded-scan (Scan never
	// touches the *arr app; Apply does), and whose Identify has Boxes wired
	// (unlike the plain GiveBack-only literals in the cascade-hit tests above)
	// because this test's fallback path actually reaches searchInternalDBs ->
	// SearchStashBox, unlike a cascade hit which never touches AI/Boxes.
	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodPost:
			json.NewEncoder(w).Encode(map[string]any{"id": 77})
		case r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected *arr call during Apply: %s %s", r.Method, r.URL.Path)
		}
	})
	boxes := map[string]*stashbox.Client{"stashdb": stashdb}
	sess.Identify = &identify.Identifier{
		AI:       ai,
		GiveBack: identify.NewGiveBack(boxes),
		Boxes:    identify.NewBoxSearcher(boxes, nil),
		Throttle: throttle.New(0),
	}

	candidates := []adultCandidate{{
		root: servarr.RootFolder{Path: "/media/Adult"},
		uf:   servarr.UnmappedFolder{Name: "scene1.mp4", Path: path},
	}}
	out := scanAdultPhashFirst(context.Background(), sess, hasher, prober, candidates, nil, []servarr.QualityProfile{{ID: 4}})
	if len(out) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(out), out)
	}
	p := out[0]
	if ai.calls == 0 {
		t.Error("expected the legacy AI/text pipeline to actually run on a cascade miss")
	}
	if p.Status != proposals.Pending {
		t.Fatalf("expected the text match's valid scene ID to classify as Pending, got %+v", p)
	}
	if p.PHash != "hash1" || p.DurationSeconds != 1800 {
		t.Fatalf("expected the cascade-miss/text-match proposal to still carry the LOCAL phash+prober duration (the Part 1 fix), got phash=%q duration=%d", p.PHash, p.DurationSeconds)
	}
	if p.GiveBackBox != "stashdb" || p.GiveBackSceneID != "box-scene-1" {
		t.Fatalf("expected give-back target captured from the text match, got box=%q scene=%q", p.GiveBackBox, p.GiveBackSceneID)
	}

	trackedID, submitted, err := Apply(context.Background(), sess, p)
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}
	if trackedID != 77 {
		t.Fatalf("expected Apply to register the proposal with Whisparr, got trackedID=%d", trackedID)
	}
	if !submitted {
		t.Fatal("expected give-back to fire at Apply for a text-matched proposal, Stash-free (the Part 1 fix) — no sess.Stash was ever configured in this test")
	}
	if !rec.submitted {
		t.Fatal("expected the stash-box to have received a give-back submission end-to-end through Apply")
	}
	if rec.hash != "hash1" || rec.duration != 1800 || rec.sceneID != "box-scene-1" {
		t.Errorf("expected give-back to carry the local phash/prober duration/text-matched scene, got hash=%q duration=%d scene=%q", rec.hash, rec.duration, rec.sceneID)
	}
}
