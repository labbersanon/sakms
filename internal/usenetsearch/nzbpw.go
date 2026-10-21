package usenetsearch

import (
	"context"
	"strings"
)

// Claude 2026-10-21: Newznab password lookup for namefix (API, not HTML).
// Reason: ngPost passwords are in the NZB; NNTP hash.7z is 7z-aes. Lookup
//   searches the obfuscated archive stem on Prowlarr-listed Newznab
//   indexers. Do not scrape indexer sites. Do not brute-force.
// Troubleshooting: o2cli grep "nzb-password"; name_source=nzb|srrdb after
//   payload=7z-aes. Review if: indexers stop matching q=filename.
// Related files: internal/newznab/client.go, namefix.go

const namefixNZBPasswordPerPass = 6

// NZBPasswordHit is an indexer title and unpack passwords. Never log Passwords.
type NZBPasswordHit struct {
	Title     string
	Passwords []string
	Indexer   string
}

// NZBPasswordLookup finds an NZB by obfuscated archive filename.
type NZBPasswordLookup interface {
	Lookup(ctx context.Context, filename string) (NZBPasswordHit, bool)
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
