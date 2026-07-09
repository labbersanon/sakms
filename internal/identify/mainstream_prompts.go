package identify

import (
	"context"
	"fmt"

	"github.com/curtiswtaylorjr/sak/internal/ollama"
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
