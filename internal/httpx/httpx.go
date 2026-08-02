// Package httpx holds small shared HTTP helpers used across every external
// service client in this program.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// WrapTransportError normalizes a client.Do failure for host into a safe,
// loggable error.
//
// SECURITY: a transport failure comes back as *url.Error, whose Error()
// renders the FULL request URL — which for these clients routinely carries a
// credential (TMDB's api_key query parameter, an indexer's download link).
// Callers stringify this error into logs, DB columns and API responses, so
// the URL is stripped here rather than trusted to every caller. Op + the
// inner cause keep it diagnosable, and host is already named separately.
// Every hand-rolled http.Client caller in this program (not just DoJSON)
// should route its transport-error handling through this function rather
// than wrapping client.Do's err directly.
func WrapTransportError(host string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("request to %s failed: %s: %w", host, ue.Op, ue.Err)
	}
	return fmt.Errorf("request to %s failed: %w", host, err)
}

// DoJSON executes req via client, requires a 2xx status, and decodes the
// response body (capped at maxBytes) as JSON into out. This is the shared
// request/status-check/decode skeleton every external client in this
// program otherwise duplicated by hand.
func DoJSON(client *http.Client, req *http.Request, maxBytes int64, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return WrapTransportError(req.URL.Host, err)
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

// DoJSONBytes is DoJSON's byte-returning sibling: same transport-error
// wrapping and the same 2xx-only contract, but it returns the size-capped raw
// body instead of decoding it. It exists for internal/tmdb's response cache,
// which must hold the bytes and unmarshal them per hit.
//
// DELIBERATELY NOT the shared implementation of DoJSON. DoJSONAllowEmpty
// relies on json.Decoder.Decode returning exactly io.EOF for a zero-byte body
// (see its doc); json.Unmarshal on the same input returns a *json.SyntaxError
// ("unexpected end of JSON input") instead, so re-expressing DoJSON as
// DoJSONBytes + json.Unmarshal would silently break DoJSONAllowEmpty's
// documented empty-body tolerance for every DELETE/204 caller. The few
// duplicated status-check lines are the deliberate price of not touching that.
func DoJSONBytes(client *http.Client, req *http.Request, maxBytes int64) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, WrapTransportError(req.URL.Host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned status %d", req.URL.Host, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("reading response from %s: %w", req.URL.Host, err)
	}
	return body, nil
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
