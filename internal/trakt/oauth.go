package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/httpx"
)

// oobRedirectURI is the standard OAuth2 "out-of-band" redirect URI apps
// register for flows with no real browser callback (device code flow, plus
// the classic-token refresh grant, which Trakt requires a redirect_uri on
// even though nothing ever redirects there). Not a secret, not
// operator-configurable — it's the same fixed value every Trakt device-flow
// integration (and Trakt's own docs) uses.
const oobRedirectURI = "urn:ietf:wg:oauth:2.0:oob"

// DeviceCode is Trakt's response to POST /oauth/device/code: the code pair
// the operator must approve at VerificationURL by entering UserCode.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"` // seconds the device_code remains valid
	Interval        int    `json:"interval"`   // minimum seconds between poll attempts
}

// Token is a successful oauth/device/token or oauth/token (refresh)
// response. CreatedAt+ExpiresIn is Trakt's own wire shape (a Unix
// timestamp plus a duration); Session/Store work in terms of the computed
// absolute ExpiresAt instead — see rawToken.expiresAt.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

type rawToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	CreatedAt    int64  `json:"created_at"`
}

func (r rawToken) expiresAt() time.Time {
	return time.Unix(r.CreatedAt, 0).Add(time.Duration(r.ExpiresIn) * time.Second).UTC()
}

// Sentinel errors for PollDeviceToken's documented non-2xx statuses (Trakt
// developer docs, corroborated by github.com/BrenekH/go-traktdeviceauth's
// identical status mapping): 400 pending, 404 not found, 409 already used,
// 410 expired, 418 denied, 429 slow down. A caller's poll loop switches on
// errors.Is against these to distinguish "keep polling" (ErrAuthorizationPending,
// ErrSlowDown) from terminal failure (everything else).
var (
	ErrAuthorizationPending = errors.New("trakt: authorization pending")
	ErrSlowDown             = errors.New("trakt: polling too fast, slow down")
	ErrDeviceCodeExpired    = errors.New("trakt: device code expired, restart the flow")
	ErrDeviceCodeDenied     = errors.New("trakt: user denied the device code")
	ErrDeviceCodeNotFound   = errors.New("trakt: invalid device code")
	ErrDeviceCodeUsed       = errors.New("trakt: device code already used")
)

func statusToPollError(status int) error {
	switch status {
	case http.StatusBadRequest:
		return ErrAuthorizationPending
	case http.StatusNotFound:
		return ErrDeviceCodeNotFound
	case http.StatusConflict:
		return ErrDeviceCodeUsed
	case http.StatusGone:
		return ErrDeviceCodeExpired
	case 418: // net/http has no named constant for 418 (I'm a teapot)
		return ErrDeviceCodeDenied
	case http.StatusTooManyRequests:
		return ErrSlowDown
	default:
		return fmt.Errorf("trakt: unexpected status %d polling for device token", status)
	}
}

// postJSON POSTs body as JSON and decodes a 2xx response into out via
// httpx.DoJSON. Returns the raw status code alongside any httpx error so
// callers needing non-2xx-but-meaningful statuses (PollDeviceToken) can
// still tell those apart from a real transport failure.
func (c *Client) postJSON(ctx context.Context, path string, body, out any) (int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("encoding request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return 0, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request to %s failed: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, nil // caller interprets; not a transport error
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, httpx.MaxResponseBodySize)).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("decoding response from %s: %w", req.URL.Host, err)
	}
	return resp.StatusCode, nil
}

// RequestDeviceCode starts the device-code flow: POST /oauth/device/code
// with this Client's configured client_id. The caller shows the operator
// UserCode + VerificationURL, then polls PollDeviceToken using DeviceCode
// and Interval until the operator approves (or the code expires).
func (c *Client) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	var dc DeviceCode
	status, err := c.postJSON(ctx, "/oauth/device/code", map[string]string{"client_id": c.cfg.ClientID}, &dc)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("trakt: unexpected status %d requesting device code", status)
	}
	return &dc, nil
}

// PollDeviceToken makes one attempt at POST /oauth/device/token for the
// given device code. Returns the Token on success (200); on any documented
// non-2xx status, returns one of the sentinel errors above (never both a
// Token and an error). Callers implementing their own poll loop should sleep
// at least DeviceCode.Interval seconds between calls, and increase the
// interval on ErrSlowDown — see PollUntilToken for a ready-made loop that
// already does this.
func (c *Client) PollDeviceToken(ctx context.Context, deviceCode string) (*Token, error) {
	var raw rawToken
	status, err := c.postJSON(ctx, "/oauth/device/token", map[string]string{
		"code":          deviceCode,
		"client_id":     c.cfg.ClientID,
		"client_secret": c.cfg.ClientSecret,
	}, &raw)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, statusToPollError(status)
	}
	return &Token{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, ExpiresAt: raw.expiresAt()}, nil
}

// PollUntilToken blocks, polling PollDeviceToken at dc.Interval (minimum 1s)
// until the operator approves, the device code expires (dc.ExpiresIn), the
// context is cancelled, or a terminal error (denied/not-found/already-used)
// occurs. On ErrSlowDown it backs off by doubling the wait once, per Trakt's
// documented guidance to slow down rather than treat it as fatal.
//
// This is a convenience for callers that want a single blocking call (e.g.
// a background goroutine backing a "waiting for approval" Settings UI
// state); a caller that needs to report incremental progress (e.g. push
// "still waiting" over SSE) should drive PollDeviceToken directly instead.
func (c *Client) PollUntilToken(ctx context.Context, dc *DeviceCode) (*Token, error) {
	interval := time.Duration(dc.Interval) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
		if time.Now().After(deadline) {
			return nil, ErrDeviceCodeExpired
		}
		tok, err := c.PollDeviceToken(ctx, dc.DeviceCode)
		if err == nil {
			return tok, nil
		}
		switch {
		case errors.Is(err, ErrAuthorizationPending):
			timer.Reset(interval)
		case errors.Is(err, ErrSlowDown):
			interval *= 2
			timer.Reset(interval)
		default:
			return nil, err
		}
	}
}

// RefreshToken exchanges a refresh token for a new access+refresh token pair
// via POST /oauth/token (grant_type=refresh_token). Trakt's refresh tokens
// are single-use — the returned Token's RefreshToken must replace the one
// passed in, which callers (see Session.ensureFreshToken) persist alongside
// the new access token.
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (*Token, error) {
	var raw rawToken
	status, err := c.postJSON(ctx, "/oauth/token", map[string]string{
		"refresh_token": refreshToken,
		"client_id":     c.cfg.ClientID,
		"client_secret": c.cfg.ClientSecret,
		"redirect_uri":  oobRedirectURI,
		"grant_type":    "refresh_token",
	}, &raw)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("trakt: unexpected status %d refreshing token", status)
	}
	return &Token{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, ExpiresAt: raw.expiresAt()}, nil
}
