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

// Claude 2026-10-21: Newznab filename + poster search, NZB password (no HTML).
// Reason: ngPost title/password live in NZB meta. Stem q= misses when the
//   indexer stores the real title. Poster is nZEDb advanced fromname
//   (t=search&poster=), then q=From. Do not GET /details or geekseek.
// Troubleshooting: o2cli grep "nzb-password"; pw=true without crcs → decrypt
//   miss. Review if: NZBGeek adds poster to caps supportedParams.
// Related files: internal/api/nzbpassword.go, namefix.go, nzbpw.go

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

// Query is one Newznab password/title lookup. Filename is the obfuscated
// archive stem (t=search&q=). Poster is the NNTP From (t=search&poster=).
type Query struct {
	Filename string
	Poster   string
}

type searchReq struct {
	q           string
	poster      string
	allowSingle bool
}

// Lookup searches each indexer and returns the first NZB that carries a
// password or a non-empty title. HTML paths are never requested.
func (c *Client) Lookup(ctx context.Context, indexers []Indexer, q Query) (Hit, bool) {
	q.Filename = strings.TrimSpace(q.Filename)
	q.Poster = strings.TrimSpace(q.Poster)
	if (q.Filename == "" && q.Poster == "") || len(indexers) == 0 {
		return Hit{}, false
	}
	if len(indexers) > maxIndexers {
		indexers = indexers[:maxIndexers]
	}
	for _, idx := range indexers {
		hit, ok := c.lookupOne(ctx, idx, q)
		if ok {
			return hit, true
		}
	}
	return Hit{}, false
}

func (c *Client) lookupOne(ctx context.Context, idx Indexer, q Query) (Hit, bool) {
	idx.Endpoint = NormalizeEndpoint(idx.Endpoint)
	needles := []string{q.Filename, q.Poster}
	// Claude 2026-10-21: poster after stem miss; never browse unfiltered.
	// Reason: ngPost title/password are on the NZB keyed by From
	//   (nZEDb searchadvposter / fromname). NZBGeek caps list q,group
	//   only, so poster= may be ignored and empty-q would be latest-N.
	//   pickItems then requires the From or stem in the RSS blob.
	// Troubleshooting: nzb-password q= miss poster= hit → stem not
	//   indexed. Review if: an indexer documents a different poster key.
	attempts := make([]searchReq, 0, 3)
	if q.Filename != "" {
		attempts = append(attempts, searchReq{q: q.Filename, allowSingle: true})
	}
	if q.Poster != "" {
		attempts = append(attempts, searchReq{poster: q.Poster})
		if q.Poster != q.Filename {
			attempts = append(attempts, searchReq{q: q.Poster})
		}
	}
	for _, req := range attempts {
		items, err := c.search(ctx, idx, req)
		if err != nil {
			log.Printf("usenetsearch: nzb-password indexer=%s search err=%v", idx.Name, err)
			continue
		}
		picked := pickItems(items, needles, req.allowSingle)
		for i, it := range picked {
			if i >= maxNZBsPerIndexer {
				break
			}
			hit, ok := c.fetchHit(ctx, idx, it)
			if ok {
				return hit, true
			}
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

func pickItems(items []rssItem, needles []string, allowSingle bool) []rssItem {
	if len(items) == 0 {
		return nil
	}
	var matched []rssItem
	for _, it := range items {
		for _, n := range needles {
			if it.matches(n) {
				matched = append(matched, it)
				break
			}
		}
	}
	if len(matched) > 0 {
		if len(matched) > maxSearchItems {
			return matched[:maxSearchItems]
		}
		return matched
	}
	if allowSingle && len(items) == 1 {
		return items
	}
	return nil
}

func (c *Client) search(ctx context.Context, idx Indexer, req searchReq) ([]rssItem, error) {
	u, err := url.Parse(idx.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("indexer endpoint")
	}
	q := strings.TrimSpace(req.q)
	poster := strings.TrimSpace(req.poster)
	if q == "" && poster == "" {
		return nil, fmt.Errorf("empty search")
	}
	vals := u.Query()
	vals.Set("t", "search")
	vals.Set("apikey", idx.APIKey)
	if q != "" {
		vals.Set("q", q)
	}
	if poster != "" {
		vals.Set("poster", poster)
	}
	vals.Set("o", "xml")
	vals.Set("extended", "1")
	vals.Set("limit", fmt.Sprintf("%d", maxSearchItems))
	u.RawQuery = vals.Encode()
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	if err := parseAPIError(body); err != nil {
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

type apiError struct {
	XMLName     xml.Name `xml:"error"`
	Code        string   `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

func parseAPIError(body []byte) error {
	var e apiError
	if err := xml.Unmarshal(body, &e); err != nil || e.XMLName.Local != "error" {
		return nil
	}
	desc := strings.TrimSpace(e.Description)
	if desc == "" {
		desc = strings.TrimSpace(e.Code)
	}
	if desc == "" {
		desc = "error"
	}
	return fmt.Errorf("newznab: %s", desc)
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
	if err := parseAPIError(body); err != nil {
		return err
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
