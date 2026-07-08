package tag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/curtiswtaylorjr/tidyarr/internal/mode"
	"github.com/curtiswtaylorjr/tidyarr/internal/servarr"
)

func newTestSession(t *testing.T, handler http.HandlerFunc) *mode.Session {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &mode.Session{
		Mode:    mode.Movies,
		Servarr: servarr.New(servarr.Config{BaseURL: srv.URL, APIKey: "test-key", App: servarr.Radarr}, srv.Client()),
	}
}

func TestVocabulary_ReturnsAppsTags(t *testing.T) {
	sess := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/tag" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Write([]byte(`[{"id":1,"label":"kids"},{"id":2,"label":"needs-review"}]`))
	})

	got, err := Vocabulary(context.Background(), sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[1].Label != "needs-review" {
		t.Errorf("unexpected vocabulary: %+v", got)
	}
}

func TestAdd_ReusesExistingTagCaseInsensitively(t *testing.T) {
	var putBody map[string]any
	created := false
	sess := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodGet:
			w.Write([]byte(`[{"id":3,"label":"Needs-Review"}]`))
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodPost:
			created = true
			t.Fatal("should not create a tag that already exists (case-insensitively)")
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodGet:
			w.Write([]byte(`{"id":9,"title":"X","tags":[1]}`))
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodPut:
			json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := Add(context.Background(), sess, 9, "needs-review"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created {
		t.Fatal("expected the existing tag to be reused, not recreated")
	}
	tags, _ := putBody["tags"].([]any)
	if len(tags) != 2 || tags[0] != float64(1) || tags[1] != float64(3) {
		t.Errorf("expected the item's tags to become [1, 3], got %+v", putBody["tags"])
	}
}

func TestAdd_CreatesNewTagWhenItDoesntExist(t *testing.T) {
	var createBody map[string]any
	sess := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodGet:
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&createBody)
			w.Write([]byte(`{"id":7,"label":"brand-new"}`))
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodGet:
			w.Write([]byte(`{"id":9,"title":"X","tags":[]}`))
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := Add(context.Background(), sess, 9, "brand-new"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if createBody["label"] != "brand-new" {
		t.Errorf("expected the new tag to be created upstream, got %+v", createBody)
	}
}

func TestAdd_NoOpWhenAlreadyTagged(t *testing.T) {
	sess := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodGet:
			w.Write([]byte(`[{"id":3,"label":"needs-review"}]`))
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodGet:
			w.Write([]byte(`{"id":9,"title":"X","tags":[3]}`))
		case r.Method == http.MethodPut:
			t.Fatal("should not PUT when the item already has the tag")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := Add(context.Background(), sess, 9, "needs-review"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRemove_DropsTagFromItem(t *testing.T) {
	var putBody map[string]any
	sess := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodGet:
			w.Write([]byte(`{"id":9,"title":"X","tags":[1,3]}`))
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodPut:
			json.NewDecoder(r.Body).Decode(&putBody)
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := Remove(context.Background(), sess, 9, 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tags, _ := putBody["tags"].([]any)
	if len(tags) != 1 || tags[0] != float64(1) {
		t.Errorf("expected the item's tags to become [1], got %+v", putBody["tags"])
	}
}

func TestRemove_NoOpWhenTagNotPresent(t *testing.T) {
	sess := newTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie/9" && r.Method == http.MethodGet:
			w.Write([]byte(`{"id":9,"title":"X","tags":[1]}`))
		case r.Method == http.MethodPut:
			t.Fatal("should not PUT when the tag isn't present")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	if err := Remove(context.Background(), sess, 9, 99); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
