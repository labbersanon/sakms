package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChatJSON_ParsesContent(t *testing.T) {
	var gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": `{"title":"Some Title"}`},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "test-key", "claude-haiku-4-5", &http.Client{Timeout: 5 * time.Second})
	result, err := client.ChatJSON(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["title"] != "Some Title" {
		t.Fatalf("got %+v", result)
	}
	if gotKey != "test-key" {
		t.Errorf("expected the x-api-key header, got %q", gotKey)
	}
	if gotVersion != anthropicVersion {
		t.Errorf("expected the anthropic-version header, got %q", gotVersion)
	}
}

func TestChatJSON_NoTextBlockErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"content": []map[string]any{}})
	}))
	defer srv.Close()

	client := New(srv.URL, "k", "m", &http.Client{Timeout: 5 * time.Second})
	if _, err := client.ChatJSON(context.Background(), "prompt"); err == nil {
		t.Fatal("expected an error for no text content block")
	}
}

func TestChatJSON_MalformedContentErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "not json"}},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "k", "m", &http.Client{Timeout: 5 * time.Second})
	if _, err := client.ChatJSON(context.Background(), "prompt"); err == nil {
		t.Fatal("expected an error for malformed JSON content")
	}
}
