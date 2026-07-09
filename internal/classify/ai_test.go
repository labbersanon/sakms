package classify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sak/internal/ollama"
)

func fakeOllamaServer(t *testing.T, content string) *ollama.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"content": content},
		})
	}))
	t.Cleanup(srv.Close)
	return ollama.New(srv.URL, "qwen2.5vl:7b", &http.Client{Timeout: 5 * time.Second})
}

func TestWithAI_ParsesKidsTrue(t *testing.T) {
	c := fakeOllamaServer(t, `{"kids": true}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r, err := WithAI(ctx, c, "Bluey", "An animated series about a lovable family of dogs.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.IsKids || !r.Confident {
		t.Errorf("expected confident kids=true, got %+v", r)
	}
}

func TestWithAI_ParsesKidsFalse(t *testing.T) {
	c := fakeOllamaServer(t, `{"kids": false}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r, err := WithAI(ctx, c, "Breaking Bad", "A chemistry teacher turns to manufacturing drugs.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.IsKids || !r.Confident {
		t.Errorf("expected confident kids=false, got %+v", r)
	}
}

func TestWithAI_MissingKidsFieldErrors(t *testing.T) {
	c := fakeOllamaServer(t, `{"something_else": true}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := WithAI(ctx, c, "Foo", "Bar")
	if err == nil {
		t.Error("expected an error when the AI response has no 'kids' field")
	}
}

func TestWithAI_MalformedContentErrors(t *testing.T) {
	c := fakeOllamaServer(t, `not json`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := WithAI(ctx, c, "Foo", "Bar")
	if err == nil {
		t.Error("expected an error for malformed AI response content")
	}
}
