package identify

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/labbersanon/sakms/internal/httpx"
)

const maxPageBytes = 512 * 1024

var (
	titleTagRe    = regexp.MustCompile(`(?is)<title[^>]*>([^<]*)</title>`)
	ogTitleRe     = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:title["'][^>]+content=["']([^"']*)["']`)
	ogDescRe      = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:description["'][^>]+content=["']([^"']*)["']`)
	metaDescRe    = regexp.MustCompile(`(?is)<meta[^>]+name=["']description["'][^>]+content=["']([^"']*)["']`)
	tagStripRe    = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceCollapse = regexp.MustCompile(`\s+`)
)

// PageSnippet is trimmed page text fed to ExtractFromURL.
type PageSnippet struct {
	URL         string
	Title       string
	Description string
	BodyText    string
}

// FetchPageSnippet fetches a public http(s) URL and extracts title, description,
// and a truncated text body for AI parsing. Rejects private/internal targets.
func FetchPageSnippet(ctx context.Context, client *http.Client, rawURL string) (PageSnippet, error) {
	u, err := parsePublicHTTPURL(rawURL)
	if err != nil {
		return PageSnippet{}, err
	}
	if err := validatePublicHTTPURL(ctx, u); err != nil {
		return PageSnippet{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return PageSnippet{}, err
	}
	req.Header.Set("User-Agent", "sakms-adult-url-resolve/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	body, err := httpx.DoJSONBytes(publicRedirectClient(ctx, client), req, maxPageBytes)
	if err != nil {
		return PageSnippet{}, fmt.Errorf("fetching page: %w", err)
	}
	html := string(body)
	snippet := PageSnippet{URL: u.String()}
	if m := ogTitleRe.FindStringSubmatch(html); len(m) > 1 {
		snippet.Title = strings.TrimSpace(m[1])
	}
	if snippet.Title == "" {
		if m := titleTagRe.FindStringSubmatch(html); len(m) > 1 {
			snippet.Title = strings.TrimSpace(m[1])
		}
	}
	if m := ogDescRe.FindStringSubmatch(html); len(m) > 1 {
		snippet.Description = strings.TrimSpace(m[1])
	}
	if snippet.Description == "" {
		if m := metaDescRe.FindStringSubmatch(html); len(m) > 1 {
			snippet.Description = strings.TrimSpace(m[1])
		}
	}
	text := tagStripRe.ReplaceAllString(html, " ")
	text = spaceCollapse.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	if len(text) > 8000 {
		text = text[:8000]
	}
	snippet.BodyText = text
	return snippet, nil
}

// Claude 2026-08-12: validate redirect targets for pasted Adult resolve URLs.
// Reason: the initial host check is not enough when a public URL redirects to a
// private/internal address.
// Troubleshooting: prevents SSRF through HTTP 30x redirects while preserving the
// shared outbound client's timeout/transport settings.
// Review if: arbitrary URL fetching moves to a dedicated safe HTTP client.
func publicRedirectClient(ctx context.Context, base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	c := *base
	previous := base.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if err := validatePublicHTTPURL(ctx, req.URL); err != nil {
			return err
		}
		if previous != nil {
			return previous(req, via)
		}
		return nil
	}
	return &c
}

func validatePublicHTTPURL(ctx context.Context, u *url.URL) error {
	if u == nil || u.Host == "" {
		return fmt.Errorf("invalid URL")
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("URL must use http or https")
	}
	return validatePublicHost(ctx, u.Hostname())
}
