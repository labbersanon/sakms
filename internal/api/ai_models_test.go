package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOllamaModelsHandler_ReturnsModelNames proves the endpoint returns the
// installed model names from a live-fetched /api/tags call.
func TestOllamaModelsHandler_ReturnsModelNames(t *testing.T) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"name":"qwen2.5vl:7b"},{"name":"llama3.2:latest"}]}`))
	}))
	defer ollamaSrv.Close()

	h := ollamaModelsHandler(testHTTPClient())
	req := httptest.NewRequest(http.MethodGet, "/api/ollama/models?url="+ollamaSrv.URL, nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var models []string
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("response is not a JSON array of strings: %v (%s)", err, rec.Body.String())
	}
	want := []string{"qwen2.5vl:7b", "llama3.2:latest"}
	if len(models) != len(want) || models[0] != want[0] || models[1] != want[1] {
		t.Errorf("got %v, want %v", models, want)
	}
}

// TestOllamaModelsHandler_MissingURL proves a missing url query param is a
// clean 400, never a 500 or a panic.
func TestOllamaModelsHandler_MissingURL(t *testing.T) {
	h := ollamaModelsHandler(testHTTPClient())
	req := httptest.NewRequest(http.MethodGet, "/api/ollama/models", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing url, got %d", rec.Code)
	}
}

// TestOllamaModelsHandler_UnreachableInstance proves an unreachable Ollama
// instance surfaces as a clean 502 Bad Gateway, not a crash — the same
// gateway-style error netscanProwlarrKeyHandler uses for the equivalent
// "operator-supplied unreachable URL" case, never a bare 500.
func TestOllamaModelsHandler_UnreachableInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // closed: connection refused

	h := ollamaModelsHandler(testHTTPClient())
	req := httptest.NewRequest(http.MethodGet, "/api/ollama/models?url="+srv.URL, nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway for an unreachable instance, got %d", rec.Code)
	}
}

// TestOllamaModelsHandler_UnexpectedShape proves a response that doesn't
// decode as the expected {"models": [...]} shape is a clean 502 Bad Gateway
// (the upstream Ollama instance responded, just not usefully), never a 500.
func TestOllamaModelsHandler_UnexpectedShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	h := ollamaModelsHandler(testHTTPClient())
	req := httptest.NewRequest(http.MethodGet, "/api/ollama/models?url="+srv.URL, nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway for a bad-shape response, got %d", rec.Code)
	}
}
