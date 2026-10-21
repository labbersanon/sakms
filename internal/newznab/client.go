// Package newznab talks to Newznab indexers over t=search and t=get.
// It does not scrape HTML. API keys and unpack passwords must not be logged.
package newznab

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/httpx"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-21: Newznab filename search + NZB password (no HTML scrape).
// Reason: ngPost title/password live in NZB meta. The operator-configured
//   `newznab` connection is t=search / t=get. Do not GET /details or login.
// Troubleshooting: o2cli grep "nzb-password"; pw=true without crcs → decrypt
//   miss, not a scrape gap. Review if: an indexer requires t=tvsearch only.
// Related files: internal/api/nzbpassword.go, namefix.go

const (
	maxSearchItems    = 5
	maxNZBsPerIndexer = 2
	maxIndexers       = 3
	userAgent         = "sakms-newznab/1"
	minInterval       = time.Second
)

// Indexer is a Newznab API endpoint. APIKey must not be logged.
type Indexer struct {
	Name     string
	Endpoint string
	APIKey   string
}

// Hit is a filename search result. Passwords must not be logged.
type Hit struct {
	Title     string
	Passwords []string
	Indexer   string
}

type Client struct {
	HTTP     *http.Client
	Interval time.Duration

	mu   sync.Mutex
	last time.Time
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) interval() time.Duration {
	if c.Interval > 0 {
		return c.Interval
	}
	return minInterval
}

func (c *Client) throttle(ctx context.Context) error {
	wait := time.Duration(0)
	c.mu.Lock()
	if !c.last.IsZero() {
		wait = c.interval() - time.Since(c.last)
	}
	c.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) mark() {
	c.mu.Lock()
	c.last = time.Now()
	c.mu.Unlock()
}

// Lookup searches q=filename on each indexer and returns the first NZB that
// carries a password or a non-empty title. HTML paths are never requested.
func (c *Client) Lookup(ctx context.Context, indexers []Indexer, filename string) (Hit, bool) {
	filename = strings.TrimSpace(filename)
	if filename == "" || len(indexers) == 0 {
		return Hit{}, false
	}
	if len(indexers) > maxIndexers {
		indexers = indexers[:maxIndexers]
	}
	for _, idx := range indexers {
		hit, ok := c.lookupOne(ctx, idx, filename)
		if ok {
			return hit, true
		}
	}
	return Hit{}, false
}

func (c *Client) lookupOne(ctx context.Context, idx Indexer, filename string) (Hit, bool) {
	idx.Endpoint = NormalizeEndpoint(idx.Endpoint)
	items, err := c.search(ctx, idx, filename)
	if err != nil {
		log.Printf("usenetsearch: nzb-password indexer=%s search err=%v", idx.Name, err)
		return Hit{}, false
	}
	picked := pickItems(items, filename)
	for i, it := range picked {
		if i >= maxNZBsPerIndexer {
			break
		}
		hit, ok := c.fetchHit(ctx, idx, it)
		if ok {
			return hit, true
		}
	}
	return Hit{}, false
}

type rssFeed struct {
	Items []rssItem `xml:"channel>item"`
}

type rssItem struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	GUID        string    `xml:"guid"`
	Enclosure   rssEnc    `xml:"enclosure"`
	Attrs       []rssAttr `xml:"http://www.newznab.com/DTD/2007/feeds/attributes/ attr"`
}

type rssEnc struct {
	URL string `xml:"url,attr"`
}

type rssAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

func (it rssItem) attr(name string) string {
	for _, a := range it.Attrs {
		if strings.EqualFold(a.Name, name) {
			return strings.TrimSpace(a.Value)
		}
	}
	return ""
}

func (it rssItem) matches(stem string) bool {
	stem = strings.ToLower(strings.TrimSpace(stem))
	if stem == "" {
		return false
	}
	blob := strings.ToLower(it.Title + " " + it.Description + " " + it.GUID + " " + it.Link)
	for _, a := range it.Attrs {
		blob += " " + strings.ToLower(a.Value)
	}
	return strings.Contains(blob, stem)
}

func pickItems(items []rssItem, stem string) []rssItem {
	if len(items) == 0 {
		return nil
	}
	var matched []rssItem
	for _, it := range items {
		if it.matches(stem) {
			matched = append(matched, it)
		}
	}
	if len(matched) > 0 {
		if len(matched) > maxSearchItems {
			return matched[:maxSearchItems]
		}
		return matched
	}
	if len(items) == 1 {
		return items
	}
	return nil
}

func (c *Client) search(ctx context.Context, idx Indexer, q string) ([]rssItem, error) {
	u, err := url.Parse(idx.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("indexer endpoint")
	}
	vals := u.Query()
	vals.Set("t", "search")
	vals.Set("apikey", idx.APIKey)
	vals.Set("q", q)
	vals.Set("o", "xml")
	vals.Set("extended", "1")
	vals.Set("limit", fmt.Sprintf("%d", maxSearchItems))
	u.RawQuery = vals.Encode()
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("search xml")
	}
	if len(feed.Items) > maxSearchItems {
		feed.Items = feed.Items[:maxSearchItems]
	}
	return feed.Items, nil
}

func (c *Client) fetchHit(ctx context.Context, idx Indexer, it rssItem) (Hit, bool) {
	pws := mergeUnique(usenet.PasswordsFromTitles([]string{it.Title}), []string{it.attr("password")})
	title := strings.TrimSpace(it.Title)
	nzbURL := strings.TrimSpace(it.Enclosure.URL)
	if nzbURL == "" {
		nzbURL = strings.TrimSpace(it.Link)
	}
	if nzbURL == "" {
		if id := it.attr("guid"); id != "" {
			nzbURL = getURL(idx, id)
		}
	}
	if nzbURL != "" {
		nzb, dnzb, err := usenet.FetchNZB(ctx, c.httpClient(), nzbURL)
		if err != nil {
			log.Printf("usenetsearch: nzb-password indexer=%s nzb err=%v", idx.Name, err)
		} else {
			pws = mergeUnique(usenet.PasswordsFromNZB(dnzb, nzb, it.Title), pws)
			if t := usenet.TitleFromNZB(dnzb, nzb, it.Title); t != "" {
				title = t
			}
		}
	}
	if title == "" && len(pws) == 0 {
		return Hit{}, false
	}
	return Hit{Title: title, Passwords: pws, Indexer: idx.Name}, true
}

func getURL(idx Indexer, id string) string {
	u, err := url.Parse(idx.Endpoint)
	if err != nil {
		return ""
	}
	vals := u.Query()
	vals.Set("t", "get")
	vals.Set("apikey", idx.APIKey)
	vals.Set("id", id)
	u.RawQuery = vals.Encode()
	return u.String()
}

func mergeUnique(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range [][]string{a, b} {
		for _, s := range g {
			s = strings.TrimSpace(s)
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// NormalizeEndpoint turns a Newznab base (site origin or /api URL) into the
// API endpoint used for t=search / t=caps. Query strings are dropped so an
// apikey never stays in the stored URL.
func NormalizeEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return raw
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/api"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

type capsError struct {
	XMLName     xml.Name `xml:"error"`
	Code        string   `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

// Caps is the Test-connection ping: t=caps, never HTML login/details.
func (c *Client) Caps(ctx context.Context, idx Indexer) error {
	idx.Endpoint = NormalizeEndpoint(idx.Endpoint)
	u, err := url.Parse(idx.Endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("indexer endpoint")
	}
	vals := u.Query()
	vals.Set("t", "caps")
	vals.Set("apikey", idx.APIKey)
	vals.Set("o", "xml")
	u.RawQuery = vals.Encode()
	body, err := c.get(ctx, u)
	if err != nil {
		return err
	}
	var capErr capsError
	if err := xml.Unmarshal(body, &capErr); err == nil && capErr.XMLName.Local == "error" {
		desc := strings.TrimSpace(capErr.Description)
		if desc == "" {
			desc = strings.TrimSpace(capErr.Code)
		}
		if desc == "" {
			desc = "error"
		}
		return fmt.Errorf("newznab: %s", desc)
	}
	if !bytes.Contains(body, []byte("<caps")) {
		return fmt.Errorf("not a Newznab caps response")
	}
	return nil
}

func (c *Client) get(ctx context.Context, u *url.URL) ([]byte, error) {
	if err := c.throttle(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	c.mark()
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, httpx.WrapTransportError(u.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned status %d", u.Host, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, httpx.MaxResponseBodySize))
	if err != nil {
		return nil, httpx.WrapTransportError(u.Host, err)
	}
	return body, nil
}
