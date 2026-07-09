// Package ollama is a minimal client for a local Ollama chat endpoint. It's
// deliberately generic (no filename-parsing or Stash-specific prompts here —
// that domain logic lives in internal/identify, which uses this client).
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/curtiswtaylorjr/sak/internal/httpx"
)

type Client struct {
	baseURL string
	model   string
	http    *http.Client
	sem     chan struct{} // nil = unlimited concurrency
}

func New(baseURL, model string, httpClient *http.Client) *Client {
	return &Client{baseURL: baseURL, model: model, http: httpClient}
}

// NewWithConcurrencyLimit returns a client that allows at most maxConcurrent
// simultaneous ChatJSON calls.
//
// Local LLM inference on one GPU/CPU doesn't parallelize the way network-
// bound calls do — a bounded worker pool sized for network-bound work (e.g.
// 5 concurrent AI-fallback tasks, most of whose steps are DB/web-search
// round-trips) could otherwise fire 5 simultaneous qwen2.5vl:7b calls and
// thrash rather than speed anything up. This is a separate, tighter limit
// specifically for the Ollama client, independent of the caller's own
// worker-pool size.
func NewWithConcurrencyLimit(baseURL, model string, httpClient *http.Client, maxConcurrent int) *Client {
	c := New(baseURL, model, httpClient)
	if maxConcurrent > 0 {
		c.sem = make(chan struct{}, maxConcurrent)
	}
	return c
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Format   string        `json:"format"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

// ChatJSON sends prompt to Ollama in format=json (structured output) mode and
// unmarshals the model's response content as a JSON object.
func (c *Client) ChatJSON(ctx context.Context, prompt string) (map[string]any, error) {
	if c.sem != nil {
		select {
		case c.sem <- struct{}{}:
			defer func() { <-c.sem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	reqBody, err := json.Marshal(chatRequest{
		Model:    c.model,
		Messages: []chatMessage{{Role: "user", Content: prompt}},
		Stream:   false,
		Format:   "json",
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling ollama request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("building ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	var cr chatResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &cr); err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(cr.Message.Content), &result); err != nil {
		return nil, fmt.Errorf("parsing ollama JSON content %q: %w", cr.Message.Content, err)
	}
	return result, nil
}

// Ping checks that a server is actually reachable and speaks Ollama's API,
// without invoking any model — a lightweight alternative to ChatJSON for
// connection-testing purposes (a real chat call would incur an inference
// cost just to check a URL/key).
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("building ollama ping request: %w", err)
	}
	var discard json.RawMessage
	return httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &discard)
}

// NormalizeField cleans a value extracted from an Ollama JSON response.
//
// Qwen's format=json mode sometimes returns the literal STRING "null" (or
// "none"/"unknown"/"n/a", any case) instead of a real JSON null for a field it
// couldn't determine. Those strings are truthy/non-empty, so a naive "is this
// field present" check lets them through. Returns "" (this package's
// convention for "absent") for any of those sentinel strings, nil, or
// whitespace-only input.
func NormalizeField(v any) string {
	var s string
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		s = val
	case float64:
		s = fmt.Sprintf("%v", val)
	default:
		s = fmt.Sprintf("%v", val)
	}
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "null", "none", "unknown", "n/a":
		return ""
	}
	return s
}
