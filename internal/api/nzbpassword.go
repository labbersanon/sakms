package api

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/newznab"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/usenetsearch"
)

// Claude 2026-10-21: namefix Newznab lookup uses the dedicated `newznab`
// connection (Settings → Download → Usenet), not Prowlarr's indexer list.
// Reason: Prowlarr is grab-search; password/title recovery needs a Newznab
//   host URL + API key the operator sets. HTML scrape is not used.
// Troubleshooting: nzb-password never logs → newznab connection missing,
//   kill switch off, or t=search miss. poster= is nZEDb fromname after
//   stem miss. NZBGeek browse is group+maxage+size then t=get. Review if:
//   NZBGeek adds poster to caps or RSS.
// Related files: internal/newznab/client.go, frontend Usenet.tsx

type nzbPasswordLookup struct {
	conns    *connections.Store
	settings *settings.Store
	client   *newznab.Client
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
		client:   &newznab.Client{HTTP: httpClient},
	})
}

func (c *nzbPasswordLookup) Lookup(ctx context.Context, q usenetsearch.NZBPasswordQuery) (usenetsearch.NZBPasswordHit, bool) {
	if c == nil {
		return usenetsearch.NZBPasswordHit{}, false
	}
	q.Filename = strings.TrimSpace(q.Filename)
	q.Poster = strings.TrimSpace(q.Poster)
	if q.Filename == "" && q.Poster == "" {
		return usenetsearch.NZBPasswordHit{}, false
	}
	if c.settings != nil {
		on, err := c.settings.GetBool(ctx, usenetsearch.KeyNZBPasswordEnabled, true)
		if err == nil && !on {
			return usenetsearch.NZBPasswordHit{}, false
		}
	}
	conn, err := optionalConnAPI(ctx, c.conns, "newznab")
	if err != nil {
		log.Printf("usenetsearch: nzb-password newznab err=%v", err)
		return usenetsearch.NZBPasswordHit{}, false
	}
	if conn == nil || strings.TrimSpace(conn.URL) == "" || strings.TrimSpace(conn.APIKey) == "" {
		return usenetsearch.NZBPasswordHit{}, false
	}
	idx := newznab.Indexer{
		Name:     "newznab",
		Endpoint: conn.URL,
		APIKey:   conn.APIKey,
	}
	hit, ok := c.client.Lookup(ctx, []newznab.Indexer{idx}, newznab.Query{
		Filename:  q.Filename,
		Poster:    q.Poster,
		Group:     q.Group,
		PostedAt:  q.PostedAt,
		SizeBytes: q.SizeBytes,
	})
	return usenetsearch.NZBPasswordHit{Title: hit.Title, Passwords: hit.Passwords, Indexer: hit.Indexer}, ok
}
