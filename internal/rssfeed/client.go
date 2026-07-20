// Package rssfeed fetches and parses a raw RSS 2.0 feed URL — the first
// raw-XML-parsing code in this codebase (every other external client speaks
// JSON; see internal/httpx). Deliberately a stateless function, not a
// Config+Client struct: unlike this project's usual house HTTP client
// pattern, there is no persistent per-feed config (base URL, API key) to
// hold — each call is handed a full feed URL and fetches it directly. See
// internal/rssfeeds for the separate row-config store that persists which
// feed URLs are configured; this package only knows how to fetch one.
//
// RSS 2.0 only — Atom is explicitly out of scope. NZBGeek and virtually
// every indexer feed this is built for is RSS 2.0, and doubling the parser
// for a shape nothing concrete needs would be premature.
package rssfeed

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/labbersanon/sakms/internal/httpx"
)

// Item is one parsed <item> from an RSS 2.0 feed.
type Item struct {
	Title           string
	Link            string
	PubDate         string
	EnclosureURL    string
	EnclosureLength int64 // bytes, 0 if absent
}

// rss is the raw RSS 2.0 XML shape this package decodes — only the fields
// this program actually uses (title/link/pubDate/enclosure); anything else
// in a real-world feed is silently ignored by encoding/xml.
type rss struct {
	Channel struct {
		Items []struct {
			Title     string `xml:"title"`
			Link      string `xml:"link"`
			PubDate   string `xml:"pubDate"`
			Enclosure struct {
				URL    string `xml:"url,attr"`
				Length int64  `xml:"length,attr"`
			} `xml:"enclosure"`
		} `xml:"item"`
	} `xml:"channel"`
}

// FetchItems fetches feedURL and parses it as an RSS 2.0 document, returning
// every <item> in feed order. The response body is capped at
// httpx.MaxResponseBodySize, the same defensive limit every other external
// client in this program reads under, against a misbehaving or malicious
// feed returning an unbounded payload.
func FetchItems(ctx context.Context, httpClient *http.Client, feedURL string) ([]Item, error) {
	parsed, err := url.Parse(feedURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("rssfeed: unsupported URL scheme %q (only http/https allowed)", parsed.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", feedURL, err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", feedURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned status %d", feedURL, resp.StatusCode)
	}

	var doc rss
	if err := xml.NewDecoder(io.LimitReader(resp.Body, httpx.MaxResponseBodySize)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing rss feed %s: %w", feedURL, err)
	}

	items := make([]Item, len(doc.Channel.Items))
	for i, it := range doc.Channel.Items {
		items[i] = Item{
			Title:           it.Title,
			Link:            it.Link,
			PubDate:         it.PubDate,
			EnclosureURL:    it.Enclosure.URL,
			EnclosureLength: it.Enclosure.Length,
		}
	}
	return items, nil
}
