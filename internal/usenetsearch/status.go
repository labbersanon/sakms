package usenetsearch

import (
	"context"

	"github.com/labbersanon/sakms/internal/usenet"
)

// CrawlChunkStat is the most recent OVER chunk the crawler logged.
type CrawlChunkStat struct {
	Dir       string `json:"dir"`
	Group     string `json:"group"`
	RangeLow  int    `json:"rangeLow"`
	RangeHigh int    `json:"rangeHigh"`
	Rows      int    `json:"rows"`
	Releases  int    `json:"releases"`
	Admitted  int    `json:"admitted"`
	StagedObf int    `json:"stagedObf"`
	SkipRes   int    `json:"skipRes"`
	SkipStat  int    `json:"skipStat"`
	Gaps      int    `json:"gaps"`
	ElapsedMs int64  `json:"elapsedMs"`
	AtUnixMs  int64  `json:"atUnixMs"`
}

// CrawlerStatus is the Dashboard snapshot of native NNTP crawl progress.
// HeadersBinary / Identified are non-pictures.* headers vs recovered/scene names.
type CrawlerStatus struct {
	Enabled            bool            `json:"enabled"`
	ProbeState         string          `json:"probeState"`
	ProbeDetail        string          `json:"probeDetail"`
	Groups             int             `json:"groups"`
	Headers            int64           `json:"headers"`
	IndexBytes         int64           `json:"indexBytes"`
	IndexMaxGB         int             `json:"indexMaxGb"`
	ReleasesSearchable int64           `json:"releasesSearchable"`
	ReleasesBelowGate  int64           `json:"releasesBelowGate"`
	PendingName        int64           `json:"pendingName"`
	MissedRanges       int64           `json:"missedRanges"`
	HeadersBinary      int64           `json:"headersBinary"`
	HeadersIdentified  int64           `json:"headersIdentified"`
	IdentifiedPct      int             `json:"identifiedPct"`
	LastPassBusy       bool            `json:"lastPassBusy"`
	LastPassAdvanced   bool            `json:"lastPassAdvanced"`
	LastChunk          *CrawlChunkStat `json:"lastChunk,omitempty"`
}

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

// CrawlerStatus reports probe, index size, gap-fill counters, and last chunk.
func (s *Service) CrawlerStatus(ctx context.Context) (CrawlerStatus, error) {
	s.mu.Lock()
	cfg := s.cfg
	idx := s.idx
	probe := s.probe
	lastPass := s.lastPass
	var lastChunk *CrawlChunkStat
	if s.lastChunk.Group != "" {
		cp := s.lastChunk
		lastChunk = &cp
	}
	s.mu.Unlock()

	out := CrawlerStatus{
		Enabled:          cfg.Enabled,
		ProbeState:       probe.State,
		ProbeDetail:      probe.Detail,
		Groups:           len(cfg.AllGroups()),
		IndexMaxGB:       cfg.IndexMaxGB,
		LastPassBusy:     lastPass.Busy,
		LastPassAdvanced: lastPass.Advanced,
		LastChunk:        lastChunk,
	}
	if out.IndexMaxGB <= 0 {
		out.IndexMaxGB = DefaultIndexMaxGB
	}
	if !cfg.Enabled || idx == nil {
		return out, nil
	}
	n, err := idx.CountHeaders(ctx)
	if err != nil {
		return out, err
	}
	out.Headers = n
	b, err := idx.ApproxBytes(ctx)
	if err != nil {
		return out, err
	}
	out.IndexBytes = b
	gf, err := s.GapFillStatus(ctx)
	if err != nil {
		return out, err
	}
	out.ReleasesSearchable = gf.ReleasesSearchable
	out.ReleasesBelowGate = gf.ReleasesBelowGate
	out.PendingName = gf.PendingName
	out.MissedRanges = gf.MissedRanges
	id, bin, err := idx.identifiedStats(ctx)
	if err != nil {
		return out, err
	}
	out.HeadersIdentified = id
	out.HeadersBinary = bin
	if bin > 0 {
		out.IdentifiedPct = int(id * 100 / bin)
	}
	return out, nil
}

func (idx *Index) identifiedStats(ctx context.Context) (identified, binary int64, err error) {
	err = idx.db.QueryRowContext(ctx, `
SELECT
  COUNT(*) FILTER (
    WHERE h.group_name NOT LIKE '%pictures%'
      AND (r.release_key IS NULL OR NOT (`+junkNameSQL+`))),
  COUNT(*) FILTER (
    WHERE h.group_name NOT LIKE '%pictures%' AND r.state='named' AND (`+identNameSQL+`))
FROM usenet_nntp_headers h
LEFT JOIN usenet_nntp_releases r ON r.group_name=h.group_name AND r.release_key=h.release_name`).Scan(&binary, &identified)
	return identified, binary, err
}
