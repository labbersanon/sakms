package identify

import "context"

// AIClient is the one capability every AI-assisted prompt in this package
// needs: send a prompt, get back a parsed JSON object. Every provider this
// program supports (Ollama, OpenAI, Gemini, Anthropic — see internal/ollama,
// internal/openai, internal/gemini, internal/anthropic) implements this
// exact shape, so ParseFilename/ExtractFromSearch/GuessTitle/WithAI never
// know or care which one is actually configured. Prompts are written to be
// provider-agnostic: each one spells out its exact expected JSON schema and
// an explicit "respond with null if unsure" escape valve inline, rather than
// relying on any one model's fine-tuned behavior.
type AIClient interface {
	ChatJSON(ctx context.Context, prompt string) (map[string]any, error)
}
