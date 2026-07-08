// Package httpx holds small shared HTTP helpers used across every external
// service client in this program.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// MaxResponseBodySize caps how much of an HTTP response body this program
// will read before giving up — a defensive limit against a misbehaving or
// compromised external service returning an unbounded or malicious payload.
// 10MB is generous for any legitimate REST/JSON response these clients
// expect.
const MaxResponseBodySize = 10 * 1024 * 1024

// MaxResponseBodySizeLarge is for the rare query that is deliberately
// unbounded by the API itself, where MaxResponseBodySize is sized for a
// paginated request and would be too tight.
const MaxResponseBodySizeLarge = 50 * 1024 * 1024

// DoJSON executes req via client, requires a 2xx status, and decodes the
// response body (capped at maxBytes) as JSON into out. This is the shared
// request/status-check/decode skeleton every external client in this
// program otherwise duplicated by hand.
func DoJSON(client *http.Client, req *http.Request, maxBytes int64, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned status %d", req.URL.Host, resp.StatusCode)
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBytes)).Decode(out); err != nil {
		return fmt.Errorf("decoding response from %s: %w", req.URL.Host, err)
	}
	return nil
}

// DoJSONAllowEmpty is DoJSON but tolerates a response with a truly empty
// body — common on a successful DELETE or a 204 No Content. A zero-byte body
// decodes as exactly io.EOF, distinct from *json.SyntaxError (which is what
// malformed-but-non-empty content decodes as) — only io.EOF is tolerated
// here, so a corrupt/unexpected non-empty response still surfaces as a real
// error instead of being silently swallowed.
func DoJSONAllowEmpty(client *http.Client, req *http.Request, maxBytes int64, out any) error {
	err := DoJSON(client, req, maxBytes, out)
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
