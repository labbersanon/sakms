package usenetsearch

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-15: continuous index liveness — STAT then delete only when gone.
// Reason: age prune must not drop still-available headers; removals are STAT-gated.
// Troubleshooting: ErrBusy skips the batch (downloads win); transport errors keep rows.
// Review if: STAT depth gets a settings knob beyond livenessBatchSize.
// Related files: headersource.go StatArticle, service.go loop, crawler.go
//
// Claude 2026-10-15: switched to StatArticleAll (all-pool) per the crawl-admission
// plan's locked decision #9.
// Reason: single-pool StatArticle deleted a header on ONE provider's lie (a false
//   430 while every other subscribed provider still served the article) — the same
//   false-positive risk the admission plan's sampled STAT exists to avoid.
// Troubleshooting: a StatInconclusive verdict (busy/transport problem anywhere)
//   keeps the row, same as a StatArticle error used to; only StatMissing deletes.
// Review if: liveness gets its own pool-exhaustion policy independent of admission.
const (
	livenessBatchSize = 100
	statPause         = 50 * time.Millisecond // keeps downloads ahead of STAT traffic
)

// LivenessOnce STATs up to livenessBatchSize oldest headers (across every
// subscribed pool, via StatArticleAll) and deletes those confirmed missing.
// Returns how many were deleted. On ErrBusy, returns 0 without deleting
// (yield to downloads).
func LivenessOnce(ctx context.Context, src usenet.HeaderSource, idx *Index, offset int) (deleted int64, nextOffset int, err error) {
	if src == nil || idx == nil {
		return 0, offset, errors.New("usenetsearch: liveness not configured")
	}
	rows, err := idx.ListOldestMsgIDs(ctx, livenessBatchSize, offset)
	if err != nil {
		return 0, offset, err
	}
	if len(rows) == 0 {
		return 0, 0, nil // wrap cursor
	}
	var gone []string
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return 0, offset, err
		}
		verdict, serr := src.StatArticleAll(ctx, r.MsgID)
		if errors.Is(serr, usenet.ErrBusy) {
			log.Printf("usenetsearch: liveness yield (busy)")
			return 0, offset, nil
		}
		switch verdict {
		case usenet.StatMissing:
			gone = append(gone, r.MsgID)
		case usenet.StatInconclusive:
			// Busy/transport problem on at least one pool — keep the row;
			// advance past it rather than risk a false delete.
			if serr != nil {
				log.Printf("usenetsearch: liveness STAT %s: %v", r.MsgID, serr)
			}
		}
		select {
		case <-ctx.Done():
			return 0, offset, ctx.Err()
		case <-time.After(statPause):
		}
	}
	n, err := idx.DeleteByMsgIDs(ctx, gone)
	if err != nil {
		return 0, offset, err
	}
	if n > 0 {
		log.Printf("usenetsearch: liveness removed %d unavailable headers", n)
	}
	next := offset + len(rows)
	if len(rows) < livenessBatchSize {
		next = 0
	}
	return n, next, nil
}
