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

const (
	livenessBatchSize = 100
	statPause         = 50 * time.Millisecond // keeps downloads ahead of STAT traffic
)

// LivenessOnce STATs up to livenessBatchSize oldest headers and deletes those
// confirmed missing/removed. Returns how many were deleted. On ErrBusy, returns
// 0 without deleting (yield to downloads).
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
		found, serr := src.StatArticle(ctx, r.MsgID)
		if errors.Is(serr, usenet.ErrBusy) {
			log.Printf("usenetsearch: liveness yield (busy)")
			return 0, offset, nil
		}
		if serr != nil {
			// Transport/protocol failure — keep the row; advance past it.
			log.Printf("usenetsearch: liveness STAT %s: %v", r.MsgID, serr)
			continue
		}
		if !found {
			gone = append(gone, r.MsgID)
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
