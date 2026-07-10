package rename

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/ollama"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/servarr"
	"github.com/curtiswtaylorjr/sakms/internal/stashbox"
)

func newTestSession(t *testing.T, app servarr.App, handler http.HandlerFunc) *mode.Session {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	m := mode.Movies
	switch app {
	case servarr.Sonarr:
		m = mode.Series
	case servarr.Whisparr:
		m = mode.Adult
	}
	return &mode.Session{
		Mode:    m,
		Servarr: servarr.New(servarr.Config{BaseURL: srv.URL, APIKey: "test-key", App: app}, srv.Client()),
	}
}

// fakeRadarr wires up the three read-only endpoints Scan needs, keyed by
// unmapped-folder name so each test case can control what Lookup returns.
type fakeRadarr struct {
	rootFolders string
	tracked     string
	profiles    string
	lookups     map[string]string // search term -> raw JSON response
}

func (f *fakeRadarr) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/rootfolder":
			w.Write([]byte(f.rootFolders))
		case r.URL.Path == "/api/v3/movie":
			w.Write([]byte(f.tracked))
		case r.URL.Path == "/api/v3/qualityprofile":
			w.Write([]byte(f.profiles))
		case r.URL.Path == "/api/v3/movie/lookup":
			term := r.URL.Query().Get("term")
			resp, ok := f.lookups[term]
			if !ok {
				t.Fatalf("unexpected lookup term %q", term)
			}
			w.Write([]byte(resp))
		case r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}
}

func TestScan_ProducesPendingProposalForNewItem(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP","path":"/media/Movies/A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP","relativePath":"A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP"}
		]}]`,
		tracked:  `[]`,
		profiles: `[{"id":4,"name":"HD-1080p"}]`,
		lookups: map[string]string{
			"A Beautiful Mind 2001": `[{"title":"A Beautiful Mind","year":2001,"tmdbId":453,"genres":["Drama"],"overview":"...","certification":"PG-13"}]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending {
		t.Fatalf("expected Pending, got %+v", p)
	}
	if p.Title != "A Beautiful Mind" || p.TMDBID != 453 {
		t.Errorf("unexpected identification: %+v", p)
	}
	if p.QualityProfileID != 4 {
		t.Errorf("expected fallback to the only available profile (4), got %d", p.QualityProfileID)
	}
	if p.RootFolderPath != "/media/Movies" || p.SourcePath != "/media/Movies/A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP" {
		t.Errorf("unexpected paths: %+v", p)
	}
}

// fakeMainstreamAI returns an *ollama.Client whose /api/chat always responds
// with responseContent, regardless of the prompt — enough for tests that
// only care about the AI-guessed title Rename's fallback goes on to use.
func fakeMainstreamAI(t *testing.T, responseContent string) *ollama.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"content": responseContent},
		})
	}))
	t.Cleanup(srv.Close)
	return ollama.New(srv.URL, "test-model", srv.Client())
}

// TestScan_FallsBackToAIWhenLookupFindsNothing proves Rename's mainstream AI
// fallback: an opaque name that Radarr's own lookup can't resolve gets a
// second chance via an Ollama-guessed title.
func TestScan_FallsBackToAIWhenLookupFindsNothing(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"xyz123.mkv","path":"/media/Movies/xyz123.mkv","relativePath":"xyz123.mkv"}
		]}]`,
		tracked:  `[]`,
		profiles: `[{"id":4,"name":"HD-1080p"}]`,
		lookups: map[string]string{
			"xyz123 mkv":      `[]`,
			"Some Movie 2020": `[{"title":"Some Movie","year":2020,"tmdbId":999,"genres":[],"overview":"...","certification":"PG"}]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))
	sess.MainstreamAI = fakeMainstreamAI(t, `{"title":"Some Movie 2020"}`)

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.Title != "Some Movie" || p.TMDBID != 999 {
		t.Fatalf("expected the AI-guessed title's lookup to resolve the proposal, got %+v", p)
	}
}

// TestScan_UnmatchedWhenAIDeclines confirms the "I don't know" escape valve
// is honored: a decline surfaces as Unmatched, not a crash or a bogus match.
func TestScan_UnmatchedWhenAIDeclines(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"xyz123.mkv","path":"/media/Movies/xyz123.mkv","relativePath":"xyz123.mkv"}
		]}]`,
		tracked:  `[]`,
		profiles: `[]`,
		lookups: map[string]string{
			"xyz123 mkv": `[]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))
	sess.MainstreamAI = fakeMainstreamAI(t, `{"title":null}`)

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected an Unmatched proposal when AI declines, got %+v", got)
	}
	if !strings.Contains(got[0].Reason, "AI title guess failed") {
		t.Errorf("expected the reason to mention the AI decline, got %q", got[0].Reason)
	}
}

// TestScan_NoAIFallbackWhenUnconfigured confirms existing behavior is
// unchanged when MainstreamAI is nil (the default, pre-existing case) — no
// regression for installs without an Ollama connection configured.
func TestScan_NoAIFallbackWhenUnconfigured(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"xyz123.mkv","path":"/media/Movies/xyz123.mkv","relativePath":"xyz123.mkv"}
		]}]`,
		tracked:  `[]`,
		profiles: `[]`,
		lookups: map[string]string{
			"xyz123 mkv": `[]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))
	if sess.MainstreamAI != nil {
		t.Fatal("precondition: expected a nil MainstreamAI for this test")
	}

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected an Unmatched proposal, got %+v", got)
	}
	if strings.Contains(got[0].Reason, "AI") {
		t.Errorf("expected no AI mention when MainstreamAI is unconfigured, got %q", got[0].Reason)
	}
}

func TestScan_SkipsSidecarFiles(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"Barbie (2023) 1080p.trickplay","path":"/media/Movies/Barbie (2023) 1080p.trickplay","relativePath":"Barbie (2023) 1080p.trickplay"}
		]}]`,
		tracked:  `[]`,
		profiles: `[]`,
		lookups:  map[string]string{},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected sidecar file to be skipped entirely, got %+v", got)
	}
}

func TestScan_MarksUnmatchedWhenNoLookupResult(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"FathersLLDVD","path":"/media/Movies/FathersLLDVD","relativePath":"FathersLLDVD"}
		]}]`,
		tracked:  `[]`,
		profiles: `[]`,
		lookups:  map[string]string{"FathersLLDVD": `[]`},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected 1 unmatched proposal, got %+v", got)
	}
	if got[0].Reason == "" {
		t.Error("expected a populated reason explaining why it's unmatched")
	}
}

func TestScan_MarksUnmatchedForAlreadyTrackedDuplicate(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP","path":"/media/Movies/A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP","relativePath":"A.Beautiful.Mind.2001.1080p.BluRay.x264-GROUP"}
		]}]`,
		tracked:  `[{"id":9,"title":"A Beautiful Mind","path":"/media/Movies/A Beautiful Mind (2001)","rootFolderPath":"/media/Movies","tmdbId":453,"qualityProfileId":4}]`,
		profiles: `[{"id":4,"name":"HD-1080p"}]`,
		lookups: map[string]string{
			"A Beautiful Mind 2001": `[{"title":"A Beautiful Mind","year":2001,"tmdbId":453}]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Status != proposals.Unmatched {
		t.Fatalf("expected the duplicate to surface as unmatched rather than re-registering it, got %+v", got)
	}
}

func TestScan_QualityProfileLearnsFromExistingRootFolderConvention(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"New Movie (2020)","path":"/media/Movies/New Movie (2020)","relativePath":"New Movie (2020)"}
		]}]`,
		tracked: `[
			{"id":1,"title":"X","rootFolderPath":"/media/Movies","tmdbId":1,"qualityProfileId":7},
			{"id":2,"title":"Y","rootFolderPath":"/media/Movies","tmdbId":2,"qualityProfileId":7},
			{"id":3,"title":"Z","rootFolderPath":"/media/Movies","tmdbId":3,"qualityProfileId":9}
		]`,
		profiles: `[{"id":7,"name":"HD"},{"id":9,"name":"4K"}]`,
		lookups: map[string]string{
			"New Movie (2020)": `[{"title":"New Movie","year":2020,"tmdbId":999}]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].QualityProfileID != 7 {
		t.Fatalf("expected the majority profile (7) already used in this root folder, got %+v", got)
	}
}

func TestApply_RegistersAndTriggersDownloadedScan(t *testing.T) {
	var addBody map[string]any
	var scanTriggered bool
	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/movie":
			json.NewDecoder(r.Body).Decode(&addBody)
			w.Write([]byte(`{"id":55}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "DownloadedMoviesScan" {
				t.Errorf("expected a DownloadedMoviesScan trigger, got %+v", body)
			}
			scanTriggered = true
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "A Beautiful Mind", TMDBID: 453,
		QualityProfileID: 4, RootFolderPath: "/media/Movies",
	}
	id, _, err := Apply(context.Background(), sess, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 55 {
		t.Errorf("expected the tracked id from Add's response (55), got %d", id)
	}
	if !scanTriggered {
		t.Error("expected Apply to trigger a downloaded-files scan after registering")
	}
	if addBody["tmdbId"] != float64(453) || addBody["title"] != "A Beautiful Mind" {
		t.Errorf("unexpected Add request body: %+v", addBody)
	}
}

func TestApply_RejectsNonPendingProposal(t *testing.T) {
	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Apply must not make any HTTP call for a non-pending proposal")
	})

	for _, status := range []proposals.Status{proposals.Applied, proposals.Dismissed, proposals.Unmatched} {
		if _, _, err := Apply(context.Background(), sess, proposals.Proposal{Status: status}); err == nil {
			t.Errorf("expected Apply to refuse a %q proposal", status)
		}
	}
}

func TestApply_RegistersWhisparrSceneWithForeignID(t *testing.T) {
	var addBody map[string]any
	var scanTriggered bool
	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/movie":
			json.NewDecoder(r.Body).Decode(&addBody)
			w.Write([]byte(`{"id":77}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["name"] != "DownloadedMoviesScan" {
				t.Errorf("expected a DownloadedMoviesScan trigger, got %+v", body)
			}
			scanTriggered = true
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Some Scene",
		ForeignID: "abc-uuid", ItemType: "scene",
		QualityProfileID: 4, RootFolderPath: "/media/Adult",
	}
	id, _, err := Apply(context.Background(), sess, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 77 {
		t.Errorf("expected the tracked id from Add's response (77), got %d", id)
	}
	if !scanTriggered {
		t.Error("expected Apply to trigger a downloaded-files scan after registering")
	}
	if addBody["foreignId"] != "abc-uuid" || addBody["itemType"] != "scene" {
		t.Errorf("expected the scene identifiers in the Add body, got %+v", addBody)
	}
	addOptions, ok := addBody["addOptions"].(map[string]any)
	if !ok || addOptions["searchForMovie"] != false {
		t.Errorf("expected addOptions.searchForMovie=false, got %+v", addBody["addOptions"])
	}
}

func TestApply_RefusesWhisparrProposalWithoutIdentifier(t *testing.T) {
	// The guard must fire when EITHER field is blank — an empty ItemType alone
	// still misclassifies a scene as a movie (client.go's AddRequest doc).
	cases := []struct {
		name string
		p    proposals.Proposal
	}{
		{"blank foreignId", proposals.Proposal{Status: proposals.Pending, ForeignID: "", ItemType: "scene", Title: "S"}},
		{"blank itemType", proposals.Proposal{Status: proposals.Pending, ForeignID: "abc", ItemType: "", Title: "S"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("guard must refuse before any HTTP call, but got %s %s", r.Method, r.URL.Path)
			})
			if _, _, err := Apply(context.Background(), sess, tc.p); err == nil {
				t.Fatal("expected Apply to refuse a Whisparr proposal missing a scene identifier")
			}
		})
	}
}

func TestClassifyAdultMatch(t *testing.T) {
	cases := []struct {
		name          string
		res           *identify.MatchResult
		err           error
		wantStatus    proposals.Status
		wantReason    string
		wantForeignID string
		wantItemType  string
		wantTitle     string
	}{
		{
			name: "identify error", err: errTest,
			wantStatus: proposals.Unmatched, wantReason: "identification failed: boom",
		},
		{
			name: "nil match", res: nil,
			wantStatus: proposals.Unmatched, wantReason: "no confident identification",
		},
		{
			name:       "web_search only (no scene id)",
			res:        &identify.MatchResult{Source: "web_search", SceneID: "", Box: ""},
			wantStatus: proposals.Unmatched, wantReason: "web-identified only (no scene ID) — needs manual review",
		},
		{
			name:       "stashdb match",
			res:        &identify.MatchResult{Box: "stashdb", SceneID: "u1", Type: "scene", Title: "T"},
			wantStatus: proposals.Pending, wantForeignID: "u1", wantItemType: "scene", wantTitle: "T",
		},
		{
			name:       "fansdb match",
			res:        &identify.MatchResult{Box: "fansdb", SceneID: "u2", Type: "scene"},
			wantStatus: proposals.Pending, wantForeignID: "u2", wantItemType: "scene",
		},
		{
			name:       "tpdb match gets tpdbId prefix",
			res:        &identify.MatchResult{Box: "tpdb", SceneID: "77", Type: "scene"},
			wantStatus: proposals.Pending, wantForeignID: "tpdbId:77", wantItemType: "scene",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, reason, title, foreignID, itemType := classifyAdultMatch(tc.res, tc.err)
			if status != tc.wantStatus {
				t.Errorf("status: got %q, want %q", status, tc.wantStatus)
			}
			if reason != tc.wantReason {
				t.Errorf("reason: got %q, want %q", reason, tc.wantReason)
			}
			if foreignID != tc.wantForeignID {
				t.Errorf("foreignID: got %q, want %q", foreignID, tc.wantForeignID)
			}
			if itemType != tc.wantItemType {
				t.Errorf("itemType: got %q, want %q", itemType, tc.wantItemType)
			}
			if title != tc.wantTitle {
				t.Errorf("title: got %q, want %q", title, tc.wantTitle)
			}
		})
	}
}

var errTest = &testError{"boom"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func TestScan_AdultFailsFastWhenIdentifyUnconfigured(t *testing.T) {
	// newTestSession(Whisparr) builds an Adult session with Identify left nil —
	// exactly the "no Ollama backbone configured" case. Scan must fail fast
	// before any HTTP call.
	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("Scan must fail fast before any HTTP call, but got %s %s", r.Method, r.URL.Path)
	})
	if sess.Identify != nil {
		t.Fatal("precondition: expected a nil Identify for this test")
	}

	_, err := Scan(context.Background(), sess, nil, nil, true)
	if err == nil {
		t.Fatal("expected Scan to fail fast when adult identification isn't configured")
	}
	if !strings.Contains(err.Error(), "identification isn't configured") {
		t.Errorf("expected an actionable config error mentioning identification, got %v", err)
	}
}

func TestSubmitDraft_Success(t *testing.T) {
	var gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Input struct {
					Title string `json:"title"`
				} `json:"input"`
			} `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		gotTitle = req.Variables.Input.Title
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"submitSceneDraft":{"id":"draft123"}}}`))
	}))
	defer srv.Close()

	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("SubmitDraft must never call the *arr app, got %s %s", r.Method, r.URL.Path)
	})
	sess.Identify = &identify.Identifier{GiveBack: identify.NewGiveBack(map[string]*stashbox.Client{
		"tpdb": stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k", IsBearer: true}, srv.Client()),
	})}

	p := proposals.Proposal{
		ID: 1, Workflow: proposals.Rename, Status: proposals.Unmatched,
		Title: "Some Scene", Studio: "Some Studio", Date: "2024",
	}
	draftID, err := SubmitDraft(context.Background(), sess, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if draftID != "draft123" {
		t.Fatalf("got draft id %q", draftID)
	}
	if gotTitle != "Some Scene" {
		t.Fatalf("expected the proposal's title to reach the give-back mutation, got %q", gotTitle)
	}
}

func TestSubmitDraft_RejectsNonUnmatchedProposal(t *testing.T) {
	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the *arr app")
	})
	sess.Identify = &identify.Identifier{GiveBack: identify.NewGiveBack(nil)}

	p := proposals.Proposal{ID: 1, Workflow: proposals.Rename, Status: proposals.Pending, Title: "X"}
	if _, err := SubmitDraft(context.Background(), sess, p); err == nil {
		t.Fatal("expected an error for a non-Unmatched proposal")
	}
}

func TestSubmitDraft_RejectsAlreadyDrafted(t *testing.T) {
	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the *arr app")
	})
	sess.Identify = &identify.Identifier{GiveBack: identify.NewGiveBack(nil)}

	p := proposals.Proposal{ID: 1, Workflow: proposals.Rename, Status: proposals.Unmatched, Title: "X", DraftID: "already-there"}
	if _, err := SubmitDraft(context.Background(), sess, p); err == nil {
		t.Fatal("expected an error for a proposal that already has a draft")
	}
}

func TestSubmitDraft_RejectsUnconfiguredGiveBack(t *testing.T) {
	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not call the *arr app")
	})
	// sess.Identify left nil — no Ollama backbone configured at all.

	p := proposals.Proposal{ID: 1, Workflow: proposals.Rename, Status: proposals.Unmatched, Title: "X"}
	if _, err := SubmitDraft(context.Background(), sess, p); err == nil {
		t.Fatal("expected an error when give-back isn't configured")
	}
}

// TestScan_RoutesKidsClassifiedContentToKidsRoot proves the deterministic
// (no-AI) path: a "G" certification is a confident kids signal on its own
// (see internal/classify.FromMetadata), so a proposal for it should target
// sess.KidsRootPath instead of the general root it was found under.
func TestScan_RoutesKidsClassifiedContentToKidsRoot(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[
			{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
				{"name":"Kids.Movie.2020","path":"/media/Movies/Kids.Movie.2020","relativePath":"Kids.Movie.2020"}
			]},
			{"id":2,"path":"/media/Movies (Kids)","accessible":true,"freeSpace":1,"unmappedFolders":[]}
		]`,
		tracked:  `[]`,
		profiles: `[{"id":4,"name":"HD"}]`,
		lookups: map[string]string{
			"Kids Movie 2020": `[{"title":"Kids Movie","year":2020,"tmdbId":111,"certification":"G"}]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))
	sess.KidsRootPath = "/media/Movies (Kids)"

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.RootFolderPath != "/media/Movies (Kids)" {
		t.Fatalf("expected the proposal to be routed to the Kids root, got %+v", p)
	}
	// SourcePath must stay where the file was actually found — Apply is what
	// physically moves it, not Scan.
	if p.SourcePath != "/media/Movies/Kids.Movie.2020" {
		t.Errorf("expected SourcePath to stay put, got %q", p.SourcePath)
	}
}

// TestScan_NoRerouteWhenKidsPathNotConfigured confirms unconfigured (empty)
// KidsRootPath is a complete no-op — the default for every existing install.
func TestScan_NoRerouteWhenKidsPathNotConfigured(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"Kids.Movie.2020","path":"/media/Movies/Kids.Movie.2020","relativePath":"Kids.Movie.2020"}
		]}]`,
		tracked:  `[]`,
		profiles: `[{"id":4,"name":"HD"}]`,
		lookups: map[string]string{
			"Kids Movie 2020": `[{"title":"Kids Movie","year":2020,"tmdbId":111,"certification":"G"}]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))
	if sess.KidsRootPath != "" {
		t.Fatal("precondition: expected an empty KidsRootPath")
	}

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].RootFolderPath != "/media/Movies" {
		t.Fatalf("expected the proposal to stay in the general root, got %+v", got)
	}
}

// TestScan_IgnoresKidsPathNotAmongRealRootFolders guards against a stale or
// mistyped setting silently misrouting content into a folder the *arr app
// doesn't actually report — the same "never guess/never trust unverified
// config" posture as everything else in this project.
func TestScan_IgnoresKidsPathNotAmongRealRootFolders(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"Kids.Movie.2020","path":"/media/Movies/Kids.Movie.2020","relativePath":"Kids.Movie.2020"}
		]}]`,
		tracked:  `[]`,
		profiles: `[{"id":4,"name":"HD"}]`,
		lookups: map[string]string{
			"Kids Movie 2020": `[{"title":"Kids Movie","year":2020,"tmdbId":111,"certification":"G"}]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))
	sess.KidsRootPath = "/media/Movies (Kids)" // never reported by the fake above

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].RootFolderPath != "/media/Movies" {
		t.Fatalf("expected no reroute to a nonexistent root folder, got %+v", got)
	}
}

// TestScan_SkipsClassificationForItemsAlreadyInKidsRoot confirms an orphan
// found directly under the Kids root is registered there without ever
// running classification (a fatal-on-call fake AI proves it isn't invoked)
// — it's already correctly placed by definition.
func TestScan_SkipsClassificationForItemsAlreadyInKidsRoot(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies (Kids)","accessible":true,"freeSpace":1,"unmappedFolders":[
			{"name":"Already.Kids.2020","path":"/media/Movies (Kids)/Already.Kids.2020","relativePath":"Already.Kids.2020"}
		]}]`,
		tracked:  `[]`,
		profiles: `[{"id":4,"name":"HD"}]`,
		lookups: map[string]string{
			"Already Kids 2020": `[{"title":"Already Kids","year":2020,"tmdbId":222}]`,
		},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))
	sess.KidsRootPath = "/media/Movies (Kids)"
	var aiCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aiCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": `{"kids":true}`}})
	}))
	defer srv.Close()
	sess.MainstreamAI = ollama.New(srv.URL, "test-model", srv.Client())

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].RootFolderPath != "/media/Movies (Kids)" {
		t.Fatalf("expected the proposal to stay in the Kids root, got %+v", got)
	}
	if aiCalled {
		t.Error("expected classification to be skipped entirely for an item already under the Kids root")
	}
}

// TestApply_RelocatesFileIntoTargetRootWhenDifferentFromSource is the real
// filesystem proof: Apply must physically move a classified-Kids item from
// its source root into the target root before registering it, or Sonarr/
// Radarr's own import scan would never find it.
func TestApply_RelocatesFileIntoTargetRootWhenDifferentFromSource(t *testing.T) {
	base := t.TempDir()
	sourceRoot := filepath.Join(base, "Movies")
	destRoot := filepath.Join(base, "Movies (Kids)")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(sourceRoot, "Kids.Movie.2020.mkv")
	if err := os.WriteFile(sourcePath, []byte("fake video data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var addBody map[string]any
	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/movie":
			json.NewDecoder(r.Body).Decode(&addBody)
			w.Write([]byte(`{"id":77}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Kids Movie", TMDBID: 111,
		QualityProfileID: 4, SourcePath: sourcePath, RootFolderPath: destRoot,
	}
	id, _, err := Apply(context.Background(), sess, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 77 {
		t.Errorf("got id %d", id)
	}

	wantDest := filepath.Join(destRoot, "Kids.Movie.2020.mkv")
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Errorf("expected the source file to be gone, stat returned: %v", err)
	}
	if data, err := os.ReadFile(wantDest); err != nil || string(data) != "fake video data" {
		t.Errorf("expected the file to have moved to %q intact, err=%v data=%q", wantDest, err, data)
	}
	if addBody["rootFolderPath"] != destRoot {
		t.Errorf("expected Add to register against the target root, got %+v", addBody)
	}
}

// TestApply_RelocateAvoidsFilenameCollision proves place.UniquePath is
// actually wired in: a pre-existing file at the plain destination path must
// not be overwritten.
func TestApply_RelocateAvoidsFilenameCollision(t *testing.T) {
	base := t.TempDir()
	sourceRoot := filepath.Join(base, "Movies")
	destRoot := filepath.Join(base, "Movies (Kids)")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(sourceRoot, "Movie.mkv")
	if err := os.WriteFile(sourcePath, []byte("new file"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	collidingPath := filepath.Join(destRoot, "Movie.mkv")
	if err := os.WriteFile(collidingPath, []byte("already there"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/movie":
			w.Write([]byte(`{"id":1}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Movie", TMDBID: 1,
		QualityProfileID: 4, SourcePath: sourcePath, RootFolderPath: destRoot,
	}
	if _, _, err := Apply(context.Background(), sess, p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if data, err := os.ReadFile(collidingPath); err != nil || string(data) != "already there" {
		t.Errorf("expected the pre-existing file to survive untouched, err=%v data=%q", err, data)
	}
	uniqued := filepath.Join(destRoot, "Movie.2.mkv")
	if data, err := os.ReadFile(uniqued); err != nil || string(data) != "new file" {
		t.Errorf("expected the moved file at the .2 collision path, err=%v data=%q", err, data)
	}
}

// TestApply_NoRelocateWhenRootFolderPathMatchesSource confirms the common
// (non-Kids) case never touches the filesystem — proven by SourcePath's
// directory genuinely not existing on disk at all, which would surface as a
// real error if relocate were mistakenly attempted anyway.
func TestApply_NoRelocateWhenRootFolderPathMatchesSource(t *testing.T) {
	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/movie":
			w.Write([]byte(`{"id":1}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Movie", TMDBID: 1, QualityProfileID: 4,
		SourcePath: "/this/path/does/not/exist/Movie.mkv", RootFolderPath: "/this/path/does/not/exist",
	}
	if _, _, err := Apply(context.Background(), sess, p); err != nil {
		t.Fatalf("expected no relocate attempt (and so no error) when the root already matches, got: %v", err)
	}
}

// TestScan_ReconcilesGeneralToKidsForTrackedItem proves the counterpart to
// proposeOne's classification: an already-tracked item's classification can
// drift (a re-rated title, a fixed-up genre list), and Scan must surface
// that even though the item was never an orphan.
func TestScan_ReconcilesGeneralToKidsForTrackedItem(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[
			{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[]},
			{"id":2,"path":"/media/Movies (Kids)","accessible":true,"freeSpace":1,"unmappedFolders":[]}
		]`,
		tracked: `[
			{"id":50,"title":"Old Kids Movie","path":"/media/Movies/Old Kids Movie","rootFolderPath":"/media/Movies","tmdbId":50,"certification":"G"}
		]`,
		profiles: `[{"id":4,"name":"HD"}]`,
		lookups:  map[string]string{},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))
	sess.KidsRootPath = "/media/Movies (Kids)"

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 reconcile proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.TrackedID != 50 || p.RootFolderPath != "/media/Movies (Kids)" {
		t.Fatalf("expected a reconcile proposal moving id=50 to the Kids root, got %+v", p)
	}
	if !strings.Contains(p.Reason, "should move to /media/Movies (Kids)") {
		t.Errorf("expected an explanatory reason, got %q", p.Reason)
	}
}

// TestScan_ReconcilesKidsToGeneralWhenUnambiguous proves the reverse
// direction works when there's exactly one candidate general root to send
// the item back to.
func TestScan_ReconcilesKidsToGeneralWhenUnambiguous(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[
			{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[]},
			{"id":2,"path":"/media/Movies (Kids)","accessible":true,"freeSpace":1,"unmappedFolders":[]}
		]`,
		tracked: `[
			{"id":60,"title":"Actually Adult Movie","path":"/media/Movies (Kids)/Actually Adult Movie","rootFolderPath":"/media/Movies (Kids)","tmdbId":60,"certification":"R"}
		]`,
		profiles: `[{"id":4,"name":"HD"}]`,
		lookups:  map[string]string{},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))
	sess.KidsRootPath = "/media/Movies (Kids)"

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].TrackedID != 60 || got[0].RootFolderPath != "/media/Movies" {
		t.Fatalf("expected a reconcile proposal moving id=60 back to the general root, got %+v", got)
	}
}

// TestScan_NoKidsToGeneralReconcileWhenAmbiguous confirms an ambiguous
// (more than one candidate) general root disables ONLY the kids→general
// direction — general→kids is unaffected since its destination is never
// ambiguous.
func TestScan_NoKidsToGeneralReconcileWhenAmbiguous(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[
			{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[]},
			{"id":2,"path":"/media/Movies2","accessible":true,"freeSpace":1,"unmappedFolders":[]},
			{"id":3,"path":"/media/Movies (Kids)","accessible":true,"freeSpace":1,"unmappedFolders":[]}
		]`,
		tracked: `[
			{"id":60,"title":"Actually Adult Movie","path":"/media/Movies (Kids)/x","rootFolderPath":"/media/Movies (Kids)","tmdbId":60,"certification":"R"},
			{"id":70,"title":"Should Be Kids","path":"/media/Movies/y","rootFolderPath":"/media/Movies","tmdbId":70,"certification":"G"}
		]`,
		profiles: `[{"id":4,"name":"HD"}]`,
		lookups:  map[string]string{},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))
	sess.KidsRootPath = "/media/Movies (Kids)"

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].TrackedID != 70 || got[0].RootFolderPath != "/media/Movies (Kids)" {
		t.Fatalf("expected only the unambiguous general->kids reconcile, got %+v", got)
	}
}

// TestScan_NoReconcileWhenKidsPathNotConfigured confirms reconcileTracked is
// a complete no-op for the default (unconfigured) case.
func TestScan_NoReconcileWhenKidsPathNotConfigured(t *testing.T) {
	f := &fakeRadarr{
		rootFolders: `[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":1,"unmappedFolders":[]}]`,
		tracked: `[
			{"id":50,"title":"Old Kids Movie","path":"/media/Movies/Old Kids Movie","rootFolderPath":"/media/Movies","tmdbId":50,"certification":"G"}
		]`,
		profiles: `[{"id":4,"name":"HD"}]`,
		lookups:  map[string]string{},
	}
	sess := newTestSession(t, servarr.Radarr, f.handler(t))

	got, err := Scan(context.Background(), sess, nil, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no reconcile proposals without a configured Kids path, got %+v", got)
	}
}

// TestApply_ReconcileCallsUpdateRootFolder proves a nonzero TrackedID routes
// Apply through UpdateRootFolder instead of Add — Radarr/Sonarr's own
// moveFiles=true handles the physical move, so this proposal shape never
// touches the filesystem directly.
func TestApply_ReconcileCallsUpdateRootFolder(t *testing.T) {
	var gotMoveFiles string
	var putBody map[string]any
	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/movie/50":
			w.Write([]byte(`{"id":50,"title":"Old Kids Movie","rootFolderPath":"/media/Movies","tmdbId":50}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v3/movie/50":
			gotMoveFiles = r.URL.Query().Get("moveFiles")
			json.NewDecoder(r.Body).Decode(&putBody)
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Old Kids Movie", TMDBID: 50,
		TrackedID: 50, RootFolderPath: "/media/Movies (Kids)",
	}
	id, _, err := Apply(context.Background(), sess, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 50 {
		t.Errorf("expected the proposal's own TrackedID (50) back, got %d", id)
	}
	if gotMoveFiles != "true" {
		t.Errorf("expected moveFiles=true, got %q", gotMoveFiles)
	}
	if putBody["rootFolderPath"] != "/media/Movies (Kids)" {
		t.Errorf("expected the PUT to carry the new root folder, got %+v", putBody)
	}
}

// TestBuildAdultProposal_CapturesGiveBackBoxAndSceneID_SeparatelyFromForeignID
// is the single most important correctness regression test for the give-back
// restoration: WhisparrForeignID() returns the SAME raw uuid string for a
// fansdb match as it would for a stashdb match (only tpdb gets a distinct
// "tpdbId:" prefix) — so GiveBackBox/GiveBackSceneID must be captured
// directly from the MatchResult, never reconstructed from ForeignID alone.
func TestBuildAdultProposal_CapturesGiveBackBoxAndSceneID_SeparatelyFromForeignID(t *testing.T) {
	res := &identify.MatchResult{Box: "fansdb", SceneID: "shared-uuid", Type: "scene", Title: "T", Studio: "S", Date: "2020"}
	p := buildAdultProposal(mode.Adult,
		servarr.RootFolder{Path: "/media/Adult"},
		servarr.UnmappedFolder{Name: "f.mp4", Path: "/media/Adult/f.mp4"},
		res, nil, nil, nil)
	if p.ForeignID != "shared-uuid" {
		t.Fatalf("expected ForeignID %q, got %q", "shared-uuid", p.ForeignID)
	}
	if p.GiveBackBox != "fansdb" || p.GiveBackSceneID != "shared-uuid" {
		t.Fatalf("expected GiveBackBox=fansdb/GiveBackSceneID=shared-uuid distinct from ForeignID, got box=%q scene=%q", p.GiveBackBox, p.GiveBackSceneID)
	}
}

// newFakeGiveBackBox stands in for one stash-box's submitFingerprint
// mutation, capturing whatever input it was called with into *gotInput
// (left nil if never called).
func newFakeGiveBackBox(t *testing.T, gotInput *map[string]any) *stashbox.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		*gotInput = req.Variables.Input
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"submitFingerprint":true}}`)
	}))
	t.Cleanup(srv.Close)
	return stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k", HasVoteField: true}, srv.Client())
}

func TestApply_Adult_SubmitsFingerprintGiveBack_WhenPHashAndDurationKnown(t *testing.T) {
	gotInput := map[string]any{}
	stashdb := newFakeGiveBackBox(t, &gotInput)

	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/movie":
			w.Write([]byte(`{"id":77}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	sess.Identify = &identify.Identifier{GiveBack: identify.NewGiveBack(map[string]*stashbox.Client{"stashdb": stashdb})}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Some Scene",
		ForeignID: "abc-uuid", ItemType: "scene",
		QualityProfileID: 4, RootFolderPath: "/media/Adult",
		GiveBackBox: "stashdb", GiveBackSceneID: "abc-uuid", PHash: "hash1", DurationSeconds: 1800,
	}
	_, submitted, err := Apply(context.Background(), sess, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !submitted {
		t.Fatal("expected Apply to report a successful fingerprint give-back")
	}
	fp, _ := gotInput["fingerprint"].(map[string]any)
	if gotInput["scene_id"] != "abc-uuid" || fp["hash"] != "hash1" {
		t.Errorf("expected the proposal's phash to be submitted for its scene, got %+v", gotInput)
	}
}

func TestApply_Adult_NoGiveBack_WhenPHashUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must never call give-back when the proposal has no phash")
	}))
	defer srv.Close()
	stashdb := stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k", HasVoteField: true}, srv.Client())

	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/movie":
			w.Write([]byte(`{"id":77}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	sess.Identify = &identify.Identifier{GiveBack: identify.NewGiveBack(map[string]*stashbox.Client{"stashdb": stashdb})}

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Some Scene",
		ForeignID: "abc-uuid", ItemType: "scene",
		QualityProfileID: 4, RootFolderPath: "/media/Adult",
	}
	_, submitted, err := Apply(context.Background(), sess, p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if submitted {
		t.Fatal("expected no give-back to be attempted without a phash")
	}
}
