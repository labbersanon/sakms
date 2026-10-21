package usenetsearch

import (
	"context"
	"net/mail"
	"strings"
)

// Claude 2026-10-21: Newznab password lookup for namefix (API, not HTML).
// Reason: ngPost passwords are in the NZB; NNTP hash.7z is 7z-aes. Lookup
//   searches the archive stem then the NNTP From (poster) on the configured
//   Newznab host. Do not scrape indexer sites. Do not brute-force.
// Troubleshooting: o2cli grep "nzb-password"; browse skip group_not_listed →
//   caps omits the NNTP group; cands=0 → size/group; name_source=nzb|srrdb
//   after payload=7z-aes. Review if: NZBGeek adds x264 or poster to caps.
// Related files: internal/newznab/client.go, namefix.go

const namefixNZBPasswordPerPass = 6

// NZBPasswordHit is an indexer title and unpack passwords. Never log Passwords.
type NZBPasswordHit struct {
	Title     string
	Passwords []string
	Indexer   string
}

// NZBPasswordQuery is one namefix indexer lookup (stem and/or NNTP From).
// Group/PostedAt/SizeBytes are for the NZBGeek group+maxage browse after
// q= and poster= miss.
type NZBPasswordQuery struct {
	Filename  string
	Poster    string
	Group     string
	PostedAt  int64
	SizeBytes int64
}

// NZBPasswordLookup finds an NZB by obfuscated archive filename and/or poster.
type NZBPasswordLookup interface {
	Lookup(ctx context.Context, q NZBPasswordQuery) (NZBPasswordHit, bool)
}

func nzbSearchStem(name string) string {
	s := strings.TrimSpace(name)
	if s == "" {
		return ""
	}
	if q := quotedFilename(s); q != "" {
		s = q
	}
	low := strings.ToLower(s)
	if loc := reSplitVol.FindStringIndex(low); loc != nil {
		s = s[:loc[0]]
		low = strings.ToLower(s)
	}
	if loc := reArchivePart.FindStringIndex(low); loc != nil {
		s = s[:loc[0]]
		low = strings.ToLower(s)
	}
	for _, ext := range []string{".par2", ".nfo", ".sfv", ".7z", ".rar", ".zip", ".mkv", ".nzb"} {
		if strings.HasSuffix(low, ext) {
			s = s[:len(s)-len(ext)]
			low = strings.ToLower(s)
		}
	}
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return ""
	}
	return s
}

func archiveSearchStem(rows []HeaderRow) string {
	var fallback string
	for _, r := range rows {
		n := r.Filename
		if n == "" {
			n = quotedFilename(r.Subject)
		}
		stem := nzbSearchStem(n)
		if stem == "" {
			continue
		}
		if is7zName(n) || is7zName(r.Subject) {
			return stem
		}
		if fallback == "" {
			fallback = stem
		}
	}
	return fallback
}

func nzbSearchPoster(from string) string {
	from = strings.TrimSpace(from)
	if from == "" {
		return ""
	}
	if a, err := mail.ParseAddress(from); err == nil {
		addr := strings.TrimSpace(a.Address)
		if strings.Contains(addr, "@") {
			return addr
		}
	}
	if i := strings.Index(from, "<"); i >= 0 {
		rest := from[i+1:]
		if j := strings.Index(rest, ">"); j > 0 {
			cand := strings.TrimSpace(rest[:j])
			if strings.Contains(cand, "@") {
				return cand
			}
		}
	}
	if strings.Contains(from, "@") && !strings.ContainsAny(from, " <>") {
		return from
	}
	return ""
}

func archiveSearchPosted(rows []HeaderRow) int64 {
	var t int64
	for _, r := range rows {
		if r.PostedAt <= 0 {
			continue
		}
		if t == 0 || r.PostedAt < t {
			t = r.PostedAt
		}
	}
	return t
}

func archiveSearchPoster(rows []HeaderRow) string {
	var fallback string
	for _, r := range rows {
		p := nzbSearchPoster(r.From)
		if p == "" {
			continue
		}
		if looksLikeNgPostFrom(p) {
			return p
		}
		if fallback == "" {
			fallback = p
		}
	}
	return fallback
}
