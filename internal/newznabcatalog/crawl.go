package newznabcatalog

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/newznab"
)

const (
	// Interval is the minimum delay between catalog t=search calls on one
	// indexer. Grab search stays on its own 1s client. Two hosts still run
	// in parallel because they are independent APIs.
	Interval = 5 * time.Second
	// IdleRecheck is how often Run looks for a newly saved host after every
	// configured indexer has finished (or none are configured).
	IdleRecheck    = time.Minute
	rateLimitMin   = time.Minute
	rateLimitMax   = 10 * time.Minute
	zeroInsertStop = 3
)

// Run pages movies/series/adult on every configured Newznab host until each
// cursor is complete, then idles. It does not scrape HTML and does not fetch NZBs.
func Run(ctx context.Context, store *Store, conns *connections.Store, httpClient *http.Client) {
	if store == nil || conns == nil {
		return
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	client := &newznab.Client{HTTP: httpClient, Interval: Interval}
	for {
		if ctx.Err() != nil {
			return
		}
		idxs := indexersFromStore(ctx, conns)
		if len(idxs) == 0 {
			if !sleepCtx(ctx, IdleRecheck) {
				return
			}
			continue
		}
		var wg sync.WaitGroup
		for _, idx := range idxs {
			wg.Add(1)
			go func(idx newznab.Indexer) {
				defer wg.Done()
				crawlHost(ctx, store, client, idx)
			}(idx)
		}
		wg.Wait()
		if !sleepCtx(ctx, IdleRecheck) {
			return
		}
	}
}

func crawlHost(ctx context.Context, store *Store, client *newznab.Client, idx newznab.Indexer) {
	backoff := rateLimitMin
	for _, lane := range kinds {
		hardErrs := 0
		for {
			if ctx.Err() != nil {
				return
			}
			cur, err := store.Cursor(ctx, idx.Name, lane.Kind)
			if err != nil {
				log.Printf("newznab catalog: cursor indexer=%s kind=%s err=%v", idx.Name, lane.Kind, err)
				return
			}
			if cur.Complete {
				break
			}
			hits, err := client.SearchCatalog(ctx, idx, lane.Cat, cur.PageOffset, newznab.CatalogPageSize())
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if isRateLimit(err) {
					log.Printf("newznab catalog: indexer=%s kind=%s rate-limit backoff=%s", idx.Name, lane.Kind, backoff)
					cur.LastError = "rate limited"
					cur.UpdatedAt = time.Now().Unix()
					_ = store.SaveCursor(ctx, cur)
					if !sleepCtx(ctx, backoff) {
						return
					}
					if backoff < rateLimitMax {
						backoff *= 2
						if backoff > rateLimitMax {
							backoff = rateLimitMax
						}
					}
					continue
				}
				log.Printf("newznab catalog: indexer=%s kind=%s offset=%d err=%v", idx.Name, lane.Kind, cur.PageOffset, err)
				cur.LastError = trimErr(err)
				cur.UpdatedAt = time.Now().Unix()
				_ = store.SaveCursor(ctx, cur)
				hardErrs++
				if hardErrs >= 8 {
					break
				}
				if !sleepCtx(ctx, Interval) {
					return
				}
				continue
			}
			hardErrs = 0
			backoff = rateLimitMin
			inserted, err := store.InsertHits(ctx, lane.Kind, hits)
			if err != nil {
				log.Printf("newznab catalog: insert indexer=%s kind=%s err=%v", idx.Name, lane.Kind, err)
				return
			}
			cur.PageOffset += len(hits)
			cur.LastPageNew = inserted
			cur.LastError = ""
			cur.UpdatedAt = time.Now().Unix()
			if inserted == 0 {
				cur.ZeroStreak++
			} else {
				cur.ZeroStreak = 0
			}
			if len(hits) == 0 || len(hits) < newznab.CatalogPageSize() || cur.ZeroStreak >= zeroInsertStop {
				cur.Complete = true
			}
			if err := store.SaveCursor(ctx, cur); err != nil {
				log.Printf("newznab catalog: save cursor indexer=%s kind=%s err=%v", idx.Name, lane.Kind, err)
				return
			}
			log.Printf("newznab catalog: indexer=%s kind=%s offset=%d page=%d new=%d complete=%v",
				idx.Name, lane.Kind, cur.PageOffset, len(hits), inserted, cur.Complete)
			if cur.Complete {
				break
			}
		}
	}
}

func indexersFromStore(ctx context.Context, store *connections.Store) []newznab.Indexer {
	if store == nil {
		return nil
	}
	var out []newznab.Indexer
	for _, svc := range catalogServices {
		conn, err := store.Get(ctx, svc)
		if errors.Is(err, connections.ErrNotFound) || conn == nil {
			continue
		}
		if err != nil {
			log.Printf("newznab catalog: %s err=%v", svc, err)
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

func isRateLimit(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "429") ||
		strings.Contains(s, "rate limit") ||
		strings.Contains(s, "too many request") ||
		strings.Contains(s, "request limit")
}

func trimErr(err error) string {
	s := err.Error()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
