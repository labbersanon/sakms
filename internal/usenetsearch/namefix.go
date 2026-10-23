package usenetsearch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/release"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-22: obfuscated-set name recovery without PreDB/IRC.
// Reason: dump/IRC reverse-lookup never named live ngPost random stems
//   (join key miss vs 6.9M dump rows). A staged set is renamed from a
//   scene-shaped header filename, first-segment PAR2/RAR/yEnc/NFO, parhash
//   donors, srrdb CRC, or NZB-password titles, then the same admission gate
//   + STAT as crawl.
// Troubleshooting: sets never renamed → o2cli grep "namefix pass"; check
//   body_fetches, unresolved, and namefix_body_mb_per_hour.
//   Undo: docs/usenet-nntp-native.md.
// Review if: BODY traffic needs download-activity-aware (bytes) throttling.

// PreDBHit is a scene title+size used by srrdb CRC lookup (historical name).
type PreDBHit struct {
	Title     string
	SizeBytes int64 // 0 when the source did not record a size
}

const (
	// Claude 2026-10-16: 40 (was 20) so one pass can drain yield-group due sets.
	// Reason: 25 due anime/highspeed/tv were split across loops while boneless ate slots.
	// Troubleshooting: namefix pass sets=20 with yield due leftover → bump this.
	// Review if: BODY budget (namefix_body_mb_per_hour) starts hitting BudgetStop.
	namefixSetsPerPass      = 40
	namefixIdentBodyPerPass = 20
	// Header-ident quoted titles (no BODY). Run before BODY.
	namefixHeaderPerPass = 40
	// Claude 2026-10-20: re-BODY named/shelved PAR2 once for hash_16K.
	// Reason: 65 live par2 titles and 225 shelved hash.par2 had FileDesc
	//   packets discarded; donors cannot match until hashes are stored.
	// Troubleshooting: parhash=0 in "namefix starting" with named par2 →
	//   DueParHashSeed / parhash_sampled. Review if: seed starves BODY.
	namefixParHashSeedPerPass = 20
	namefixBodiesPerSet       = 3
	namefixMaxDuration        = 20 * time.Second
	// Claude 2026-10-22: BODY phase 90s; header/parhash stay 20s.
	// Reason: live leftover=42 after one PAR2 FileDesc; 8s BodyHead already
	//   exceeded 20s so searchable moved at 1 rename/loop.
	// Troubleshooting: deadline phase=body leftover=40 with 90s → BodyHead
	//   still serial. Review if: namefix pass >2min starves OVER.
	namefixBodyPhaseDuration = 90 * time.Second
	// Claude 2026-10-22: BODY sets in parallel; ErrBusy still yields.
	// Reason: live leftover=38 with 5 sequential 8s FileDesc fetches / 90s
	//   (~4 min/pass after skip-crawl). Idle NNTP slots were unused.
	// Troubleshooting: body_fetches still ~5/pass with leftover>0 → workers
	//   not running or Src serializes BodyHead. Review if: downloads starve
	//   (then drop to 2; do not block on get()).
	namefixBodyWorkers = 4
	// Claude 2026-10-19: namefixMinBodyRemain skipped BodyHead when the 20s
	//   phase was already gone (live remain_ms=-1 on a 43KB par2). Replaced
	//   by WithoutCancel+8s so one overtime article still runs.
	// namefixMinBodyRemain = 200 * time.Millisecond
	// namefixMinPayload: smaller sets without PAR2 are finalized (no BODY).
	namefixMinPayload = 100 << 20
	// namefixHeadBytes caps the decoded bytes kept from one BODY.
	namefixHeadBytes = 256 << 10
	// Claude 2026-10-20: first .7z.NNN volume only needs the 32-byte start.
	// Reason: hash-7z packed data has no CRC; NextHeaderOffset lives in 32
	//   bytes. A 256KiB first-volume BODY wasted the 20s on packed streams.
	// Troubleshooting: namefix pattern pack=7z-split still wire=700k on
	//   .7z.001 → HeadOnly not set. Review if: a non-split .7z needs more
	//   than the signature (then keep 256KiB for bare .7z).
	namefix7zStartBytes = 64
	// Claude 2026-10-20: last-segment 7z BODY keeps up to 2MiB (article tail).
	// Reason: 7z NextHeader is at EOF of the last .7z.NNN volume. BodyHead
	//   already reads the whole article then truncates the *prefix*; 256KiB
	//   of a 768KiB last yEnc part drops the footer. Typical last parts are
	//   <1MiB. Review if: BodyHead grows a keep-tail flag for >2MiB parts.
	namefixTailBytes = 2 << 20
	// DefaultNameFixBodyMBPerHour caps BODY volume spent on name recovery.
	DefaultNameFixBodyMBPerHour = 512
	namefixSizeTolerance        = 0.15
	namefixMaxCandidates        = 6
)

// namefixBodyArticleTimeout caps one BodyHead (dial + BODY). Tests shrink it.
//
// Claude 2026-10-19: 8s per article, independent of the BODY phase cap.
// Reason: live every pass was body leftover=40 fetches=1 bytes=0 unresolved=0.
//
//	One hung BODY/dial returned DeadlineExceeded and aborted all 40 due sets.
//
// Troubleshooting: o2cli grep "namefix body"; elapsed_ms=0 + leftover=body →
//
//	article timeout not applied or parent ctx already dead before BodyHead.
//
// Review if: 8s leaves 20KB par2 unnamed (raise, still continue to next pick).
var namefixBodyArticleTimeout = 8 * time.Second

// Name sources / confidences (usenet_nntp_releases.name_source/name_confidence).
const (
	sourcePar2    = "par2"
	sourceParHash = "parhash"
	sourceRar     = "rar"
	sourceYenc    = "ybegin"
	sourceNFO     = "nfo"
	sourceFile    = "file"
	sourceSFV     = "sfv"
	sourceSRRDB   = "srrdb"
	sourceNZB     = "nzb"
	confMedium    = "medium"
	verdictNamed  = "namefix"
)

// bodyBudget is a rolling one-hour BODY byte budget. BODY workers share it.
type bodyBudget struct {
	mu          sync.Mutex
	capBytes    int64
	windowStart time.Time
	used        int64
}

func (b *bodyBudget) setCapMB(mb int) {
	if mb <= 0 {
		mb = DefaultNameFixBodyMBPerHour
	}
	b.mu.Lock()
	b.capBytes = int64(mb) << 20
	b.mu.Unlock()
}

// fits reports whether est more bytes stay within the hourly cap.
func (b *bodyBudget) fits(est int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if b.windowStart.IsZero() || now.Sub(b.windowStart) >= time.Hour {
		b.windowStart, b.used = now, 0
	}
	return b.used+est <= b.capBytes
}

func (b *bodyBudget) add(n int64) {
	b.mu.Lock()
	b.used += n
	b.mu.Unlock()
}

// NameFixResult is one name-fix pass's aggregate counters.
type NameFixResult struct {
	Sets        int
	BodyFetches int
	BodyBytes   int64
	Renamed     int
	RejectGate  int
	RejectStat  int
	Unresolved  int
	Finalized   int
	BySource    map[string]int
	Busy        bool
	BudgetStop  bool
	// BodyLeftover is BODY-queue sets not started when the 90s phase ended.
	// Zero means the queued BODY sets finished (or SkipBody). The loop skips
	// OVER while this is >0 so namefix does not wait on crawl catch-up.
	BodyLeftover int
}

// Advanced reports whether the pass made progress (drives catchUpPause).
func (r NameFixResult) Advanced() bool {
	return r.Renamed+r.RejectGate+r.RejectStat+r.Unresolved+r.Finalized+r.BodyLeftover > 0
}

// namefixTiming is per-set wall time in milliseconds. One log line after
// fixSet so leftover vs rename can be attributed to a stage, not guessed.
//
// Claude 2026-10-22: instrument only; no prefetch.
// Reason: critic rejected BODY cache — live FileDesc returns in the same
//
//	second as namefix starting; renamed lands ~19s later. remain_ms was the
//	8s constant so BodyHead latency was invisible.
//
// Troubleshooting: o2cli grep "namefix timing"; body_ms dominates → NNTP;
//
//	snapshot/stat/rename/siblings dominate → that stage is the next fix.
//
// Review if: a stage is identified and fixed (then drop or sample this log).
type namefixTiming struct {
	setrows, body, donor, accept, crc, snapshot, stat, rename, siblings int64
}

func (t *namefixTiming) add(dst *int64, start time.Time) int64 {
	ms := time.Since(start).Milliseconds()
	*dst += ms
	return ms
}

func (t *namefixTiming) log(set PendingSet) {
	log.Printf("usenetsearch: namefix timing group=%s key=%s setrows_ms=%d body_ms=%d donor_ms=%d accept_ms=%d crc_ms=%d snapshot_ms=%d stat_ms=%d rename_ms=%d siblings_ms=%d",
		set.Group, set.Key, t.setrows, t.body, t.donor, t.accept, t.crc, t.snapshot, t.stat, t.rename, t.siblings)
}

// NameFixer runs the bounded name-recovery pass over staged sets.
type NameFixer struct {
	Src     usenet.HeaderSource
	Index   *Index
	Crawler *Crawler // admission snapshot, group modes, statVerdict
	// SRRDB resolves RAR/ZIP/SFV inner-file CRC32 (nil disables it).
	SRRDB ArchiveCRCLookup
	// NZBPassword is an optional Newznab supplement after a local 7z-aes miss
	// (nil disables it).
	NZBPassword NZBPasswordLookup
	Groups      []string
	Budget      *bodyBudget
	// SkipBody skips BODY when OVER/download is busy. Header names still run.
	SkipBody bool
	// PassBudget caps every phase; zero uses 90s BODY / 20s other. Tests shorten it.
	PassBudget time.Duration
	// BodyWorkers is BODY-phase parallelism. Zero uses namefixBodyWorkers.
	// Tests set 1 to keep leftover-deadline sequential.
	BodyWorkers int

	UnnamedTTL time.Duration

	mu        sync.Mutex
	nzbPWUsed int
}

func (n *NameFixer) phaseBudget(phase string) time.Duration {
	if n.PassBudget > 0 {
		return n.PassBudget
	}
	if phase == "body" {
		return namefixBodyPhaseDuration
	}
	return namefixMaxDuration
}

// Run does one pass: TTL/attempt sweep, then up to namefixSetsPerPass due sets.
// ErrBusy or an exhausted BODY budget stops the pass without charging attempts.
func (n *NameFixer) Run(ctx context.Context) (NameFixResult, error) {
	n.nzbPWUsed = 0
	res := NameFixResult{BySource: map[string]int{}}
	ttl := n.UnnamedTTL
	if ttl <= 0 {
		ttl = DefaultUnnamedTTLDays * 24 * time.Hour
	}
	swept, err := n.Index.SweepUnnamed(ctx, ttl)
	if err != nil {
		return res, err
	}
	res.Finalized += swept
	statBudget := statReleasesPerChunk
	keyOf := func(set PendingSet) string { return set.Group + "\x00" + set.Key }
	done := map[string]bool{}
	headerSets, err := n.Index.DuePendingHeaderNames(ctx, namefixHeaderPerPass)
	if err != nil {
		return res, err
	}
	var sets, ident, parSeed []PendingSet
	if !n.SkipBody {
		sets, err = n.Index.DuePendingNames(ctx, n.Groups, namefixSetsPerPass)
		if err != nil {
			return res, err
		}
		ident, err = n.Index.DuePendingIdentBody(ctx, n.Groups, namefixIdentBodyPerPass)
		if err != nil {
			return res, err
		}
		parSeed, err = n.Index.DueParHashSeed(ctx, n.Groups, namefixParHashSeedPerPass)
		if err != nil {
			return res, err
		}
		keep := parSeed[:0]
		for _, set := range parSeed {
			if !done[keyOf(set)] {
				keep = append(keep, set)
			}
		}
		parSeed = keep
	}
	if len(sets)+len(ident)+len(headerSets)+len(parSeed) > 0 {
		log.Printf("usenetsearch: namefix starting header=%d body=%d ident=%d parhash=%d skip_body=%t",
			len(headerSets), len(sets), len(ident), len(parSeed), n.SkipBody)
	}
	// Claude 2026-10-19: each phase's context is passed into BodyHead.
	// Reason: live namefix starting body=2 sat 3.5 min; the budget only
	//   ran between sets while BodyHead used a 60s socket timeout.
	// Troubleshooting: namefix starting with no pass for >3min → BodyHead
	//   still ignoring ctx (fetchSegmentUntil / armNNTPDeadlineUntil).
	// Review if: articleIOTimeout becomes a settings knob.
	run := func(in []PendingSet, phase string) error {
		phaseCtx, cancel := context.WithTimeout(ctx, n.phaseBudget(phase))
		defer cancel()
		deadline := func(i int) error {
			log.Printf("usenetsearch: namefix deadline phase=%s leftover=%d", phase, len(in)-i)
			return nil
		}
		for i, set := range in {
			// Claude 2026-10-22: always start the first set of a phase.
			// Reason: CI leftover=1 at i=0 when 50ms elapsed between
			//   WithTimeout and this check under go test -p 4. BODY already
			//   overruns via WithoutCancel.
			// Troubleshooting: namefix deadline leftover=N at the start of
			//   a non-empty queue → this i==0 skip restored.
			// Review if: PassBudget is no longer per-phase.
			if i > 0 && phaseCtx.Err() != nil {
				return deadline(i)
			}
			if done[keyOf(set)] {
				continue
			}
			renamed := res.Renamed
			stop, err := n.fixSet(phaseCtx, set, &res, &statBudget)
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return deadline(i)
			}
			if err != nil {
				return err
			}
			if res.Renamed > renamed {
				done[keyOf(set)] = true
			}
			if stop {
				return nil
			}
		}
		return nil
	}
	// Claude 2026-10-19: quoted ident titles before BODY.
	// Reason: live 95% Black Clover/Yameii were in the BODY queue; header
	//   quotes need no BODY.
	// Troubleshooting: header=0 while pending95 subjects quote 1080p titles
	//   → DuePendingHeaderNames regex. Review if: 4K-without-p.
	if err := run(headerSets, "header"); err != nil {
		return res, err
	}
	var body []PendingSet
	// Claude 2026-10-20: 95% BODY before ident.
	// Reason: live after #228, ident=1 was an 81% 231GB part001.rar (1/4761)
	//   that ate the 20s; 96 complete rar pending never ran. crcs=0 on RAR5.
	// Troubleshooting: namefix starting ident=1 then crc on 80% leftover=39
	//   → this order reversed. Review if: ident FileDesc titles stall.
	body = append(body, sets...)
	body = append(body, ident...)
	if err := n.runBodyPhase(ctx, body, &res, done, keyOf); err != nil {
		return res, err
	}
	// Claude 2026-10-22: park parhash BODY when leftover remains.
	// Reason: parhash competes for NNTP; header names already ran.
	// Troubleshooting: o2cli grep "namefix park"; leftover stays >0 and
	//   parhash starves named donors → then a skip-crawl seed.
	if res.BodyLeftover > 0 {
		log.Printf("usenetsearch: namefix park parhash=%d leftover=%d",
			len(parSeed), res.BodyLeftover)
	} else if err := run(parSeed, "parhash"); err != nil {
		return res, err
	}
	if res.Sets > 0 || res.Finalized > 0 || res.BodyFetches > 0 || res.Renamed > 0 {
		log.Printf("usenetsearch: namefix pass sets=%d body_fetches=%d body_bytes=%d renamed=%d by_source=%s reject_gate=%d reject_stat=%d unresolved=%d finalized=%d leftover=%d busy=%t",
			res.Sets, res.BodyFetches, res.BodyBytes, res.Renamed, formatBySource(res.BySource),
			res.RejectGate, res.RejectStat, res.Unresolved, res.Finalized, res.BodyLeftover, res.Busy)
	}
	return res, nil
}

func formatBySource(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, m[k]))
	}
	return strings.Join(parts, ",")
}

func mergeNameFixResult(dst *NameFixResult, src NameFixResult) {
	dst.Sets += src.Sets
	dst.BodyFetches += src.BodyFetches
	dst.BodyBytes += src.BodyBytes
	dst.Renamed += src.Renamed
	dst.RejectGate += src.RejectGate
	dst.RejectStat += src.RejectStat
	dst.Unresolved += src.Unresolved
	dst.Finalized += src.Finalized
	dst.Busy = dst.Busy || src.Busy
	dst.BudgetStop = dst.BudgetStop || src.BudgetStop
	if dst.BySource == nil {
		dst.BySource = map[string]int{}
	}
	for k, v := range src.BySource {
		dst.BySource[k] += v
	}
}

func (n *NameFixer) runBodyPhase(ctx context.Context, in []PendingSet, res *NameFixResult, done map[string]bool, keyOf func(PendingSet) string) error {
	if len(in) == 0 {
		return nil
	}
	phaseCtx, cancel := context.WithTimeout(ctx, n.phaseBudget("body"))
	defer cancel()
	workers := n.BodyWorkers
	if workers <= 0 {
		workers = namefixBodyWorkers
	}
	if workers > len(in) {
		workers = len(in)
	}
	skip := make(map[string]bool, len(done))
	for k, v := range done {
		skip[k] = v
	}
	statEach := statReleasesPerChunk / workers
	if statEach < 1 {
		statEach = 1
	}
	jobs := make(chan PendingSet)
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			localStat := statEach
			for set := range jobs {
				local := NameFixResult{BySource: map[string]int{}}
				stop, err := n.fixSet(phaseCtx, set, &local, &localStat)
				mu.Lock()
				mergeNameFixResult(res, local)
				if local.Renamed > 0 {
					done[keyOf(set)] = true
				}
				if err != nil && firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				if stop || err != nil {
					cancel()
				}
			}
		}()
	}
	leftover := 0
	for i, set := range in {
		if skip[keyOf(set)] {
			continue
		}
		if i == 0 {
			jobs <- set
			continue
		}
		if phaseCtx.Err() != nil {
			leftover = len(in) - i
			break
		}
		select {
		case jobs <- set:
		case <-phaseCtx.Done():
			leftover = len(in) - i
		}
		if leftover > 0 {
			break
		}
	}
	close(jobs)
	wg.Wait()
	if leftover > 0 {
		res.BodyLeftover = leftover
		log.Printf("usenetsearch: namefix deadline phase=body leftover=%d", leftover)
	}
	if firstErr != nil && !errors.Is(firstErr, context.DeadlineExceeded) && !errors.Is(firstErr, context.Canceled) {
		return firstErr
	}
	return nil
}

// fixSet attempts one staged set. stop=true ends the pass (busy / budget).
func (n *NameFixer) fixSet(ctx context.Context, set PendingSet, res *NameFixResult, statBudget *int) (stop bool, err error) {
	if set.AlreadyNamed {
		return n.seedParHashes(ctx, set, res, statBudget)
	}
	tm := &namefixTiming{}
	defer tm.log(set)
	// Claude 2026-10-20: setRows must not inherit leftover phase crumbs.
	// Reason: CI ExpiredPhase failed `setRows` with 15ms ctx (91 header rows)
	//   before BodyHead's WithoutCancel could fetch part01. Live 20s phase
	//   ending mid-query would skip FILE_CRC the same way.
	// Troubleshooting: namefix deadline leftover with no crc log on a due
	//   rar set → this WithoutCancel missing. Review if: setRows grows past
	//   a few hundred ms (then cap it, still fetch part01).
	work := context.WithoutCancel(ctx)
	tRows := time.Now()
	rows, err := n.Index.setRowsBodyTargets(work, set.Group, set.Key)
	tm.add(&tm.setrows, tRows)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		_, err := n.Index.skipAndDelete(ctx, []releaseKey{{set.Group, set.Key}}, skipReasonOrphan)
		return false, err
	}
	res.Sets++

	for _, cand := range headerNameCandidates(rows) {
		tAccept := time.Now()
		name, source, conf, via, ok := n.accept(cand.name, cand.source)
		tm.add(&tm.accept, tAccept)
		if ok {
			return n.promote(ctx, set, rows, name, source, conf, via, res, statBudget, tm)
		}
	}
	// Claude 2026-10-21: password packs never BODY; titles are not on NNTP.
	// Reason: live skip_7z still charged 7z Tail on attempts=0 and ranked
	//   ahead of 434 anime hash.mkv+PAR2.
	// Troubleshooting: body_fetches>0 on ngPost hash.7z / .7z.enc → this
	//   return missing. Review if: a non-ngPost 7z still needs srrdb CRC.
	if passwordLockedPack(rows) {
		if err := n.Index.ShelveBodyAttempts(ctx, set.Group, set.Key); err != nil {
			return false, err
		}
		return false, nil
	}

	if set.PayloadBytes < namefixMinPayload && !set.HasPar2 {
		res.Finalized++
		return false, n.finalize(ctx, set)
	}

	hashOnly := false
	sampled := false
	triedBody := false
	crcTried := false
	var zstart []byte
	var zsize uint64
	pat := detectNamePattern(rows)
	ngPost := looksLikeNgPost(rows)
	skip7z := ngPost && pat.Pack == "7z-split" && set.Attempts > 0
	var nzbHit NZBPasswordHit
	var nzbGot bool
	log.Printf("usenetsearch: namefix pattern obf=%s pack=%s files=%s ngpost=%t skip_7z=%t",
		pat.Obf, pat.Pack, pat.Ext, ngPost, skip7z)
	for _, pick := range pickBodyTargets(rows) {
		if skip7z && (pick.HeadOnly || pick.Tail) {
			continue
		}
		// Claude 2026-10-20: budget against the 256KiB head, not article bytes.
		// Reason: part01 first segments are often 1–2MB; fits(pick.Bytes) would
		//   skip FILE_CRC when the hourly cap has <2MB left. BodyHead already
		//   truncates at namefixHeadBytes.
		// Troubleshooting: namefix crc=0 with BudgetStop and pending rar →
		//   this min() missing. Review if: BodyHead starts streaming full
		//   articles.
		headCap := namefixHeadBytes
		if pick.Tail {
			headCap = namefixTailBytes
		} else if pick.HeadOnly {
			headCap = namefix7zStartBytes
		}
		est := pick.Bytes
		if est > int64(headCap) {
			est = int64(headCap)
		} else if est < 0 {
			est = 0
		}
		if !n.Budget.fits(est) {
			res.BudgetStop = true
			return true, nil
		}
		// Claude 2026-10-19: 8s article ctx is not the leftover phase crumbs.
		// Reason: live after #222, first BODY set skipped a 43KB par2 with
		//   remain_ms=-1 (setRows ate the 20s) and leftover=40
		//   with body_fetches=0. Hash.par2 never moved.
		// Troubleshooting: o2cli grep "namefix body skip" → this WithoutCancel
		//   missing. Review if: one overtime BODY delays crawl (>30s/pass).
		bodyCtx, cancel := context.WithTimeout(work, namefixBodyArticleTimeout)
		tBody := time.Now()
		sample, berr := n.Src.BodyHead(bodyCtx, pick.MsgID, headCap)
		cancel()
		elapsedMs := tm.add(&tm.body, tBody)
		if errors.Is(berr, usenet.ErrBusy) {
			res.Busy = true
			return true, nil
		}
		res.BodyFetches++
		triedBody = true
		if berr != nil {
			log.Printf("usenetsearch: namefix body msgid=%s bytes=%d elapsed_ms=%d wire=0 yenc=0 cands=0 par2=false err=%v",
				pick.MsgID, pick.Bytes, elapsedMs, berr)
			continue
		}
		sampled = true
		n.Budget.add(sample.WireBytes)
		res.BodyBytes += sample.WireBytes
		if st, ok := usenet.Parse7zStart(sample.Data); ok {
			zstart = append([]byte(nil), sample.Data[:32]...)
			zsize = st.Size
			log.Printf("usenetsearch: namefix crc msgid=%s 7z off=%d sz=%d", pick.MsgID, st.Offset, st.Size)
		}
		cands := candidatesFromSample(sample, pick.NFO)
		crcs := sampleCRCs(sample)
		var extra []usenet.ArchiveFileCRC
		if len(crcs) == 0 && len(zstart) == 32 {
			extra = usenet.ArchiveFileCRCsFrom7zParts(zstart, sample.Data)
			for _, f := range extra {
				if f.CRC != "" {
					crcs = append(crcs, f.CRC)
				}
				if f.Name != "" {
					cands = append(cands, nameCand{f.Name, sourceRar})
				}
			}
		}
		isPar2 := bytes.HasPrefix(sample.Data, par2Magic)
		cand0 := ""
		if len(cands) > 0 {
			cand0 = cands[0].name
			if len(cand0) > 80 {
				cand0 = cand0[:80]
			}
		}
		log.Printf("usenetsearch: namefix body msgid=%s bytes=%d elapsed_ms=%d wire=%d yenc=%d cands=%d par2=%t cand=%q err=<nil>",
			pick.MsgID, pick.Bytes, elapsedMs, sample.WireBytes, len(sample.YencName), len(cands), isPar2, cand0)
		kind := payloadKind(sample.Data, zstart, zsize)
		if pick.CRC {
			if len(crcs) > 0 {
				crcTried = true
			}
			magic := sample.Data
			if len(magic) > 8 {
				magic = magic[:8]
			}
			inner := ""
			if names := usenet.ArchiveMemberNames(sample.Data); len(names) > 0 {
				inner = names[0]
			} else if len(extra) > 0 {
				inner = extra[0].Name
			}
			if len(inner) > 80 {
				inner = inner[:80]
			}
			log.Printf("usenetsearch: namefix crc msgid=%s crcs=%d magic=%x payload=%s name=%q",
				pick.MsgID, len(crcs), magic, kind, inner)
		}
		if isPar2 {
			tDonor := time.Now()
			n.rememberParHashes(work, set, sample.Data)
			name, ok, err := n.Index.LookupParHashDonor(work, set.Group, set.Key, set.PayloadBytes)
			tm.add(&tm.donor, tDonor)
			if err != nil {
				log.Printf("usenetsearch: parhash lookup group=%s err=%v", set.Group, err)
			} else if ok {
				return n.promote(work, set, rows, name, sourceParHash, confMedium, "parhash", res, statBudget, tm)
			}
		}
		allHash := true
		if len(cands) > 0 {
			for _, cand := range cands {
				if recoveredUnusable(cand.name) {
					continue
				}
				allHash = false
				tAccept := time.Now()
				name, source, conf, via, ok := n.accept(cand.name, cand.source)
				tm.add(&tm.accept, tAccept)
				if ok {
					return n.promote(work, set, rows, name, source, conf, via, res, statBudget, tm)
				}
			}
		}
		// Claude 2026-10-20: empty PAR2 FileDesc is still a hash-only sample.
		// Reason: live remaining hash.par2 often return par2=true cands=0;
		//   hashOnly stayed false so an expired phase ctx skipped part01.
		// Troubleshooting: namefix crc never logs after par2=true cands=0
		//   → this allHash path missing. Review if: FileDesc parser changes.
		if isPar2 && allHash {
			hashOnly = true
		}
		tCRC := time.Now()
		hit, ok := n.lookupCRCs(work, set, crcs)
		tm.add(&tm.crc, tCRC)
		if ok {
			return n.promote(work, set, rows, hit.Title, sourceSRRDB, confMedium, "srrdb", res, statBudget, tm)
		}
		// Claude 2026-10-21: rar5-enc is a password pack; NNTP has no title.
		// Reason: live hash-rar after PAR2 still had leftover=41 with
		//   payload=rar5-enc crcs=0. BODY cannot recover the NZB password.
		// Troubleshooting: namefix crc payload=rar5-enc then BumpNameAttempt
		//   → this return missing. Review if: an unencrypted RAR5 is
		//   misclassified as rar5-enc (then PayloadKind).
		if kind == "rar5-enc" && keepCompleteYield(set.Group, set.CompletenessBP, set.PayloadBytes) {
			res.Unresolved++
			if triedBody {
				_ = n.Index.MarkParHashSampled(work, set.Group, set.Key)
			}
			return false, n.Index.ShelveBodyAttempts(work, set.Group, set.Key)
		}
		// Claude 2026-10-21: Newznab is supplemental after a local 7z-aes miss.
		// Reason: NZBGeek (and peers) do not ingest ngPost hash.7z NZBs;
		//   header-first lookup + heldPW stalled name_attempts on miss.
		//   Native PAR2/srrdb stay the path; t=search is optional.
		// Troubleshooting: nzb-password on skip_7z ngPost → this block
		//   must not run (no 7z BODY). Review if: an indexer lists x264.
		if kind == "7z-aes" && len(crcs) == 0 {
			if !nzbGot {
				nzbHit, nzbGot = n.fetchNZBPassword(work, set, rows)
			}
			if nzbGot {
				stop, done, err := n.applyNZBHit(work, set, rows, sample, zstart, nzbHit, res, statBudget, tm)
				if done || err != nil {
					return stop, err
				}
			}
		}
		// Claude 2026-10-20: do not break the pick loop on leftover phase ctx.
		// Reason: #227 still skipped rar when vol PAR2 filled 3 slots, or when
		//   cands=0 left hashOnly false. BodyHead is already 8s WithoutCancel;
		//   at most namefixBodiesPerSet articles. Review if: 3×8s overtime
		//   delays crawl (lower per-article timeout, do not skip part01).
	}

	res.Unresolved++
	// Claude 2026-10-20: one hash_16K BODY, even when the article is not PAR2.
	// Reason: DueParHashSeed would re-fetch shelved sets every pass (backoff
	//   tests). Live 225 still seed because the column defaults false until
	//   this runs once after 0049.
	// Troubleshooting: parhash=N on every pass for the same keys → sampled
	//   not set after BodyHead. Review if: BODY is retried after a new name source.
	if triedBody {
		_ = n.Index.MarkParHashSampled(work, set.Group, set.Key)
	}
	if sampled && hashOnly && keepCompleteYield(set.Group, set.CompletenessBP, set.PayloadBytes) {
		// Claude 2026-10-20: do not shelf until a first-volume RAR/SFV ran.
		// Reason: live #226/#227 shelved after 3 vol PAR2 BODYs; name_attempts
		//   hit max so DuePendingNames never retried part01 for srrdb.
		// Troubleshooting: pending95 rar still attempts>=max with no
		//   namefix crc log → this crcTried gate missing. Review if:
		//   crc_sampled column replaces the gate.
		// Claude 2026-10-21: also shelve when main PAR2 was sampled.
		// Reason: RAR volumes are no longer picked beside PAR2, so crcTried
		//   stayed false and the same hash-rar filled BODY every pass.
		// Troubleshooting: namefix pass sets=4 unresolved=1 on hash-rar
		//   forever → hasMainPar2Name missing here.
		// Review if: unencrypted hash-rar CRC is picked again.
		par2Sampled := false
		for _, r := range rows {
			if isMainPar2Name(r.Filename) || isMainPar2Name(r.Subject) {
				par2Sampled = true
				break
			}
		}
		if crcTried || !hasCRCCapableTarget(rows) || par2Sampled {
			return false, n.Index.ShelveBodyAttempts(work, set.Group, set.Key)
		}
	}
	return false, n.Index.BumpNameAttempt(work, set.Group, set.Key, set.Attempts)
}

// seedParHashes BODY-reads a named PAR2 set once to index hash_16K donors.
func (n *NameFixer) seedParHashes(ctx context.Context, set PendingSet, res *NameFixResult, statBudget *int) (bool, error) {
	if n.Src == nil || n.Index == nil {
		return false, nil
	}
	rows, err := n.Index.setRowsBodyTargets(ctx, set.Group, set.Key)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, n.Index.MarkParHashSampled(ctx, set.Group, set.Key)
	}
	work := context.WithoutCancel(ctx)
	tried := false
	for _, pick := range pickPar2Targets(rows) {
		if !n.Budget.fits(pick.Bytes) {
			res.BudgetStop = true
			return true, nil
		}
		bodyCtx, cancel := context.WithTimeout(work, namefixBodyArticleTimeout)
		tBody := time.Now()
		sample, berr := n.Src.BodyHead(bodyCtx, pick.MsgID, namefixHeadBytes)
		cancel()
		elapsedMs := time.Since(tBody).Milliseconds()
		if errors.Is(berr, usenet.ErrBusy) {
			res.Busy = true
			return true, nil
		}
		res.BodyFetches++
		tried = true
		if berr != nil {
			log.Printf("usenetsearch: namefix body msgid=%s bytes=%d elapsed_ms=%d wire=0 yenc=0 cands=0 par2=false err=%v",
				pick.MsgID, pick.Bytes, elapsedMs, berr)
			continue
		}
		n.Budget.add(sample.WireBytes)
		res.BodyBytes += sample.WireBytes
		isPar2 := bytes.HasPrefix(sample.Data, par2Magic)
		log.Printf("usenetsearch: namefix body msgid=%s bytes=%d elapsed_ms=%d wire=%d yenc=%d cands=0 par2=%t err=<nil>",
			pick.MsgID, pick.Bytes, elapsedMs, sample.WireBytes, len(sample.YencName), isPar2)
		if isPar2 {
			n.rememberParHashes(work, set, sample.Data)
			break
		}
	}
	if tried {
		if err := n.Index.MarkParHashSampled(work, set.Group, set.Key); err != nil {
			return false, err
		}
	}
	var display string
	if err := n.Index.db.QueryRowContext(work, `
SELECT display_name FROM usenet_nntp_releases WHERE group_name=? AND release_key=?`,
		set.Group, set.Key).Scan(&display); err != nil || display == "" {
		return false, nil
	}
	n.promoteParHashSiblings(work, set, display, res, statBudget)
	return false, nil
}

func (n *NameFixer) rememberParHashes(ctx context.Context, set PendingSet, data []byte) {
	if n == nil || n.Index == nil {
		return
	}
	if err := n.Index.StoreParHashes(ctx, set.Group, set.Key, par2FileDescs(data)); err != nil {
		log.Printf("usenetsearch: parhash store group=%s err=%v", set.Group, err)
	}
	if err := n.Index.MarkParHashSampled(ctx, set.Group, set.Key); err != nil {
		log.Printf("usenetsearch: parhash sampled group=%s err=%v", set.Group, err)
	}
}

func (n *NameFixer) promoteParHashSiblings(ctx context.Context, set PendingSet, name string, res *NameFixResult, statBudget *int) {
	if n == nil || n.Index == nil || name == "" {
		return
	}
	hashes, err := n.Index.ParHashesFor(ctx, set.Group, set.Key)
	if err != nil || len(hashes) == 0 {
		return
	}
	sibs, err := n.Index.PendingSharingParHashes(ctx, hashes, set.PayloadBytes, set.Group, set.Key)
	if err != nil {
		log.Printf("usenetsearch: parhash siblings group=%s err=%v", set.Group, err)
		return
	}
	for i, sib := range sibs {
		if i >= 8 {
			break
		}
		rows, err := n.Index.setRowsBodyTargets(ctx, sib.Group, sib.Key)
		if err != nil || len(rows) == 0 {
			continue
		}
		_, _ = n.promote(ctx, sib, rows, name, sourceParHash, confMedium, "parhash", res, statBudget, nil)
	}
}

func (n *NameFixer) finalize(ctx context.Context, set PendingSet) error {
	n.markRejected(set)
	_, err := n.Index.FinalizeUnnamed(ctx, []releaseKey{{set.Group, set.Key}})
	return err
}

// markRejected caches a negative verdict so re-crawled rows of a dropped set
// are not staged (and BODY-fetched) all over again.
func (n *NameFixer) markRejected(set PendingSet) {
	if n.Crawler != nil && n.Crawler.Admitter != nil {
		n.Crawler.Admitter.putVerdict(set.Group, set.Key, false, verdictNamed)
	}
}

func recoveredUnusable(raw string) bool {
	norm := normalizeRecovered(raw)
	if norm == "" || hashCore(norm) != "" {
		return true
	}
	// Claude 2026-10-19: skip tokenLooksObfuscated when the name has `. `_/space.
	// Reason: that helper's last `digits>0` check false-positives
	//   `[UQW] Black Clover - Ep170 [BDRip 1080p…]` (1080 / Ep170) and
	//   `[Fancutfags]_Puella_…1080p…` (underscore, no space).
	// Troubleshooting: accept() returns false on a quoted ident title →
	//   this early return missing. Review if: a hash token includes `_`.
	if strings.ContainsAny(norm, ". _") {
		return false
	}
	return looksObfuscated(norm) || tokenLooksObfuscated(norm)
}

// accept normalizes a recovered name. par2/rar/ybegin/file/NFO names that
// parse as a quoted/scene title are accepted (NNTmux fixNamesWithNfo / files).
// Garbage NFO is ignored.
func (n *NameFixer) accept(raw, source string) (name, src, conf, via string, ok bool) {
	if recoveredUnusable(raw) {
		return "", "", "", "", false
	}
	norm := normalizeRecovered(raw)
	if quotedReleaseName(norm) {
		return norm, source, confMedium, "n/a", true
	}
	return "", "", "", "", false
}

// identShapedName is the Go form of identNameSQL: a resolution plus a year,
// season, or multi-word title.
// Claude 2026-10-19: fansub `Ep170` / spaces, not only SxxEyy.
// Reason: hasTitleAndMarker only counts SxxEyy/year, so a Black Clover 1080p
// quoted name failed accept() after recoveredUnusable was fixed.
// Troubleshooting: unnamed 1080p spacey title means Resolution parse missed
// (need 720/1080/2160/480p).
// Review if: 576p / 4K-without-p.
func identShapedName(name string) bool {
	if release.Parse(name).Resolution == 0 {
		return false
	}
	if hasTitleAndMarker(name) {
		return true
	}
	// Claude 2026-10-19: '_' is a word separator (underscore fansubs).
	// Reason: live `[Fancutfags]_Puella_Magi_…1080p…` is pending_name at 100%
	//   because ContainsAny(". ") failed and header SQL required a space
	//   inside the quotes. Searchable sat at 115.
	// Troubleshooting: 100% quoted 1080p still pending → this still
	//   requiring space/dot. Review if: 576p / 4K-without-p.
	if !strings.ContainsAny(name, ". _") {
		return false
	}
	return letterCount(name) >= 3
}

// quotedReleaseName is a destemmed quoted filename that is already a title:
// ident-shaped (resolution), a scene parse, or a multi-word / bracketed name
// that is not a hash token. Hash.par2 stays unnamed (PAR2/srrdb).
func quotedReleaseName(name string) bool {
	n := normalizeRecovered(name)
	if n == "" || hashCore(n) != "" {
		return false
	}
	if identShapedName(n) || parsesAsRelease(n) {
		return true
	}
	if letterCount(n) < 6 {
		return false
	}
	if strings.Contains(n, "[") && strings.Contains(n, "]") {
		return true
	}
	if strings.Contains(n, " ") {
		return true
	}
	// Claude 2026-10-20: underscore titles with a year or 3+ real words.
	// Reason: live pending95 `Metropolis_Magazine_-_September_2026.part1.rar`
	//   failed quotedReleaseName (no 1080p; yearRe `\b` does not cut `_2026`
	//   because `_` is a word char) so namefix BODYed RAR5. A bare `_`
	//   check would also accept live hash `0G6IJMEdZG_f-Evmq`.
	// Troubleshooting: namefix crc on `Title_Name_2026.part1.rar` → year/word
	//   helper missed. Review if: hash tokens glue `_20xx`.
	return underscoreSeparatedTitle(n)
}

var reSepYear = regexp.MustCompile(`(?:[._-])(?:19|20)\d{2}(?:[._-]|$)`)

func underscoreSeparatedTitle(n string) bool {
	if !strings.Contains(n, "_") {
		return false
	}
	words := 0
	for _, tok := range strings.FieldsFunc(n, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	}) {
		if letterCount(tok) < 4 || obfuscatedChurn(tok) {
			continue
		}
		words++
	}
	return words >= 3 || (words >= 1 && reSepYear.MatchString(n))
}

func letterCount(s string) int {
	n := 0
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			n++
		}
	}
	return n
}

// promote runs an accepted name through the admission gate and STAT (when
// admission is on), then renames the set. A gate reject or confirmed-missing
// STAT deletes the set (same irreversibility as crawl admission).
func (n *NameFixer) promote(ctx context.Context, set PendingSet, rows []HeaderRow, name, source, conf, via string, res *NameFixResult, statBudget *int, tm *namefixTiming) (bool, error) {
	if tm == nil {
		tm = &namefixTiming{}
	}
	reject := func(counter *int) (bool, error) {
		*counter++
		n.markRejected(set)
		_, err := n.Index.skipAndDelete(ctx, []releaseKey{{set.Group, set.Key}}, skipReasonRejected)
		return false, err
	}
	busy := false
	c := n.Crawler
	if c != nil && !c.languageOK(name) {
		return reject(&res.RejectGate)
	}
	if c != nil && c.AdmissionEnabled && c.Admitter != nil {
		tSnap := time.Now()
		snap, err := c.Admitter.Snapshot(ctx)
		tm.add(&tm.snapshot, tSnap)
		if err != nil {
			return false, err
		}
		if snap != nil {
			// Claude 2026-10-20: skipGate is HD ident only (snapped >= 720).
			// Reason: live `[WhyNot] … [DVD 480p AAC]` skipped MinRes=720 at
			//   ingest and would skip again here. 1080p fansubs under a 2160p
			//   movies floor still skip. Scene PAR2 names stay dotted.
			// Troubleshooting: reject_gate missing on a 480p quoted title →
			//   skipAdmissionGate. Review if: 576p PAL ident should skip 720.
			if !skipAdmissionGate(name) {
				var modes []string
				if c.GroupModesFn != nil {
					modes = c.GroupModesFn(set.Group)
				}
				stds, _ := snap.Resolve(set.Group, modes, name)
				info := release.Parse(name)
				passed := false
				for _, std := range stds {
					if ok, _ := gate(name, info, std); ok {
						passed = true
						break
					}
				}
				if !passed {
					return reject(&res.RejectGate)
				}
			}
			var st chunkStats
			tStat := time.Now()
			keep, _, _ := c.statVerdict(ctx, set.Group, name, rows, false, statBudget, &busy, &st)
			tm.add(&tm.stat, tStat)
			if !keep {
				return reject(&res.RejectStat)
			}
		}
	}
	tRename := time.Now()
	if err := n.Index.RenameRelease(ctx, set.Group, set.Key, name, source, conf); err != nil {
		return false, err
	}
	tm.add(&tm.rename, tRename)
	res.Renamed++
	res.BySource[source]++
	log.Printf("usenetsearch: renamed group=%s key=%s name=%s source=%s confidence=%s via=%s pct=%.1f",
		set.Group, set.Key, name, source, conf, via, float64(set.CompletenessBP)/100)
	tSib := time.Now()
	n.promoteParHashSiblings(ctx, set, name, res, statBudget)
	tm.add(&tm.siblings, tSib)
	if busy {
		res.Busy = true
	}
	return busy, nil
}

func (n *NameFixer) fetchNZBPassword(ctx context.Context, set PendingSet, rows []HeaderRow) (NZBPasswordHit, bool) {
	if n.NZBPassword == nil {
		return NZBPasswordHit{}, false
	}
	stem := archiveSearchStem(rows)
	poster := archiveSearchPoster(rows)
	if stem == "" && poster == "" {
		return NZBPasswordHit{}, false
	}
	n.mu.Lock()
	if n.nzbPWUsed >= namefixNZBPasswordPerPass {
		n.mu.Unlock()
		return NZBPasswordHit{}, false
	}
	n.nzbPWUsed++
	n.mu.Unlock()
	hit, ok := n.NZBPassword.Lookup(ctx, NZBPasswordQuery{
		Filename:  stem,
		Poster:    poster,
		Group:     set.Group,
		PostedAt:  archiveSearchPosted(rows),
		SizeBytes: set.PayloadBytes,
	})
	if !ok {
		log.Printf("usenetsearch: namefix nzb-password q=%q poster=%q miss", stem, poster)
		return NZBPasswordHit{}, false
	}
	shown := hit.Title
	if len(shown) > 80 {
		shown = shown[:80]
	}
	log.Printf("usenetsearch: namefix nzb-password q=%q poster=%q indexer=%s pw=%t title=%q",
		stem, poster, hit.Indexer, len(hit.Passwords) > 0, shown)
	return hit, true
}

func (n *NameFixer) applyNZBHit(ctx context.Context, set PendingSet, rows []HeaderRow, sample usenet.BodySample, zstart []byte, hit NZBPasswordHit, res *NameFixResult, statBudget *int, tm *namefixTiming) (stop bool, done bool, err error) {
	try := func(name, source, conf, via string) (bool, bool, error) {
		stop, err := n.promote(ctx, set, rows, name, source, conf, via, res, statBudget, tm)
		return stop, true, err
	}
	if len(hit.Passwords) > 0 && len(zstart) == 32 {
		var crcs []string
		var cands []nameCand
		for _, f := range usenet.ArchiveFileCRCsFrom7zPartsWithPasswords(zstart, sample.Data, hit.Passwords) {
			if f.CRC != "" {
				crcs = append(crcs, f.CRC)
			}
			if f.Name != "" {
				cands = append(cands, nameCand{f.Name, sourceRar})
			}
		}
		if srr, ok := n.lookupCRCs(ctx, set, crcs); ok {
			return try(srr.Title, sourceSRRDB, confMedium, "srrdb")
		}
		for _, cand := range cands {
			name, source, conf, via, ok := n.accept(cand.name, cand.source)
			if ok {
				return try(name, source, conf, via)
			}
		}
	}
	name, source, conf, via, ok := n.accept(hit.Title, sourceNZB)
	if ok {
		return try(name, source, conf, via)
	}
	return false, false, nil
}

func (n *NameFixer) lookupCRCs(ctx context.Context, set PendingSet, crcs []string) (PreDBHit, bool) {
	if n.SRRDB == nil {
		return PreDBHit{}, false
	}
	for _, crc := range crcs {
		hit, ok := n.SRRDB.ByCRC(ctx, crc)
		if !ok || recoveredUnusable(hit.Title) || !sizeWithin(hit.SizeBytes, set.PayloadBytes) {
			continue
		}
		return hit, true
	}
	return PreDBHit{}, false
}

func sampleCRCs(s usenet.BodySample) []string {
	if bytes.HasPrefix(s.Data, par2Magic) {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(crc string) {
		crc = strings.ToUpper(strings.TrimSpace(crc))
		if crc == "" || crc == "00000000" || seen[crc] {
			return
		}
		seen[crc] = true
		out = append(out, crc)
	}
	if files := usenet.ArchiveFileCRCs(s.Data); len(files) > 0 {
		for _, f := range files {
			add(f.CRC)
		}
		return out
	}
	for _, e := range preferredSFVEntries(sfvEntries(s.Data)) {
		add(e.CRC)
	}
	if len(out) > 2 {
		out = out[:2]
	}
	return out
}

func headerNameCandidates(rows []HeaderRow) []nameCand {
	seen := map[string]bool{}
	var out []nameCand
	add := func(raw, source string) {
		key := strings.ToLower(normalizeRecovered(raw))
		if key == "" || hashCore(key) != "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, nameCand{raw, source})
	}
	for _, r := range rows {
		// Claude 2026-10-22: header filename is NNTmux files, not yEnc.
		// Reason: fixNamesWithFiles promotes a scene-shaped stored filename
		//   without BODY. Hash.par2 still skipped (hashCore).
		// Troubleshooting: name_source=ybegin on a .mkv header → this still
		//   sourceYenc. Review if: quoted subject should stay ybegin only.
		add(r.Filename, sourceFile)
		// Claude 2026-10-19: quoted subject even when Filename is `01.par2`.
		// Reason: rows ingested before quotedFilename keep the reFileTok
		//   stub; live pending95 still has the title in Subject quotes.
		// Troubleshooting: header namefix misses Black Clover while
		//   ParseSubject on a fresh crawl would store it. Review if:
		//   headers are rewritten on re-OVER (then Filename is enough).
		add(quotedFilename(r.Subject), sourceYenc)
	}
	return out
}

// sizeWithin reports whether payload is within ±15% of a reported size
// (unknown on either side passes). Used by srrdb CRC matching.
func sizeWithin(preSize, payload int64) bool {
	if preSize <= 0 || payload <= 0 {
		return true
	}
	return sizeWithinKnown(preSize, payload, namefixSizeTolerance)
}

// sizeWithinKnown is NNTmux donor matching: both sizes known, delta/ref ≤ pct.
func sizeWithinKnown(ref, payload int64, pct float64) bool {
	if ref <= 0 || payload <= 0 {
		return false
	}
	diff := ref - payload
	if diff < 0 {
		diff = -diff
	}
	return float64(diff)/float64(ref) <= pct
}

var reRecoveredSuffix = regexp.MustCompile(`(?i)(?:\.vol\d+\+\d+)?(?:\.part\d+)?\.(?:par2|rar|r\d{2,3}|zip|7z(?:\.\d{3})?|mkv|mp4|avi|m4v|wmv|mpg|mpeg|ts|iso|nfo|sfv|nzb)$`)

// normalizeRecovered strips directory, volume and extension suffixes so a
// filename ("Show.S01E01.1080p.WEB.x264-GRP.part01.rar") reduces to the
// release name.
func normalizeRecovered(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	for {
		next := reRecoveredSuffix.ReplaceAllString(name, "")
		if next == name {
			break
		}
		name = next
	}
	return strings.Trim(name, ". ")
}

// parsesAsRelease: a usable title plus (year or SxxEyy-style episode) plus a
// resolution or source token.
func parsesAsRelease(name string) bool {
	if !hasTitleAndMarker(name) {
		return false
	}
	info := release.Parse(name)
	return info.Resolution > 0 || info.Source != ""
}

// hasTitleAndMarker: a usable title prefix plus a year or episode marker.
func hasTitleAndMarker(name string) bool {
	prefix, year, isEpisode := splitReleaseTitle(name)
	return cleanTitleKey(prefix) != "" && (year > 0 || isEpisode)
}

type nameCand struct{ name, source string }

// candidatesFromSample derives name candidates from one decoded BODY head:
// PAR2 FileDesc names, NFO scene names, or RAR/ZIP member names, then the
// yEnc =ybegin name. Deduped on the normalized form, capped.
func candidatesFromSample(s usenet.BodySample, nfo bool) []nameCand {
	var raw []nameCand
	switch {
	case bytes.HasPrefix(s.Data, par2Magic):
		for _, nm := range par2FileNames(s.Data) {
			raw = append(raw, nameCand{nm, sourcePar2})
		}
	case nfo:
		for _, nm := range nfoSceneNames(s.Data) {
			raw = append(raw, nameCand{nm, sourceNFO})
		}
	default:
		if entries := sfvEntries(s.Data); len(entries) > 0 {
			for _, e := range entries {
				raw = append(raw, nameCand{e.Name, sourceSFV})
			}
		} else {
			for _, nm := range usenet.ArchiveMemberNames(s.Data) {
				raw = append(raw, nameCand{nm, sourceRar})
			}
		}
	}
	if s.YencName != "" {
		raw = append(raw, nameCand{s.YencName, sourceYenc})
	}
	seen := map[string]bool{}
	var out []nameCand
	for _, c := range raw {
		key := strings.ToLower(normalizeRecovered(c.name))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
		if len(out) >= namefixMaxCandidates {
			break
		}
	}
	return out
}

type bodyPick struct {
	MsgID    string
	Bytes    int64
	NFO      bool
	CRC      bool
	Tail     bool // last yEnc segment (7z end header)
	HeadOnly bool // 7z signature header only (first volume)
}

// pickBodyTargets chooses up to namefixBodiesPerSet articles:
// SFV, main non-vol PAR2, first-volume RAR/ZIP/7z (part01 / .7z.001 / bare
// .rar), last volume (7z last segment; RAR first segment only when no main
// PAR2), then NFO. Volume PAR2 never fills a slot. Encrypted .7z.enc is skipped.
// Claude 2026-10-21: PAR2 before archive volumes.
// Reason: live after 7z dequeue, hash-rar still fetched two 716KB part01
//
//	articles first (leftover=41, FileDesc never ran). Small PAR2 FileDesc /
//	hash_16K is the title path for non-password sets.
//
// Troubleshooting: namefix body wire=716800 then leftover with par2 unused
//
//	→ this still adding first-volume before isMainPar2Name.
//
// Review if: RAR5 last-volume CRC must share the 3-slot cap with PAR2
//
//	(today last volume is 4th and dropped).
func pickBodyTargets(rows []HeaderRow) []bodyPick {
	type file struct {
		key   string
		first HeaderRow
		last  HeaderRow
		total int64
		nfo   bool
	}
	files := map[string]*file{}
	var order []string
	for _, r := range rows {
		if usenet.IsSampleName(r.Filename) || usenet.IsSampleName(r.Subject) {
			continue
		}
		key := r.Filename
		if key == "" {
			key = rePartNM.ReplaceAllString(r.Subject, "")
		}
		f := files[key]
		if f == nil {
			low := strings.ToLower(r.Subject + " " + r.Filename)
			f = &file{key: key, first: r, last: r, nfo: strings.Contains(low, ".nfo")}
			files[key] = f
			order = append(order, key)
		}
		f.total += r.Bytes
		if r.PartN > 0 && (r.PartN < f.first.PartN || f.first.PartN <= 0) {
			f.first = r
		}
		if r.PartN > f.last.PartN {
			f.last = r
		}
	}
	sorted := make([]*file, 0, len(order))
	for _, k := range order {
		sorted = append(sorted, files[k])
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].total != sorted[j].total {
			return sorted[i].total < sorted[j].total
		}
		return sorted[i].key < sorted[j].key
	})
	if len(sorted) == 0 {
		return nil
	}
	anyName := func(f *file, pred func(string) bool) bool {
		for _, n := range []string{f.key, f.first.Filename, f.first.Subject, f.last.Filename, f.last.Subject} {
			if pred(n) {
				return true
			}
		}
		return false
	}
	var picks []bodyPick
	seen := map[string]bool{}
	addRow := func(row HeaderRow, nfo, crc, tail, headOnly bool) {
		if row.MsgID == "" || seen[row.MsgID] || len(picks) >= namefixBodiesPerSet {
			return
		}
		seen[row.MsgID] = true
		picks = append(picks, bodyPick{MsgID: row.MsgID, Bytes: row.Bytes, NFO: nfo, CRC: crc, Tail: tail, HeadOnly: headOnly})
	}
	addFirst := func(f *file) {
		if f == nil {
			return
		}
		crc := anyName(f, crcCapableName)
		is7z := anyName(f, is7zName)
		firstVol := anyName(f, isFirstArchiveVolume)
		// Claude 2026-10-20: split .7z.001 is never the end header.
		// Reason: ngPost first volume is often one 716800-byte yEnc part, so
		//   last==first msgid used to set Tail and skip HeadOnly (live
		//   wire=716800 on .7z.001). CRC is the last .7z.NNN segment.
		//   Bare unsplit .7z (no .NNN) still uses Tail on that one article.
		// Troubleshooting: namefix pattern pack=7z-split still wire=700k on
		//   .7z.001 → HeadOnly not set. Review if: a single-volume .7z.001
		//   exists (same msgid: HeadOnly is added first, last-volume Tail skipped).
		split7z := is7z && (archivePartNum(f.key) > 0 || archivePartNum(f.first.Filename) > 0 || archivePartNum(f.first.Subject) > 0)
		tail := is7z && f.last.MsgID == f.first.MsgID && !split7z
		headOnly := is7z && firstVol && !tail
		addRow(f.first, f.nfo, (crc && !headOnly) || tail, tail, headOnly)
	}
	for _, f := range sorted {
		if anyName(f, isSFVName) {
			addFirst(f)
			break
		}
	}
	par2Present := false
	for _, f := range sorted {
		if anyName(f, isMainPar2Name) {
			addFirst(f)
			par2Present = true
			break
		}
	}
	for _, f := range sorted {
		if anyName(f, isFirstArchiveVolume) {
			// Claude 2026-10-21: first-volume RAR after PAR2 for srrdb CRC.
			// Reason: skip-RAR-when-PAR2 dropped unique FILE_CRC hits
			//   (TestNameFix_SRRDBArchiveCRCRenamesHashRAR). Titled FileDesc
			//   still returns before this pick. rar5-enc shelves in fixSet.
			// Troubleshooting: leftover=41 with two 716KB rar after par2 →
			//   last-volume still added while par2Present. Review if:
			//   hash.mkv FileDesc never reaches this pick (then drop rar).
			addFirst(f)
			break
		}
	}
	var last *file
	lastN := 0
	for _, f := range sorted {
		n := 0
		for _, name := range []string{f.key, f.first.Filename, f.first.Subject, f.last.Filename, f.last.Subject} {
			if p := archivePartNum(name); p > n {
				n = p
			}
		}
		if n > lastN {
			lastN, last = n, f
		}
	}
	if last != nil && lastN >= 1 {
		if anyName(last, is7zName) {
			addRow(last.last, last.nfo, true, true, false)
		} else if lastN > 1 && !par2Present {
			addRow(last.first, last.nfo, true, false, false)
		}
	}
	for _, f := range sorted {
		if f.nfo {
			addFirst(f)
			break
		}
	}
	return picks
}

var (
	reArchivePart = regexp.MustCompile(`(?i)\.part(\d+)\.(?:rar|zip|7z)\b`)
	reSplitVol    = regexp.MustCompile(`(?i)\.(?:7z|zip)\.(\d{3})\b`)
	reRarFollow   = regexp.MustCompile(`(?i)\.r\d{2}\b`)
	reBareArchive = regexp.MustCompile(`(?i)\.(?:rar|zip|7z)\b`)
)

func isSFVName(name string) bool {
	return strings.Contains(strings.ToLower(name), ".sfv")
}

func archivePartNum(name string) int {
	low := strings.ToLower(name)
	for _, re := range []*regexp.Regexp{reArchivePart, reSplitVol} {
		if m := re.FindStringSubmatch(low); len(m) >= 2 {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

func isEncrypted7zName(name string) bool {
	return strings.Contains(strings.ToLower(name), ".7z.enc")
}

func is7zName(name string) bool {
	low := strings.ToLower(name)
	if isEncrypted7zName(low) {
		return false
	}
	return strings.Contains(low, ".7z")
}

func isFirstArchiveVolume(name string) bool {
	low := strings.ToLower(name)
	if isSFVName(low) || strings.Contains(low, ".par2") || strings.Contains(low, ".nfo") || isEncrypted7zName(low) || usenet.IsSampleName(low) {
		return false
	}
	if reArchivePart.MatchString(low) || reSplitVol.MatchString(low) {
		return archivePartNum(low) == 1
	}
	if reRarFollow.MatchString(low) {
		return false
	}
	return reBareArchive.MatchString(low)
}

func crcCapableName(name string) bool {
	// Claude 2026-10-20: last volume is CRC-capable (RAR5 unpacked CRC).
	// Reason: live last-volume BODY (part1of9, 640KB, cands=2) extracted
	//   CRC 322CD4F6 but pick.CRC was false so `namefix crc` never logged
	//   and crcTried stayed 0 on first-volume split-after. Review if:
	//   .r99 follow volumes need a last-volume detector besides partN.
	return isSFVName(name) || isFirstArchiveVolume(name) || archivePartNum(name) > 1 || is7zName(name)
}

func hasCRCCapableTarget(rows []HeaderRow) bool {
	for _, r := range rows {
		if crcCapableName(r.Filename) || crcCapableName(r.Subject) {
			return true
		}
	}
	return false
}

func isMainPar2Name(name string) bool {
	low := strings.ToLower(name)
	return strings.Contains(low, ".par2") && !strings.Contains(low, ".vol")
}

// pickPar2Targets returns the first-segment non-vol .par2 article only.
// Named hash_16K seed must not BODY RAR volumes (live seed was 4.5MB/pass).
func pickPar2Targets(rows []HeaderRow) []bodyPick {
	for _, r := range rows {
		if r.PartN > 1 || r.MsgID == "" {
			continue
		}
		if !isMainPar2Name(r.Filename) && !isMainPar2Name(r.Subject) {
			continue
		}
		return []bodyPick{{MsgID: r.MsgID, Bytes: r.Bytes}}
	}
	return nil
}
