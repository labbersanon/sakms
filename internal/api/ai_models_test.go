package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func ollamaModelsHandlerFor(t *testing.T, ollamaURL string) http.HandlerFunc {
	t.Helper()
	connStore, _, _, _, _, _, _, _, _, _ := testStores(t)
	if err := connStore.Upsert(context.Background(), "ollama", ollamaURL, ""); err != nil {
		t.Fatalf("ollama upsert: %v", err)
	}
	return ollamaModelsHandler(connStore, testHTTPClient())
}

func TestOllamaModelsHandler_ReturnsModelNames(t *testing.T) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"name":"qwen2.5vl:7b"},{"name":"llama3.2:latest"}]}`))
	}))
	defer ollamaSrv.Close()

	h := ollamaModelsHandlerFor(t, ollamaSrv.URL)
	req := httptest.NewRequest(http.MethodGet, "/api/ollama/models", nil)
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

func TestOllamaModelsHandler_MissingConnection(t *testing.T) {
	connStore, _, _, _, _, _, _, _, _, _ := testStores(t)
	h := ollamaModelsHandler(connStore, testHTTPClient())
	req := httptest.NewRequest(http.MethodGet, "/api/ollama/models", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing ollama connection, got %d", rec.Code)
	}
}

func TestOllamaModelsHandler_IgnoresQueryURL(t *testing.T) {
	saved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"name":"saved:latest"}]}`))
	}))
	defer saved.Close()
	h := ollamaModelsHandlerFor(t, saved.URL)
	req := httptest.NewRequest(http.MethodGet, "/api/ollama/models?url=http://169.254.169.254/", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from saved connection, got %d (%s)", rec.Code, rec.Body.String())
	}
	var models []string
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0] != "saved:latest" {
		t.Errorf("got %v, want [saved:latest] (query url must be ignored)", models)
	}
}

func TestOllamaModelsHandler_UnreachableInstance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	h := ollamaModelsHandlerFor(t, srv.URL)
	req := httptest.NewRequest(http.MethodGet, "/api/ollama/models", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway for an unreachable instance, got %d", rec.Code)
	}
}

func TestOllamaModelsHandler_UnexpectedShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	h := ollamaModelsHandlerFor(t, srv.URL)
	req := httptest.NewRequest(http.MethodGet, "/api/ollama/models", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway for a bad-shape response, got %d", rec.Code)
	}
}
