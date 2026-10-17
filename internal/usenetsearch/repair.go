package usenetsearch

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-15: bounded missed-range repair (plan D5).
// Reason: OVER chunks return holes (lagging servers, cancels); NNTmux's
//   MissedPartHandler re-OVERs them up to 3 times. Ranges, not articles.
// Troubleshooting: "repair pass ... busy=true" every iteration → downloads
//   hold every slot (expected); ranges stuck → check attempts/next_try_at.
// Review if: repair needs its own connection budget.
// Related files: ingest.go (shared chunk path), ranges.go (queue storage)

// repairRangesPerPass bounds ranges processed per loop iteration.
const repairRangesPerPass = 20

// Claude 2026-10-16: yield the crawl loop even if 20 span ranges are due.
// Reason: each span can be 20k articles (10 OVER windows) with no log; 20 of
// those would look like another crash after the recompute hang.
// Troubleshooting: "repair pass ranges=N" every loop; if N stays 20 and crawl
// chunks vanish, this budget is not breaking the range loop.
// Review if: missed_ranges stays empty (then this never fires).
const repairMaxDuration = 20 * time.Second

// RepairResult summarizes one repair pass.
type RepairResult struct {
	Ranges        int
	RecoveredRows int
	Exhausted     int
	Advanced      bool
	Busy          bool
}

// RepairOnce re-OVERs due missed ranges. It never writes the forward `high`
// watermark and never calls DemoteForFailure/NoteSuccess. ErrBusy stops the
// pass (no attempt is charged); an empty OVER, or an error, charges an attempt.
func (c *Crawler) RepairOnce(ctx context.Context) (RepairResult, error) {
	var res RepairResult
	if c.Src == nil || c.Index == nil {
		return res, errors.New("usenetsearch: crawler not configured")
	}
	ranges, err := c.Index.DueMissedRanges(ctx, c.Groups, repairRangesPerPass)
	if err != nil {
		return res, err
	}
	deadline := time.Now().Add(repairMaxDuration)
	for i, r := range ranges {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if i > 0 && time.Now().After(deadline) {
			break
		}
		log.Printf("usenetsearch: repair range %s %d-%d", r.Group, r.Lo, r.Hi)
		got, err := c.repairRange(ctx, r)
		if errors.Is(err, usenet.ErrBusy) {
			res.Busy = true
			break
		}
		res.Ranges++
		if err != nil {
			log.Printf("usenetsearch: repair %s %d-%d: %v", r.Group, r.Lo, r.Hi, err)
			got = 0
		}
		if got > 0 {
			res.RecoveredRows += got
			res.Advanced = true
			if err := c.Index.DeleteMissedRange(ctx, r); err != nil {
				return res, err
			}
		} else {
			exhausted, err := c.Index.BumpMissedRange(ctx, r)
			if err != nil {
				return res, err
			}
			if exhausted {
				res.Exhausted++
			}
		}
		if i < len(ranges)-1 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(chunkPause):
			}
		}
	}
	if res.Ranges > 0 || res.Busy {
		log.Printf("usenetsearch: repair pass ranges=%d recovered_rows=%d exhausted=%d busy=%t",
			res.Ranges, res.RecoveredRows, res.Exhausted, res.Busy)
	}
	return res, nil
}

// repairRange OVERs one range in overviewChunk windows through the shared
// ingest path and returns the rows ingested. Gap capture is off: the range
// itself is the queue entry being worked.
func (c *Crawler) repairRange(ctx context.Context, r MissedRange) (int, error) {
	total := 0
	for a := r.Lo; a <= r.Hi; a += overviewChunk {
		b := a + overviewChunk - 1
		if b > r.Hi {
			b = r.Hi
		}
		n, err := c.ingestRange(ctx, ingestOpts{Dir: dirRepair, ExistingDirect: true}, r.Group, a, b)
		if err != nil {
			return total, err
		}
		total += n
		if a+overviewChunk <= r.Hi {
			select {
			case <-ctx.Done():
				return total, ctx.Err()
			case <-time.After(chunkPause):
			}
		}
	}
	return total, nil
}
