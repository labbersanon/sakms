package tmdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/labbersanon/sakms/internal/httpx"
)

// Claude 2026-08-06: TMDB user session (login) for give-back attempts
// Reason: deep-interview-rename-apply-all-giveback-settings
// Troubleshooting: CreateSessionWithLogin fails → check API key + TMDB account.
// Review if: switch to request-token browser approve flow instead of password.

type tokenResponse struct {
	Success      bool   `json:"success"`
	RequestToken string `json:"request_token"`
}

type sessionResponse struct {
	Success   bool   `json:"success"`
	SessionID string `json:"session_id"`
}

// CreateSessionWithLogin creates a TMDB user session_id via request token +
// validate_with_login + create session. Password is not stored.
func (c *Client) CreateSessionWithLogin(ctx context.Context, username, password string) (string, error) {
	var tok tokenResponse
	if err := c.do(ctx, "/authentication/token/new", nil, &tok); err != nil {
		return "", fmt.Errorf("request token: %w", err)
	}
	if !tok.Success || tok.RequestToken == "" {
		return "", fmt.Errorf("request token: empty response")
	}
	body, _ := json.Marshal(map[string]string{
		"username":      username,
		"password":      password,
		"request_token": tok.RequestToken,
	})
	if err := c.doPOST(ctx, "/authentication/token/validate_with_login", nil, body, &tok); err != nil {
		return "", fmt.Errorf("validate login: %w", err)
	}
	if !tok.Success {
		return "", fmt.Errorf("validate login: rejected")
	}
	createBody, _ := json.Marshal(map[string]string{"request_token": tok.RequestToken})
	var sess sessionResponse
	if err := c.doPOST(ctx, "/authentication/session/new", nil, createBody, &sess); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	if !sess.Success || sess.SessionID == "" {
		return "", fmt.Errorf("create session: empty session id")
	}
	return sess.SessionID, nil
}

func (c *Client) doPOST(ctx context.Context, path string, query url.Values, body []byte, out any) error {
	if query == nil {
		query = url.Values{}
	}
	reqQuery := url.Values{}
	for k, vs := range query {
		for _, v := range vs {
			reqQuery.Add(k, v)
		}
	}
	reqQuery.Set("api_key", c.cfg.APIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path+"?"+reqQuery.Encode(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	raw, err := httpx.DoJSONBytes(c.http, req, httpx.MaxResponseBodySize)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decoding response from %s: %w", req.URL.Host, err)
	}
	return nil
}
