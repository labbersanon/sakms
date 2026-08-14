package rename

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mediainfo"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/servarr"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/throttle"
)

// countingAI counts ChatJSON calls and always returns resp — lets tests
// assert whether the legacy AI/text pipeline actually ran.
//
// Claude 2026-08-07: prompts capture added (additive — .calls is unchanged)
// Reason: sess.MainstreamAI has eleven-plus consumers reachable from one scan
//
//	(Movies GuessTitle, Kids classify.WithAI, ...), so a bare .calls counter
//	is not a discriminator for "THIS path made no AI call": it fails
//	spuriously when an unrelated consumer fires and proves nothing when it is
//	0. Series-episode assertions must inspect prompt CONTENT for
//	identify.SeriesEpisodePromptMarker instead — see seriesPromptCount in
//	series_ai_episode_match_test.go.
//
// Troubleshooting: a "no AI call" test passing vacuously — confirm it uses
//
//	seriesPromptCount, not .calls.
//
// Review if: .calls gains a reader. Verified 2026-08-07 by grep: it has NONE,
//
//	in this package or any other — it is incremented and never asserted on,
//	and was already write-only at HEAD before this change, so no Adult-mode
//	assertion was disturbed. Delete the field if no reader ever lands.
type countingAI struct {
	calls   int
	prompts []string
	resp    map[string]any
}

func (a *countingAI) ChatJSON(ctx context.Context, prompt string) (map[string]any, error) {
	a.calls++
	a.prompts = append(a.prompts, prompt)
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

type fakeAdultScene struct {
	id, title, image string
	tags             []string
}

// newFakeAdultBox stands in for one stash-box's fingerprint + text-search
// endpoints. It serves the cascade lookup (findScenesBySceneFingerprints, keyed
// by phash — a missing key means no match); when rec is non-nil, the give-back
// submitFingerprint mutation, recording the submitted duration; and, when
// textMatch is non-nil, the searchScene text-search query used by the legacy
// AI/text identification fallback. Reimplemented here (rather than shared with
// internal/identify's own fingerprint test fake) since that one is unexported
// to its own package.
func newFakeAdultBox(t *testing.T, results map[string]fakeAdultScene, rec *giveBackRecord, textMatch *textMatchScene) *stashbox.Client {
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
					row := map[string]any{"id": scene.id, "title": scene.title, "release_date": "", "studio": map[string]any{"name": ""}}
					if scene.image != "" {
						row["images"] = []map[string]any{{"url": scene.image}}
					}
					if len(scene.tags) > 0 {
						tagObjs := make([]map[string]any, len(scene.tags))
						for i, name := range scene.tags {
							tagObjs[i] = map[string]any{"name": name}
						}
						row["tags"] = tagObjs
					}
					matches[i] = []map[string]any{row}
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
// the phash-first cascade (identifyAdultFiles) and its legacy AI/text fallback
// never touch the *arr app; that's Apply's job, not Scan's.
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
