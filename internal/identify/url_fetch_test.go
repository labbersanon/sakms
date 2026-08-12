package identify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestFetchPageSnippetExtractsBasicHTML(t *testing.T) {
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "93.184.216.34" {
				t.Fatalf("unexpected host %q", r.URL.Host)
			}
			return htmlResponse(http.StatusOK, "", `<html><head><title>Scene Page</title><meta name="description" content="Studio - Title"></head><body><h1>Body text</h1></body></html>`), nil
		}),
	}

	snippet, err := FetchPageSnippet(context.Background(), client, "http://93.184.216.34/page")
	if err != nil {
		t.Fatal(err)
	}
	if snippet.Title != "Scene Page" {
		t.Fatalf("title = %q, want Scene Page", snippet.Title)
	}
	if snippet.Description != "Studio - Title" {
		t.Fatalf("description = %q, want Studio - Title", snippet.Description)
	}
	if !strings.Contains(snippet.BodyText, "Body text") {
		t.Fatalf("body text = %q, want extracted body text", snippet.BodyText)
	}
}

// Claude 2026-08-12: lock redirect SSRF behavior for Adult pasted URL fetches.
// Reason: URL resolve accepts arbitrary operator-pasted pages, so redirect hops
// need the same public-target validation as the original URL.
// Troubleshooting: a public landing page redirecting to loopback must fail
// before the private request is issued.
// Review if: FetchPageSnippet stops following redirects entirely.
func TestFetchPageSnippetRejectsRedirectToPrivateAddress(t *testing.T) {
	requests := 0
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests++
			if requests > 1 {
				t.Fatalf("private redirect target was requested: %s", r.URL.String())
			}
			return htmlResponse(http.StatusFound, "http://127.0.0.1/private", ""), nil
		}),
	}

	_, err := FetchPageSnippet(context.Background(), client, "http://93.184.216.34/start")
	if !errors.Is(err, ErrURLNotAllowed) {
		t.Fatalf("error = %v, want ErrURLNotAllowed", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func htmlResponse(status int, location, body string) *http.Response {
	header := http.Header{"Content-Type": []string{"text/html"}}
	if location != "" {
		header.Set("Location", location)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
