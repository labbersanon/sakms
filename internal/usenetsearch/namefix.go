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
	"time"

	"github.com/labbersanon/sakms/internal/release"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-19: obfuscated-set name recovery (plan D2/D3 + PreDB reverse).
// Reason: obfuscated posts carry no release name; a staged set is renamed from
//   a local PreDB hash (md5/md5md5/sha1 in subject or filename), PreDB
//   filename / IRC [FN:], a scene-shaped header filename, or first-segment
//   PAR2/RAR/yEnc/NFO sampling, then the same admission gate + STAT as crawl.
//   PreDB-only also runs on ≥80% sets still below the 95% BODY/autograb gate
//   so the real title is searchable (NewsLeecher SuperSearch) before grab.
// Troubleshooting: sets never renamed → o2cli grep "namefix pass"; check
//   body_fetches (0 = nothing due, or PreDB-only exhausted/below-gate yield),
//   hash_hits, unresolved, and namefix_body_mb_per_hour.
//   Undo: docs/usenet-nntp-native.md.
// Review if: BODY traffic needs download-activity-aware (bytes) throttling.

// PreDBHit is one scene-release record resolved from the local PreDB.
type PreDBHit struct {
	Title     string
	SizeBytes int64 // 0 when the PreDB did not record a size
}

// PreDBLookup resolves names against the local PreDB (HTTP lookups cached in
// the local table). Implementations must never return an error to the pass:
// outages and malformed responses are misses.
type PreDBLookup interface {
	// ByHash resolves a bare md5/sha1 hex string to a release title.
	ByHash(ctx context.Context, hash string) (PreDBHit, bool)
	// ByName confirms a recovered release name exists.
	ByName(ctx context.Context, name string) (PreDBHit, bool)
	// ByFilename resolves a PreDB inner/archive filename.
	ByFilename(ctx context.Context, filename string) (PreDBHit, bool)
	// ByRequestID resolves an nZEDb/IRC request id, preferring group.
	ByRequestID(ctx context.Context, id int64, group string) (PreDBHit, bool)
}

const (
	// Claude 2026-10-16: 40 (was 20) so one pass can drain yield-group due sets.
	// Reason: 25 due anime/highspeed/tv were split across loops while boneless ate slots.
	// Troubleshooting: namefix pass sets=20 with yield due leftover → bump this.
	// Review if: BODY budget (namefix_body_mb_per_hour) starts hitting BudgetStop.
	namefixSetsPerPass      = 40
	namefixIdentBodyPerPass = 20
	namefixPreDBPerPass     = 200
	// Header-ident quoted titles (no BODY/PreDB HTTP). Run before PreDB.
	namefixHeaderPerPass = 40
	// Claude 2026-10-20: re-BODY named/shelved PAR2 once for hash_16K.
	// Reason: 65 live par2 titles and 225 shelved hash.par2 had FileDesc
	//   packets discarded; donors cannot match until hashes are stored.
	// Troubleshooting: parhash=0 in "namefix starting" with named par2 →
	//   DueParHashSeed / parhash_sampled. Review if: seed starves BODY.
	namefixParHashSeedPerPass = 20
	namefixBodiesPerSet       = 3
	// namefixMaxDuration: stop mid-pass so a hung BODY cannot freeze crawl.
	// Claude 2026-10-19: 20s wall clock like recompute.
	// Reason: after #204 the loop went silent 14 min in namefix BODY
	//   (60s articleIOTimeout × dozens of sets, log only at pass end).
	// Troubleshooting: "namefix starting" with no "namefix pass" for >90s
	//   → this deadline missing or BodyHead ignoring articleIOTimeout.
	// Review if: 20s leaves 95% yield unnamed (raise, do not remove).
	namefixMaxDuration = 20 * time.Second
	// Claude 2026-10-19: namefixMinBodyRemain skipped BodyHead when the 20s
	//   phase was already gone (live remain_ms=-1 on a 43KB par2). Replaced
	//   by WithoutCancel+8s so one overtime article still runs.
	// namefixMinBodyRemain = 200 * time.Millisecond
	// PreDB-only needs hash/filename tokens, not every header.
	namefixPreDBHeaderSample = 64
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
	exhaustedPreDBRetry         = time.Hour
)

// namefixBodyArticleTimeout caps one BodyHead (dial + BODY). Tests shrink it.
//
// Claude 2026-10-19: 8s per article, independent of the 20s phase.
// Reason: live every pass was body leftover=40 fetches=1 bytes=0 unresolved=0.
//
//	One hung BODY/dial returned DeadlineExceeded and aborted all 40 due sets.
//
// Troubleshooting: o2cli grep "namefix body"; remain_ms=0 + leftover=body →
//
//	article timeout not applied or parent ctx already dead before BodyHead.
//
// Review if: 8s leaves 20KB par2 unnamed (raise, still continue to next pick).
var namefixBodyArticleTimeout = 8 * time.Second

// Name sources / confidences (usenet_nntp_releases.name_source/name_confidence).
const (
	sourcePreDB   = "predb"
	sourcePar2    = "par2"
	sourceParHash = "parhash"
	sourceRar     = "rar"
	sourceYenc    = "ybegin"
	sourceNFO     = "nfo"
	sourceSFV     = "sfv"
	sourceSRRDB   = "srrdb"
	sourceNZB     = "nzb"
	confPreDB     = "predb_exact"
	confMedium    = "medium"
	verdictNamed  = "namefix"
)

// bodyBudget is a rolling one-hour BODY byte budget. It lives in the service
// loop (one goroutine), so it needs no locking.
type bodyBudget struct {
	capBytes    int64
	windowStart time.Time
	used        int64
}

func (b *bodyBudget) setCapMB(mb int) {
	if mb <= 0 {
		mb = DefaultNameFixBodyMBPerHour
	}
	b.capBytes = int64(mb) << 20
}

// fits reports whether est more bytes stay within the hourly cap.
func (b *bodyBudget) fits(est int64) bool {
	now := time.Now()
	if b.windowStart.IsZero() || now.Sub(b.windowStart) >= time.Hour {
		b.windowStart, b.used = now, 0
	}
	return b.used+est <= b.capBytes
}

func (b *bodyBudget) add(n int64) { b.used += n }

// NameFixResult is one name-fix pass's aggregate counters.
type NameFixResult struct {
	Sets        int
	HashHits    int
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
}

// Advanced reports whether the pass made progress (drives catchUpPause).
func (r NameFixResult) Advanced() bool {
	return r.Renamed+r.RejectGate+r.RejectStat+r.Unresolved+r.Finalized > 0
}

// NameFixer runs the bounded name-recovery pass over staged sets.
type NameFixer struct {
	Src     usenet.HeaderSource
	Index   *Index
	Crawler *Crawler // admission snapshot, group modes, statVerdict
	PreDB   PreDBLookup
	// SRRDB resolves RAR/ZIP/SFV inner-file CRC32 (nil disables it).
	SRRDB ArchiveCRCLookup
	// NZBPassword looks up unpack passwords by obfuscated archive name
	// (Newznab via Prowlarr-listed indexers; nil disables it).
	NZBPassword NZBPasswordLookup
	Groups      []string
	Budget      *bodyBudget
	// SkipBody runs PreDB-only (OVER/download busy). PAR2 BODY waits.
	SkipBody bool
	// PassBudget caps one Run; zero uses namefixMaxDuration. Tests shorten it.
	PassBudget time.Duration

	UnnamedTTL time.Duration

	nzbPWUsed int
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
	headerSets, err := n.Index.DuePendingHeaderNames(ctx, namefixHeaderPerPass)
	if err != nil {
		return res, err
	}
	predbSets, err := n.Index.DuePendingPreDB(ctx, namefixPreDBPerPass)
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
	}
	// Claude 2026-10-19: PreDB-only first, then ident BODY, then 95% BODY.
	// Reason: live leftover=220 after one ~750KB BODY; hash_hits=0 for hours
	//   while 200 PreDB keys (1883 complete boneless) never ran. 95% yield
	//   still stays BODY-capable (not in preOnly) so a PreDB miss can PAR2.
	// Troubleshooting: namefix deadline leftover≈predb with hash_hits=0 →
	//   add(sets) ran before preOnly. Review if: 20s is raised, not removed.
	skipPre := map[string]bool{}
	keyOf := func(set PendingSet) string { return set.Group + "\x00" + set.Key }
	for _, list := range [][]PendingSet{sets, ident, headerSets} {
		for _, set := range list {
			skipPre[keyOf(set)] = true
		}
	}
	var preOnly []PendingSet
	for _, set := range predbSets {
		if !skipPre[keyOf(set)] {
			preOnly = append(preOnly, set)
		}
	}
	if len(sets)+len(ident)+len(predbSets)+len(headerSets)+len(parSeed) > 0 {
		log.Printf("usenetsearch: namefix starting header=%d body=%d ident=%d predb=%d parhash=%d skip_body=%t",
			len(headerSets), len(sets), len(ident), len(predbSets), len(parSeed), n.SkipBody)
	}
	budget := n.PassBudget
	if budget <= 0 {
		budget = namefixMaxDuration
	}
	done := map[string]bool{}
	// Claude 2026-10-19: each phase's 20s context is passed into BodyHead.
	// Reason: live namefix starting body=2 sat 3.5 min; the budget only
	//   ran between sets while BodyHead used a 60s socket timeout.
	// Troubleshooting: namefix starting with no pass for >90s → BodyHead
	//   still ignoring ctx (fetchSegmentUntil / armNNTPDeadlineUntil).
	// Review if: articleIOTimeout becomes a settings knob.
	run := func(in []PendingSet, phase string) error {
		phaseCtx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		deadline := func(i int) error {
			log.Printf("usenetsearch: namefix deadline phase=%s leftover=%d", phase, len(in)-i)
			return nil
		}
		for i, set := range in {
			if phaseCtx.Err() != nil {
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
	// Claude 2026-10-19: PreDB-only and BODY each get the 20s budget.
	// Reason: live due PreDB sets have 3k–7k headers; SetRows of one set
	//   burned the shared deadline (hash_hits=1 leftover=222 body_fetches=0).
	// Troubleshooting: leftover=predb with body_fetches=0 after a hit →
	//   BODY phase missing. Review if: PreDB sample 64 misses a hash token.
	// Claude 2026-10-19: quoted ident titles before PreDB HTTP / BODY.
	// Reason: live 95% Black Clover/Yameii were in the BODY queue, stripped
	//   from preOnly; one hash SetRows+ByHash burned PreDB (leftover=162,
	//   i=0) and BODY leftover=59, renamed=0. Header quotes need no BODY.
	// Troubleshooting: header=0 while pending95 subjects quote 1080p titles
	//   → DuePendingHeaderNames regex. Review if: 4K-without-p.
	if err := run(headerSets, "header"); err != nil {
		return res, err
	}
	if err := run(preOnly, "predb"); err != nil {
		return res, err
	}
	// Named PAR2 donors before pending BODY so hash_16K matches this pass.
	if err := run(parSeed, "parhash"); err != nil {
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
	if err := run(body, "body"); err != nil {
		return res, err
	}
	if res.Sets > 0 || res.Finalized > 0 || res.BodyFetches > 0 || res.Renamed > 0 {
		log.Printf("usenetsearch: namefix pass sets=%d hash_hits=%d body_fetches=%d body_bytes=%d renamed=%d by_source=%s reject_gate=%d reject_stat=%d unresolved=%d finalized=%d busy=%t",
			res.Sets, res.HashHits, res.BodyFetches, res.BodyBytes, res.Renamed, formatBySource(res.BySource),
			res.RejectGate, res.RejectStat, res.Unresolved, res.Finalized, res.Busy)
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

// fixSet attempts one staged set. stop=true ends the pass (busy / budget).
func (n *NameFixer) fixSet(ctx context.Context, set PendingSet, res *NameFixResult, statBudget *int) (stop bool, err error) {
	if set.AlreadyNamed {
		return n.seedParHashes(ctx, set, res, statBudget)
	}
	limit := 0
	if set.PreDBOnly {
		limit = namefixPreDBHeaderSample
	}
	// Claude 2026-10-20: setRows must not inherit leftover phase crumbs.
	// Reason: CI ExpiredPhase failed `setRows` with 15ms ctx (91 header rows)
	//   before BodyHead's WithoutCancel could fetch part01. Live 20s phase
	//   ending mid-query would skip FILE_CRC the same way.
	// Troubleshooting: namefix deadline leftover with no crc log on a due
	//   rar set → this WithoutCancel missing. Review if: setRows grows past
	//   a few hundred ms (then cap it, still fetch part01).
	rows, err := n.Index.setRows(context.WithoutCancel(ctx), set.Group, set.Key, limit)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		_, err := n.Index.skipAndDelete(ctx, []releaseKey{{set.Group, set.Key}}, skipReasonOrphan)
		return false, err
	}
	res.Sets++

	for _, cand := range headerNameCandidates(rows) {
		name, source, conf, predb, ok := n.accept(ctx, cand.name, cand.source, set.PayloadBytes)
		if ok {
			return n.promote(ctx, set, rows, name, source, conf, predb, res, statBudget)
		}
	}
	if hit, ok := n.lookupPreDB(ctx, set, rows); ok {
		res.HashHits++
		return n.promote(ctx, set, rows, hit.Title, sourcePreDB, confPreDB, "hit", res, statBudget)
	}
	if set.PreDBOnly {
		return false, n.Index.ScheduleNameRetry(ctx, set.Group, set.Key, exhaustedPreDBRetry)
	}

	if set.PayloadBytes < namefixMinPayload && !set.HasPar2 {
		res.Finalized++
		return false, n.finalize(ctx, set)
	}

	hashOnly := false
	sampled := false
	triedBody := false
	crcTried := false
	heldPW := false
	var zstart []byte
	var zsize uint64
	pat := detectNamePattern(rows)
	ngPost := looksLikeNgPost(rows)
	skip7z := ngPost && pat.Pack == "7z-split" && set.Attempts > 0
	work := context.WithoutCancel(ctx)
	var nzbHit NZBPasswordHit
	var nzbGot bool
	// Claude 2026-10-21: Newznab lookup uses the header filename, not BODY.
	// Reason: ngPost skip_7z after Attempts>0 skipped the 7z-aes miss path,
	//   so live hash.7z never queried indexers. Title can promote without
	//   a footer; a password clears skip_7z so Tail can decrypt CRC.
	// Troubleshooting: skip_7z=true and no nzb-password log → this header
	//   lookup missing. Review if: password-only hits should persist.
	if n.NZBPassword != nil && (pat.Pack == "7z-split" || pat.Obf == "hash-7z") {
		if n.nzbPWUsed >= namefixNZBPasswordPerPass {
			heldPW = true
		} else if hit, ok := n.fetchNZBPassword(work, rows); ok {
			nzbHit, nzbGot = hit, true
			if name, source, conf, predb, ok := n.accept(work, hit.Title, sourceNZB, set.PayloadBytes); ok {
				return n.promote(work, set, rows, name, source, conf, predb, res, statBudget)
			}
			if len(hit.Passwords) > 0 {
				skip7z = false
			}
		}
	}
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
		//   remain_ms=-1 (setRows/lookupPreDB ate the 20s) and leftover=40
		//   with body_fetches=0. Hash.par2 never moved.
		// Troubleshooting: o2cli grep "namefix body skip" → this WithoutCancel
		//   missing. Review if: one overtime BODY delays crawl (>30s/pass).
		bodyCtx, cancel := context.WithTimeout(work, namefixBodyArticleTimeout)
		sample, berr := n.Src.BodyHead(bodyCtx, pick.MsgID, headCap)
		cancel()
		if errors.Is(berr, usenet.ErrBusy) {
			res.Busy = true
			return true, nil
		}
		res.BodyFetches++
		triedBody = true
		if berr != nil {
			log.Printf("usenetsearch: namefix body msgid=%s bytes=%d remain_ms=%d wire=0 yenc=0 cands=0 par2=false err=%v",
				pick.MsgID, pick.Bytes, namefixBodyArticleTimeout.Milliseconds(), berr)
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
		log.Printf("usenetsearch: namefix body msgid=%s bytes=%d remain_ms=%d wire=%d yenc=%d cands=%d par2=%t cand=%q err=<nil>",
			pick.MsgID, pick.Bytes, namefixBodyArticleTimeout.Milliseconds(), sample.WireBytes, len(sample.YencName), len(cands), isPar2, cand0)
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
			n.rememberParHashes(work, set, sample.Data)
			name, ok, err := n.Index.LookupParHashDonor(work, set.Group, set.Key, set.PayloadBytes)
			if err != nil {
				log.Printf("usenetsearch: parhash lookup group=%s err=%v", set.Group, err)
			} else if ok {
				return n.promote(work, set, rows, name, sourceParHash, confMedium, "parhash", res, statBudget)
			}
		}
		allHash := true
		if len(cands) > 0 {
			// Claude 2026-10-19: FileDesc/yEnc hashes are not in header rows.
			// Reason: live BODY now returns par2=true cands=1–2 renamed=0;
			//   subjects are random tokens, PAR2 FileDesc is often md5(title).
			// Troubleshooting: body_bytes>0 par2=true hash_hits=0 → this
			//   ByHash/ByFilename on sample names not called. Review if:
			//   FileDesc is a non-hex random token (then IRC FN only).
			if hit, ok := n.lookupPreDBNames(work, set, cands); ok {
				res.HashHits++
				return n.promote(work, set, rows, hit.Title, sourcePreDB, confPreDB, "hit", res, statBudget)
			}
			for _, cand := range cands {
				if recoveredUnusable(cand.name) {
					continue
				}
				allHash = false
				name, source, conf, predb, ok := n.accept(work, cand.name, cand.source, set.PayloadBytes)
				if ok {
					return n.promote(work, set, rows, name, source, conf, predb, res, statBudget)
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
		if hit, ok := n.lookupCRCs(work, set, crcs); ok {
			return n.promote(work, set, rows, hit.Title, sourceSRRDB, confMedium, "srrdb", res, statBudget)
		}
		// Claude 2026-10-21: Newznab password after empty 7zAES miss.
		// Reason: payload=7z-aes crcs=0 is an empty-password miss; the
		//   password is in the indexer NZB (X-DNZB / meta), not on NNTP.
		//   HTML scrape is not used. Cap lookups per pass; if the cap
		//   hits, do not bump attempts so the set stays due.
		// Troubleshooting: nzb-password miss with skip_7z=true → lookup
		//   already ran. Review if: RAR passwords should use this too.
		if kind == "7z-aes" && len(crcs) == 0 {
			if !nzbGot && n.NZBPassword != nil && n.nzbPWUsed >= namefixNZBPasswordPerPass {
				heldPW = true
			} else {
				if !nzbGot {
					nzbHit, nzbGot = n.fetchNZBPassword(work, rows)
				}
				if nzbGot {
					stop, done, err := n.applyNZBHit(work, set, rows, sample, zstart, nzbHit, res, statBudget)
					if done || err != nil {
						return stop, err
					}
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
	//   tests and PreDB-only retries). Live 225 still seed because the
	//   column defaults false until this runs once after 0049.
	// Troubleshooting: parhash=N on every pass for the same keys → sampled
	//   not set after BodyHead. Review if: 430 should retry after dump import.
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
		if crcTried || !hasCRCCapableTarget(rows) {
			return false, n.Index.ShelveBodyAttempts(work, set.Group, set.Key)
		}
	}
	if heldPW {
		return false, nil
	}
	return false, n.Index.BumpNameAttempt(work, set.Group, set.Key, set.Attempts)
}

// seedParHashes BODY-reads a named PAR2 set once to index hash_16K donors.
func (n *NameFixer) seedParHashes(ctx context.Context, set PendingSet, res *NameFixResult, statBudget *int) (bool, error) {
	if n.Src == nil || n.Index == nil {
		return false, nil
	}
	rows, err := n.Index.setRows(ctx, set.Group, set.Key, 0)
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
		sample, berr := n.Src.BodyHead(bodyCtx, pick.MsgID, namefixHeadBytes)
		cancel()
		if errors.Is(berr, usenet.ErrBusy) {
			res.Busy = true
			return true, nil
		}
		res.BodyFetches++
		tried = true
		if berr != nil {
			log.Printf("usenetsearch: namefix body msgid=%s bytes=%d remain_ms=%d wire=0 yenc=0 cands=0 par2=false err=%v",
				pick.MsgID, pick.Bytes, namefixBodyArticleTimeout.Milliseconds(), berr)
			continue
		}
		n.Budget.add(sample.WireBytes)
		res.BodyBytes += sample.WireBytes
		isPar2 := bytes.HasPrefix(sample.Data, par2Magic)
		log.Printf("usenetsearch: namefix body msgid=%s bytes=%d remain_ms=%d wire=%d yenc=%d cands=0 par2=%t err=<nil>",
			pick.MsgID, pick.Bytes, namefixBodyArticleTimeout.Milliseconds(), sample.WireBytes, len(sample.YencName), isPar2)
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
		rows, err := n.Index.setRows(ctx, sib.Group, sib.Key, 0)
		if err != nil || len(rows) == 0 {
			continue
		}
		_, _ = n.promote(ctx, sib, rows, name, sourceParHash, confMedium, "parhash", res, statBudget)
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

// accept normalizes a recovered name and applies the D2 acceptance rules:
// PreDB exact (size within ±15%) is always accepted; par2/rar/ybegin names that
// parse as title + (year|episode) + (resolution|source) are accepted without a
// PreDB match; NFO-only names need a PreDB hit.
func (n *NameFixer) accept(ctx context.Context, raw, source string, payload int64) (name, src, conf, predb string, ok bool) {
	if recoveredUnusable(raw) {
		return "", "", "", "", false
	}
	norm := normalizeRecovered(raw)
	predb = "n/a"
	// Only release-shaped names are worth a (rate-limited) PreDB lookup.
	if n.PreDB != nil && hasTitleAndMarker(norm) {
		if hit, found := n.PreDB.ByName(ctx, norm); found && sizeWithin(hit.SizeBytes, payload) {
			title := hit.Title
			if title == "" {
				title = norm
			}
			return title, sourcePreDB, confPreDB, "hit", true
		}
		predb = "miss"
	}
	if source != sourceNFO && quotedReleaseName(norm) {
		return norm, source, confMedium, predb, true
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
// that is not a hash token. Hash.par2 stays unnamed (IRC/PAR2).
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
func (n *NameFixer) promote(ctx context.Context, set PendingSet, rows []HeaderRow, name, source, conf, predb string, res *NameFixResult, statBudget *int) (bool, error) {
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
		snap, err := c.Admitter.Snapshot(ctx)
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
			keep, _, _ := c.statVerdict(ctx, set.Group, name, rows, false, statBudget, &busy, &st)
			if !keep {
				return reject(&res.RejectStat)
			}
		}
	}
	if err := n.Index.RenameRelease(ctx, set.Group, set.Key, name, source, conf); err != nil {
		return false, err
	}
	res.Renamed++
	res.BySource[source]++
	log.Printf("usenetsearch: renamed group=%s key=%s name=%s source=%s confidence=%s predb=%s pct=%.1f",
		set.Group, set.Key, name, source, conf, predb, float64(set.CompletenessBP)/100)
	n.promoteParHashSiblings(ctx, set, name, res, statBudget)
	if busy {
		res.Busy = true
	}
	return busy, nil
}

// lookupPreDB tries nZEDb-style reverse lookups: md5/sha1/md5md5 tokens from
// every header's subject and filename, then PreDB.filename / IRC [FN:], then
// dump/IRC request ids from EFNet/Bin-Req wrappers.
func preDBHitOK(hit PreDBHit, ok bool, payload int64) bool {
	return ok && hit.Title != "" && sizeWithin(hit.SizeBytes, payload)
}

func (n *NameFixer) lookupPreDB(ctx context.Context, set PendingSet, rows []HeaderRow) (PreDBHit, bool) {
	if n.PreDB == nil {
		return PreDBHit{}, false
	}
	for _, h := range hashCandidates(rows) {
		if hit, ok := n.PreDB.ByHash(ctx, h); preDBHitOK(hit, ok, set.PayloadBytes) {
			return hit, true
		}
	}
	for _, fn := range filenameCandidates(rows) {
		if hit, ok := n.PreDB.ByFilename(ctx, fn); preDBHitOK(hit, ok, set.PayloadBytes) {
			return hit, true
		}
	}
	for _, id := range requestIDCandidates(rows) {
		if hit, ok := n.PreDB.ByRequestID(ctx, id, set.Group); preDBHitOK(hit, ok, set.PayloadBytes) {
			return hit, true
		}
	}
	return PreDBHit{}, false
}

func (n *NameFixer) fetchNZBPassword(ctx context.Context, rows []HeaderRow) (NZBPasswordHit, bool) {
	if n.NZBPassword == nil {
		return NZBPasswordHit{}, false
	}
	stem := archiveSearchStem(rows)
	if stem == "" {
		return NZBPasswordHit{}, false
	}
	n.nzbPWUsed++
	hit, ok := n.NZBPassword.Lookup(ctx, stem)
	if !ok {
		log.Printf("usenetsearch: namefix nzb-password q=%q miss", stem)
		return NZBPasswordHit{}, false
	}
	shown := hit.Title
	if len(shown) > 80 {
		shown = shown[:80]
	}
	log.Printf("usenetsearch: namefix nzb-password q=%q indexer=%s pw=%t title=%q",
		stem, hit.Indexer, len(hit.Passwords) > 0, shown)
	return hit, true
}

func (n *NameFixer) applyNZBHit(ctx context.Context, set PendingSet, rows []HeaderRow, sample usenet.BodySample, zstart []byte, hit NZBPasswordHit, res *NameFixResult, statBudget *int) (stop bool, done bool, err error) {
	try := func(name, source, conf, predb string) (bool, bool, error) {
		stop, err := n.promote(ctx, set, rows, name, source, conf, predb, res, statBudget)
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
		if pre, ok := n.lookupPreDBNames(ctx, set, cands); ok {
			res.HashHits++
			return try(pre.Title, sourcePreDB, confPreDB, "hit")
		}
		for _, cand := range cands {
			name, source, conf, predb, ok := n.accept(ctx, cand.name, cand.source, set.PayloadBytes)
			if ok {
				return try(name, source, conf, predb)
			}
		}
	}
	name, source, conf, predb, ok := n.accept(ctx, hit.Title, sourceNZB, set.PayloadBytes)
	if ok {
		return try(name, source, conf, predb)
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

func (n *NameFixer) lookupPreDBNames(ctx context.Context, set PendingSet, cands []nameCand) (PreDBHit, bool) {
	if n.PreDB == nil {
		return PreDBHit{}, false
	}
	for _, cand := range cands {
		for _, h := range hashTokens(cand.name) {
			if hit, ok := n.PreDB.ByHash(ctx, h); preDBHitOK(hit, ok, set.PayloadBytes) {
				return hit, true
			}
		}
		if hit, ok := n.PreDB.ByFilename(ctx, cand.name); preDBHitOK(hit, ok, set.PayloadBytes) {
			return hit, true
		}
		if recovered := normalizeRecovered(cand.name); recovered != cand.name {
			if hit, ok := n.PreDB.ByFilename(ctx, recovered); preDBHitOK(hit, ok, set.PayloadBytes) {
				return hit, true
			}
		}
	}
	return PreDBHit{}, false
}

func hashCandidates(rows []HeaderRow) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		for _, t := range hashTokens(s) {
			if seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, r := range rows {
		add(obfSkeleton(r.Subject))
		add(r.Subject)
		add(r.Filename)
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func requestIDCandidates(rows []HeaderRow) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, r := range rows {
		id := requestIDFromSubject(r.Subject)
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) >= 4 {
			break
		}
	}
	return out
}

func filenameCandidates(rows []HeaderRow) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		key := strings.ToLower(s)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, s)
	}
	for _, r := range rows {
		q := quotedFilename(r.Subject)
		add(r.Filename)
		add(normalizeRecovered(r.Filename))
		add(q)
		add(normalizeRecovered(q))
	}
	return out
}

func headerNameCandidates(rows []HeaderRow) []nameCand {
	seen := map[string]bool{}
	var out []nameCand
	add := func(raw string) {
		key := strings.ToLower(normalizeRecovered(raw))
		if key == "" || hashCore(key) != "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, nameCand{raw, sourceYenc})
	}
	for _, r := range rows {
		add(r.Filename)
		// Claude 2026-10-19: quoted subject even when Filename is `01.par2`.
		// Reason: rows ingested before quotedFilename keep the reFileTok
		//   stub; live pending95 still has the title in Subject quotes.
		// Troubleshooting: header namefix misses Black Clover while
		//   ParseSubject on a fresh crawl would store it. Review if:
		//   headers are rewritten on re-OVER (then Filename is enough).
		add(quotedFilename(r.Subject))
	}
	return out
}

// sizeWithin reports whether payload is within ±15% of a PreDB-reported size
// (unknown on either side passes).
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
// SFV, first-volume RAR/ZIP/7z (part01 / .7z.001 / bare .rar), last volume
// (RAR first segment; 7z last segment), main non-vol PAR2, then NFO.
// Volume PAR2 never fills a slot. Encrypted .7z.enc is skipped.
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
	for _, f := range sorted {
		if anyName(f, isFirstArchiveVolume) {
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
		} else if lastN > 1 {
			addRow(last.first, last.nfo, true, false, false)
		}
	}
	for _, f := range sorted {
		if anyName(f, isMainPar2Name) {
			addFirst(f)
			break
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
	if isSFVName(low) || strings.Contains(low, ".par2") || strings.Contains(low, ".nfo") || isEncrypted7zName(low) {
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
