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

// Claude 2026-10-16: bound one forward CrawlOnce the same way BackfillOnce is bounded.
// Reason: LIST ACTIVE membership is hundreds of groups; a firehose bin
// (bloaf/ath/amazing) never reaches HIGH, so the old per-group-until-caught-up
// loop never visited moovee/teevee and never returned to namefix.
// Troubleshooting: o2cli grep "crawl chunk" stuck on one group with admitted=0 —
// check these caps and CrawlStart rotation, not membership.
// Review if: pass/group caps become Settings knobs.
// Related files: backfill.go (backfillChunksPerPass), service.go (crawlStart)
const (
	forwardChunksPerPass  = 25
	forwardChunksPerGroup = 8
)

// Claude 2026-10-16: skip LIST-tail OVER (and backfill) while a yield group
// lags provider HIGH by this many articles.
// Reason: each deploy drained a finite complete-pending queue then idle;
// dvd/bloaf OVER + BackfillOnce STAT burned the loop while x264/hdtv/tv
// sat ~1.2M behind HIGH at 0% complete — searchable bursted then stopped.
// Troubleshooting: o2cli grep "skip LIST tail" missing while bloaf/dvd
// crawl chunks continue and yield watermarks lag HIGH — this gate is off.
// Review if: coreCatchUpLag becomes a Settings knob.
const coreCatchUpLag = 50_000

// CrawlResult summarizes one continuous crawl pass.
type CrawlResult struct {
	Advanced bool // ingested at least one OVER chunk
	Busy     bool // yielded on ErrBusy
	// NextStart is the round-robin offset into non-core groups for the next
	// pass; NextCoreStart is the same for packages.json cores.
	NextStart     int
	NextCoreStart int
}

// Crawler incrementally OVERs configured groups into the Index.
//
// Claude 2026-10-15: crawl-time quality admission (approved plan
// .omc/plans/nntp-crawl-quality-admission.md). AdmissionEnabled==false (the
// zero value) or a nil Admitter reproduces the pre-admission upsert-
// everything behavior byte-for-byte — Service only sets these when the
// usenet_nntp_admission_enabled kill switch is on and a policy is wired.
// Troubleshooting: crawlGroup's admitChunk helper carries the whole
// bucket→gate→STAT→upsert flow; admission.go holds the pure decisions.
// Review if: a second Crawler construction site (tests aside) needs the
// same fields wired differently.
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
	// behavior. UnnamedMaxPct caps staged header occupancy as a share of the
	// index budget (0 = DefaultUnnamedMaxPct).
	NameFixEnabled bool
	UnnamedMaxPct  int

	// BackfillEnabled turns on the retention backfill (usenet_nntp_backfill_enabled).
	// BackfillDays limits history depth (0 = to provider retention);
	// BackfillBudgetPct pauses backfill at that share of MaxGB
	// (0 = DefaultBackfillBudgetPct). BackfillStart is the loop-owned
	// round-robin offset.
	BackfillEnabled   bool
	BackfillDays      int
	BackfillBudgetPct int
	BackfillStart     int

	// CrawlStart / CoreStart are loop-owned round-robin offsets into the
	// LIST-tail and name-fix-yield slices. MaxChunksPerPass /
	// MaxChunksPerGroup override the forward caps (0 = defaults); tests set
	// them small so a firehose group cannot eat the fixture.
	CrawlStart        int
	CoreStart         int
	MaxChunksPerPass  int
	MaxChunksPerGroup int
	// SkipListTail is set by CrawlOnce when a yield group is still
	// coreCatchUpLag+ articles behind HIGH. LIST-tail OVER is skipped;
	// backfill still runs on yield groups only.
	SkipListTail bool
	// obfStageOK caches per-group "may stage new obfuscated sets" for this Crawler.
	obfStageOK map[string]bool

	// ranges holds each group's last GroupRange (set by crawlGroup); backfill
	// uses Low as its target. budgetOverride is a test hook.
	ranges         map[string]usenet.GroupRange
	budgetOverride int64

	// curDir/curGaps/curStaged/curSkipObfCap/curSkipStub/curEmpty/curMaxPosted
	// describe the chunk ingestRange is processing; admitChunk and backfill
	// read them. A Crawler is single-goroutine.
	curDir        string
	curGaps       int
	curStaged     int
	curSkipObfCap int
	curSkipStub   int
	curEmpty      bool
	curMaxPosted  int64
	// OnChunk, if set, is invoked after each OVER chunk is admitted (Dashboard).
	OnChunk func(CrawlChunkStat)

	// LanguageFilter applies Settings preferred grab languages (empty list =
	// english-assumed, same as autograb). false = tests / unwired Service.
	LanguageFilter     bool
	PreferredLanguages []string
}

func (c *Crawler) forwardBudget() (pass, perGroup int) {
	pass = c.MaxChunksPerPass
	if pass <= 0 {
		pass = forwardChunksPerPass
	}
	perGroup = c.MaxChunksPerGroup
	if perGroup <= 0 {
		perGroup = forwardChunksPerGroup
	}
	return pass, perGroup
}

func rotateGroups(groups []string, start int) []string {
	n := len(groups)
	if n == 0 {
		return groups
	}
	rot := start % n
	if rot < 0 {
		rot = 0
	}
	return append(append([]string{}, groups[rot:]...), groups[:rot]...)
}

func splitCoreRest(groups []string) (cores, rest []string) {
	for _, g := range groups {
		if IsStagingCore(g) {
			cores = append(cores, g)
		} else {
			rest = append(rest, g)
		}
	}
	return cores, rest
}

// Claude 2026-10-16: crawl 2/3 share is yield groups, not staging cores.
// Reason: tv/anime/highspeed complete-pending sat in the LIST tail (step 3–5)
// while boneless/dvd took the rest third; searchable bursted then stopped.
// splitCoreRest stays IsStagingCore so DuePendingNames still fills moovee
// before anime. Troubleshooting: crawl chunks on bloaf/dvd while tv
// watermark lags HIGH — this split must put yield groups in the core share.
// Review if: StagingCoreMaxStep is raised to 4 (then this matches again).
func splitYieldRest(groups []string) (yield, rest []string) {
	for _, g := range groups {
		if IsNameFixYieldGroup(g) {
			yield = append(yield, g)
		} else {
			rest = append(rest, g)
		}
	}
	return yield, rest
}

func (c *Crawler) shouldBackfill() bool {
	return c.BackfillEnabled
}

// yieldGroupsCatchingUp reports whether any name-fix yield group still has
// forward OVER work of coreCatchUpLag or more articles.
func (c *Crawler) yieldGroupsCatchingUp(ctx context.Context, cores []string) bool {
	for _, g := range cores {
		gr, err := c.Src.GroupRange(ctx, g)
		if err != nil {
			continue
		}
		if c.ranges == nil {
			c.ranges = map[string]usenet.GroupRange{}
		}
		c.ranges[g] = gr
		wm, err := c.Index.Watermark(ctx, g)
		if err != nil {
			continue
		}
		if forwardLag(wm, gr) >= coreCatchUpLag {
			return true
		}
	}
	return false
}

// crawlList OVERs list (already language-filtered) until budget chunks, round-robin from start.
func (c *Crawler) crawlList(ctx context.Context, list []string, start, budget, groupCap int) (adv, busy bool) {
	if len(list) == 0 || budget <= 0 {
		return false, false
	}
	rotated := rotateGroups(list, start)
	used := 0
	empty := map[string]bool{}
	for used < budget {
		progressed := false
		for _, group := range rotated {
			if used >= budget {
				break
			}
			if empty[group] {
				continue
			}
			n, a, err := c.crawlGroup(ctx, group, min(groupCap, budget-used))
			if err != nil {
				if errors.Is(err, usenet.ErrBusy) {
					log.Printf("usenetsearch: crawl yield (busy) on %s", group)
					return adv, true
				}
				log.Printf("usenetsearch: crawl %s: %v", group, err)
				continue
			}
			if c.curEmpty {
				empty[group] = true
			}
			used += n
			progressed = progressed || n > 0
			adv = adv || a
		}
		if !progressed {
			break
		}
	}
	return adv, false
}

// CrawlOnce advances watermarks round-robin across configured groups, up to
// forwardChunksPerPass OVER chunks (forwardChunksPerGroup per group). Name-fix
// yield groups get 2/3 of the pass when mixed with the LIST tail; when any of
// them is still coreCatchUpLag behind HIGH, they get the whole pass and the
// LIST tail is skipped (NNTmux: spend header budget on groups you index).
func (c *Crawler) CrawlOnce(ctx context.Context) (CrawlResult, error) {
	var res CrawlResult
	c.SkipListTail = false
	if c.Src == nil || c.Index == nil {
		return res, errors.New("usenetsearch: crawler not configured")
	}
	var groups []string
	for _, group := range c.Groups {
		group = strings.TrimSpace(group)
		if group == "" || !c.groupLanguageOK(group) {
			continue
		}
		groups = append(groups, group)
	}
	if len(groups) == 0 {
		return res, nil
	}
	cores, rest := splitYieldRest(groups)
	res.NextStart = c.CrawlStart
	res.NextCoreStart = c.CoreStart
	passCap, groupCap := c.forwardBudget()
	var coreBudget, restBudget int
	switch {
	case len(rest) == 0:
		coreBudget = passCap
	case len(cores) == 0:
		restBudget = passCap
	case c.yieldGroupsCatchingUp(ctx, cores):
		c.SkipListTail = true
		coreBudget = passCap
		log.Printf("usenetsearch: crawl skip LIST tail (yield groups catching up, lag>=%d)", coreCatchUpLag)
	default:
		coreBudget = max(1, passCap*2/3)
		restBudget = passCap - coreBudget
	}
	if len(rest) > 0 && restBudget > 0 {
		res.NextStart = c.CrawlStart + 1
	}
	if len(cores) > 0 {
		res.NextCoreStart = c.CoreStart + 1
	}
	adv, busy := c.crawlList(ctx, cores, c.CoreStart, coreBudget, groupCap)
	res.Advanced = adv
	if busy {
		res.Busy = true
		return res, nil
	}
	adv, busy = c.crawlList(ctx, rest, c.CrawlStart, restBudget, groupCap)
	res.Advanced = res.Advanced || adv
	if busy {
		res.Busy = true
		return res, nil
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

// forwardFrom is the next article CrawlOnce will OVER in group (wm==0 starts
// ~100k behind HIGH so a new group does not walk full retention).
func forwardFrom(wm int, gr usenet.GroupRange) int {
	from := wm + 1
	if from < gr.Low {
		from = gr.Low
	}
	if wm == 0 {
		span := overviewChunk * 50 // ~100k articles first pass
		from = gr.High - span + 1
		if from < gr.Low {
			from = gr.Low
		}
	}
	return from
}

func forwardLag(wm int, gr usenet.GroupRange) int {
	from := forwardFrom(wm, gr)
	if from > gr.High {
		return 0
	}
	return gr.High - from + 1
}

func (c *Crawler) crawlGroup(ctx context.Context, group string, maxChunks int) (chunks int, advanced bool, err error) {
	c.curEmpty = false
	gr, err := c.Src.GroupRange(ctx, group)
	if err != nil {
		return 0, false, err
	}
	if c.ranges == nil {
		c.ranges = map[string]usenet.GroupRange{}
	}
	c.ranges[group] = gr
	wm, err := c.Index.Watermark(ctx, group)
	if err != nil {
		return 0, false, err
	}
	from := forwardFrom(wm, gr)
	if from > gr.High {
		if wm != gr.High {
			if err := c.Index.SetWatermark(ctx, group, gr.High); err != nil {
				return 0, false, err
			}
		}
		return 0, false, nil
	}
	for a := from; a <= gr.High; a += overviewChunk {
		if chunks >= maxChunks {
			log.Printf("usenetsearch: crawl %s: paused after %d chunks (high=%d watermark=%d)", group, chunks, gr.High, a-1)
			return chunks, advanced, nil
		}
		if err := ctx.Err(); err != nil {
			return chunks, advanced, err
		}
		b := a + overviewChunk - 1
		if b > gr.High {
			b = gr.High
		}
		if _, err := c.ingestRange(ctx, ingestOpts{Dir: dirForward, CaptureGaps: c.RepairEnabled}, group, a, b); err != nil {
			return chunks, advanced, err
		}
		if err := c.Index.SetWatermark(ctx, group, b); err != nil {
			return chunks, advanced, err
		}
		if wm == 0 && !advanced {
			// Claude 2026-10-15: a brand-new group's backfill cursor starts at the
			// first forward chunk (plan D6); it only ever moves down from here.
			if err := c.Index.EnsureLow(ctx, group, from); err != nil {
				return chunks, advanced, err
			}
		}
		chunks++
		advanced = true
		// Claude 2026-10-16: one empty OVER is enough for this group this pass.
		// Reason: teevee/moovee were burning groupCap chunks at 100% skip_obf_cap
		//   and dvd/amazing at 100% skip_unparsed; the watermark still advances.
		// Troubleshooting: crawl chunk logs skip_*=rows admitted=0 staged_obf=0
		//   then the next line is a different group — this break.
		// Review if: a group with rare named posts mixed into noise needs more
		//   than one probe chunk per pass.
		if c.curEmpty {
			log.Printf("usenetsearch: crawl %s: empty chunk, skip rest of group this pass (high=%d watermark=%d)", group, gr.High, b)
			return chunks, advanced, nil
		}
		select {
		case <-ctx.Done():
			return chunks, advanced, ctx.Err()
		case <-time.After(chunkPause):
		}
	}
	return chunks, advanced, nil
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
		// Claude 2026-10-16: drop already-indexed hash-token "names".
		// Reason: TOKEN [k/N] teevee collections passed the quality gate
		// (min_res=0, unknown source) as state=named, so namefix never ran.
		// Troubleshooting: DistinctReleases still lists 5JSoQ-… after a
		// restart → this check is not applied (or the name has a dot).
		// Review if: lead-token parse stages these so this prune is a no-op.
		if !strings.ContainsAny(name, ". ") && tokenLooksObfuscated(name) {
			reject = append(reject, name)
			continue
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
	skipLang     int
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
		return c.dropWrongLanguage(headers), nil
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
		if !c.languageOK(name) {
			st.skipLang++
			continue
		}
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
	elapsed := time.Since(start).Milliseconds()
	if c.OnChunk != nil {
		c.OnChunk(CrawlChunkStat{
			Dir: dir, Group: group, RangeLow: a, RangeHigh: b,
			Rows: st.rows, Releases: st.releases, Admitted: st.admitted,
			StagedObf: c.curStaged, SkipRes: st.skipRes, SkipStat: st.skipStat,
			Gaps: c.curGaps, ElapsedMs: elapsed, AtUnixMs: time.Now().UnixMilli(),
		})
	}
	log.Printf("usenetsearch: crawl chunk dir=%s group=%s range=%d-%d rows=%d releases=%d admitted=%d staged_obf=%d skip_obf_cap=%d skip_stub=%d skip_unparsed=%d skip_res=%d skip_tier=%d skip_lang=%d skip_stat=%d stat_calls=%d deferred=%d gaps=%d elapsed_ms=%d",
		dir, group, a, b, st.rows, st.releases, st.admitted, c.curStaged, c.curSkipObfCap, c.curSkipStub, st.skipUnparsed, st.skipRes, st.skipTier, st.skipLang, st.skipStat, st.statCalls, st.deferred, c.curGaps, elapsed)

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
