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
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// NZB represents a parsed .nzb file per the NZB 1.1 specification.
type NZB struct {
	Meta  []NZBMeta  `xml:"head>meta"`
	Files []NZBFile  `xml:"file"`
}

// NZBMeta is an optional key=value metadata pair in the NZB header.
type NZBMeta struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

// NZBFile is one binary file described in the NZB, broken into NNTP segments.
type NZBFile struct {
	// Claude 2026-09-27: poster is NZB 1.1 file/@poster (From-style name/email).
	// Reason: Downloads popup shows who posted the NZB, not the indexer.
	// Troubleshooting: popup Posted by empty — expect this attr on <file>.
	// Review if: poster is taken from a later file when the first is blank.
	Poster  string       `xml:"poster,attr"`
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

// nzbSource returns the first non-empty file poster and unique newsgroups
// across the NZB. Groups keep first-seen order.
//
// Claude 2026-09-27: Downloads popup Usenet source + poster.
// Reason: NZB 1.1 carries groups and poster; the engine snapshot did not.
// Troubleshooting: news: links empty — check <groups><group> in the NZB.
// Review if: a later file's poster should win over the first.
func nzbSource(nzb *NZB) (poster string, groups []string) {
	if nzb == nil {
		return "", nil
	}
	seen := make(map[string]struct{})
	for _, f := range nzb.Files {
		if poster == "" {
			poster = strings.TrimSpace(f.Poster)
		}
		for _, g := range f.Groups {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			if _, ok := seen[g]; ok {
				continue
			}
			seen[g] = struct{}{}
			groups = append(groups, g)
		}
	}
	return poster, groups
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
	Name    string // X-DNZB-Name — release name
	RCode   int    // X-DNZB-RCode — 200 = ok; other = failure
	Failure string // X-DNZB-Failure — human-readable reason when RCode != 200
}

// parseDNZBHeaders extracts X-DNZB-* headers from an HTTP response header set.
func parseDNZBHeaders(h http.Header) DNZBHeaders {
	code, _ := strconv.Atoi(h.Get("X-DNZB-RCode"))
	return DNZBHeaders{
		Name:    h.Get("X-DNZB-Name"),
		RCode:   code,
		Failure: h.Get("X-DNZB-Failure"),
	}
}

// fetchNZB GETs the NZB URL via httpClient, extracts X-DNZB-* metadata, and
// parses the body. Returns an error if the indexer signals a failure via
// X-DNZB-RCode even when the HTTP status is 200.
func fetchNZB(httpClient *http.Client, url string) (*NZB, DNZBHeaders, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, DNZBHeaders{}, fmt.Errorf("usenet: fetching NZB: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, DNZBHeaders{}, fmt.Errorf("usenet: NZB URL returned %d", resp.StatusCode)
	}

	dnzb := parseDNZBHeaders(resp.Header)
	if dnzb.RCode != 0 && dnzb.RCode != http.StatusOK {
		if dnzb.Failure != "" {
			return nil, dnzb, fmt.Errorf("usenet: indexer rejected NZB: %s", dnzb.Failure)
		}
		return nil, dnzb, fmt.Errorf("usenet: indexer rejected NZB (rcode %d)", dnzb.RCode)
	}

	const maxNZBBytes = 10 << 20 // 10 MB
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxNZBBytes))
	if err != nil {
		return nil, dnzb, fmt.Errorf("usenet: reading NZB body: %w", err)
	}
	if int64(len(data)) == maxNZBBytes {
		return nil, dnzb, fmt.Errorf("usenet: NZB response body exceeds 10 MB limit")
	}

	nzb, err := ParseNZB(data)
	if err != nil {
		return nil, dnzb, err
	}
	return nzb, dnzb, nil
}
