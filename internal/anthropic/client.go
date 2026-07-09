// Package anthropic is a minimal client for Anthropic's Messages API.
// Deliberately generic (no filename-parsing or Stash-specific prompts here —
// that domain logic lives in internal/identify, which consumes this client
// through the identify.AIClient interface, same as internal/ollama).
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/curtiswtaylorjr/tidyarr/internal/httpx"
)

// anthropicVersion is the Messages API version this client speaks — a
// required header, not a user-facing setting, so a constant is correct.
const anthropicVersion = "2023-06-01"

// maxTokens caps the model's response length — generous for the short JSON
// objects every prompt in this program asks for, nowhere near enough to run
// away on cost for a single filename/title/classification call.
const maxTokens = 1024

// Client talks to Anthropic's /v1/messages endpoint. baseURL is caller-
// provided (typically https://api.anthropic.com/v1) so a proxied endpoint
// works the same as the default.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	http    *http.Client
}

func New(baseURL, apiKey, model string, httpClient *http.Client) *Client {
	return &Client{baseURL: baseURL, apiKey: apiKey, model: model, http: httpClient}
}

type messagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messagesResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// ChatJSON sends prompt to Claude and unmarshals its response text as a JSON
// object — the same identify.AIClient shape internal/ollama's Client
// satisfies, so the two are interchangeable from internal/identify's point
// of view.
//
// Unlike OpenAI/Gemini, the Messages API has no dedicated "JSON mode" toggle
// — every prompt in internal/identify already explicitly instructs "respond
// with ONLY valid JSON", which is what this client relies on instead.
func (c *Client) ChatJSON(ctx context.Context, prompt string) (map[string]any, error) {
	reqBody, err := json.Marshal(messagesRequest{
		Model:     c.model,
		MaxTokens: maxTokens,
		Messages:  []message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling anthropic request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("building anthropic request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	var mr messagesResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &mr); err != nil {
		return nil, err
	}

	for _, block := range mr.Content {
		if block.Type != "text" {
			continue
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(block.Text), &result); err != nil {
			return nil, fmt.Errorf("parsing anthropic JSON content %q: %w", block.Text, err)
		}
		return result, nil
	}
	return nil, fmt.Errorf("anthropic response had no text content block")
}
