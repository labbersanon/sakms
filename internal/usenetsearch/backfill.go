package usenetsearch

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"

	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-15: retention backfill (plan D6).
// Reason: the forward crawl only starts ~100k articles behind the group head,
//   so history (and the names/PAR2 sets namefix can recover) is never indexed.
//   Backfill walks a separate `low` cursor from the oldest indexed article down
//   to provider retention, through the SAME ingest path (parse -> staging /
//   admission+STAT -> upsert -> gap capture), under the same tryOnly OVER,
//   chunkPause, and ErrBusy rules as the forward crawl.
// Troubleshooting: backfill idle → o2cli grep "backfill group=" for state=; paused
//   reason=budget means the index is at backfill_budget_pct of index_max_gb
//   (resumes when liveness/TTL/prune free space); reason=admission_off means
//   admission is disabled (backfill would fill the budget with junk). Re-run a
//   finished group: UPDATE usenet_nntp_watermarks SET backfill_done=FALSE ...
// Review if: backfill needs its own connection budget or a per-group days limit.
// Related files: ingest.go, crawler.go (forward), service.go (loop wiring)

const (
	// backfillChunksPerPass bounds OVER chunks per loop iteration.
	backfillChunksPerPass = 25
	// DefaultBackfillBudgetPct pauses backfill at this share of index_max_gb.
	DefaultBackfillBudgetPct = 90

	backfillReasonRetention = "retention"
	backfillReasonDays      = "days"
	backfillReasonBudget    = "budget"
)

// BackfillResult summarizes one backfill pass.
type BackfillResult struct {
	Chunks   int
	Advanced bool
	Busy     bool
	// HasWork is true when at least one group can continue next iteration
	// (drives catchUpPause instead of idleBackoff).
	HasWork bool
	// Skipped is non-empty when the pass did not run (e.g. "admission_off").
	Skipped string
	// NextStart is the round-robin offset for the next pass.
	NextStart int
	// Filtered is how many crawl-eligible groups were skipped because they
	// are not a package core and have no named index (NNTmux backfill=1).
	Filtered int
}

type backfillGroup struct {
	group    string
	low      int
	target   int
	chunks   int
	finished bool // no more chunks this pass
	state    string
	reason   string
}

// backfillBudget is the index size at which backfill pauses (0 = unlimited).
func (c *Crawler) backfillBudget() int64 {
	if c.budgetOverride > 0 {
		return c.budgetOverride
	}
	if c.MaxGB <= 0 {
		return 0
	}
	pct := c.BackfillBudgetPct
	if pct <= 0 {
		pct = DefaultBackfillBudgetPct
	}
	return int64(c.MaxGB) * 1024 * 1024 * 1024 * int64(pct) / 100
}

func (c *Crawler) backfillWanted(ctx context.Context, group string) bool {
	// Claude 2026-10-16: while yield groups lag HIGH, do not backfill dvd/bloaf.
	// Reason: SkipListTail used to skip ALL backfill, so moovee/x264 history
	// (the RAR bodies of 80%+ pending sets) never ran; searchable stalled.
	// Troubleshooting: dir=backfill on bloaf while crawl skip LIST tail →
	// this filter is off. Review if: backfill is limited to staging cores.
	if c.SkipListTail && !IsNameFixYieldGroup(group) {
		return false
	}
	if IsPackageCore(group) {
		return true
	}
	ok, err := c.Index.GroupHasNamedRelease(ctx, group)
	return err == nil && ok
}

// BackfillOnce walks up to backfillChunksPerPass OVER chunks round-robin
// across groups that still have history below their `low` cursor. It never
// writes the forward `high` watermark. ErrBusy stops the pass (Busy); the
// cursor only moves after a chunk is fully ingested.
func (c *Crawler) BackfillOnce(ctx context.Context) (BackfillResult, error) {
	res := BackfillResult{NextStart: c.BackfillStart}
	if !c.BackfillEnabled {
		return res, nil
	}
	if c.Src == nil || c.Index == nil {
		return res, errors.New("usenetsearch: crawler not configured")
	}
	if !c.AdmissionEnabled || c.Admitter == nil {
		res.Skipped = "admission_off"
		return res, nil
	}

	var groups []*backfillGroup
	for _, g := range c.Groups {
		gr, ok := c.ranges[g]
		if !ok {
			continue
		}
		if !c.backfillWanted(ctx, g) {
			res.Filtered++
			continue
		}
		low, done, _, found, err := c.Index.BackfillState(ctx, g)
		if err != nil {
			return res, err
		}
		if !found || done || low <= 0 {
			continue
		}
		groups = append(groups, &backfillGroup{group: g, low: low, target: gr.Low, state: "running"})
	}
	if len(groups) == 0 {
		return res, nil
	}
	rot := c.BackfillStart % len(groups)
	groups = append(groups[rot:], groups[:rot]...)
	res.NextStart = c.BackfillStart + 1

	size, err := c.Index.ApproxBytes(ctx)
	if err != nil {
		return res, err
	}
	budget := c.backfillBudget()
	var cutoff int64
	if c.BackfillDays > 0 {
		cutoff = time.Now().Add(-time.Duration(c.BackfillDays) * 24 * time.Hour).Unix()
	}

	turns := 0
pass:
	for turns < backfillChunksPerPass {
		progressed := false
		for _, g := range groups {
			if turns >= backfillChunksPerPass {
				break
			}
			if g.finished {
				continue
			}
			if g.low <= g.target {
				c.finishBackfill(ctx, g, true, backfillReasonRetention)
				continue
			}
			if budget > 0 && size >= budget {
				c.finishBackfill(ctx, g, false, backfillReasonBudget)
				continue
			}
			a := g.low - overviewChunk
			if a < g.target {
				a = g.target
			}
			rows, err := c.ingestRange(ctx, ingestOpts{Dir: dirBackfill, CaptureGaps: c.RepairEnabled}, g.group, a, g.low-1)
			if errors.Is(err, usenet.ErrBusy) {
				res.Busy = true
				break pass
			}
			if err != nil {
				log.Printf("usenetsearch: backfill %s %d-%d: %v", g.group, a, g.low-1, err)
				g.finished = true
				continue
			}
			if err := c.Index.SetLow(ctx, g.group, a); err != nil {
				return res, err
			}
			g.low = a
			g.chunks++
			turns++
			res.Chunks++
			res.Advanced = true
			progressed = true
			size += int64(rows) * approxHeaderRowBytes
			if cutoff > 0 && c.curMaxPosted > 0 && c.curMaxPosted < cutoff {
				c.finishBackfill(ctx, g, true, backfillReasonDays)
			} else if g.low <= g.target {
				c.finishBackfill(ctx, g, true, backfillReasonRetention)
			}
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(chunkPause):
			}
		}
		if !progressed {
			break
		}
	}

	for _, g := range groups {
		if !g.finished && !res.Busy {
			res.HasWork = true
		}
		if g.chunks > 0 || g.state != "running" {
			log.Printf("usenetsearch: backfill group=%s low=%d target=%d chunks=%d state=%s reason=%s",
				g.group, g.low, g.target, g.chunks, g.state, g.reason)
		}
	}
	return res, nil
}

// finishBackfill records a terminal (done) or paused (budget) state.
func (c *Crawler) finishBackfill(ctx context.Context, g *backfillGroup, done bool, reason string) {
	g.finished = true
	g.reason = reason
	g.state = "paused"
	if done {
		g.state = "done"
	}
	if err := c.Index.SetBackfillState(ctx, g.group, done, reason); err != nil {
		log.Printf("usenetsearch: backfill state %s: %v", g.group, err)
	}
}

// BackfillState reads a group's backfill cursor. found=false when the group has
// no watermark row yet (the forward crawl creates it).
func (idx *Index) BackfillState(ctx context.Context, group string) (low int, done bool, reason string, found bool, err error) {
	err = idx.db.QueryRowContext(ctx,
		`SELECT low, backfill_done, backfill_reason FROM usenet_nntp_watermarks WHERE group_name=?`, group).Scan(&low, &done, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, "", false, nil
	}
	if err != nil {
		return 0, false, "", false, err
	}
	return low, done, reason, true, nil
}

// SetLow moves the backfill cursor down (never up) and clears a stale pause.
func (idx *Index) SetLow(ctx context.Context, group string, low int) error {
	_, err := idx.db.ExecContext(ctx,
		`UPDATE usenet_nntp_watermarks SET low=?, backfill_reason='' WHERE group_name=? AND (low=0 OR low>?)`,
		low, group, low)
	return err
}

// SetBackfillState records done/paused state for a group.
func (idx *Index) SetBackfillState(ctx context.Context, group string, done bool, reason string) error {
	_, err := idx.db.ExecContext(ctx,
		`UPDATE usenet_nntp_watermarks SET backfill_done=?, backfill_reason=? WHERE group_name=?`, done, reason, group)
	return err
}
