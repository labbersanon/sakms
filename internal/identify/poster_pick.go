package identify

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/labbersanon/sakms/internal/ollama"
	"github.com/labbersanon/sakms/internal/websearch"
)

// PickPosterURL asks AI to choose one https image URL from web search hits
// for a movie/TV poster. Returns "" when search/AI are missing, decline, or
// produce a non-https URL. Does not fetch the image — caller validates via
// imageproxy.
// Claude 2026-09-22: last stage of Library poster fallback after TMDB+TVDB.
// Reason: operator chose auto-apply AI pick when catalogs have no art.
// Troubleshooting: letter tiles for titles TMDB/TVDB both miss.
// Review if: operator confirmation gate is required, or image-search API
//   replaces snippet URL picking.
func PickPosterURL(ctx context.Context, search websearch.Client, ai AIClient, title string, year int, kind string) (string, error) {
	title = strings.TrimSpace(title)
	if search == nil || ai == nil || title == "" {
		return "", nil
	}
	if kind != "movie" && kind != "series" {
		kind = "movie"
	}
	query := title + " " + kind + " poster"
	if year > 0 {
		query = fmt.Sprintf("%s %d %s poster", title, year, kind)
	}
	results, err := search.Search(ctx, query, 8)
	if err != nil || len(results) == 0 {
		return "", err
	}
	var snippets strings.Builder
	for i, r := range results {
		if i > 0 {
			snippets.WriteString("\n\n")
		}
		fmt.Fprintf(&snippets, "Result %d:\nTitle: %s\nSnippet: %s\nURL: %s", i+1, r.Title, r.Description, r.URL)
	}
	prompt := fmt.Sprintf(`You are finding an official theatrical or key-art poster image URL for a %s.

Title: %q
Year: %v

Web search results:
%s

Pick ONE https URL that is most likely a direct link to a poster image file (jpg/png/webp), or a stable CDN image URL for that title's poster.
Prefer well-known image hosts (TMDB, TVDB artworks, Wikipedia/Wikimedia, official studio).
If none of the URLs look like a real poster image for this exact title, respond with {"url": null}.
Do NOT invent a URL. Do NOT pick trailers, articles, or search pages.

Respond with ONLY JSON: {"url": "https://..."} or {"url": null}.`, kind, title, yearOrNull(year), snippets.String())

	resp, err := ai.ChatJSON(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("AI poster pick failed: %w", err)
	}
	raw := ollama.NormalizeField(resp["url"])
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", nil
	}
	return u.String(), nil
}

func yearOrNull(year int) any {
	if year > 0 {
		return year
	}
	return nil
}
