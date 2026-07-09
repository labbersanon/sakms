package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChatJSON_ParsesContent(t *testing.T) {
	var gotAuth, gotFormat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req chatRequest
		json.NewDecoder(r.Body).Decode(&req)
		gotFormat = req.ResponseFormat.Type
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"title":"Some Title"}`}},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "test-key", "gpt-4o-mini", &http.Client{Timeout: 5 * time.Second})
	result, err := client.ChatJSON(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["title"] != "Some Title" {
		t.Fatalf("got %+v", result)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("expected bearer auth, got %q", gotAuth)
	}
	if gotFormat != "json_object" {
		t.Errorf("expected json_object response format, got %q", gotFormat)
	}
}

func TestChatJSON_NoChoicesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{}})
	}))
	defer srv.Close()

	client := New(srv.URL, "k", "m", &http.Client{Timeout: 5 * time.Second})
	if _, err := client.ChatJSON(context.Background(), "prompt"); err == nil {
		t.Fatal("expected an error for an empty choices array")
	}
}

func TestChatJSON_MalformedContentErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "not json"}}},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, "k", "m", &http.Client{Timeout: 5 * time.Second})
	if _, err := client.ChatJSON(context.Background(), "prompt"); err == nil {
		t.Fatal("expected an error for malformed JSON content")
	}
}
