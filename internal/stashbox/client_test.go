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

func TestSearchScene_PopulatesTagsImageAndDuration(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"searchScene":[{"id":"1","title":"T","release_date":"2024-01-01",` +
			`"studio":{"name":"Vixen","parent":null},"tags":[{"name":"Blonde"},{"name":"Outdoor"}],` +
			`"images":[{"url":"http://cdn/scene1.jpg"},{"url":"http://cdn/scene1-alt.jpg"}],"duration":1800}]}}`))
	})
	defer closeSrv()

	out, err := c.SearchScene(context.Background(), "T")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 scene, got %d", len(out))
	}
	if out[0].ImageURL != "http://cdn/scene1.jpg" {
		t.Errorf("ImageURL = %q, want first image url", out[0].ImageURL)
	}
	if len(out[0].Tags) != 2 || out[0].Tags[0] != "Blonde" || out[0].Tags[1] != "Outdoor" {
		t.Errorf("Tags = %v, want [Blonde Outdoor]", out[0].Tags)
	}
	// Regression: SearchScene (the identification path) previously omitted
	// duration from its GraphQL selection entirely — only QueryScenes (the
	// browse path) requested it. A caller building a grab request from a
	// SearchScene match with no duration silently failed to auto-qualify
	// anything against Adult's bitrate-quality-floor scorer.
	if out[0].Duration != 1800 {
		t.Errorf("Duration = %d, want 1800", out[0].Duration)
	}
}

func TestFindScene_PopulatesTagsImageAndDuration(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findScene":{"id":"1","title":"T","release_date":"2020-01-01",` +
			`"studio":{"name":"Tushy","parent":null},"tags":[{"name":"Anal"}],"images":[{"url":"http://cdn/f.jpg"}],"duration":2400}}}`))
	})
	defer closeSrv()

	sc, err := c.FindScene(context.Background(), "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc == nil {
		t.Fatal("expected a scene, got nil")
	}
	if sc.ImageURL != "http://cdn/f.jpg" {
		t.Errorf("ImageURL = %q, want http://cdn/f.jpg", sc.ImageURL)
	}
	if len(sc.Tags) != 1 || sc.Tags[0] != "Anal" {
		t.Errorf("Tags = %v, want [Anal]", sc.Tags)
	}
	if sc.Duration != 2400 {
		t.Errorf("Duration = %d, want 2400", sc.Duration)
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

func TestMe_ReturnsAuthenticatedUser(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Query != meQuery {
			t.Errorf("unexpected query: %s", req.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"me":{"id":"42","name":"curtis"}}}`))
	})
	defer closeSrv()

	me, err := c.Me(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if me == nil || me.ID != "42" || me.Name != "curtis" {
		t.Errorf("unexpected result: %+v", me)
	}
}

func TestMe_UnauthorizedKey(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "bad-key"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"not authorized"}]}`))
	})
	defer closeSrv()

	_, err := c.Me(context.Background())
	if err == nil {
		t.Fatal("expected an error for an unauthorized key")
	}
}

func TestSearchPerformer_ParsesResults(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Term  string `json:"term"`
				Limit int    `json:"limit"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Variables.Term != "riley reid" || req.Variables.Limit != 5 {
			t.Errorf("unexpected variables: %+v", req.Variables)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"searchPerformer":[{"id":"p1","name":"Riley Reid"}]}}`))
	})
	defer closeSrv()

	out, err := c.SearchPerformer(context.Background(), "riley reid", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Riley Reid" || out[0].ID != "p1" {
		t.Fatalf("unexpected result: %+v", out)
	}
}

func TestSearchPerformer_EmptyResults(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"searchPerformer":[]}}`))
	})
	defer closeSrv()

	out, err := c.SearchPerformer(context.Background(), "nobody", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected no results, got %+v", out)
	}
}

func TestFindStudio_Found(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findStudio":{"id":"s1","name":"Tushy"}}}`))
	})
	defer closeSrv()

	studio, err := c.FindStudio(context.Background(), "Tushy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if studio == nil || studio.Name != "Tushy" || studio.ID != "s1" {
		t.Fatalf("unexpected result: %+v", studio)
	}
}

func TestFindStudio_PopulatesImage(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findStudio":{"id":"s1","name":"Tushy","images":[{"url":"http://cdn/studio.png"}]}}}`))
	})
	defer closeSrv()

	studio, err := c.FindStudio(context.Background(), "Tushy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if studio == nil || studio.Name != "Tushy" || studio.ID != "s1" {
		t.Fatalf("unexpected result: %+v", studio)
	}
	if studio.ImageURL != "http://cdn/studio.png" {
		t.Errorf("ImageURL = %q, want http://cdn/studio.png", studio.ImageURL)
	}
}

func TestSearchPerformer_PopulatesImage(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"searchPerformer":[` +
			`{"id":"p1","name":"Riley Reid","images":[{"url":"http://cdn/perf.jpg"}]},` +
			`{"id":"p2","name":"Artless","images":[]}]}}`))
	})
	defer closeSrv()

	out, err := c.SearchPerformer(context.Background(), "riley reid", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 performers, got %d", len(out))
	}
	if out[0].Name != "Riley Reid" || out[0].ID != "p1" || out[0].ImageURL != "http://cdn/perf.jpg" {
		t.Errorf("out[0] = %+v, want image populated", out[0])
	}
	if out[1].ImageURL != "" {
		t.Errorf("out[1].ImageURL = %q, want empty for no images", out[1].ImageURL)
	}
}

func TestFindStudio_NotFound(t *testing.T) {
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findStudio":null}}`))
	})
	defer closeSrv()

	studio, err := c.FindStudio(context.Background(), "Nonexistent Studio")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if studio != nil {
		t.Fatalf("expected nil for no match, got %+v", studio)
	}
}

func TestQueryScenes_DecodesBrowseFields(t *testing.T) {
	tests := []struct {
		name         string
		responseBody string
		wantTitle    string
		wantStudio   string
		wantImageURL string
		wantDuration int
		wantPHashes  []string
	}{
		{
			name: "full scene with image, duration, and a PHASH fingerprint",
			responseBody: `{"data":{"queryScenes":{"scenes":[{"id":"s1","title":"Full Scene","release_date":"2024-01-01",` +
				`"studio":{"name":"Vixen","parent":null},"images":[{"url":"http://cdn/scene1.jpg"}],"duration":1800,` +
				`"fingerprints":[{"hash":"ph1","algorithm":"PHASH"}]}]}}}`,
			wantTitle:    "Full Scene",
			wantStudio:   "Vixen",
			wantImageURL: "http://cdn/scene1.jpg",
			wantDuration: 1800,
			wantPHashes:  []string{"ph1"},
		},
		{
			name: "empty images array yields blank ImageURL",
			responseBody: `{"data":{"queryScenes":{"scenes":[{"id":"s2","title":"Artless Scene","release_date":"2024-02-02",` +
				`"studio":{"name":"Tushy","parent":null},"images":[],"duration":600,"fingerprints":[]}]}}}`,
			wantTitle:    "Artless Scene",
			wantStudio:   "Tushy",
			wantImageURL: "",
			wantDuration: 600,
			wantPHashes:  nil,
		},
		{
			name: "non-PHASH fingerprints (MD5/OSHASH) are excluded from PHashes",
			responseBody: `{"data":{"queryScenes":{"scenes":[{"id":"s3","title":"Mixed FP Scene","release_date":"2024-03-03",` +
				`"studio":{"name":"","parent":{"name":"Parent Studio"}},"images":[{"url":"http://cdn/scene3.jpg"}],"duration":0,` +
				`"fingerprints":[{"hash":"md5hash","algorithm":"MD5"},{"hash":"oshash1","algorithm":"OSHASH"},{"hash":"ph3","algorithm":"PHASH"}]}]}}}`,
			wantTitle:    "Mixed FP Scene",
			wantStudio:   "Parent Studio",
			wantImageURL: "http://cdn/scene3.jpg",
			wantDuration: 0,
			wantPHashes:  []string{"ph3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.responseBody))
			})
			defer closeSrv()

			out, err := c.QueryScenes(context.Background(), SceneSortDate, 1, 20)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(out) != 1 {
				t.Fatalf("expected 1 scene, got %d", len(out))
			}
			s := out[0]
			if s.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", s.Title, tt.wantTitle)
			}
			if s.StudioName != tt.wantStudio {
				t.Errorf("StudioName = %q, want %q", s.StudioName, tt.wantStudio)
			}
			if s.ImageURL != tt.wantImageURL {
				t.Errorf("ImageURL = %q, want %q", s.ImageURL, tt.wantImageURL)
			}
			if s.Duration != tt.wantDuration {
				t.Errorf("Duration = %d, want %d", s.Duration, tt.wantDuration)
			}
			if len(s.PHashes) != len(tt.wantPHashes) {
				t.Fatalf("PHashes = %v, want %v", s.PHashes, tt.wantPHashes)
			}
			for i, h := range tt.wantPHashes {
				if s.PHashes[i] != h {
					t.Errorf("PHashes[%d] = %q, want %q", i, s.PHashes[i], h)
				}
			}
		})
	}
}

// TestQueryScenes_SendsPaginationAndSort proves the input variable carries the
// clamped page/per_page, the requested sort, and the hardcoded DESC direction.
func TestQueryScenes_SendsPaginationAndSort(t *testing.T) {
	var gotInput map[string]any
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotInput = req.Variables.Input
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"queryScenes":{"scenes":[]}}}`))
	})
	defer closeSrv()

	// page 0 / perPage -5 must clamp to page 1 / per_page 20 (defaultBrowsePerPage).
	if _, err := c.QueryScenes(context.Background(), SceneSortTrending, 0, -5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotInput["page"].(float64) != 1 {
		t.Errorf("page = %v, want clamped 1", gotInput["page"])
	}
	if gotInput["per_page"].(float64) != 20 {
		t.Errorf("per_page = %v, want defaulted 20", gotInput["per_page"])
	}
	if gotInput["sort"] != "TRENDING" {
		t.Errorf("sort = %v, want TRENDING", gotInput["sort"])
	}
	if gotInput["direction"] != "DESC" {
		t.Errorf("direction = %v, want DESC", gotInput["direction"])
	}
}

func TestQueryStudios_DecodesAndPaginates(t *testing.T) {
	var gotInput map[string]any
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotInput = req.Variables.Input
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"queryStudios":{"studios":[` +
			`{"id":"st1","name":"With Art","images":[{"url":"http://cdn/studio.png"}]},` +
			`{"id":"st2","name":"No Art","images":[]}]}}}`))
	})
	defer closeSrv()

	out, err := c.QueryStudios(context.Background(), 2, 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotInput["page"].(float64) != 2 || gotInput["per_page"].(float64) != 7 {
		t.Errorf("pagination not sent through: %+v", gotInput)
	}
	if gotInput["sort"] != "NAME" {
		t.Errorf("sort = %v, want NAME", gotInput["sort"])
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 studios, got %d", len(out))
	}
	if out[0].Name != "With Art" || out[0].ImageURL != "http://cdn/studio.png" {
		t.Errorf("out[0] = %+v", out[0])
	}
	if out[1].ImageURL != "" {
		t.Errorf("out[1].ImageURL = %q, want empty for no images", out[1].ImageURL)
	}
}

func TestQueryPerformers_DecodesAndPaginates(t *testing.T) {
	var gotInput map[string]any
	c, closeSrv := newTestClient(t, Config{APIKey: "k"}, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotInput = req.Variables.Input
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"queryPerformers":{"performers":[` +
			`{"id":"pf1","name":"Riley","images":[{"url":"http://cdn/perf.jpg"}]},` +
			`{"id":"pf2","name":"Artless","images":[]}]}}}`))
	})
	defer closeSrv()

	out, err := c.QueryPerformers(context.Background(), 3, 15)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotInput["page"].(float64) != 3 || gotInput["per_page"].(float64) != 15 {
		t.Errorf("pagination not sent through: %+v", gotInput)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 performers, got %d", len(out))
	}
	if out[0].Name != "Riley" || out[0].ImageURL != "http://cdn/perf.jpg" {
		t.Errorf("out[0] = %+v", out[0])
	}
	if out[1].ImageURL != "" {
		t.Errorf("out[1].ImageURL = %q, want empty for no images", out[1].ImageURL)
	}
}
