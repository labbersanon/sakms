package usenetsearch

import (
	"context"

	"github.com/labbersanon/sakms/internal/usenet"
)

// BackfillGroupStatus is one group's backfill cursor for the status response.
// Target is the provider's lowest article from the last crawl pass (0 until
// the first pass of this process).
type BackfillGroupStatus struct {
	Group  string `json:"group"`
	Low    int    `json:"low"`
	Target int    `json:"target"`
	Done   bool   `json:"done"`
	Reason string `json:"reason"`
}

// GapFillStatus is the read-only counter set behind the NNTP settings GET.
type GapFillStatus struct {
	ReleasesSearchable int64                 `json:"releasesSearchable"`
	ReleasesBelowGate  int64                 `json:"releasesBelowGate"`
	PendingName        int64                 `json:"pendingName"`
	NamedBySource      map[string]int64      `json:"namedBySource"`
	MissedRanges       int64                 `json:"missedRanges"`
	Backfill           []BackfillGroupStatus `json:"backfill"`
}

// GapFillStatus reports index-derived gap-fill counters. The zero status is
// returned when the index is not open (native search disabled).
func (s *Service) GapFillStatus(ctx context.Context) (GapFillStatus, error) {
	s.mu.Lock()
	idx := s.idx
	ranges := s.ranges
	s.mu.Unlock()
	st := GapFillStatus{NamedBySource: map[string]int64{}, Backfill: []BackfillGroupStatus{}}
	if idx == nil {
		return st, nil
	}
	return idx.gapFillStatus(ctx, ranges)
}

func (idx *Index) gapFillStatus(ctx context.Context, ranges map[string]usenet.GroupRange) (GapFillStatus, error) {
	st := GapFillStatus{NamedBySource: map[string]int64{}, Backfill: []BackfillGroupStatus{}}
	err := idx.db.QueryRowContext(ctx, `
SELECT
  COUNT(*) FILTER (WHERE searchable),
  COUNT(*) FILTER (WHERE state='named' AND NOT searchable),
  COUNT(*) FILTER (WHERE state='pending_name')
FROM usenet_nntp_releases`).Scan(&st.ReleasesSearchable, &st.ReleasesBelowGate, &st.PendingName)
	if err != nil {
		return st, err
	}
	if err := idx.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usenet_nntp_missed_ranges`).Scan(&st.MissedRanges); err != nil {
		return st, err
	}
	rows, err := idx.db.QueryContext(ctx, `
SELECT name_source, COUNT(*) FROM usenet_nntp_releases
WHERE name_source <> '' AND state='named' GROUP BY name_source`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var src string
		var n int64
		if err := rows.Scan(&src, &n); err != nil {
			rows.Close()
			return st, err
		}
		st.NamedBySource[src] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return st, err
	}
	rows.Close()

	wrows, err := idx.db.QueryContext(ctx, `
SELECT group_name, low, backfill_done, backfill_reason FROM usenet_nntp_watermarks ORDER BY group_name`)
	if err != nil {
		return st, err
	}
	defer wrows.Close()
	for wrows.Next() {
		var b BackfillGroupStatus
		if err := wrows.Scan(&b.Group, &b.Low, &b.Done, &b.Reason); err != nil {
			return st, err
		}
		b.Target = ranges[b.Group].Low
		st.Backfill = append(st.Backfill, b)
	}
	return st, wrows.Err()
}
