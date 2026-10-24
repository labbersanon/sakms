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
	// idleRecheck is how often Run looks for a newly saved host after every
	// configured indexer has finished (or none are configured).
	idleRecheck = time.Minute
	// Claude 2026-10-23: NZB.life 429 Retry-After is 5s; error 500 is quota.
	// Reason: helpapi says wait ≥5s on 429; 500 is daily/hourly, not a 5s blip.
	// Troubleshooting: Cloudflare challenge HTML instead of XML → 429 path.
	// Review if: NZB.life publishes a different Retry-After.
	rateLimitMin   = 5 * time.Second
	rateLimitMax   = 10 * time.Minute
	quotaBackoff   = time.Hour
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
	// Claude 2026-10-23: rewrite overlap_key from NZB title once per process.
	// Reason: rows inserted under IMDB keys would keep title-overlap until recrawl.
	// Troubleshooting: Dock still shows same-title counts after this ship → recompute
	//   did not run (Run not started) or overlap_key still imdb:.
	// Review if: new inserts already use nzb: keys and no imdb: rows remain.
	if n, err := store.RecomputeOverlapKeys(ctx); err != nil {
		log.Printf("newznab catalog: recompute overlap keys err=%v", err)
	} else if n > 0 {
		log.Printf("newznab catalog: recomputed overlap keys n=%d", n)
	}
	// Claude 2026-10-24: backfill Dock overlap off the GET path, before crawl.
	// Reason: GET GROUP BY timed out at 120s on 1.3M rows; crawl must wait so
	//   InsertHits bumps cannot race this scan.
	// Troubleshooting: Same-NZBs empty after ship with catalog rows → this
	//   returned err (see log) or overlap table already had a stale row.
	// Review if: summary is incremental from empty (skip rebuild).
	start := time.Now()
	if err := store.EnsureOverlap(ctx); err != nil {
		log.Printf("newznab catalog: overlap backfill err=%v", err)
	} else {
		log.Printf("newznab catalog: overlap backfill done in %s", time.Since(start))
	}
	for {
		if ctx.Err() != nil {
			return
		}
		idxs := indexersFromStore(ctx, conns)
		if len(idxs) == 0 {
			if !sleepCtx(ctx, idleRecheck) {
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
		if !sleepCtx(ctx, idleRecheck) {
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
				if isQuota(err) {
					log.Printf("newznab catalog: indexer=%s kind=%s quota backoff=%s", idx.Name, lane.Kind, quotaBackoff)
					cur.LastError = "request limit reached"
					cur.UpdatedAt = time.Now().Unix()
					_ = store.SaveCursor(ctx, cur)
					if !sleepCtx(ctx, quotaBackoff) {
						return
					}
					continue
				}
				if isRateLimit(err) {
					wait := backoff
					if ra := newznab.RetryAfter(err); ra > wait {
						wait = ra
					}
					log.Printf("newznab catalog: indexer=%s kind=%s rate-limit backoff=%s", idx.Name, lane.Kind, wait)
					cur.LastError = "rate limited"
					cur.UpdatedAt = time.Now().Unix()
					_ = store.SaveCursor(ctx, cur)
					if !sleepCtx(ctx, wait) {
						return
					}
					backoff *= 2
					if backoff > rateLimitMax {
						backoff = rateLimitMax
					}
					continue
				}
				log.Printf("newznab catalog: indexer=%s kind=%s offset=%d err=%v", idx.Name, lane.Kind, cur.PageOffset, err)
				msg := err.Error()
				if len(msg) > 200 {
					msg = msg[:200]
				}
				cur.LastError = msg
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

func isQuota(err error) bool {
	var ae newznab.APIError
	if errors.As(err, &ae) && ae.Quota() {
		return true
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "request limit reached")
}

func isRateLimit(err error) bool {
	if err == nil || isQuota(err) {
		return false
	}
	var se *newznab.HTTPStatusError
	if errors.As(err, &se) && se.Status == http.StatusTooManyRequests {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "429") ||
		strings.Contains(s, "rate limit") ||
		strings.Contains(s, "too many request")
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
