package predb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultHTTPURL is the default PreDB HTTP provider (see
// docs/usenet-nntp-native.md for the verification record).
const DefaultHTTPURL = "https://api.predb.net/"

// MinHTTPInterval spaces requests at or under the provider's published
// 30-requests-per-minute limit.
const MinHTTPInterval = 2 * time.Second

const maxHTTPBody = 2 << 20

// HTTPClient is the api.predb.net-style adapter: GET {base}?type=pre&q=<name>
// returning {"status":"success","data":[{"release":…,"size":<MB>,"files":…,
// "section":…,"pretime":…,"status":…}]}. The query is a loose match, so the
// result is filtered to an exact (case-insensitive) release name client-side.
type HTTPClient struct {
	BaseURL string
	Client  *http.Client
	// Interval overrides MinHTTPInterval (tests).
	Interval time.Duration

	mu   sync.Mutex
	last time.Time
}

// NewHTTPClient returns an adapter for baseURL (empty = inert: Lookup reports
// ErrDisabled).
func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{BaseURL: strings.TrimSpace(baseURL), Client: &http.Client{Timeout: 15 * time.Second}}
}

// ErrDisabled is returned when no provider URL is configured.
var ErrDisabled = errors.New("predb: http provider not configured")

type httpResponse struct {
	Status string `json:"status"`
	Data   []struct {
		Release string  `json:"release"`
		Section string  `json:"section"`
		Files   int     `json:"files"`
		Size    float64 `json:"size"`
		PreTime int64   `json:"pretime"`
		Status  int     `json:"status"`
	} `json:"data"`
}

// wait enforces the minimum spacing between requests.
func (h *HTTPClient) wait(ctx context.Context) error {
	iv := h.Interval
	if iv <= 0 {
		iv = MinHTTPInterval
	}
	h.mu.Lock()
	next := h.last.Add(iv)
	now := time.Now()
	if next.Before(now) {
		next = now
	}
	h.last = next
	h.mu.Unlock()
	d := time.Until(next)
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// Lookup resolves name to a record. found=false with a nil error is a
// confirmed miss (safe to cache); any error (transport, non-200, malformed
// JSON) means "no answer" and must not be cached or acted on.
func (h *HTTPClient) Lookup(ctx context.Context, name string) (Entry, bool, error) {
	if h == nil || h.BaseURL == "" {
		return Entry{}, false, ErrDisabled
	}
	u, err := url.Parse(h.BaseURL)
	if err != nil {
		return Entry{}, false, fmt.Errorf("predb: bad url: %w", err)
	}
	q := u.Query()
	q.Set("type", "pre")
	q.Set("q", name)
	u.RawQuery = q.Encode()
	if err := h.wait(ctx); err != nil {
		return Entry{}, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Entry{}, false, err
	}
	req.Header.Set("Accept", "application/json")
	cl := h.Client
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		return Entry{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Entry{}, false, fmt.Errorf("predb: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if err != nil {
		return Entry{}, false, err
	}
	var out httpResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return Entry{}, false, fmt.Errorf("predb: malformed response: %w", err)
	}
	if !strings.EqualFold(out.Status, "success") {
		return Entry{}, false, fmt.Errorf("predb: provider status %q", out.Status)
	}
	for _, d := range out.Data {
		if strings.EqualFold(d.Release, name) {
			return Entry{
				Title:     d.Release,
				SizeBytes: int64(d.Size * (1 << 20)),
				Files:     d.Files,
				Category:  d.Section,
				PreAt:     d.PreTime,
				Nuked:     d.Status == 1,
			}, true, nil
		}
	}
	return Entry{}, false, nil
}
