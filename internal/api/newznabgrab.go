package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/prowlarr"
)

// Claude 2026-10-21: Settings `newznab` host is an alternate Usenet grab search.
// Reason: NZBGeek cannot recover ngPost passwords; native NNTP is first, then
//   this indexer, then Prowlarr. Do not scrape HTML. Do not fetch NZBs at search.
// Troubleshooting: grab phase miss → host unset or t=search empty; indexerIds=-4
//   on Prowlarr means ScopeNewznab leaked. Review if: Prowlarr already includes
//   the same host (duplicate NZBs are deduped by URL/title).
// Related files: internal/newznab/client.go, frontend Usenet.tsx

var (
	newznabGrabMu     sync.RWMutex
	newznabGrabConns  *connections.Store
	newznabGrabClient *newznab.Client
)

// AttachNewznabGrabSearch registers the Settings NZB host for grab search.
// Pass a nil store to clear (tests).
func AttachNewznabGrabSearch(connStore *connections.Store, httpClient *http.Client) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	newznabGrabMu.Lock()
	newznabGrabConns = connStore
	if connStore == nil {
		newznabGrabClient = nil
	} else {
		newznabGrabClient = &newznab.Client{HTTP: httpClient}
	}
	newznabGrabMu.Unlock()
}

func newznabGrabConfigured(ctx context.Context) bool {
	idx, ok := newznabGrabIndexer(ctx)
	return ok && idx.Endpoint != "" && idx.APIKey != ""
}

func newznabGrabIndexer(ctx context.Context) (newznab.Indexer, bool) {
	newznabGrabMu.RLock()
	conns := newznabGrabConns
	client := newznabGrabClient
	newznabGrabMu.RUnlock()
	if conns == nil || client == nil {
		return newznab.Indexer{}, false
	}
	conn, err := optionalConnAPI(ctx, conns, "newznab")
	if err != nil || conn == nil {
		return newznab.Indexer{}, false
	}
	url := strings.TrimSpace(conn.URL)
	key := strings.TrimSpace(conn.APIKey)
	if url == "" || key == "" {
		return newznab.Indexer{}, false
	}
	return newznab.Indexer{Name: "newznab", Endpoint: url, APIKey: key}, true
}

func insertNewznabPhase(phases []prowlarr.Scope) []prowlarr.Scope {
	for _, p := range phases {
		if p.IsNewznab() {
			return phases
		}
	}
	i := 0
	for i < len(phases) && phases[i].IsNative() {
		i++
	}
	out := make([]prowlarr.Scope, 0, len(phases)+1)
	out = append(out, phases[:i]...)
	out = append(out, prowlarr.ScopeNewznab)
	out = append(out, phases[i:]...)
	return out
}

func newznabGrabSearch(ctx context.Context, query string, cats []int) ([]prowlarr.Release, error) {
	idx, ok := newznabGrabIndexer(ctx)
	if !ok {
		return nil, nil
	}
	newznabGrabMu.RLock()
	client := newznabGrabClient
	newznabGrabMu.RUnlock()
	if client == nil {
		return nil, nil
	}
	rels, err := client.SearchGrab(ctx, idx, query, cats)
	if err != nil {
		return nil, err
	}
	return adaptNewznabReleases(rels), nil
}

func newznabAutoGrabSearch(ctx context.Context, m mode.Mode, req AutoGrabRequest) ([]prowlarr.Release, error) {
	q := strings.TrimSpace(req.Title)
	if m == mode.Adult && strings.TrimSpace(req.ReleaseTitle) != "" {
		q = strings.TrimSpace(req.ReleaseTitle)
	}
	if m == mode.Series && req.SeasonSpecified {
		if req.Episode > 0 {
			q = fmt.Sprintf("%s S%02dE%02d", q, req.Season, req.Episode)
		} else {
			q = fmt.Sprintf("%s S%02d", q, req.Season)
		}
	}
	return newznabGrabSearch(ctx, q, categoriesForSearch(m))
}

func adaptNewznabReleases(cs []newznab.Release) []prowlarr.Release {
	out := make([]prowlarr.Release, 0, len(cs))
	for _, c := range cs {
		if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.DownloadURL) == "" {
			continue
		}
		name := c.Indexer
		if name == "" {
			name = "newznab"
		}
		out = append(out, prowlarr.Release{
			GUID:        c.GUID,
			Title:       c.Title,
			Indexer:     name,
			Protocol:    prowlarr.Usenet,
			Size:        c.Size,
			DownloadURL: c.DownloadURL,
			PublishDate: c.PublishDate,
			Categories:  c.Categories,
		})
	}
	return out
}
