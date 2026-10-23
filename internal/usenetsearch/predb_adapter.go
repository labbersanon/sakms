package usenetsearch

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/usenetsearch/predb"
)

// PreDBService is the PreDBLookup backed by the local usenet_nntp_predb table
// (fed by the optional IRC client and by cached HTTP lookups). HTTP may be nil
// or unconfigured; the adapter is then local-only.
//
// Claude 2026-10-15: outages and malformed provider replies are misses that
// are NOT cached (only a well-formed "no such release" is cached, 30 days), so
// a provider blip can neither rename nor poison the miss cache.
// Troubleshooting: o2cli grep "predb http" for provider errors (logged at most
// once per 10 minutes); predb_http_enabled=false turns lookups off.
// Review if: a second HTTP provider is added behind the same interface.
type PreDBService struct {
	Store *predb.Store
	HTTP  *predb.HTTPClient

	mu        sync.Mutex
	lastWarn  time.Time
	warnedOff bool
}

func asHit(e predb.Entry, ok bool, err error) (PreDBHit, bool) {
	if err != nil || !ok {
		return PreDBHit{}, false
	}
	return PreDBHit{Title: e.Title, SizeBytes: e.SizeBytes}, true
}

// ByHash resolves a bare md5/sha1 subject against the local corpus only (an
// HTTP name API cannot reverse a hash).
func (p *PreDBService) ByHash(ctx context.Context, hash string) (PreDBHit, bool) {
	if p == nil || p.Store == nil {
		return PreDBHit{}, false
	}
	return asHit(p.Store.ByHash(ctx, hash))
}

// ByFilename resolves a PreDB inner/archive filename against the local table.
func (p *PreDBService) ByFilename(ctx context.Context, filename string) (PreDBHit, bool) {
	if p == nil || p.Store == nil {
		return PreDBHit{}, false
	}
	return asHit(p.Store.ByFilename(ctx, filename))
}

// Search is NNTmux match:prefiles against the local table (no HTTP).
func (p *PreDBService) Search(ctx context.Context, query string) (PreDBHit, bool) {
	if p == nil || p.Store == nil {
		return PreDBHit{}, false
	}
	return asHit(p.Store.Search(ctx, query))
}

// ByRequestID resolves an nZEDb/IRC request id against the local table.
func (p *PreDBService) ByRequestID(ctx context.Context, id int64, group string) (PreDBHit, bool) {
	if p == nil || p.Store == nil {
		return PreDBHit{}, false
	}
	return asHit(p.Store.ByRequestID(ctx, id, group))
}

// ByName confirms name locally, then via HTTP (rate limited, cached).
func (p *PreDBService) ByName(ctx context.Context, name string) (PreDBHit, bool) {
	if p == nil || p.Store == nil {
		return PreDBHit{}, false
	}
	if hit, ok := asHit(p.Store.ByTitle(ctx, name)); ok {
		return hit, true
	}
	if p.HTTP == nil {
		return PreDBHit{}, false
	}
	if miss, err := p.Store.RecentMiss(ctx, name, predb.MissTTL); err == nil && miss {
		return PreDBHit{}, false
	}
	e, found, err := p.HTTP.Lookup(ctx, name)
	if err != nil {
		p.warn(err)
		return PreDBHit{}, false
	}
	if !found {
		_ = p.Store.RecordMiss(ctx, name)
		return PreDBHit{}, false
	}
	if err := p.Store.Upsert(ctx, e, predb.SourceHTTP); err != nil {
		p.warn(err)
	}
	return PreDBHit{Title: e.Title, SizeBytes: e.SizeBytes}, true
}

func (p *PreDBService) warn(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if errors.Is(err, predb.ErrDisabled) {
		if !p.warnedOff {
			p.warnedOff = true
			log.Printf("usenetsearch: predb http provider not configured; HTTP lookups inert")
		}
		return
	}
	if time.Since(p.lastWarn) > 10*time.Minute {
		p.lastWarn = time.Now()
		log.Printf("usenetsearch: predb http lookup failed (treated as miss): %v", err)
	}
}
