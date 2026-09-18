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

// Crawler incrementally OVERs configured groups into the Index.
type Crawler struct {
	Src     usenet.HeaderSource
	Index   *Index
	Groups  []string
	Window  time.Duration
	MaxGB   int
}

// CrawlOnce advances watermarks for every configured group.
func (c *Crawler) CrawlOnce(ctx context.Context) error {
	if c.Src == nil || c.Index == nil {
		return errors.New("usenetsearch: crawler not configured")
	}
	for _, group := range c.Groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		if err := c.crawlGroup(ctx, group); err != nil {
			if errors.Is(err, usenet.ErrBusy) {
				log.Printf("usenetsearch: crawl yield (busy) on %s", group)
				return nil
			}
			log.Printf("usenetsearch: crawl %s: %v", group, err)
			continue
		}
	}
	if c.Window > 0 {
		cutoff := time.Now().Add(-c.Window).Unix()
		if _, err := c.Index.PruneOlderThan(ctx, cutoff); err != nil {
			log.Printf("usenetsearch: prune window: %v", err)
		}
	}
	if c.MaxGB > 0 {
		if err := c.Index.PruneOldest(ctx, int64(c.MaxGB)*1024*1024*1024); err != nil {
			log.Printf("usenetsearch: prune size: %v", err)
		}
	}
	return nil
}

func (c *Crawler) crawlGroup(ctx context.Context, group string) error {
	gr, err := c.Src.GroupRange(ctx, group)
	if err != nil {
		return err
	}
	wm, err := c.Index.Watermark(ctx, group)
	if err != nil {
		return err
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
		return c.Index.SetWatermark(ctx, group, gr.High)
	}
	for a := from; a <= gr.High; a += overviewChunk {
		if err := ctx.Err(); err != nil {
			return err
		}
		b := a + overviewChunk - 1
		if b > gr.High {
			b = gr.High
		}
		rows, err := c.Src.Overview(ctx, group, a, b)
		if err != nil {
			return err
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
			return err
		}
		if err := c.Index.SetWatermark(ctx, group, b); err != nil {
			return err
		}
	}
	return nil
}
