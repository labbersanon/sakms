// Package newznab talks to Newznab indexers over t=search and t=get.
// It does not scrape HTML. API keys and unpack passwords must not be logged.
package newznab

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/httpx"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-21: Newznab grab search (t=search/t=get) plus leftover
//   filename lookup used only by tests.
// Reason: the Settings `newznab` host is an alternate Usenet grab indexer,
//   not a password engine. Passwords are not recoverable from NZBGeek.
// Troubleshooting: grab results empty → caps omit cat; enclosure/link missing.
// Review if: NZBGeek grows tvsearch/movie id params we should send.
// Related files: internal/api/newznabgrab.go, frontend Usenet.tsx

const (
	maxSearchItems     = 5
	maxGrabSearchItems = 50
	maxNZBsPerIndexer  = 2
	maxIndexers        = 3
	maxBrowsePages     = 5
	maxBrowseLimit     = 100
	maxBrowseNZBs      = 4
	browseSizeTol      = 0.15
	userAgent          = "sakms-newznab/1"
	minInterval        = time.Second
	// catalogPageSize is NZB.life's max t=search limit (1–100, default 100).
	catalogPageSize = 100
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

// Release is one grab-search hit. DownloadURL is an NZB enclosure or t=get.
// Passwords are not a grab identity and are not populated here.
type Release struct {
	GUID        string
	Title       string
	Indexer     string
	Size        int64
	DownloadURL string
	PublishDate string
	Categories  []int
}

type Client struct {
	HTTP     *http.Client
	Interval time.Duration

	mu     sync.Mutex
	last   map[string]time.Time
	capsMu sync.Mutex
	caps   map[string]capsInfo
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

// Claude 2026-10-23: throttle per indexer host, not process-wide.
// Reason: NZBGeek and NZBFinder are independent APIs; a shared last-call
// timestamp serialized t=search by minInterval even when both hosts are
// configured. Same-host spacing stays.
// Troubleshooting: host 2 always ~1s behind host 1 → last is still one Time.
// Review if: NZB.life documents a stricter per-key rate than minInterval.
func (c *Client) throttle(ctx context.Context, host string) error {
	wait := time.Duration(0)
	c.mu.Lock()
	if t, ok := c.last[host]; ok {
		wait = c.interval() - time.Since(t)
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

func (c *Client) mark(host string) {
	c.mu.Lock()
	if c.last == nil {
		c.last = map[string]time.Time{}
	}
	c.last[host] = time.Now()
	c.mu.Unlock()
}

// Query is one Newznab password/title lookup. Filename is the obfuscated
// archive stem (t=search&q=). Poster is the NNTP From (t=search&poster=).
// Group/PostedAt/SizeBytes drive a bounded recent-index browse when q= and
// poster= miss (NZBGeek caps are q,group only and omit poster from RSS).
type Query struct {
	Filename  string
	Poster    string
	Group     string
	PostedAt  int64
	SizeBytes int64
}

type searchReq struct {
	q           string
	poster      string
	group       string
	cats        []int
	maxage      int
	offset      int
	limit       int
	allowSingle bool
	// catalog skips the empty-q gate so t=search can be cat-only.
	catalog bool
}

// SearchGrab runs t=search for a title and returns NZB download URLs.
// It does not fetch NZBs and does not scrape HTML.
func (c *Client) SearchGrab(ctx context.Context, idx Indexer, query string, cats []int) ([]Release, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	idx.Endpoint = NormalizeEndpoint(idx.Endpoint)
	items, err := c.search(ctx, idx, searchReq{q: query, cats: cats, limit: maxGrabSearchItems})
	if err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(items))
	for _, it := range items {
		rel, ok := grabReleaseFromItem(idx, it)
		if !ok {
			continue
		}
		out = append(out, rel)
	}
	return FilterGrabReleases(query, out), nil
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
	var needles []string
	if q.Filename != "" {
		needles = append(needles, q.Filename)
	}
	if q.Poster != "" && q.Poster != q.Filename {
		needles = append(needles, q.Poster)
	}
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
		if len(picked) > maxNZBsPerIndexer {
			picked = picked[:maxNZBsPerIndexer]
		}
		for _, it := range picked {
			if hit, ok := c.fetchHit(ctx, idx, it, nil); ok {
				return hit, true
			}
		}
	}
	return c.lookupBrowse(ctx, idx, q, needles)
}

// Claude 2026-10-21: NZBGeek cannot search poster or the hash filename.
// Reason: caps q,group so poster= is latest-N; browse group+maxage, t=get, NZB stem. No HTML.
// Troubleshooting: browse skip group_not_listed → caps omits the NNTP group;
// cands=0 → size/group mismatch; nzb=N hit=0 → no hash.
// Review if: NZBGeek adds alt.binaries.x264 or poster to caps.
func (c *Client) lookupBrowse(ctx context.Context, idx Indexer, q Query, needles []string) (Hit, bool) {
	group := strings.TrimSpace(q.Group)
	if group == "" || q.SizeBytes <= 0 || len(needles) == 0 {
		return Hit{}, false
	}
	info, err := c.capsInfo(ctx, idx)
	if err != nil {
		log.Printf("usenetsearch: nzb-password indexer=%s browse skip group=%s reason=caps err=%v", idx.Name, group, err)
		return Hit{}, false
	}
	if !groupInCaps(info, group) {
		log.Printf("usenetsearch: nzb-password indexer=%s browse skip group=%s reason=group_not_listed n=%d",
			idx.Name, group, len(info.Groups))
		return Hit{}, false
	}
	maxage := browseMaxAgeDays(q.PostedAt)
	var nzbTried, seenItems, cands int
	for page := 0; page < maxBrowsePages; page++ {
		items, err := c.search(ctx, idx, searchReq{
			group:  group,
			maxage: maxage,
			offset: page * maxBrowseLimit,
			limit:  maxBrowseLimit,
		})
		if err != nil {
			log.Printf("usenetsearch: nzb-password indexer=%s browse err=%v", idx.Name, err)
			break
		}
		if len(items) == 0 {
			break
		}
		seenItems += len(items)
		for _, it := range items {
			if !sizeClose(it.attr("size"), q.SizeBytes) {
				continue
			}
			if q.PostedAt > 0 {
				if ts := usenetDateUnix(it.attr("usenetdate")); ts > 0 {
					delta := ts - q.PostedAt
					if delta < 0 {
						delta = -delta
					}
					if delta > 48*3600 {
						continue
					}
				}
			}
			cands++
			if nzbTried >= maxBrowseNZBs {
				continue
			}
			nzbTried++
			if hit, ok := c.fetchHit(ctx, idx, it, needles); ok {
				log.Printf("usenetsearch: nzb-password indexer=%s browse group=%s maxage=%d pages=%d items=%d cands=%d nzb=%d hit=1",
					idx.Name, group, maxage, page+1, seenItems, cands, nzbTried)
				return hit, true
			}
		}
		if len(items) < maxBrowseLimit {
			break
		}
	}
	log.Printf("usenetsearch: nzb-password indexer=%s browse group=%s maxage=%d items=%d cands=%d nzb=%d hit=0",
		idx.Name, group, maxage, seenItems, cands, nzbTried)
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
	PubDate     string    `xml:"pubDate"`
	Enclosure   rssEnc    `xml:"enclosure"`
	Attrs       []rssAttr `xml:"http://www.newznab.com/DTD/2007/feeds/attributes/ attr"`
	Attrs2010   []rssAttr `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ attr"`
}

type rssEnc struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
}

type rssAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

func (it rssItem) allAttrs() []rssAttr {
	out := make([]rssAttr, 0, len(it.Attrs)+len(it.Attrs2010))
	out = append(out, it.Attrs...)
	out = append(out, it.Attrs2010...)
	return out
}

func (it rssItem) attr(name string) string {
	for _, a := range it.allAttrs() {
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
	for _, a := range it.allAttrs() {
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
	group := strings.TrimSpace(req.group)
	parts := make([]string, 0, len(req.cats))
	for _, cat := range req.cats {
		if cat > 0 {
			parts = append(parts, strconv.Itoa(cat))
		}
	}
	// Claude 2026-10-23: catalog t=search may omit q.
	// Reason: NZB.life pages cat=2000 without q; q=* is a literal
	//   asterisk search. Grab still requires a query.
	// Troubleshooting: empty search on catalog → q=* first request.
	// Review if: NZB.life documents a real latest-in-cat query.
	if req.catalog {
		if len(parts) == 0 {
			return nil, fmt.Errorf("catalog category required")
		}
	} else if q == "" && poster == "" && (group == "" || req.maxage <= 0) {
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
	if group != "" {
		vals.Set("group", group)
	}
	if req.maxage > 0 {
		vals.Set("maxage", strconv.Itoa(req.maxage))
	}
	if req.offset > 0 {
		vals.Set("offset", strconv.Itoa(req.offset))
	}
	if len(parts) > 0 {
		vals.Set("cat", strings.Join(parts, ","))
	}
	limit := req.limit
	if limit <= 0 {
		limit = maxSearchItems
	}
	vals.Set("o", "xml")
	vals.Set("extended", "1")
	vals.Set("limit", strconv.Itoa(limit))
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
	if len(feed.Items) > limit {
		feed.Items = feed.Items[:limit]
	}
	return feed.Items, nil
}

func (c *Client) fetchHit(ctx context.Context, idx Indexer, it rssItem, needles []string) (Hit, bool) {
	pws := mergeUnique(usenet.PasswordsFromTitles([]string{it.Title}), []string{unpackPasswordAttr(it.attr("password"))})
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
	var nzb *usenet.NZB
	var dnzb usenet.DNZBHeaders
	if nzbURL != "" {
		var err error
		nzb, dnzb, err = usenet.FetchNZB(ctx, c.httpClient(), nzbURL)
		if err != nil {
			log.Printf("usenetsearch: nzb-password indexer=%s nzb err=%v", idx.Name, err)
		} else {
			pws = mergeUnique(usenet.PasswordsFromNZB(dnzb, nzb, it.Title), pws)
			if t := usenet.TitleFromNZB(dnzb, nzb, it.Title); t != "" {
				title = t
			}
		}
	}
	if len(needles) > 0 && !nzbMatchesNeedles(title, dnzb, nzb, needles) {
		return Hit{}, false
	}
	if title == "" && len(pws) == 0 {
		return Hit{}, false
	}
	return Hit{Title: title, Passwords: pws, Indexer: idx.Name}, true
}

func nzbMatchesNeedles(title string, dnzb usenet.DNZBHeaders, nzb *usenet.NZB, needles []string) bool {
	var b strings.Builder
	b.WriteString(title)
	b.WriteByte(' ')
	b.WriteString(dnzb.Name)
	if nzb != nil {
		for _, m := range nzb.Meta {
			b.WriteByte(' ')
			b.WriteString(m.Value)
		}
		for _, f := range nzb.Files {
			b.WriteByte(' ')
			b.WriteString(f.Subject)
		}
	}
	blob := strings.ToLower(b.String())
	for _, n := range needles {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != "" && strings.Contains(blob, n) {
			return true
		}
	}
	return false
}

func sizeClose(attr string, payload int64) bool {
	n, err := strconv.ParseInt(strings.TrimSpace(attr), 10, 64)
	if err != nil || n <= 0 || payload <= 0 {
		return false
	}
	diff := n - payload
	if diff < 0 {
		diff = -diff
	}
	return float64(diff)/float64(n) <= browseSizeTol
}

func browseMaxAgeDays(postedAt int64) int {
	now := time.Now().Unix()
	if postedAt <= 0 || postedAt > now {
		return 2
	}
	d := int((now-postedAt)/86400) + 1
	if d > 7 {
		d = 7
	}
	return d
}

func unpackPasswordAttr(v string) string {
	v = strings.TrimSpace(v)
	switch v {
	case "", "0", "1", "2", "-1":
		return ""
	}
	return v
}

func usenetDateUnix(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func grabReleaseFromItem(idx Indexer, it rssItem) (Release, bool) {
	title := strings.TrimSpace(it.Title)
	nzbURL := strings.TrimSpace(it.Enclosure.URL)
	if nzbURL == "" {
		nzbURL = strings.TrimSpace(it.Link)
	}
	guid := strings.TrimSpace(it.GUID)
	if guid == "" {
		guid = it.attr("guid")
	}
	if nzbURL == "" && guid != "" {
		nzbURL = getURL(idx, guid)
	}
	if title == "" || nzbURL == "" {
		return Release{}, false
	}
	if guid == "" {
		guid = nzbURL
	}
	size, _ := strconv.ParseInt(it.attr("size"), 10, 64)
	if size <= 0 {
		size, _ = strconv.ParseInt(it.Enclosure.Length, 10, 64)
	}
	var cats []int
	if raw := it.attr("category"); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err == nil && n > 0 {
				cats = append(cats, n)
			}
		}
	}
	return Release{
		GUID:        guid,
		Title:       title,
		Indexer:     idx.Name,
		Size:        size,
		DownloadURL: nzbURL,
		PublishDate: strings.TrimSpace(it.PubDate),
		Categories:  cats,
	}, true
}

// Claude 2026-10-23: t=get id is not the RSS <guid> URL.
// Reason: catalog stores geekseek.php?guid= and /details/hash; t=get wants the hash.
// Troubleshooting: newznab: No such item (300) on every On Demand get.
// Review if: an indexer uses a third guid shape.
func GetNZBID(guid string) string {
	s := strings.TrimSpace(guid)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s
	}
	q := u.Query()
	if g := strings.TrimSpace(q.Get("guid")); g != "" {
		return g
	}
	if g := strings.TrimSpace(q.Get("id")); g != "" {
		return g
	}
	if base := path.Base(u.Path); base != "." && base != "/" {
		return base
	}
	return s
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

// GetNZB runs t=get for id. The apikey URL is never returned or logged.
func (c *Client) GetNZB(ctx context.Context, idx Indexer, id string) (*usenet.NZB, []byte, error) {
	id = GetNZBID(id)
	if id == "" || strings.TrimSpace(idx.Endpoint) == "" || strings.TrimSpace(idx.APIKey) == "" {
		return nil, nil, fmt.Errorf("newznab: missing indexer or id")
	}
	idx.Endpoint = NormalizeEndpoint(idx.Endpoint)
	raw := getURL(idx, id)
	if raw == "" {
		return nil, nil, fmt.Errorf("newznab: cannot build t=get")
	}
	u, err := url.Parse(idx.Endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("newznab: indexer endpoint")
	}
	if err := c.throttle(ctx, u.Host); err != nil {
		return nil, nil, err
	}
	nzb, _, data, err := usenet.FetchNZBBytes(ctx, c.httpClient(), raw)
	c.mark(u.Host)
	if err != nil {
		return nil, nil, err
	}
	if err := parseAPIError(data); err != nil {
		return nil, nil, err
	}
	if bytes.Contains(bytes.ToLower(data), []byte("apikey=")) {
		return nil, nil, fmt.Errorf("newznab: nzb body contained apikey")
	}
	return nzb, data, nil
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
//
// Claude 2026-10-23: NZB.su/NZB.life must hit api.nzb.life, not nzb.su.
// Reason: helpapi says the wrong domain returns Missing parameter on every call.
// Troubleshooting: error 200 with a nzb.su URL → this rewrite missed a host alias.
// Review if: NZB.life publishes a new API host.
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
	switch strings.ToLower(u.Hostname()) {
	case "nzb.su", "www.nzb.su", "nzb.life", "www.nzb.life":
		u.Scheme = "https"
		u.Host = "api.nzb.life"
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

// APIError is a Newznab <error code="…"> payload.
type APIError struct {
	Code        string
	Description string
}

func (e APIError) Error() string {
	desc := strings.TrimSpace(e.Description)
	if desc == "" {
		desc = strings.TrimSpace(e.Code)
	}
	if desc == "" {
		desc = "error"
	}
	if e.Code == "" {
		return "newznab: " + desc
	}
	return fmt.Sprintf("newznab: %s (%s)", desc, e.Code)
}

func (e APIError) NoItems() bool { return e.Code == "300" }
func (e APIError) Quota() bool   { return e.Code == "500" }

// Claude 2026-10-23: error 200 is missing q/apikey/t or the wrong domain.
// Reason: catalog retries q=* only on this; 429 and 300 must not retry.
// Troubleshooting: q=* on every catalog error doubled 429s and marked the NZB.life series/adult lanes complete on a literal asterisk search.
// Review if: NZB.life splits missing-q from wrong-domain into new codes.
func (e APIError) MissingParam() bool {
	return e.Code == "200" || strings.Contains(strings.ToLower(e.Description), "missing parameter")
}

func parseAPIError(body []byte) error {
	var e apiError
	if err := xml.Unmarshal(body, &e); err != nil || e.XMLName.Local != "error" {
		return nil
	}
	return APIError{Code: strings.TrimSpace(e.Code), Description: strings.TrimSpace(e.Description)}
}

// HTTPStatusError is a non-2xx response. 429 may carry Retry-After.
type HTTPStatusError struct {
	Host       string
	Status     int
	RetryAfter time.Duration
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return "indexer status"
	}
	if e.Status == http.StatusTooManyRequests && e.RetryAfter > 0 {
		return fmt.Sprintf("%s returned status %d retry-after %s", e.Host, e.Status, e.RetryAfter)
	}
	return fmt.Sprintf("%s returned status %d", e.Host, e.Status)
}

// RetryAfter is the 429 wait from the response, or 0.
func RetryAfter(err error) time.Duration {
	var e *HTTPStatusError
	if errors.As(err, &e) {
		return e.RetryAfter
	}
	return 0
}

func parseRetryAfter(h string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

type capsInfo struct {
	Groups []string
}

type capsDoc struct {
	XMLName xml.Name `xml:"caps"`
	Groups  []struct {
		Name string `xml:"name,attr"`
	} `xml:"groups>group"`
}

func parseCaps(body []byte) (capsInfo, error) {
	if err := parseAPIError(body); err != nil {
		return capsInfo{}, err
	}
	if !bytes.Contains(body, []byte("<caps")) {
		return capsInfo{}, fmt.Errorf("not a Newznab caps response")
	}
	var doc capsDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return capsInfo{}, fmt.Errorf("caps xml")
	}
	info := capsInfo{}
	for _, g := range doc.Groups {
		n := strings.TrimSpace(g.Name)
		if n != "" {
			info.Groups = append(info.Groups, n)
		}
	}
	return info, nil
}

func groupInCaps(info capsInfo, group string) bool {
	group = strings.ToLower(strings.TrimSpace(group))
	if group == "" {
		return false
	}
	if len(info.Groups) == 0 {
		return true
	}
	for _, g := range info.Groups {
		if strings.ToLower(strings.TrimSpace(g)) == group {
			return true
		}
	}
	return false
}

func (c *Client) capsInfo(ctx context.Context, idx Indexer) (capsInfo, error) {
	ep := NormalizeEndpoint(idx.Endpoint)
	c.capsMu.Lock()
	info, ok := c.caps[ep]
	c.capsMu.Unlock()
	if ok {
		return info, nil
	}
	body, err := c.getCaps(ctx, idx)
	if err != nil {
		return capsInfo{}, err
	}
	info, err = parseCaps(body)
	if err != nil {
		return capsInfo{}, err
	}
	c.capsMu.Lock()
	if c.caps == nil {
		c.caps = map[string]capsInfo{}
	}
	c.caps[ep] = info
	c.capsMu.Unlock()
	return info, nil
}

func (c *Client) getCaps(ctx context.Context, idx Indexer) ([]byte, error) {
	idx.Endpoint = NormalizeEndpoint(idx.Endpoint)
	u, err := url.Parse(idx.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("indexer endpoint")
	}
	vals := u.Query()
	vals.Set("t", "caps")
	vals.Set("apikey", idx.APIKey)
	vals.Set("o", "xml")
	u.RawQuery = vals.Encode()
	return c.get(ctx, u)
}

// Caps is the Test-connection ping: t=caps, never HTML login/details.
func (c *Client) Caps(ctx context.Context, idx Indexer) error {
	body, err := c.getCaps(ctx, idx)
	if err != nil {
		return err
	}
	_, err = parseCaps(body)
	return err
}

func (c *Client) get(ctx context.Context, u *url.URL) ([]byte, error) {
	if err := c.throttle(ctx, u.Host); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	c.mark(u.Host)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, httpx.WrapTransportError(u.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ra := time.Duration(0)
		if resp.StatusCode == http.StatusTooManyRequests {
			ra = parseRetryAfter(resp.Header.Get("Retry-After"))
			if ra <= 0 {
				ra = 5 * time.Second
			}
		}
		return nil, &HTTPStatusError{Host: u.Host, Status: resp.StatusCode, RetryAfter: ra}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, httpx.MaxResponseBodySize))
	if err != nil {
		return nil, httpx.WrapTransportError(u.Host, err)
	}
	return body, nil
}
