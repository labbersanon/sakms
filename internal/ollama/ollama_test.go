package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestNormalizeField(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"literal null string", "null", ""},
		{"literal null string uppercase", "NULL", ""},
		{"none", "none", ""},
		{"None mixed case", "None", ""},
		{"unknown", "unknown", ""},
		{"n/a", "n/a", ""},
		{"N/A uppercase", "N/A", ""},
		{"empty string", "", ""},
		{"whitespace only", "   ", ""},
		{"real value", "Tushy", "Tushy"},
		{"real value with surrounding whitespace trimmed", "  Tushy  ", "Tushy"},
		{"float year", float64(2024), "2024"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeField(c.in); got != c.want {
				t.Errorf("NormalizeField(%#v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestChatJSON_ParsesModelContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		if req.Format != "json" {
			t.Errorf("expected format=json, got %q", req.Format)
		}
		if req.Stream {
			t.Error("expected stream=false")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatResponse{
			Message: struct {
				Content string `json:"content"`
			}{Content: `{"studio":"Tushy","title":"Some Title","year":"2024","performers":null}`},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "qwen2.5vl:7b", &http.Client{Timeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := c.ChatJSON(ctx, "some prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := NormalizeField(result["studio"]); got != "Tushy" {
		t.Errorf("studio = %q, want Tushy", got)
	}
	if got := NormalizeField(result["title"]); got != "Some Title" {
		t.Errorf("title = %q, want \"Some Title\"", got)
	}
	if got := NormalizeField(result["performers"]); got != "" {
		t.Errorf("performers (null) = %q, want empty", got)
	}
}

func TestChatJSON_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "qwen2.5vl:7b", &http.Client{Timeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.ChatJSON(ctx, "some prompt")
	if err == nil {
		t.Fatal("expected an error on non-200 status")
	}
}

func TestChatJSON_MalformedJSONContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(chatResponse{
			Message: struct {
				Content string `json:"content"`
			}{Content: "not valid json"},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "qwen2.5vl:7b", &http.Client{Timeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.ChatJSON(ctx, "some prompt")
	if err == nil {
		t.Fatal("expected an error parsing malformed JSON content")
	}
}

func TestChatJSON_ContextTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	c := New(srv.URL, "qwen2.5vl:7b", &http.Client{Timeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := c.ChatJSON(ctx, "some prompt")
	if err == nil {
		t.Fatal("expected a context-deadline error")
	}
}

func TestChatJSON_ConcurrencyLimit(t *testing.T) {
	var mu sync.Mutex
	inFlight := 0
	maxSeen := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > maxSeen {
			maxSeen = inFlight
		}
		mu.Unlock()

		time.Sleep(50 * time.Millisecond) // hold the "slot" long enough to overlap

		mu.Lock()
		inFlight--
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatResponse{
			Message: struct {
				Content string `json:"content"`
			}{Content: `{"ok":true}`},
		})
	}))
	defer srv.Close()

	c := NewWithConcurrencyLimit(srv.URL, "test-model", &http.Client{Timeout: 5 * time.Second}, 2)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.ChatJSON(ctx, "prompt")
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if maxSeen > 2 {
		t.Fatalf("expected at most 2 concurrent ChatJSON calls, observed %d", maxSeen)
	}
	if maxSeen < 2 {
		t.Fatalf("expected the limiter to actually allow 2 concurrent calls (not serialize to 1), observed max %d", maxSeen)
	}
}
