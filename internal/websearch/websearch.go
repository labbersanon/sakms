// Package websearch is a provider-agnostic web search client used by Adult
// Identify and Movies/Series Rename Phase 2 grounding.
package websearch

import (
	"context"

	"github.com/labbersanon/sakms/internal/bravesearch"
)

// Result is one search hit. Shape matches bravesearch.Result / identify.SearchSnippet.
type Result struct {
	Title       string
	Description string
	URL         string
}

// Client searches the web. Nil clients are skipped by Failover.
type Client interface {
	Search(ctx context.Context, query string, count int) ([]Result, error)
	Ping(ctx context.Context) error
}

// Brave adapts *bravesearch.Client to websearch.Client.
type Brave struct {
	Inner *bravesearch.Client
}

func (b Brave) Search(ctx context.Context, query string, count int) ([]Result, error) {
	if b.Inner == nil {
		return nil, nil
	}
	raw, err := b.Inner.Search(ctx, query, count)
	if err != nil {
		return nil, err
	}
	out := make([]Result, len(raw))
	for i, r := range raw {
		out[i] = Result{Title: r.Title, Description: r.Description, URL: r.URL}
	}
	return out, nil
}

func (b Brave) Ping(ctx context.Context) error {
	if b.Inner == nil {
		return nil
	}
	return b.Inner.Ping(ctx)
}

// Failover tries Clients in order; returns the first non-empty success.
// Soft-fails to empty results when every client errors or returns nothing.
//
// Claude 2026-08-05: primary/fallback for SearXNG + Brave
// Reason: deep-interview-searxng-websearch
// Troubleshooting: Brave 402 must not block SearXNG when SearXNG is primary or fallback
// Review if: hard-fail preferred for misconfigured primary
type Failover struct {
	Clients []Client
}

func (f *Failover) Search(ctx context.Context, query string, count int) ([]Result, error) {
	if f == nil {
		return nil, nil
	}
	for _, c := range f.Clients {
		if c == nil {
			continue
		}
		res, err := c.Search(ctx, query, count)
		if err != nil || len(res) == 0 {
			continue
		}
		return res, nil
	}
	return nil, nil
}

func (f *Failover) Ping(ctx context.Context) error {
	if f == nil {
		return nil
	}
	var last error
	for _, c := range f.Clients {
		if c == nil {
			continue
		}
		if err := c.Ping(ctx); err == nil {
			return nil
		} else {
			last = err
		}
	}
	return last
}
