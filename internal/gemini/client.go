// Package gemini is a minimal client for Google's Gemini generateContent
// endpoint. Deliberately generic (no filename-parsing or Stash-specific
// prompts here — that domain logic lives in internal/identify, which
// consumes this client through the identify.AIClient interface, same as
// internal/ollama).
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/curtiswtaylorjr/sak/internal/httpx"
)

// Client talks to Gemini's v1beta generateContent REST endpoint. baseURL is
// caller-provided (typically https://generativelanguage.googleapis.com/v1beta)
// so a regional or proxied endpoint works the same as the default.
type Client struct {
	baseURL string
	model   string
	apiKey  string
	http    *http.Client
}

func New(baseURL, apiKey, model string, httpClient *http.Client) *Client {
	return &Client{baseURL: baseURL, apiKey: apiKey, model: model, http: httpClient}
}

type generateRequest struct {
	Contents         []content        `json:"contents"`
	GenerationConfig generationConfig `json:"generationConfig"`
}

type content struct {
	Parts []part `json:"parts"`
}

type part struct {
	Text string `json:"text"`
}

type generationConfig struct {
	ResponseMimeType string `json:"responseMimeType"`
}

type generateResponse struct {
	Candidates []struct {
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

// ChatJSON sends prompt to Gemini with responseMimeType=application/json and
// unmarshals the model's response text as a JSON object — the same
// identify.AIClient shape internal/ollama's Client satisfies, so the two are
// interchangeable from internal/identify's point of view.
func (c *Client) ChatJSON(ctx context.Context, prompt string) (map[string]any, error) {
	reqBody, err := json.Marshal(generateRequest{
		Contents:         []content{{Parts: []part{{Text: prompt}}}},
		GenerationConfig: generationConfig{ResponseMimeType: "application/json"},
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling gemini request: %w", err)
	}

	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", c.baseURL, c.model, c.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("building gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	var gr generateResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &gr); err != nil {
		return nil, err
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("gemini response had no candidates")
	}

	text := gr.Candidates[0].Content.Parts[0].Text
	var result map[string]any
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("parsing gemini JSON content %q: %w", text, err)
	}
	return result, nil
}
