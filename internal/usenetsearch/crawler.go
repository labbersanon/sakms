package usenetsearch

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/usenet"
)

const overviewChunk = 2000

// Claude 2026-10-15: conservative inter-chunk pause (protect downloads).
// Reason: continuous crawl must share the pool; 250ms keeps OVER from saturating.
// Troubleshooting: downloads stall during catch-up — raise chunkPause / busyBackoff.
// Review if: rate becomes a Settings knob.
const chunkPause = 250 * time.Millisecond

// CrawlResult summarizes one continuous crawl pass.
type CrawlResult struct {
	Advanced bool // ingested at least one OVER chunk
	Busy     bool // yielded on ErrBusy
}

// Crawler incrementally OVERs configured groups into the Index.
type Crawler struct {
	Src    usenet.HeaderSource
	Index  *Index
	Groups []string
	Window time.Duration
	MaxGB  int
}

// CrawlOnce advances watermarks for every configured group.
func (c *Crawler) CrawlOnce(ctx context.Context) (CrawlResult, error) {
	var res CrawlResult
	if c.Src == nil || c.Index == nil {
		return res, errors.New("usenetsearch: crawler not configured")
	}
	for _, group := range c.Groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		adv, err := c.crawlGroup(ctx, group)
		if err != nil {
			if errors.Is(err, usenet.ErrBusy) {
				log.Printf("usenetsearch: crawl yield (busy) on %s", group)
				res.Busy = true
				return res, nil
			}
			log.Printf("usenetsearch: crawl %s: %v", group, err)
			continue
		}
		if adv {
			res.Advanced = true
		}
	}
	// Claude 2026-10-15: age prune disabled as a hard delete.
	// Reason: continuous index plan — never drop still-available articles for
	//   age; window_days is soft budget only. Removals are STAT liveness.
	// Troubleshooting: old PruneOlderThan wiped retained headers by posted_at.
	// Review if: soft age demotion (search rank) is added without DELETE.
	// if c.Window > 0 {
	// 	cutoff := time.Now().Add(-c.Window).Unix()
	// 	if _, err := c.Index.PruneOlderThan(ctx, cutoff); err != nil {
	// 		log.Printf("usenetsearch: prune window: %v", err)
	// 	}
	// }
	if c.MaxGB > 0 {
		if err := c.Index.PruneOldest(ctx, int64(c.MaxGB)*1024*1024*1024); err != nil {
			log.Printf("usenetsearch: prune size: %v", err)
		}
	}
	return res, nil
}

func (c *Crawler) crawlGroup(ctx context.Context, group string) (advanced bool, err error) {
	gr, err := c.Src.GroupRange(ctx, group)
	if err != nil {
		return false, err
	}
	wm, err := c.Index.Watermark(ctx, group)
	if err != nil {
		return false, err
	}
	from := wm + 1
	if from < gr.Low {
		from = gr.Low
	}
	// First crawl: don't try to index the entire retention — start near high.
	if wm == 0 {
		span := overviewChunk * 50 // ~100k articles first pass
		from = gr.High - span + 1
		if from < gr.Low {
			from = gr.Low
		}
	}
	if from > gr.High {
		if wm != gr.High {
			if err := c.Index.SetWatermark(ctx, group, gr.High); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	for a := from; a <= gr.High; a += overviewChunk {
		if err := ctx.Err(); err != nil {
			return advanced, err
		}
		b := a + overviewChunk - 1
		if b > gr.High {
			b = gr.High
		}
		rows, err := c.Src.Overview(ctx, group, a, b)
		if err != nil {
			return advanced, err
		}
		headers := make([]HeaderRow, 0, len(rows))
		for _, r := range rows {
			ps := ParseSubject(r.Subject)
			headers = append(headers, HeaderRow{
				Group:       group,
				MsgNum:      r.Number,
				MsgID:       r.MessageID,
				Subject:     r.Subject,
				From:        r.From,
				PostedAt:    r.DateUnix,
				Bytes:       int64(r.Bytes),
				ReleaseName: ps.Name,
				PartN:       ps.PartN,
				PartM:       ps.PartM,
				Filename:    ps.Filename,
				YencSize:    ps.YencSize,
				Obfuscated:  ps.Obfuscated || !ps.OK,
				IsMeta:      ps.Meta || IsMetaSubject(r.Subject, ps.Filename),
			})
		}
		if err := c.Index.Upsert(ctx, headers); err != nil {
			return advanced, err
		}
		if err := c.Index.SetWatermark(ctx, group, b); err != nil {
			return advanced, err
		}
		advanced = true
		select {
		case <-ctx.Done():
			return advanced, ctx.Err()
		case <-time.After(chunkPause):
		}
	}
	return advanced, nil
}
