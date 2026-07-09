package classify

import (
	"context"
	"fmt"

	"github.com/curtiswtaylorjr/tidyarr/internal/identify"
)

// WithAI asks the configured AI provider whether title/overview describes
// kids-appropriate content — used only when FromMetadata found the
// certification/genre signal too weak to trust (Result.Confident == false).
// ai is the same identify.AIClient shared across every AI-assisted feature
// (see mode.buildAIClient) — Ollama, OpenAI, Gemini, and Anthropic are all
// interchangeable here.
func WithAI(ctx context.Context, ai identify.AIClient, title, overview string) (Result, error) {
	prompt := fmt.Sprintf(`You are classifying media content for a home media library that separates "kids" content (appropriate for young children — kids' shows/movies, G/PG/TV-Y/TV-G-style content, family/children's programming) from general content (everything else, including PG-13/R/TV-14/TV-MA-rated content, or content with no particular kids appeal).

Title: %s
Overview: %s

Respond with ONLY a JSON object of the form {"kids": true} or {"kids": false} — no other text.`, title, overview)

	resp, err := ai.ChatJSON(ctx, prompt)
	if err != nil {
		return Result{}, fmt.Errorf("AI classification failed: %w", err)
	}
	kidsVal, ok := resp["kids"].(bool)
	if !ok {
		return Result{}, fmt.Errorf("AI response missing a boolean 'kids' field: %+v", resp)
	}
	return Result{IsKids: kidsVal, Confident: true, Reason: "AI classification"}, nil
}
