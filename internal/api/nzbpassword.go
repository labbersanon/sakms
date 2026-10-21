package api

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/prowlarr"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenetsearch"
)

// Claude 2026-10-21: wire Newznab password lookup without usenetsearch
// importing Prowlarr. Keys stay in Prowlarr; sakms never writes them.
// Reason: namefix 7z-aes miss needs t=search by archive filename. HTML
//   scrape is not used.
// Troubleshooting: nzb-password never logs → Prowlarr missing, kill switch
//   off, or zero Usenet Newznab indexers. Review if: Prowlarr indexer
//   list shape changes.
// Related files: internal/prowlarr/indexers.go, internal/newznab/client.go

const nzbPasswordIndexerTTL = 10 * time.Minute

type nzbPasswordLookup struct {
	conns    *connections.Store
	settings *settings.Store
	http     *http.Client
	client   *newznab.Client

	mu       sync.Mutex
	cached   []prowlarr.NewznabIndexer
	cachedAt time.Time
}

// AttachNZBPasswordLookup injects Newznab password lookup into NNTP namefix.
func AttachNZBPasswordLookup(svc *usenetsearch.Service, connStore *connections.Store, settingsStore *settings.Store, httpClient *http.Client) {
	if svc == nil {
		return
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	svc.SetNZBPassword(&nzbPasswordLookup{
		conns:    connStore,
		settings: settingsStore,
		http:     httpClient,
		client:   &newznab.Client{HTTP: httpClient},
	})
}

func (c *nzbPasswordLookup) Lookup(ctx context.Context, filename string) (usenetsearch.NZBPasswordHit, bool) {
	if c == nil {
		return usenetsearch.NZBPasswordHit{}, false
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return usenetsearch.NZBPasswordHit{}, false
	}
	if c.settings != nil {
		on, err := c.settings.GetBool(ctx, usenetsearch.KeyNZBPasswordEnabled, true)
		if err == nil && !on {
			return usenetsearch.NZBPasswordHit{}, false
		}
	}
	indexers, err := c.indexers(ctx)
	if err != nil {
		log.Printf("usenetsearch: nzb-password prowlarr err=%v", err)
		return usenetsearch.NZBPasswordHit{}, false
	}
	if len(indexers) == 0 {
		return usenetsearch.NZBPasswordHit{}, false
	}
	hit, ok := c.client.Lookup(ctx, toNewznab(indexers), filename)
	return usenetsearch.NZBPasswordHit{Title: hit.Title, Passwords: hit.Passwords, Indexer: hit.Indexer}, ok
}

func (c *nzbPasswordLookup) indexers(ctx context.Context) ([]prowlarr.NewznabIndexer, error) {
	c.mu.Lock()
	if time.Since(c.cachedAt) < nzbPasswordIndexerTTL && c.cached != nil {
		out := c.cached
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()
	conn, err := optionalConnAPI(ctx, c.conns, "prowlarr")
	if err != nil {
		return nil, err
	}
	if conn == nil || strings.TrimSpace(conn.URL) == "" || strings.TrimSpace(conn.APIKey) == "" {
		return nil, nil
	}
	pc := prowlarr.New(prowlarr.Config{BaseURL: strings.TrimRight(conn.URL, "/"), APIKey: conn.APIKey}, c.http)
	list, err := pc.UsenetNewznab(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cached, c.cachedAt = list, time.Now()
	c.mu.Unlock()
	return list, nil
}

func toNewznab(in []prowlarr.NewznabIndexer) []newznab.Indexer {
	out := make([]newznab.Indexer, 0, len(in))
	for _, x := range in {
		if strings.TrimSpace(x.Endpoint) == "" || strings.TrimSpace(x.APIKey) == "" {
			continue
		}
		name := strings.TrimSpace(x.Name)
		if name == "" {
			name = "indexer"
		}
		out = append(out, newznab.Indexer{Name: name, Endpoint: x.Endpoint, APIKey: x.APIKey})
	}
	return out
}
