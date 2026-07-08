package identify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/tidyarr/internal/ollama"
)

func fakeOllama(t *testing.T, handler func(prompt string) string) (*ollama.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := ""
		if len(req.Messages) > 0 {
			prompt = req.Messages[0].Content
		}
		content := handler(prompt)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"content": content},
		})
	}))
	return ollama.New(srv.URL, "test-model", &http.Client{Timeout: 5 * time.Second}), srv.Close
}

func TestParseFilename_IncludesParentFolderContext(t *testing.T) {
	var seenPrompt string
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		seenPrompt = prompt
		return `{"studio":"Tushy","title":"Some Title","year":"2024","performers":["Alice","Bob"]}`
	})
	defer closeSrv()

	parsed, err := ParseFilename(context.Background(), client, "some-filename", "Tushy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(seenPrompt, "parent folder named: 'Tushy'") {
		t.Error("expected parent folder context to be included in the prompt")
	}
	if parsed.Studio != "Tushy" || parsed.Title != "Some Title" || parsed.Year != "2024" {
		t.Fatalf("got %+v", parsed)
	}
	if len(parsed.Performers) != 2 || parsed.Performers[0] != "Alice" {
		t.Fatalf("got performers %+v", parsed.Performers)
	}
}

func TestParseFilename_OmitsContextWhenNoParentName(t *testing.T) {
	var seenPrompt string
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		seenPrompt = prompt
		return `{"studio":null,"title":null,"year":null,"performers":null}`
	})
	defer closeSrv()

	parsed, err := ParseFilename(context.Background(), client, "some-filename", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(seenPrompt, "parent folder named") {
		t.Error("expected no parent folder context when parentName is empty")
	}
	if parsed.Studio != "" || parsed.Title != "" {
		t.Fatalf("expected null fields to normalize to empty, got %+v", parsed)
	}
}

func TestParseFilename_PerformersAsLiteralNullString(t *testing.T) {
	// Qwen's format=json mode sometimes returns the literal string "null" for
	// performers instead of a real null or [].
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"studio":"S","title":"T","year":"2024","performers":"null"}`
	})
	defer closeSrv()

	parsed, err := ParseFilename(context.Background(), client, "f", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.Performers) != 0 {
		t.Fatalf("expected literal 'null' string performers to normalize to empty, got %+v", parsed.Performers)
	}
}

func TestParseFilename_PerformersArrayWithNullStringEntries(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"studio":"S","title":"T","year":"2024","performers":["Alice","unknown","Bob"]}`
	})
	defer closeSrv()

	parsed, err := ParseFilename(context.Background(), client, "f", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.Performers) != 2 || parsed.Performers[0] != "Alice" || parsed.Performers[1] != "Bob" {
		t.Fatalf("expected 'unknown' sentinel filtered out, got %+v", parsed.Performers)
	}
}

func TestExtractFromSearch_NoResultsReturnsEmpty(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		t.Fatal("should not call Ollama when there are no search results")
		return ""
	})
	defer closeSrv()

	got, err := ExtractFromSearch(context.Background(), client, "stem", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != (GroundedExtraction{}) {
		t.Fatalf("expected zero-value result for no search results, got %+v", got)
	}
}

func TestExtractFromSearch_RejectsDissimilarGroundedTitle(t *testing.T) {
	// The grounded title has near-zero token overlap with the original
	// filename — the sanity gate (similarity < 0.2) must reject it.
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"studio":"Totally Unrelated","title":"Completely Different Content Entirely","year":"2020"}`
	})
	defer closeSrv()

	results := []SearchSnippet{{Title: "x", Description: "y", URL: "z"}}
	got, err := ExtractFromSearch(context.Background(), client, "Original Specific Filename Words", results, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != (GroundedExtraction{}) {
		t.Fatalf("expected the dissimilar grounded result to be rejected, got %+v", got)
	}
}

func TestExtractFromSearch_AcceptsSimilarGroundedTitle(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"studio":"Exposed Latinas","title":"Threesome With The Wife And Friend Scene 1","year":null}`
	})
	defer closeSrv()

	results := []SearchSnippet{{Title: "Exposed Latinas - Threesome Scene 1", Description: "desc", URL: "url"}}
	got, err := ExtractFromSearch(context.Background(), client, "Exposed Latinas Threesome With The Wife And Friend Scene 1", results, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Studio != "Exposed Latinas" || got.Title == "" {
		t.Fatalf("expected a similar grounded result to be accepted, got %+v", got)
	}
}

func TestExtractFromSearch_IncludesSnippetsAndParentContext(t *testing.T) {
	var seenPrompt string
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		seenPrompt = prompt
		return `{"studio":null,"title":null,"year":null}`
	})
	defer closeSrv()

	results := []SearchSnippet{{Title: "Result Title", Description: "Result Desc", URL: "https://x.example"}}
	_, err := ExtractFromSearch(context.Background(), client, "some stem", results, "Some Parent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(seenPrompt, "Result Title") || !strings.Contains(seenPrompt, "Result Desc") {
		t.Error("expected search result snippets to be embedded in the prompt")
	}
	if !strings.Contains(seenPrompt, "Some Parent") {
		t.Error("expected parent folder context to be embedded in the prompt")
	}
}
