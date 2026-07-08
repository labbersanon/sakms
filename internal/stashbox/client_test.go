package stashbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestClient(t *testing.T, cfg Config, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	cfg.Endpoint = srv.URL
	return New(cfg, &http.Client{Timeout: 5 * time.Second}), srv.Close
}

func TestFindScenesByFingerprints_AlignsWithInput(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				FPs [][]map[string]string `json:"fps"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Variables.FPs) != 2 {
			t.Fatalf("expected 2 fingerprint groups, got %d", len(req.Variables.FPs))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScenesBySceneFingerprints":[
			[{"id":"1","title":"Found","release_date":"2024-01-01","studio":{"name":"Studio A","parent":null}}],
			[]
		]}}`))
	})
	defer closeSrv()

	ctx := context.Background()
	out, err := c.FindScenesByFingerprints(ctx, []string{"phash1", "phash2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 results, got %d", len(out))
	}
	if out[0] == nil || out[0].Title != "Found" || out[0].StudioName != "Studio A" {
		t.Fatalf("out[0] = %+v, want a match", out[0])
	}
	if out[1] != nil {
		t.Fatalf("out[1] should be nil (no match), got %+v", out[1])
	}
}

func TestScene_StudioFallsBackToParent(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScene":{"id":"1","title":"T","release_date":"2020-01-01","studio":{"name":"","parent":{"name":"Parent Studio"}}}}}`))
	})
	defer closeSrv()

	sc, err := c.FindScene(context.Background(), "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.StudioName != "Parent Studio" {
		t.Fatalf("expected studio to fall back to parent name, got %q", sc.StudioName)
	}
}

func TestFindScene_NotFound(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScene":null}}`))
	})
	defer closeSrv()

	sc, err := c.FindScene(context.Background(), "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc != nil {
		t.Fatalf("expected nil for not-found scene, got %+v", sc)
	}
}

// StashDB/FansDB's FingerprintSubmission has a "vote" field; TPDB's does not
// and rejects it. This test verifies the client sends "vote" only when
// HasVoteField is true.
func TestSubmitFingerprint_VoteFieldOnlyWhenConfigured(t *testing.T) {
	t.Run("StashDB-style (HasVoteField=true) includes vote", func(t *testing.T) {
		var gotVote bool
		c, closeSrv := newTestClient(t, Config{APIKey: "k", HasVoteField: true}, func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Variables struct {
					Input map[string]any `json:"input"`
				} `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			_, gotVote = req.Variables.Input["vote"]
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"submitFingerprint":true}}`))
		})
		defer closeSrv()

		if err := c.SubmitFingerprint(context.Background(), "scene1", "hash1", 120); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !gotVote {
			t.Fatal("expected 'vote' field to be present for a stash-box with HasVoteField=true")
		}
	})

	t.Run("TPDB-style (HasVoteField=false) omits vote", func(t *testing.T) {
		var hasVoteKey bool
		c, closeSrv := newTestClient(t, Config{APIKey: "k", IsBearer: true, HasVoteField: false}, func(w http.ResponseWriter, r *http.Request) {
			if auth := r.Header.Get("Authorization"); auth != "Bearer k" {
				t.Errorf("expected Bearer auth header, got %q", auth)
			}
			var req struct {
				Variables struct {
					Input map[string]any `json:"input"`
				} `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			_, hasVoteKey = req.Variables.Input["vote"]
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"submitFingerprint":true}}`))
		})
		defer closeSrv()

		if err := c.SubmitFingerprint(context.Background(), "scene1", "hash1", 120); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if hasVoteKey {
			t.Fatal("expected 'vote' field to be ABSENT for TPDB (HasVoteField=false) — sending it fails GraphQL validation against TPDB's real schema")
		}
	})
}

func TestIsBearer_UsesApiKeyHeaderWhenFalse(t *testing.T) {
	var gotApiKey, gotAuth string
	c, closeSrv := newTestClient(t, Config{APIKey: "mykey", IsBearer: false}, func(w http.ResponseWriter, r *http.Request) {
		gotApiKey = r.Header.Get("ApiKey")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"searchScene":[]}}`))
	})
	defer closeSrv()

	_, _ = c.SearchScene(context.Background(), "term")
	if gotApiKey != "mykey" {
		t.Errorf("expected ApiKey header 'mykey', got %q", gotApiKey)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header for non-bearer client, got %q", gotAuth)
	}
}

func TestDo_GraphQLErrorsSurfaced(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"not authorized"}]}`))
	})
	defer closeSrv()

	_, err := c.SubmitSceneDraft(context.Background(), "T", "S", "2024")
	if err == nil {
		t.Fatal("expected an error when the response contains GraphQL errors")
	}
	if !IsNotAuthorized(err) {
		t.Fatalf("expected IsNotAuthorized(err) to be true, got err=%v", err)
	}
}

func TestIsNotAuthorized_FalseForOtherErrors(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"some other validation error"}]}`))
	})
	defer closeSrv()

	_, err := c.SubmitSceneDraft(context.Background(), "T", "S", "2024")
	if err == nil {
		t.Fatal("expected an error")
	}
	if IsNotAuthorized(err) {
		t.Fatal("expected IsNotAuthorized(err) to be false for an unrelated error")
	}
}

func TestIsNotAuthorized_FalseForNonGraphQLError(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer closeSrv()

	_, err := c.SubmitSceneDraft(context.Background(), "T", "S", "2024")
	if err == nil {
		t.Fatal("expected an error")
	}
	if IsNotAuthorized(err) {
		t.Fatal("expected IsNotAuthorized(err) to be false for a plain HTTP error")
	}
}
