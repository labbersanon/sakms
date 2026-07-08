package identify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/tidyarr/internal/stashbox"
)

func TestSubmitFingerprint_ZeroDurationRejected(t *testing.T) {
	g := NewGiveBack(map[string]*stashbox.Client{})
	err := g.SubmitFingerprint(context.Background(), "stashdb", "scene1", "hash1", 0)
	if !errors.Is(err, ErrNoValidDuration) {
		t.Fatalf("expected ErrNoValidDuration, got %v", err)
	}
}

func TestSubmitFingerprint_UnconfiguredBoxErrors(t *testing.T) {
	g := NewGiveBack(map[string]*stashbox.Client{})
	err := g.SubmitFingerprint(context.Background(), "stashdb", "scene1", "hash1", 120)
	if err == nil {
		t.Fatal("expected an error for an unconfigured box")
	}
}

func TestSubmitFingerprint_Success(t *testing.T) {
	var gotSceneID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"submitFingerprint":true}}`))
		gotSceneID = "called"
	}))
	defer srv.Close()

	client := stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k", HasVoteField: true}, &http.Client{Timeout: 5 * time.Second})
	g := NewGiveBack(map[string]*stashbox.Client{"stashdb": client})

	err := g.SubmitFingerprint(context.Background(), "stashdb", "scene1", "hash1", 120)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSceneID != "called" {
		t.Fatal("expected the underlying client to be called")
	}
}

func TestSubmitFingerprint_TPDBUsesGraphQLClientNoVoteField(t *testing.T) {
	var hasVoteKey bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, hasVoteKey = req.Variables.Input["vote"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"submitFingerprint":true}}`))
	}))
	defer srv.Close()

	tpdbClient := stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k", IsBearer: true, HasVoteField: false}, &http.Client{Timeout: 5 * time.Second})
	g := NewGiveBack(map[string]*stashbox.Client{"tpdb": tpdbClient})

	err := g.SubmitFingerprint(context.Background(), "tpdb", "scene1", "hash1", 120)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasVoteKey {
		t.Fatal("expected no 'vote' field for TPDB fingerprint submission")
	}
}

func TestSubmitDraft_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"submitSceneDraft":{"id":"draft123"}}}`))
	}))
	defer srv.Close()

	client := stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k"}, &http.Client{Timeout: 5 * time.Second})
	g := NewGiveBack(map[string]*stashbox.Client{"stashdb": client})

	id, err := g.SubmitDraft(context.Background(), "Title", "Studio", "2024")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "draft123" {
		t.Fatalf("got %q", id)
	}
}

// A "not authorized" response latches draft submission off for the rest of
// this GiveBack's lifetime, so the caller doesn't need to log the same
// warning once per file.
func TestSubmitDraft_NotAuthorized_LatchesOffForRestOfRun(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"not authorized"}]}`))
	}))
	defer srv.Close()

	client := stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k"}, &http.Client{Timeout: 5 * time.Second})
	g := NewGiveBack(map[string]*stashbox.Client{"stashdb": client})

	_, err1 := g.SubmitDraft(context.Background(), "T1", "S1", "2024")
	if err1 == nil {
		t.Fatal("expected the first call to fail with a real not-authorized error")
	}
	if !g.DraftSubmissionBroken() {
		t.Fatal("expected draft submission to be latched off after a not-authorized response")
	}

	_, err2 := g.SubmitDraft(context.Background(), "T2", "S2", "2024")
	if !errors.Is(err2, ErrDraftSubmissionDisabled) {
		t.Fatalf("expected ErrDraftSubmissionDisabled on the second call, got %v", err2)
	}
	if calls != 1 {
		t.Fatalf("expected the real HTTP endpoint to be hit only once (latch prevents further real calls), got %d calls", calls)
	}
}

func TestSubmitDraft_OtherErrorDoesNotLatch(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"some transient validation error"}]}`))
	}))
	defer srv.Close()

	client := stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k"}, &http.Client{Timeout: 5 * time.Second})
	g := NewGiveBack(map[string]*stashbox.Client{"stashdb": client})

	_, _ = g.SubmitDraft(context.Background(), "T1", "S1", "2024")
	if g.DraftSubmissionBroken() {
		t.Fatal("expected the latch to remain OFF for a non-authorization error")
	}
	_, _ = g.SubmitDraft(context.Background(), "T2", "S2", "2024")
	if calls != 2 {
		t.Fatalf("expected both calls to reach the real endpoint (no latch), got %d", calls)
	}
}
