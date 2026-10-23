// Package usenet implements SAK's native Usenet/NZB downloader: NNTP
// connection pooling, yEnc segment decoding, and optional PAR2
// verify+repair. It is wired into the API layer as a staged addition —
// the dispatch path in internal/api/search.go still returns 400 for usenet
// grabs until the pool is fully wired (see CLAUDE.md staging note).
//
// Import discipline: this package may import stdlib + the three Usenet
// libraries (Tensai75/nntp, rapidyenc, go-newsgroups/par2) + net/http.
// It must NOT import internal/downloader, internal/grabs, or anything that
// would create a cycle with mode.Session (which will reference *Manager
// once wired).
package usenet

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labbersanon/sakms/internal/httpx"
)

// NZB represents a parsed .nzb file per the NZB 1.1 specification.
type NZB struct {
	Meta  []NZBMeta `xml:"head>meta"`
	Files []NZBFile `xml:"file"`
}

// NZBMeta is an optional key=value metadata pair in the NZB header.
type NZBMeta struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

// NZBFile is one binary file described in the NZB, broken into NNTP segments.
type NZBFile struct {
	Subject string       `xml:"subject,attr"`
	Date    int64        `xml:"date,attr"`
	Groups  []string     `xml:"groups>group"`
	Segs    []NZBSegment `xml:"segments>segment"`
}

// NZBSegment is one NNTP message-ID that carries part of a file's bytes.
type NZBSegment struct {
	Bytes  int64  `xml:"bytes,attr"`
	Number int    `xml:"number,attr"`
	MsgID  string `xml:",chardata"`
}

// ParseNZB parses an NZB 1.1 document from data.
func ParseNZB(data []byte) (*NZB, error) {
	var nzb NZB
	if err := xml.Unmarshal(data, &nzb); err != nil {
		return nil, fmt.Errorf("usenet: parsing NZB: %w", err)
	}
	return &nzb, nil
}

// DNZBHeaders holds metadata from X-DNZB-* HTTP response headers that
// Newznab/Prowlarr indexers include alongside an NZB body download.
type DNZBHeaders struct {
	Name     string // X-DNZB-Name — release name
	RCode    int    // X-DNZB-RCode — 200 = ok; other = failure
	Failure  string // X-DNZB-Failure — human-readable reason when RCode != 200
	Password string // X-DNZB-Password — unpack password when the indexer knows it
}

// parseDNZBHeaders extracts X-DNZB-* headers from an HTTP response header set.
func parseDNZBHeaders(h http.Header) DNZBHeaders {
	code, _ := strconv.Atoi(h.Get("X-DNZB-RCode"))
	return DNZBHeaders{
		Name:     h.Get("X-DNZB-Name"),
		RCode:    code,
		Failure:  h.Get("X-DNZB-Failure"),
		Password: h.Get("X-DNZB-Password"),
	}
}

func requireHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("usenet: invalid NZB URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("usenet: NZB URL scheme must be http or https")
	}
	return nil
}

func nzbHTTPClient(base *http.Client) *http.Client {
	c := &http.Client{}
	if base != nil {
		clone := *base
		c = &clone
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if req.URL != nil {
			if err := requireHTTPURL(req.URL.String()); err != nil {
				return err
			}
		}
		return nil
	}
	return c
}

// fetchNZB GETs the NZB URL via httpClient, extracts X-DNZB-* metadata, and
// parses the body. Returns an error if the indexer signals a failure via
// X-DNZB-RCode even when the HTTP status is 200.
//
// Claude 2026-09-30: scheme http/https only; redirects re-checked.
// Reason: grab SSRF is bound by GUID cache, but a remembered Prowlarr URL
//
//	that 3xx'd to file:// still must not be followed. Private LAN hosts are
//	allowed — Prowlarr NZB URLs are 10.1.10.x with apikey= in the query.
//
// Troubleshooting: "scheme must be http or https" → indexer returned a magnet
//
//	or non-http enclosure as usenet.
func fetchNZB(httpClient *http.Client, rawURL string) (*NZB, DNZBHeaders, error) {
	return FetchNZB(context.Background(), httpClient, rawURL)
}

// Claude 2026-10-21: context FetchNZB; transport errors strip the URL.
// Reason: Newznab getnzb URLs carry apikey=; logging the *url.Error leaked it.
// Troubleshooting: "request to host failed" without the query string is
//   expected. Review if: httpx.WrapTransportError is used at every Get.
// Related files: internal/newznab/client.go

// FetchNZB GETs an NZB over http(s). Errors omit the URL so an apikey query
// is not logged.
func FetchNZB(ctx context.Context, httpClient *http.Client, rawURL string) (*NZB, DNZBHeaders, error) {
	nzb, dnzb, _, err := FetchNZBBytes(ctx, httpClient, rawURL)
	return nzb, dnzb, err
}

// FetchNZBBytes is FetchNZB plus the raw NZB document for a local cache.
// Errors omit the URL so an apikey query is not logged.
func FetchNZBBytes(ctx context.Context, httpClient *http.Client, rawURL string) (*NZB, DNZBHeaders, []byte, error) {
	if err := requireHTTPURL(rawURL); err != nil {
		return nil, DNZBHeaders{}, nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, DNZBHeaders{}, nil, fmt.Errorf("usenet: fetching NZB: %w", err)
	}
	resp, err := nzbHTTPClient(httpClient).Do(req)
	if err != nil {
		host := ""
		if req.URL != nil {
			host = req.URL.Host
		}
		return nil, DNZBHeaders{}, nil, fmt.Errorf("usenet: fetching NZB: %w", httpx.WrapTransportError(host, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, DNZBHeaders{}, nil, fmt.Errorf("usenet: NZB URL returned %d", resp.StatusCode)
	}
	dnzb := parseDNZBHeaders(resp.Header)
	if dnzb.RCode != 0 && dnzb.RCode != http.StatusOK {
		if dnzb.Failure != "" {
			return nil, dnzb, nil, fmt.Errorf("usenet: indexer rejected NZB: %s", dnzb.Failure)
		}
		return nil, dnzb, nil, fmt.Errorf("usenet: indexer rejected NZB (rcode %d)", dnzb.RCode)
	}
	nzb, data, err := readNZBBody(resp.Body)
	if err != nil {
		return nil, dnzb, nil, err
	}
	return nzb, dnzb, data, nil
}

func readNZBBody(r io.Reader) (*NZB, []byte, error) {
	const maxNZBBytes = 10 << 20 // 10 MB
	data, err := io.ReadAll(io.LimitReader(r, maxNZBBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("usenet: reading NZB body: %w", err)
	}
	if int64(len(data)) == maxNZBBytes {
		return nil, nil, fmt.Errorf("usenet: NZB response body exceeds 10 MB limit")
	}
	nzb, err := ParseNZB(data)
	if err != nil {
		return nil, nil, err
	}
	return nzb, data, nil
}
