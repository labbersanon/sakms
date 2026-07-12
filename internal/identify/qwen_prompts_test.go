package identify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/ollama"
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
		return `{"studio":"Tushy","title":"Some Title","performers":["Alice","Bob"]}`
	})
	defer closeSrv()

	parsed, err := ParseFilename(context.Background(), client, "some-filename", "Tushy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(seenPrompt, "parent folder named: 'Tushy'") {
		t.Error("expected parent folder context to be included in the prompt")
	}
	if parsed.Studio != "Tushy" || parsed.Title != "Some Title" || parsed.Year != "" {
		t.Fatalf("got %+v", parsed)
	}
	if len(parsed.Performers) != 2 || parsed.Performers[0] != "Alice" {
		t.Fatalf("got performers %+v", parsed.Performers)
	}
}

func TestParseFilename_PromptIncludesSeparatorNormalizationGuidance(t *testing.T) {
	var seenPrompt string
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		seenPrompt = prompt
		return `{"studio":"Tushy","title":"Deep Desires","performers":["Riley Reid"]}`
	})
	defer closeSrv()

	parsed, err := ParseFilename(context.Background(), client, "tushy.24.03.15.riley.reid.deep.desires.1080p", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(seenPrompt, "word separators") {
		t.Error("expected the prompt to explain dots/dashes/underscores are word separators")
	}
	if !strings.Contains(seenPrompt, "'riley.reid' becomes 'Riley Reid'") {
		t.Error("expected the prompt to give a concrete separator-normalization example")
	}
	if parsed.Year != "2024" {
		t.Fatalf("expected year deterministically extracted from the '24.03.15' token, got %q", parsed.Year)
	}
}

// The DATE RULE section that used to live in the prompt asked qwen2.5:1.5b to
// infer the year itself. Live testing against real filenames found the model
// frequently mis-binds which segment of a YY.MM.DD token is the year (e.g.
// reading the DAY segment of 25.12.11 as "2011" instead of "2025"). These
// tests lock in the fix: year is resolved by ExtractYearFromToken in Go, and
// the LLM's own "year" opinion (even if wrong, or the field is absent
// entirely) is never consulted.
func TestParseFilename_YearIsDeterministicNotTrustedFromLLM(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		// Simulate the confirmed wrong-segment misparse: the LLM returns the
		// DAY segment (11) misread as the year (2011) instead of the real
		// year segment (25 -> 2025).
		return `{"studio":"FTVGirls","title":"Krystal Teen Orgasms","year":"2011","performers":["Krystal"]}`
	})
	defer closeSrv()

	parsed, err := ParseFilename(context.Background(), client, "FTVGirls.25.12.11.Krystal.Teen.Orgasms.XXX.1080p", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Year != "2025" {
		t.Fatalf("expected deterministic year 2025 (ignoring the LLM's wrong 2011 guess), got %q", parsed.Year)
	}
}

func TestParseFilename_YearFallsBackToParentNameToken(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"studio":"FTVGirls","title":"Some Title","performers":null}`
	})
	defer closeSrv()

	parsed, err := ParseFilename(context.Background(), client, "some-file-with-no-date", "FTVGirls.25.12.11.Krystal.Teen.Orgasms.XXX")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Year != "2025" {
		t.Fatalf("expected year extracted from parentName fallback, got %q", parsed.Year)
	}
}

func TestParseFilename_NoDateTokenYieldsEmptyYear(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		// Even if the LLM hallucinates a year, it must be ignored here —
		// there is no real date token in this filename.
		return `{"studio":"Brazzers","title":"Scene 442","year":"1999","performers":["Riley Reid"]}`
	})
	defer closeSrv()

	parsed, err := ParseFilename(context.Background(), client, "brazzers.scene442.riley.reid.1080p", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Year != "" {
		t.Fatalf("expected empty year for a filename with no date token, got %q", parsed.Year)
	}
}

func TestParseFilename_PromptNoLongerAsksLLMForYear(t *testing.T) {
	var seenPrompt string
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		seenPrompt = prompt
		return `{"studio":"Tushy","title":"Deep Desires","performers":["Riley Reid"]}`
	})
	defer closeSrv()

	if _, err := ParseFilename(context.Background(), client, "tushy.24.03.15.riley.reid.deep.desires.1080p", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(seenPrompt, "DATE RULE") {
		t.Error("expected the DATE RULE section to be removed from the prompt now that year is resolved deterministically")
	}
	if strings.Contains(seenPrompt, "\"year\"") {
		t.Error("expected the prompt to no longer ask the LLM to return a year field")
	}
}

func TestExtractYearFromToken(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"2-digit year", "ftvgirls.25.12.11.krystal.teen.orgasms.xxx.1080p", "2025"},
		{"4-digit year", "tushy.2024.03.15.riley.reid.deep.desires.1080p", "2024"},
		{"no date token, scene number", "brazzers.scene442.riley.reid.1080p", ""},
		{"no date token, resolution only", "some.title.1080p.mp4", ""},
		{"empty string", "", ""},
		// Real release-group-suffixed filenames pulled from
		// /mnt/Downloads-NAS/nzb/completed/Adult/ on server1 — the exact
		// input shape that exposed the wrong-segment misparse bug (see
		// sakms_adult_ai_identification.md). Confirms the deterministic
		// extractor gets all of them right, including the precise
		// 25.12.11 token that qwen2.5:1.5b previously misread as "2011".
		{"real: EvilAngel 2160p multi-performer", "EvilAngel.25.10.01.TS.Aubrey.Kate.and.TS.Jade.Venus.XXX.2160p.MP4-Narcos", "2025"},
		{"real: EvilAngel release-group suffix", "EvilAngel.25.11.11.Rebel.Rhyder.A.Day.In.The.Life.Of.Rebel.Part.1.XXX.2160p.MP4-WRB", "2025"},
		{"real: FTVGirls 2017", "FTVGirls.17.03.10.Harley.Shes.Up.For.Anything.XXX.1080p.MP4-KTR", "2017"},
		{"real: FTVGirls REMASTERED HEVC tag", "FTVGirls.24.04.01.Mali.Now.A.FTV.Girl.REMASTERED.XXX.1080p.HEVC.x265", "2024"},
		{"real: FTVGirls 25.11.11", "FTVGirls.25.11.11.Henna.Her.First.Experience.XXX.1080p.MP4-NBQ", "2025"},
		{"real: FTVGirls 25.11.23", "FTVGirls.25.11.23.Kourtney.Everything.First.Time.XXX.1080p.MP4-NBQ", "2025"},
		{"real: FTVGirls 25.11.26", "FTVGirls.25.11.26.Della.Cate.Nineteens.First.XXX.1080p.MP4-NBQ", "2025"},
		{"real: FTVGirls 25.12.01", "FTVGirls.25.12.01.Angel.Lets.Get.Kinkier.XXX.1080p.MP4-NBQ", "2025"},
		{"real: FTVGirls 25.12.11 (confirmed bug case)", "FTVGirls.25.12.11.Krystal.Teen.Orgasms.XXX.1080p.MP4-NBQ", "2025"},
		{"real: FTVGirls 25.12.24", "FTVGirls.25.12.24.Simone.Gum.Chew.Follies.XXX.1080p.MP4-NBQ", "2025"},
		{"real: FTVGirls 25.12.28", "FTVGirls.25.12.28.Katerina.Karson.Her.Desirable.Figure.XXX.1080p.MP4-NBQ", "2025"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractYearFromToken(tc.in); got != tc.want {
				t.Errorf("ExtractYearFromToken(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
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
