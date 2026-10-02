package disc

// Claude 2026-10-02: Disc TOC lookup when the image has no per-title names.
// Reason: IFO tags are the volume label, not shorts. Wikipedia Disc N tables
//   are the working name source; SearXNG only supplies a wiki URL if API
//   search misses. OVID is empty and has no cartoon names.
// Troubleshooting: len(TOC) must equal PlanWorks count or names are dropped
//   (do not pair 15 Wikipedia rows onto 2 leftover titles).
// Review if: ffprobe starts emitting unique per-title names.

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/httpx"
)

const wikiUA = "sakms-disc-lookup/1.0 (https://github.com/labbersanon/sakms)"

// wikiAPIURL is the MediaWiki API. Tests point this at httptest.
var wikiAPIURL = "https://en.wikipedia.org/w/api.php"

type wikiSearchResp struct {
	Query struct {
		Search []struct {
			Title string `json:"title"`
		} `json:"search"`
	} `json:"query"`
}

type wikiParseResp struct {
	Parse struct {
		Title    string `json:"title"`
		Wikitext string `json:"wikitext"`
	} `json:"parse"`
}

// LookupTOC names PlanWorks rows from Wikipedia (SearXNG URLs as fallback).
func LookupTOC(ctx context.Context, hc *http.Client, volume, src string, want int, extraURLs []string) []string {
	if hc == nil || want <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	q := WikiQuery(volume, src)
	discN := DiscNumber(volume, src)
	var pages []string
	if q != "" {
		found, err := wikiSearch(ctx, hc, q)
		if err == nil {
			pages = append(pages, found...)
		}
	}
	for _, u := range extraURLs {
		if p := wikiPageFromURL(u); p != "" {
			pages = append(pages, p)
		}
	}
	seen := map[string]bool{}
	for _, page := range pages {
		key := strings.ToLower(strings.ReplaceAll(page, "_", " "))
		if page == "" || seen[key] {
			continue
		}
		seen[key] = true
		wt, err := wikiParse(ctx, hc, page)
		if err != nil || wt == "" {
			continue
		}
		names := ParseDiscTOC(wt, discN)
		if len(names) == want {
			return names
		}
	}
	return nil
}

// ProbeNamed is true when every planned extract already has a distinct name
// that is not just the volume label.
func ProbeNamed(titles []Title, volume string) bool {
	works := PlanWorks(AssignRoles(titles))
	if len(works) == 0 {
		return false
	}
	byN := map[int]Title{}
	for _, t := range titles {
		byN[t.N] = t
	}
	vol := foldLabel(volume)
	seen := map[string]bool{}
	for _, w := range works {
		n := strings.TrimSpace(byN[w.Title].Name)
		if n == "" {
			return false
		}
		key := foldLabel(n)
		if key == vol || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func foldLabel(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(s, "_", " ")), " "))
}

// AttachTOCNames stores names in PlanWorks order and rewrites the sidecar.
func AttachTOCNames(m *Map, names []string) bool {
	if m == nil || len(names) == 0 {
		return false
	}
	want := len(PlanWorks(AssignRoles(m.Titles)))
	if len(names) != want {
		return false
	}
	m.TOC = append([]string(nil), names...)
	_ = writeSidecar(m)
	return true
}

// TOCForWorks is sidecar TOC, else unique probe names in PlanWorks order.
func TOCForWorks(m *Map) []string {
	if m == nil {
		return nil
	}
	works := PlanWorks(AssignRoles(m.Titles))
	if len(m.TOC) == len(works) && len(works) > 0 {
		return append([]string(nil), m.TOC...)
	}
	if !ProbeNamed(m.Titles, m.Volume) {
		return nil
	}
	byN := map[int]Title{}
	for _, t := range m.Titles {
		byN[t.N] = t
	}
	out := make([]string, len(works))
	for i, w := range works {
		out[i] = strings.TrimSpace(byN[w.Title].Name)
	}
	return out
}

func wikiSearch(ctx context.Context, hc *http.Client, query string) ([]string, error) {
	u, err := url.Parse(wikiAPIURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("action", "query")
	q.Set("list", "search")
	q.Set("srsearch", query)
	q.Set("srlimit", "5")
	q.Set("format", "json")
	q.Set("formatversion", "2")
	u.RawQuery = q.Encode()
	var body wikiSearchResp
	if err := wikiGET(ctx, hc, u.String(), &body); err != nil {
		return nil, err
	}
	var pages []string
	for _, hit := range body.Query.Search {
		if strings.TrimSpace(hit.Title) != "" {
			pages = append(pages, hit.Title)
		}
	}
	return pages, nil
}

func wikiParse(ctx context.Context, hc *http.Client, page string) (string, error) {
	u, err := url.Parse(wikiAPIURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("action", "parse")
	q.Set("page", page)
	q.Set("prop", "wikitext")
	q.Set("format", "json")
	q.Set("formatversion", "2")
	u.RawQuery = q.Encode()
	var body wikiParseResp
	if err := wikiGET(ctx, hc, u.String(), &body); err != nil {
		return "", err
	}
	return body.Parse.Wikitext, nil
}

func wikiGET(ctx context.Context, hc *http.Client, raw string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", wikiUA)
	req.Header.Set("Accept", "application/json")
	return httpx.DoJSON(hc, req, httpx.MaxResponseBodySize, out)
}

func wikiPageFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, "wikipedia.org") {
		return ""
	}
	p := strings.TrimPrefix(u.EscapedPath(), "/")
	parts := strings.Split(p, "/")
	if len(parts) < 2 || !strings.EqualFold(parts[0], "wiki") {
		return ""
	}
	name, err := url.PathUnescape(parts[1])
	if err != nil {
		name = parts[1]
	}
	name = strings.ReplaceAll(name, "_", " ")
	if i := strings.Index(name, "#"); i >= 0 {
		name = name[:i]
	}
	lower := strings.ToLower(name)
	for _, bad := range []string{"file:", "special:", "wikipedia:", "help:", "talk:", "template:", "category:", "user:"} {
		if strings.HasPrefix(lower, bad) {
			return ""
		}
	}
	if name == "" {
		return ""
	}
	return name
}
