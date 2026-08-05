package identify

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/bravesearch"
	"github.com/labbersanon/sakms/internal/ollama"
)

// GuessTitle asks Ollama to guess the real title of a movie or TV series from
// a messy file/folder name that didn't produce a usable Sonarr/Radarr lookup
// match on its own — Rename's AI fallback for Movies/Series, the mainstream
// counterpart to the Adult pipeline's ParseFilename/ExtractFromSearch.
//
// For a genuinely opaque name with no real signal, the model does not
// reliably decline to answer — it can fabricate a plausible-sounding but
// entirely unrelated title instead. The prompt explicitly gives it an "I
// don't know" escape valve, and ollama.NormalizeField treats that as absent,
// so a hallucinated title can't go on to match an unrelated lookup result and
// get registered as a real identification.
func GuessTitle(ctx context.Context, client AIClient, name string) (string, error) {
	prompt := fmt.Sprintf(`This is a filename or folder name for a movie or TV series, possibly with release-scene noise mixed in (resolution, codec, source, release group tags): %q

If you can confidently determine the real title from this name, respond with a JSON object of the form {"title": "..."} (include the year if you can tell what it is). If the name is too generic, abbreviated, or opaque to confidently identify — do NOT guess or fabricate a plausible-sounding title — respond with {"title": null} instead.

Respond with ONLY the JSON object, no other text.`, name)

	resp, err := client.ChatJSON(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("AI title guess failed: %w", err)
	}
	title := ollama.NormalizeField(resp["title"])
	if title == "" {
		return "", fmt.Errorf("AI could not confidently determine a title (or declined to guess) for %q", name)
	}
	return title, nil
}

// TitleGrounding is the mainstream (Movies/Series) result of Brave-grounded extract.
type TitleGrounding struct {
	Title string
	Year  int
}

// ExtractTitleFromSearch grounds a messy movie/TV filename in Brave snippets.
// Adult content uses ExtractFromSearch instead — do not reuse that prompt here.
//
// Claude 2026-08-05: mainstream Brave ground for Rename Phase 2
// Reason: deep-interview-rename-brave-phase2
// Troubleshooting: GuessTitle near-miss / wrong year (Jo Jo Dancer 2007→1986)
// Review if: shared prompt template with Adult ExtractFromSearch
func ExtractTitleFromSearch(ctx context.Context, client AIClient, query string, results []SearchSnippet) (TitleGrounding, error) {
	if client == nil || len(results) == 0 {
		return TitleGrounding{}, nil
	}
	var snippets strings.Builder
	for i, r := range results {
		if i > 0 {
			snippets.WriteString("\n\n")
		}
		fmt.Fprintf(&snippets, "Result %d:\nTitle: %s\nSnippet: %s\nURL: %s", i+1, r.Title, r.Description, r.URL)
	}
	prompt := fmt.Sprintf(`You are identifying a movie or TV series from a messy filename/folder name using web search results.

Query / filename: %q

Web search results:
%s

Based on these results, determine the REAL official title and release year (first release / premiere year for a film; first air year for a series).
If the filename suggests a book, autobiography, or wrong year but the search results clearly identify a film or TV show, use the film/TV title and year from the search results.
If you cannot confidently identify a real movie or series from these results, respond with {"title": null, "year": null}.

Respond with ONLY a JSON object: {"title": "...", "year": 1986} (year may be null).`, query, snippets.String())

	resp, err := client.ChatJSON(ctx, prompt)
	if err != nil {
		return TitleGrounding{}, fmt.Errorf("AI web-grounded title extract failed: %w", err)
	}
	title := ollama.NormalizeField(resp["title"])
	if title == "" {
		return TitleGrounding{}, nil
	}
	year := 0
	switch y := resp["year"].(type) {
	case float64:
		year = int(y)
	case string:
		year, _ = strconv.Atoi(y)
	}
	return TitleGrounding{Title: title, Year: year}, nil
}

// GroundTitleViaBrave searches Brave then runs ExtractTitleFromSearch. Soft-fails
// (empty grounding, nil error) when Brave/AI missing or search returns nothing.
func GroundTitleViaBrave(ctx context.Context, brave *bravesearch.Client, ai AIClient, query string) (TitleGrounding, error) {
	if brave == nil || ai == nil || strings.TrimSpace(query) == "" {
		return TitleGrounding{}, nil
	}
	results, err := brave.Search(ctx, query, 5)
	if err != nil || len(results) == 0 {
		return TitleGrounding{}, nil
	}
	snippets := make([]SearchSnippet, 0, len(results))
	for _, r := range results {
		snippets = append(snippets, SearchSnippet{Title: r.Title, Description: r.Description, URL: r.URL})
	}
	return ExtractTitleFromSearch(ctx, ai, query, snippets)
}
