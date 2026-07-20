package rename

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/servarr"
	"github.com/labbersanon/sakms/internal/stashbox"
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
