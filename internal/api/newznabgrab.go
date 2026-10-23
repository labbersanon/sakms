package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/prowlarr"
)

// Claude 2026-10-21: Settings `newznab` host is the Usenet grab catalog.
// Reason: NZBGeek cannot recover ngPost passwords. Do not scrape HTML.
//   Do not fetch NZBs at search. Prowlarr stays last.
// Troubleshooting: grab phase miss → host unset or t=search empty; indexerIds=-4
//   on Prowlarr means ScopeNewznab leaked. Review if: Prowlarr already includes
//   the same host (duplicate NZBs are deduped by URL/title).
// Related files: internal/newznab/client.go, frontend Usenet.tsx
//
// Claude 2026-10-22: optional `newznab2` is a second grab indexer.
// Reason: NZBGeek does not list ngPost obfuscated NZBs. Paste URL+key in
//   Settings — no secret in git. NZBFinder is the recommended second host.
// Troubleshooting: only host 1 is queried → connections.service newznab2
//   URL/key empty. Review if: Prowlarr already includes both.
// Claude 2026-10-23: both hosts run in the same grab phase, in parallel.
// Reason: sequential t=search hid NZBFinder behind NZBGeek latency.
// Troubleshooting: host 2 unused → empty newznab2 connection, not ordering.

var (
	newznabGrabMu     sync.RWMutex
	newznabGrabConns  *connections.Store
	newznabGrabClient *newznab.Client
)

// AttachNewznabGrabSearch registers the Settings NZB host for grab search.
// Pass a nil store to clear (tests).
func AttachNewznabGrabSearch(connStore *connections.Store, httpClient *http.Client) {
	newznabGrabMu.Lock()
	newznabGrabConns = connStore
	newznabGrabClient = nil
	if connStore != nil {
		if httpClient == nil {
			httpClient = &http.Client{Timeout: 30 * time.Second}
		}
		newznabGrabClient = &newznab.Client{HTTP: httpClient}
	}
	newznabGrabMu.Unlock()
}

func newznabGrabConfigured(ctx context.Context) bool {
	_, _, ok := newznabGrabIndexers(ctx)
	return ok
}

var newznabGrabServices = []string{"newznab", "newznab2"}

func newznabIndexersFromStore(ctx context.Context, store *connections.Store) []newznab.Indexer {
	if store == nil {
		return nil
	}
	var out []newznab.Indexer
	for _, svc := range newznabGrabServices {
		conn, err := optionalConnAPI(ctx, store, svc)
		if err != nil {
			log.Printf("search: newznab %s err=%v", svc, err)
			continue
		}
		if conn == nil {
			continue
		}
		url := strings.TrimSpace(conn.URL)
		key := strings.TrimSpace(conn.APIKey)
		if url == "" || key == "" {
			continue
		}
		out = append(out, newznab.Indexer{Name: svc, Endpoint: url, APIKey: key})
	}
	return out
}

func newznabGrabIndexers(ctx context.Context) ([]newznab.Indexer, *newznab.Client, bool) {
	newznabGrabMu.RLock()
	conns := newznabGrabConns
	client := newznabGrabClient
	newznabGrabMu.RUnlock()
	if conns == nil || client == nil {
		return nil, nil, false
	}
	idxs := newznabIndexersFromStore(ctx, conns)
	if len(idxs) == 0 {
		return nil, nil, false
	}
	return idxs, client, true
}

func insertNewznabPhase(phases []prowlarr.Scope) []prowlarr.Scope {
	for _, p := range phases {
		if p.IsNewznab() {
			return phases
		}
	}
	out := make([]prowlarr.Scope, 0, len(phases)+1)
	out = append(out, prowlarr.ScopeNewznab)
	out = append(out, phases...)
	return out
}

// mergeGrabReleases keeps Newznab hits ahead of Prowlarr. Prowlarr is last.
func mergeGrabReleases(newznab, prowlarrHits []prowlarr.Release) []prowlarr.Release {
	if len(prowlarrHits) == 0 {
		return newznab
	}
	if len(newznab) == 0 {
		return prowlarrHits
	}
	out := make([]prowlarr.Release, 0, len(newznab)+len(prowlarrHits))
	out = append(out, newznab...)
	out = append(out, prowlarrHits...)
	return out
}

func newznabGrabSearch(ctx context.Context, query string, cats []int) ([]prowlarr.Release, error) {
	idxs, client, ok := newznabGrabIndexers(ctx)
	if !ok {
		return nil, nil
	}
	// Claude 2026-10-23: query every configured Newznab host at once.
	// Reason: sequential SearchGrab waited on NZBGeek before NZBFinder, and
	//   a process-wide throttle added another second. One host error must
	//   not drop the other. WaitGroup (not errgroup) so a failure does not
	//   cancel siblings. Slot order keeps newznab ahead of newznab2.
	// Troubleshooting: only one indexer in flight → this loop is sequential
	//   again, or newznab2 URL/key empty.
	// Review if: a third Newznab host is added (still fan-out; do not
	//   serialize). Related files: internal/newznab/client.go (per-host last).
	type slot struct {
		rels []prowlarr.Release
		err  error
	}
	slots := make([]slot, len(idxs))
	var wg sync.WaitGroup
	for i, idx := range idxs {
		wg.Add(1)
		go func(i int, idx newznab.Indexer) {
			defer wg.Done()
			rels, err := client.SearchGrab(ctx, idx, query, cats)
			if err != nil {
				log.Printf("search: newznab indexer=%s err=%v", idx.Name, err)
				slots[i].err = err
				return
			}
			slots[i].rels = adaptNewznabReleases(rels)
		}(i, idx)
	}
	wg.Wait()
	var out []prowlarr.Release
	var firstErr error
	for _, s := range slots {
		if s.err != nil && firstErr == nil {
			firstErr = s.err
		}
		out = append(out, s.rels...)
	}
	out = dedupeGrabReleases(out)
	if len(out) == 0 {
		return nil, firstErr
	}
	return out, nil
}

func dedupeGrabReleases(rels []prowlarr.Release) []prowlarr.Release {
	if len(rels) < 2 {
		return rels
	}
	seen := make(map[string]bool, len(rels))
	out := make([]prowlarr.Release, 0, len(rels))
	for _, r := range rels {
		key := strings.TrimSpace(r.DownloadURL)
		if key == "" {
			key = r.GUID + "\n" + r.Title
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

func newznabAutoGrabSearch(ctx context.Context, m mode.Mode, req AutoGrabRequest) ([]prowlarr.Release, error) {
	q := strings.TrimSpace(req.Title)
	if m == mode.Adult {
		if rt := strings.TrimSpace(req.ReleaseTitle); rt != "" {
			q = rt
		}
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
