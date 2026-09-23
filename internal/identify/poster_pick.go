package identify

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/labbersanon/sakms/internal/ollama"
	"github.com/labbersanon/sakms/internal/websearch"
)

// PickPosterURL chooses one https image from an image search for a movie or
// series poster. The model picks among the hits. A decline returns "" so the
// caller can use a local folder image. An invented URL, a model error, or no
// model keeps the first direct image.
// Claude 2026-09-23: image search replaces snippet URL picking.
// Reason: page results are not poster files; TMDB and TVDB often have no art.
// Troubleshooting: empty result → SearXNG image engines, or the query matched nothing.
// Review if: posters must be confirmed before they are stored.
func PickPosterURL(ctx context.Context, search websearch.Client, ai AIClient, title string, year int, kind string) (string, error) {
	title = strings.TrimSpace(title)
	img, ok := search.(websearch.ImageSearcher)
	if !ok || img == nil || title == "" {
		return "", nil
	}
	if kind != "movie" && kind != "series" {
		kind = "movie"
	}
	query := title + " " + kind + " poster"
	if year > 0 {
		query = fmt.Sprintf("%s %d %s poster", title, year, kind)
	}
	results, err := img.SearchImages(ctx, query, 8)
	if err != nil || len(results) == 0 {
		return "", err
	}

	allowed := map[string]struct{}{}
	var snippets strings.Builder
	first := ""
	n := 0
	for _, r := range results {
		raw := strings.TrimSpace(r.URL)
		if !httpsURL(raw) {
			continue
		}
		if _, seen := allowed[raw]; seen {
			continue
		}
		allowed[raw] = struct{}{}
		if first == "" {
			first = raw
		}
		n++
		if n > 1 {
			snippets.WriteString("\n\n")
		}
		fmt.Fprintf(&snippets, "Result %d:\nTitle: %s\nImage: %s", n, r.Title, raw)
	}
	if first == "" {
		return "", nil
	}
	if ai == nil {
		return first, nil
	}
	prompt := fmt.Sprintf(`You are choosing a poster image for a %s.

Title: %q
Year: %v

Image search results (each Image line is a direct image URL):
%s

Pick ONE Image URL that is this exact title's poster.
If none of them is a poster for this title, respond with {"url": null}.
Do NOT invent a URL. Do NOT pick a photo, logo, or a different title.

Respond with ONLY JSON: {"url": "https://..."} or {"url": null}.`, kind, title, yearOrNull(year), snippets.String())

	resp, err := ai.ChatJSON(ctx, prompt)
	if err != nil {
		return first, nil
	}
	raw := ollama.NormalizeField(resp["url"])
	if raw == "" {
		return "", nil
	}
	if _, ok := allowed[raw]; ok {
		return raw, nil
	}
	return first, nil
}

func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

func yearOrNull(year int) any {
	if year > 0 {
		return year
	}
	return nil
}
