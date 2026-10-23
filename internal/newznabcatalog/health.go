package newznabcatalog

import (
	"strings"
)

const (
	HealthUnset       = "unset"
	HealthOK          = "ok"
	HealthCrawling    = "crawling"
	HealthRateLimited = "rate_limited"
	HealthQuota       = "quota"
	HealthError       = "error"
)

// IndexerHealth is one Newznab host's crawl health for the Dock card.
type IndexerHealth struct {
	Indexer   string
	Status    string
	Detail    string
	UpdatedAt int64
}

// Claude 2026-10-23: Dock indexer health is crawl cursors, not a live t=caps.
// Reason: GET every 30s must not spend NZBGeek/NZB.life quota.
// Troubleshooting: always Waiting → no cursor rows yet; lastError stays until a page succeeds.
// Review if: a separate probe is added that is not on the 30s poll.
func IndexerHealthFromCursors(cursors []Cursor) []IndexerHealth {
	by := map[string][]Cursor{}
	for _, c := range cursors {
		by[c.Indexer] = append(by[c.Indexer], c)
	}
	out := make([]IndexerHealth, 0, len(catalogServices))
	for _, name := range catalogServices {
		out = append(out, healthFor(name, by[name]))
	}
	return out
}

func healthFor(name string, cs []Cursor) IndexerHealth {
	h := IndexerHealth{Indexer: name, Status: HealthUnset, Detail: "waiting"}
	if len(cs) == 0 {
		return h
	}
	var quota, rate, errMsg string
	open := map[string]bool{}
	for _, c := range cs {
		if c.UpdatedAt > h.UpdatedAt {
			h.UpdatedAt = c.UpdatedAt
		}
		msg := strings.TrimSpace(c.LastError)
		switch {
		case msg == "request limit reached":
			quota = msg
		case msg == "rate limited":
			rate = msg
		case msg != "":
			errMsg = sanitizeHealthDetail(msg)
		}
		if !c.Complete {
			open[c.Kind] = true
		}
	}
	incomplete := ""
	for _, kind := range []string{KindMovies, KindSeries, KindAdult} {
		if open[kind] {
			incomplete = kind
			break
		}
	}
	switch {
	case quota != "":
		h.Status, h.Detail = HealthQuota, quota
	case rate != "":
		h.Status, h.Detail = HealthRateLimited, rate
	case errMsg != "":
		h.Status, h.Detail = HealthError, errMsg
	case incomplete != "":
		h.Status, h.Detail = HealthCrawling, incomplete
	default:
		h.Status, h.Detail = HealthOK, "finished"
	}
	return h
}

func sanitizeHealthDetail(msg string) string {
	if strings.Contains(strings.ToLower(msg), "apikey=") {
		return "indexer error"
	}
	if len(msg) > 200 {
		return msg[:200]
	}
	return msg
}
