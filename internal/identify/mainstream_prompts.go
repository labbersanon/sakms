package identify

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/ollama"
	"github.com/labbersanon/sakms/internal/websearch"
)

// GuessTitle asks the mainstream AI to recover a movie/TV title (and year
// when confident) from a messy file/folder name — Rename's AI fallback and
// identity-repair fallback when embedded tags / NFO are absent.
//
// Claude 2026-09-23: structured {title,year}; decline-heavy prompt (C/C/C).
// Reason: year was mentioned in prose but discarded; hallucinations matched
//   wrong TMDB rows; backfill never called GuessTitle for tmdb_id≤0 movies.
// Troubleshooting: opaque release names fabricating titles → force null.
// Review if: vision/OCR title-card path supersedes filename GuessTitle.
// Related files: rename.go, api/poster_backfill.go, releasematch.go
func GuessTitle(ctx context.Context, client AIClient, name string) (TitleGrounding, error) {
	if client == nil {
		return TitleGrounding{}, fmt.Errorf("AI title guess: no client")
	}
	prompt := fmt.Sprintf(`You identify movies and TV series from messy filenames/folder names.

Filename/folder: %q

Rules (follow strictly):
1. Respond with ONLY JSON: {"title":"...","year":1986} or {"title":null,"year":null}.
2. "title" must be the official work title ONLY — do NOT put the year inside the title string.
3. "year" is the first-release / first-air year as an integer, or null if you are not sure.
4. Strip release-scene noise (resolution, codec, source, group tags, "WEB-DL", "BluRay", etc.).
5. If the name is abbreviated, hashed, generic, multi-title ambiguous, or you are less than highly confident — do NOT invent a plausible title. Respond with {"title":null,"year":null}.
6. Prefer declining over a wrong guess. A null decline is success; a fabricated title is failure.

Examples of decline: random hashes, single generic words, unreadable abbreviations with no clear expansion.`, name)

	resp, err := client.ChatJSON(ctx, prompt)
	if err != nil {
		return TitleGrounding{}, fmt.Errorf("AI title guess failed: %w", err)
	}
	title := ollama.NormalizeField(resp["title"])
	if title == "" {
		return TitleGrounding{}, fmt.Errorf("AI could not confidently determine a title (or declined to guess) for %q", name)
	}
	// Strip a trailing "(YYYY)" if the model ignored rule 2.
	year := parseGuessYear(resp["year"])
	if year == 0 {
		if y, clean := splitTrailingYear(title); y > 0 {
			year, title = y, clean
		}
	} else {
		title = strings.TrimSpace(strings.ReplaceAll(title, fmt.Sprintf("(%d)", year), ""))
	}
	return TitleGrounding{Title: title, Year: year}, nil
}

func parseGuessYear(v any) int {
	switch y := v.(type) {
	case float64:
		n := int(y)
		if n >= 1888 && n <= 2100 {
			return n
		}
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(y))
		if n >= 1888 && n <= 2100 {
			return n
		}
	}
	return 0
}

func splitTrailingYear(title string) (year int, clean string) {
	title = strings.TrimSpace(title)
	if len(title) < 7 {
		return 0, title
	}
	// "... (1999)" or "... 1999"
	if strings.HasSuffix(title, ")") {
		open := strings.LastIndex(title, "(")
		if open > 0 {
			inner := strings.TrimSpace(title[open+1 : len(title)-1])
			if y, err := strconv.Atoi(inner); err == nil && y >= 1888 && y <= 2100 {
				return y, strings.TrimSpace(title[:open])
			}
		}
	}
	parts := strings.Fields(title)
	if len(parts) >= 2 {
		if y, err := strconv.Atoi(parts[len(parts)-1]); err == nil && y >= 1888 && y <= 2100 {
			return y, strings.TrimSpace(strings.Join(parts[:len(parts)-1], " "))
		}
	}
	return 0, title
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

// GroundTitleViaSearch searches via websearch.Client then runs ExtractTitleFromSearch.
// Soft-fails (empty grounding, nil error) when search/AI missing or search returns nothing.
//
// Claude 2026-08-05: renamed from GroundTitleViaBrave — accepts any websearch.Client
// Reason: deep-interview-searxng-websearch (SearXNG primary / Brave fallback)
// Troubleshooting: empty results → Phase 2 soft Unmatched
// Review if: callers need per-provider error surfacing in proposal Reason
func GroundTitleViaSearch(ctx context.Context, search websearch.Client, ai AIClient, query string) (TitleGrounding, error) {
	if search == nil || ai == nil || strings.TrimSpace(query) == "" {
		return TitleGrounding{}, nil
	}
	results, err := search.Search(ctx, query, 5)
	if err != nil || len(results) == 0 {
		return TitleGrounding{}, nil
	}
	snippets := make([]SearchSnippet, 0, len(results))
	for _, r := range results {
		snippets = append(snippets, SearchSnippet{Title: r.Title, Description: r.Description, URL: r.URL})
	}
	return ExtractTitleFromSearch(ctx, ai, query, snippets)
}
