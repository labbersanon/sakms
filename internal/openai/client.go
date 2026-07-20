// Package openai is a minimal client for OpenAI's (or an OpenAI-compatible)
// chat completions endpoint. Deliberately generic (no filename-parsing or
// Stash-specific prompts here — that domain logic lives in internal/identify,
// which consumes this client through the identify.AIClient interface, same
// as internal/ollama).
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/labbersanon/sakms/internal/httpx"
)

// DefaultBaseURL is OpenAI's canonical chat completions API base. A var (not
// const) so tests can override it to point at an httptest server, same as
// tmdb.DefaultBaseURL/tvdb.DefaultBaseURL.
var DefaultBaseURL = "https://api.openai.com/v1"

// Client talks to OpenAI's /chat/completions endpoint. baseURL is caller-
// provided (not hardcoded) so an OpenAI-compatible proxy or self-hosted
// gateway works the same as the real api.openai.com.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	http    *http.Client
}

func New(baseURL, apiKey, model string, httpClient *http.Client) *Client {
	return &Client{baseURL: baseURL, apiKey: apiKey, model: model, http: httpClient}
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// ChatJSON sends prompt to OpenAI's chat completions endpoint in
// response_format=json_object mode and unmarshals the model's response
// content as a JSON object — the same identify.AIClient shape
// internal/ollama's Client satisfies, so the two are interchangeable from
// internal/identify's point of view.
func (c *Client) ChatJSON(ctx context.Context, prompt string) (map[string]any, error) {
	reqBody, err := json.Marshal(chatRequest{
		Model:          c.model,
		Messages:       []chatMessage{{Role: "user", Content: prompt}},
		ResponseFormat: responseFormat{Type: "json_object"},
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling openai request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("building openai request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	var cr chatResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &cr); err != nil {
		return nil, err
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("openai response had no choices")
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(cr.Choices[0].Message.Content), &result); err != nil {
		return nil, fmt.Errorf("parsing openai JSON content %q: %w", cr.Choices[0].Message.Content, err)
	}
	return result, nil
}
