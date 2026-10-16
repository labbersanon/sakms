package usenetsearch

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/quality"
	"github.com/labbersanon/sakms/internal/release"
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
//
// Claude 2026-10-15: crawl-time quality admission (approved plan
// .omc/plans/nntp-crawl-quality-admission.md). AdmissionEnabled==false (the
// zero value) or a nil Admitter reproduces the pre-admission upsert-
// everything behavior byte-for-byte — Service only sets these when the
// usenet_nntp_admission_enabled kill switch is on and a policy is wired.
// Troubleshooting: crawlGroup's admitChunk helper carries the whole
//   bucket→gate→STAT→upsert flow; admission.go holds the pure decisions.
// Review if: a second Crawler construction site (tests aside) needs the
//   same fields wired differently.
type Crawler struct {
	Src    usenet.HeaderSource
	Index  *Index
	Groups []string
	Window time.Duration
	MaxGB  int

	AdmissionEnabled bool
	// GroupModesFn returns the enabled modes a group is crawled under
	// ("movies"/"series"/"adult", a subset) — PolicySnapshot.Resolve's
	// groupModes input. nil is treated as "no modes" (admission always
	// falls through to loosest-of mode defaults with an empty Modes map).
	GroupModesFn func(group string) []string
	Admitter     *admitter

	// RepairEnabled turns on OVER gap capture into usenet_nntp_missed_ranges
	// (usenet_nntp_repair_enabled). false = today's behavior (gaps ignored).
	RepairEnabled bool

	// NameFixEnabled stages obfuscated binary sets (release_name "obf:<key>")
	// instead of dropping them (usenet_nntp_namefix_enabled). false = today's
	// behavior. UnnamedMaxPct caps staged bytes as a share of the index budget
	// (0 = DefaultUnnamedMaxPct).
	NameFixEnabled bool
	UnnamedMaxPct  int

	// curDir/curGaps/curStaged/curSkipObfCap describe the chunk ingestRange is
	// processing; admitChunk reads them for its aggregate log line. A Crawler
	// is single-goroutine.
	curDir        string
	curGaps       int
	curStaged     int
	curSkipObfCap int
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
		if _, err := c.ingestRange(ctx, ingestOpts{Dir: dirForward, CaptureGaps: c.RepairEnabled}, group, a, b); err != nil {
			return advanced, err
		}
		if err := c.Index.SetWatermark(ctx, group, b); err != nil {
			return advanced, err
		}
		if wm == 0 && !advanced {
			// Claude 2026-10-15: a brand-new group's backfill cursor starts at the
			// first forward chunk (plan D6); it only ever moves down from here.
			if err := c.Index.EnsureLow(ctx, group, from); err != nil {
				return advanced, err
			}
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

// pruneGroupNonMatching deletes every already-indexed release in group that
// fails the quality gate under every candidate mode — the one-shot
// admission-backfill prune (locked decision #8), called once per group via
// Service.pruneNonMatchingOnce. No STAT: pruning is quality-only, so a
// release that would otherwise need a sampled STAT is left alone here (it
// gets re-evaluated, STAT included, the next time its range is OVERed —
// which for already-indexed rows is never, since the watermark already
// passed them; those rows simply stay unless a future crawl pass naturally
// revisits the group, e.g. after a watermark reset).
func pruneGroupNonMatching(ctx context.Context, idx *Index, group string, groupModes []string, snap *PolicySnapshot) (int64, error) {
	names, err := idx.DistinctReleases(ctx, group)
	if err != nil {
		return 0, err
	}
	var reject []string
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		info := release.Parse(name)
		stds, _ := snap.Resolve(group, groupModes, name)
		passes := false
		for _, std := range stds {
			if ok, _ := gate(name, info, std); ok {
				passes = true
				break
			}
		}
		if !passes {
			reject = append(reject, name)
		}
	}
	if len(reject) == 0 {
		return 0, nil
	}
	return idx.DeleteReleases(ctx, group, reject)
}

// chunkStats accumulates admitChunk's per-chunk aggregate counters (plan
// "Per-chunk aggregate count log", locked decision #7 — counts only, no
// per-item reject logging).
type chunkStats struct {
	rows         int
	releases     int
	admitted     int
	skipUnparsed int
	skipRes      int
	skipTier     int
	skipStat     int
	statCalls    int
	deferred     int
}

// admitChunk runs the plan's bucket → gate → STAT → upsert flow over one
// OVER chunk's headers, returning only the rows that are admitted (kept).
// Meta/PAR2 rows follow their release's verdict (plan: "Meta/PAR2 rows
// share the release's ReleaseName").
func (c *Crawler) admitChunk(ctx context.Context, group string, a, b int, headers []HeaderRow) ([]HeaderRow, error) {
	start := time.Now()
	st := chunkStats{rows: len(headers)}

	var groupModes []string
	if c.GroupModesFn != nil {
		groupModes = c.GroupModesFn(group)
	}
	snap, err := c.Admitter.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if snap == nil {
		// No policy wired despite AdmissionEnabled (defensive) — legacy upsert.
		return headers, nil
	}

	buckets := map[string][]HeaderRow{}
	order := make([]string, 0, len(headers))
	for _, h := range headers {
		if h.ReleaseName == "" || h.Obfuscated {
			st.skipUnparsed++
			continue
		}
		if _, ok := buckets[h.ReleaseName]; !ok {
			order = append(order, h.ReleaseName)
		}
		buckets[h.ReleaseName] = append(buckets[h.ReleaseName], h)
	}
	st.releases = len(order)

	// Claude 2026-10-15: one query covers every release bucketed in this
	// chunk (plan: "1 query/chunk" for the "already indexed" short-circuit).
	existing, err := c.Index.ExistingReleases(ctx, group, order)
	if err != nil {
		return nil, err
	}

	statBudget := statReleasesPerChunk
	busy := false
	var kept []HeaderRow

	for _, name := range order {
		rows := buckets[name]
		info := release.Parse(name)
		// MatchInfo (the release-wide key/year/episode classification) is
		// not separately logged — std.Match (per-mode outcome) already
		// carries what the admit log lines need.
		stds, _ := snap.Resolve(group, groupModes, name)

		passIdx, rejectReason := -1, ""
		for i, std := range stds {
			if ok, reason := gate(name, info, std); ok {
				passIdx = i
				break
			} else if rejectReason == "" {
				// Claude 2026-10-15: when every candidate mode rejects, the
				// aggregate counter uses the FIRST candidate's reason only
				// (deterministic by groupModes/classifyMode order) — the
				// plan's locked decision #2 explicitly forbids a per-item
				// log, so no reader ever sees which mode "won" a multi-
				// reject; the aggregate skip_res/skip_tier split is still
				// accurate for the common single-candidate-mode case.
				rejectReason = reason
			}
		}
		if passIdx < 0 {
			if rejectReason == reasonTierOutOfBand {
				st.skipTier++
			} else {
				st.skipRes++
			}
			continue
		}
		std := stds[passIdx]

		keep, label, fresh := c.statVerdict(ctx, group, name, rows, existing[name], &statBudget, &busy, &st)

		if !keep {
			st.skipStat++
			if fresh {
				logAdmitSkipStat(group, name, std, label, rows)
			}
			continue
		}
		kept = append(kept, rows...)
		st.admitted++
		if fresh {
			logAdmit(group, name, std, label, rows)
		}
	}

	dir := c.curDir
	if dir == "" {
		dir = dirForward
	}
	log.Printf("usenetsearch: crawl chunk dir=%s group=%s range=%d-%d rows=%d releases=%d admitted=%d staged_obf=%d skip_obf_cap=%d skip_unparsed=%d skip_res=%d skip_tier=%d skip_stat=%d stat_calls=%d deferred=%d gaps=%d elapsed_ms=%d",
		dir, group, a, b, st.rows, st.releases, st.admitted, c.curStaged, c.curSkipObfCap, st.skipUnparsed, st.skipRes, st.skipTier, st.skipStat, st.statCalls, st.deferred, c.curGaps, time.Since(start).Milliseconds())

	return kept, nil
}

// statVerdict decides keep/label for one quality-matching release: an
// already-indexed shortcut, a cached STAT verdict from an earlier chunk in
// this pass, a fresh sampled STAT (budget/busy permitting), or deferred.
// fresh reports whether this call just computed the verdict (vs. reusing
// existing/cached) — only a fresh verdict gets an admit/admit-skip-stat log
// line (plan: "first sighting in this process only"; existing/cached are,
// by construction, not the first sighting — see admitter.go's doc comment).
func (c *Crawler) statVerdict(ctx context.Context, group, name string, rows []HeaderRow, isExisting bool, statBudget *int, busy *bool, st *chunkStats) (keep bool, label string, fresh bool) {
	if isExisting {
		return true, "existing", false
	}
	if cachedKeep, cachedLabel, ok := c.Admitter.getVerdict(group, name); ok {
		return cachedKeep, cachedLabel, false
	}
	if *busy || *statBudget <= 0 {
		st.deferred++
		return true, "deferred", true
	}
	*statBudget--

	sample := pickSample(rows)
	results := make([]usenet.StatVerdict, 0, len(sample))
	for _, msgid := range sample {
		v, serr := c.Src.StatArticleAll(ctx, msgid)
		st.statCalls++
		if errors.Is(serr, usenet.ErrBusy) {
			*busy = true
			st.deferred++
			return true, "deferred", true
		}
		results = append(results, v)
		if len(sample) > 1 {
			select {
			case <-ctx.Done():
				return true, "deferred", true
			case <-time.After(statPause):
			}
		}
	}
	keep, label = statDecision(results)
	c.Admitter.putVerdict(group, name, keep, label)
	return keep, label, true
}

func logAdmit(group, name string, std Standard, label string, rows []HeaderRow) {
	info := release.Parse(name)
	low, high := bandBounds(std.Tiers)
	payloadRows, lowMsg, highMsg := rowStats(rows)
	log.Printf("usenetsearch: admit group=%s release=%s mode=%s policy=%s tmdb=%d match=%s res=%d source=%s codec=%s tier=%s band=%s-%s min_res=%d max_res=%d rows=%d payload_rows=%d msgs=%d-%d stat=%s",
		group, name, std.Mode, std.Policy, std.TMDBID, std.Match, info.Resolution, orUnknown(info.Source), orUnknown(info.Codec), tierLabel(info), low, high, std.MinRes, std.MaxRes, len(rows), payloadRows, lowMsg, highMsg, label)
}

func logAdmitSkipStat(group, name string, std Standard, label string, rows []HeaderRow) {
	info := release.Parse(name)
	sample := pickSample(rows)
	log.Printf("usenetsearch: admit-skip-stat group=%s release=%s mode=%s policy=%s tmdb=%d match=%s res=%d source=%s tier=%s rows=%d stat=%s sample=%s",
		group, name, std.Mode, std.Policy, std.TMDBID, std.Match, info.Resolution, orUnknown(info.Source), tierLabel(info), len(rows), label, strings.Join(sample, ","))
}

func tierLabel(info release.Info) string {
	if tier, ok := quality.InferTier(info); ok {
		return string(tier)
	}
	return "unknown"
}

func bandBounds(tiers []quality.Tier) (low, high string) {
	if len(tiers) == 0 {
		return "unknown", "unknown"
	}
	return string(quality.Lowest(tiers)), string(quality.Highest(tiers))
}

func rowStats(rows []HeaderRow) (payloadRows, lowMsg, highMsg int) {
	first := true
	for _, r := range rows {
		if !r.IsMeta {
			payloadRows++
		}
		if first || r.MsgNum < lowMsg {
			lowMsg = r.MsgNum
		}
		if first || r.MsgNum > highMsg {
			highMsg = r.MsgNum
		}
		first = false
	}
	return payloadRows, lowMsg, highMsg
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
