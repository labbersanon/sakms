package usenetsearch

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Claude 2026-10-20: srrdb archive-crc lookup (use, do not scrape).
// Reason: remaining hash.par2 FileDesc names are random tokens; nZEDb 2652
//   says hashed rename needs PreDB/IRC, and commercial indexers scrape NZBs
//   (forbidden). srrdb documents GET /v1/search/archive-crc:XXXXXXXX as the
//   public reverse lookup for an inner-file CRC32 (RAR FILE_CRC / SFV / ZIP).
// Troubleshooting: name_source=srrdb stays 0 with rar BODY → CRC extract
//   missed or resultsCount!=1. Review if: srrdb ToS forbids CRC lookup.
// Related files: namefix.go lookupArchiveCRC, archive_names.go FILE_CRC

// DefaultSRRDBURL is the public search prefix. Queries are CRC-only; do not
// page this endpoint to build a catalog (provider: "Use but don't scrape").
const DefaultSRRDBURL = "https://api.srrdb.com/v1/search/"

// MinSRRDBInterval spaces CRC lookups the same way as PreDB HTTP (≤30/min).
const MinSRRDBInterval = 2 * time.Second

const (
	maxSRRDBBody   = 1 << 20
	srrdbUserAgent = "sakms-srrdb/1"
)

var reArchiveCRC = regexp.MustCompile(`^[0-9A-F]{8}$`)

// ArchiveCRCLookup resolves an inner-file CRC32 to a unique scene title.
type ArchiveCRCLookup interface {
	ByCRC(ctx context.Context, crc string) (PreDBHit, bool)
}

// SRRDBClient is GET {base}archive-crc:XXXXXXXX. ok=false is a miss or an
// ambiguous (resultsCount!=1) hit — both are ignored.
type SRRDBClient struct {
	BaseURL  string
	Client   *http.Client
	Interval time.Duration

	mu   sync.Mutex
	last time.Time
}

// NewSRRDBClient returns an adapter for baseURL (empty = DefaultSRRDBURL).
func NewSRRDBClient(baseURL string) *SRRDBClient {
	u := strings.TrimSpace(baseURL)
	if u == "" {
		u = DefaultSRRDBURL
	}
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	return &SRRDBClient{BaseURL: u, Client: &http.Client{Timeout: 15 * time.Second}}
}

type srrdbSearch struct {
	Results      []srrdbHit      `json:"results"`
	ResultsCount json.RawMessage `json:"resultsCount"`
}

type srrdbHit struct {
	Release string `json:"release"`
	Size    int64  `json:"size"`
}

func (c *SRRDBClient) wait(ctx context.Context) error {
	iv := c.Interval
	if iv <= 0 {
		iv = MinSRRDBInterval
	}
	c.mu.Lock()
	next := c.last.Add(iv)
	now := time.Now()
	if next.Before(now) {
		next = now
	}
	c.last = next
	c.mu.Unlock()
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

func srrdbCount(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(s))
		return n
	}
	return 0
}

// ByCRC looks up an 8-hex CRC. Transport errors are misses (no retry storm).
func (c *SRRDBClient) ByCRC(ctx context.Context, crc string) (PreDBHit, bool) {
	if c == nil || c.BaseURL == "" {
		return PreDBHit{}, false
	}
	crc = strings.ToUpper(strings.TrimSpace(crc))
	if !reArchiveCRC.MatchString(crc) || crc == "00000000" {
		return PreDBHit{}, false
	}
	if err := c.wait(ctx); err != nil {
		return PreDBHit{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"archive-crc:"+crc, nil)
	if err != nil {
		return PreDBHit{}, false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", srrdbUserAgent)
	cl := c.Client
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		log.Printf("usenetsearch: srrdb crc=%s err=%v", crc, err)
		return PreDBHit{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("usenetsearch: srrdb crc=%s http=%d", crc, resp.StatusCode)
		return PreDBHit{}, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSRRDBBody))
	if err != nil {
		return PreDBHit{}, false
	}
	var out srrdbSearch
	if err := json.Unmarshal(body, &out); err != nil {
		log.Printf("usenetsearch: srrdb crc=%s malformed: %v", crc, err)
		return PreDBHit{}, false
	}
	n := srrdbCount(out.ResultsCount)
	if n != 1 || len(out.Results) != 1 {
		log.Printf("usenetsearch: srrdb crc=%s results=%d", crc, n)
		return PreDBHit{}, false
	}
	title := strings.TrimSpace(out.Results[0].Release)
	if title == "" {
		return PreDBHit{}, false
	}
	return PreDBHit{Title: title, SizeBytes: out.Results[0].Size}, true
}
