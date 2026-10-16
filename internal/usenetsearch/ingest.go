package usenetsearch

import (
	"context"
	"sort"

	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-15: one OVER-chunk ingest path shared by the forward crawl,
// backfill, and missed-range repair (plan D5/D6: "identical path").
// Reason: admission, staging, upsert, and gap capture must behave the same no
//   matter which direction produced the chunk, or a repair/backfill could
//   resurrect a release admission already rejected (Principle 3).
// Troubleshooting: rows ingested by repair/backfill look different from
//   forward rows → compare the dir= field of the "crawl chunk" log line.
// Review if: a fourth OVER caller appears.

// Chunk directions (log dir= field).
const (
	dirForward  = "forward"
	dirBackfill = "backfill"
	dirRepair   = "repair"
)

// maxGapRangesPerGroup caps the repair queue per group (oldest dropped).
const maxGapRangesPerGroup = 10_000

type ingestOpts struct {
	Dir string
	// CaptureGaps records OVER article-number holes in [a,b] as missed ranges.
	CaptureGaps bool
	// ExistingDirect upserts rows whose release key is already indexed/staged
	// without re-running admission (repair never overrides an existing verdict).
	ExistingDirect bool
}

// buildHeaders parses OVER rows into HeaderRows exactly as the crawl always has.
func (c *Crawler) buildHeaders(group string, rows []usenet.MessageOverview) []HeaderRow {
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
			FileN:       ps.FileN,
			FileM:       ps.FileM,
		})
	}
	return headers
}

// ingestRange OVERs [a,b] for group and runs parse → admission → upsert →
// gap capture. It never touches the forward watermark. Returns the number of
// OVER rows the server returned.
func (c *Crawler) ingestRange(ctx context.Context, opts ingestOpts, group string, a, b int) (int, error) {
	rows, err := c.Src.Overview(ctx, group, a, b)
	if err != nil {
		return 0, err
	}
	c.curDir = opts.Dir
	c.curGaps = 0
	var gaps [][2]int
	if opts.CaptureGaps {
		gaps = missingRanges(rows, a, b)
		c.curGaps = len(gaps)
	}
	headers := c.buildHeaders(group, rows)

	var direct []HeaderRow
	candidates := headers
	if opts.ExistingDirect && len(headers) > 0 {
		names := make([]string, 0, len(headers))
		seen := map[string]bool{}
		for _, h := range headers {
			if h.ReleaseName != "" && !seen[h.ReleaseName] {
				seen[h.ReleaseName] = true
				names = append(names, h.ReleaseName)
			}
		}
		existing, err := c.Index.ExistingReleases(ctx, group, names)
		if err != nil {
			return 0, err
		}
		candidates = candidates[:0:0]
		for _, h := range headers {
			if h.ReleaseName != "" && existing[h.ReleaseName] {
				direct = append(direct, h)
			} else {
				candidates = append(candidates, h)
			}
		}
	}

	kept := candidates
	if c.AdmissionEnabled && c.Admitter != nil {
		kept, err = c.admitChunk(ctx, group, a, b, candidates)
		if err != nil {
			return 0, err
		}
	}
	kept = append(kept, direct...)
	if err := c.Index.Upsert(ctx, kept); err != nil {
		return 0, err
	}
	if len(gaps) > 0 {
		if err := c.Index.AddMissedRanges(ctx, group, gaps, "gap"); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// missingRanges returns the contiguous article-number runs in [a,b] the
// server did not return. A long run stays one range (not split).
func missingRanges(rows []usenet.MessageOverview, a, b int) [][2]int {
	nums := make([]int, 0, len(rows))
	for _, r := range rows {
		if r.Number >= a && r.Number <= b {
			nums = append(nums, r.Number)
		}
	}
	sort.Ints(nums)
	var out [][2]int
	next := a
	for _, n := range nums {
		if n < next {
			continue
		}
		if n > next {
			out = append(out, [2]int{next, n - 1})
		}
		next = n + 1
	}
	if next <= b {
		out = append(out, [2]int{next, b})
	}
	return out
}
