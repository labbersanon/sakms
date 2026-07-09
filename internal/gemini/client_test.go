package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChatJSON_ParsesContent(t *testing.T) {
	var gotKey, gotMimeType, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		gotPath = r.URL.Path
		var req generateRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotMimeType = req.GenerationConfig.ResponseMimeType
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{"content": map[string]any{"parts": []map[string]any{{"text": `{"title":"Some Title"}`}}}},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "test-key", "gemini-2.5-flash", &http.Client{Timeout: 5 * time.Second})
	result, err := client.ChatJSON(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["title"] != "Some Title" {
		t.Fatalf("got %+v", result)
	}
	if gotKey != "test-key" {
		t.Errorf("expected the api key in the query string, got %q", gotKey)
	}
	if gotMimeType != "application/json" {
		t.Errorf("expected application/json response mime type, got %q", gotMimeType)
	}
	if gotPath != "/models/gemini-2.5-flash:generateContent" {
		t.Errorf("unexpected request path: %q", gotPath)
	}
}

func TestChatJSON_NoCandidatesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"candidates": []map[string]any{}})
	}))
	defer srv.Close()

	client := New(srv.URL, "k", "m", &http.Client{Timeout: 5 * time.Second})
	if _, err := client.ChatJSON(context.Background(), "prompt"); err == nil {
		t.Fatal("expected an error for an empty candidates array")
	}
}

func TestChatJSON_MalformedContentErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{
				{"content": map[string]any{"parts": []map[string]any{{"text": "not json"}}}},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "k", "m", &http.Client{Timeout: 5 * time.Second})
	if _, err := client.ChatJSON(context.Background(), "prompt"); err == nil {
		t.Fatal("expected an error for malformed JSON content")
	}
}
