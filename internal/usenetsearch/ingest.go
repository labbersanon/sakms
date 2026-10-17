package usenetsearch

import (
	"context"
	"sort"
	"unicode/utf8"

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
		// Claude 2026-10-16: NNTP headers are 8-bit; Postgres TEXT and
		//   ParseSubject both require valid UTF-8.
		// Reason: latin1 subjects aborted whole-group Upsert (SQLSTATE 22021)
		//   and panicked ParseSubject via ToLower index overshoot.
		// Troubleshooting: crawl GROUP: invalid byte sequence UTF8 → this
		//   sanitizer not applied to that column.
		// Review if: lib/nntp already recodes Overview strings.
		subj := nntpUTF8(r.Subject)
		from := nntpUTF8(r.From)
		msgid := nntpUTF8(r.MessageID)
		headers = append(headers, parseHeader(group, subj, from, msgid, r.Number, r.DateUnix, int64(r.Bytes), c.NameFixEnabled))
	}
	return headers
}

// parseHeader is the shared OVER → HeaderRow path for crawl ingest and the
// in-place collection-key rewrite.
func parseHeader(group, subj, from, msgid string, num int, postedAt, bytes int64, nameFix bool) HeaderRow {
	ps := ParseSubject(subj)
	h := HeaderRow{
		Group:       group,
		MsgNum:      num,
		MsgID:       msgid,
		Subject:     subj,
		From:        from,
		PostedAt:    postedAt,
		Bytes:       bytes,
		ReleaseName: ps.Name,
		PartN:       ps.PartN,
		PartM:       ps.PartM,
		Filename:    ps.Filename,
		YencSize:    ps.YencSize,
		Obfuscated:  ps.Obfuscated || !ps.OK,
		IsMeta:      ps.Meta || IsMetaSubject(subj, ps.Filename),
		FileN:       ps.FileN,
		FileM:       ps.FileM,
	}
	if nameFix {
		stageObfuscated(&h, ps)
	}
	return h
}

// stageObfuscated rewrites an obfuscated/unparsed BINARY row (segment counter
// present) to its staged-set key so it can be name-recovered later. Non-binary
// junk and readable names are left untouched (plan D3 step 1).
func stageObfuscated(h *HeaderRow, ps ParsedSubject) {
	partN, partM := yencParts(h.Subject)
	if partM <= 0 {
		return
	}
	skel := obfSkeleton(h.Subject)
	// Claude 2026-10-16: refuse empty/short skeletons (collection-grouping 2A).
	// Reason: hash(group+from+"") collapsed one poster's bluray posts into a
	//   100k-seg pending_name mega-bucket. NNTmux keeps the hash token in the
	//   cleaned name; we require a skeleton long enough to be that token.
	// Troubleshooting: staged_obf=0 on quoted ".part01.rar" subjects is expected.
	// Review if: a leftover unique filename-base is hashed instead of dropping.
	if utf8.RuneCountInString(skel) < minObfSkeleton || !(ps.Obfuscated || !ps.OK || reHexBlob.MatchString(skel) || looksObfuscated(skel)) {
		return
	}
	h.ReleaseName = ObfKey(h.Group, h.From, h.Subject)
	h.Obfuscated = true
	h.PartN, h.PartM = partN, partM
	if h.Filename == "" {
		if m := reFileTok.FindStringSubmatch(h.Subject); len(m) == 2 {
			h.Filename = m[1]
		}
	}
}

// splitStaged separates staged obfuscated rows from the rest of a chunk.
func splitStaged(headers []HeaderRow) (rest, staged []HeaderRow) {
	rest = headers[:0:0]
	for _, h := range headers {
		if isObfKey(h.ReleaseName) {
			staged = append(staged, h)
		} else {
			rest = append(rest, h)
		}
	}
	return rest, staged
}

// DefaultUnnamedMaxPct is the default share of the index budget staged
// (unnamed) sets may occupy.
const DefaultUnnamedMaxPct = 25

// capStaged enforces the unnamed-bytes cap (plan D3 step 2): above it, rows of
// NEW staged sets are dropped (skip_obf_cap) while rows of already-staged or
// renamed sets still flow in. Sets whose true name was already rejected
// (verdict cached by namefix) never re-stage.
func (c *Crawler) capStaged(ctx context.Context, group string, staged []HeaderRow) ([]HeaderRow, error) {
	if len(staged) == 0 {
		return nil, nil
	}
	if c.Admitter != nil {
		kept := staged[:0:0]
		for _, h := range staged {
			if keep, _, ok := c.Admitter.getVerdict(group, h.ReleaseName); ok && !keep {
				continue
			}
			kept = append(kept, h)
		}
		staged = kept
	}
	pct := c.UnnamedMaxPct
	if pct <= 0 {
		pct = DefaultUnnamedMaxPct
	}
	if len(staged) == 0 {
		return nil, nil
	}
	// Claude 2026-10-16: only stage *new* obfuscated sets on staging cores
	// (packages.json step ≤2 or pinned) or groups that already have a named
	// release (NNTmux backfill=1 analog). Cap is per-group.
	// Reason: IsPackageCore included step-5 noise (boneless/dvd); those filled
	//   a global unnamed_max_pct so moovee/teevee could not stage. Existing
	//   keys still flow in.
	// Troubleshooting: skip_obf_cap high on boneless with staged_obf=0 is expected.
	// Review if: an operator opt-in "stage hashes on this LIST group" is added.
	dropNew := !c.obfStageAllowed(ctx, group)
	if !dropNew && c.MaxGB > 0 {
		used, err := c.Index.StagedBytes(ctx, group)
		if err != nil {
			return nil, err
		}
		if used >= int64(c.MaxGB)*1024*1024*1024*int64(pct)/100 {
			dropNew = true
		}
	}
	if !dropNew {
		return staged, nil
	}
	keys := make([]string, 0, len(staged))
	seen := map[string]bool{}
	for _, h := range staged {
		if !seen[h.ReleaseName] {
			seen[h.ReleaseName] = true
			keys = append(keys, h.ReleaseName)
		}
	}
	existing, err := c.Index.ExistingReleases(ctx, group, keys)
	if err != nil {
		return nil, err
	}
	kept := staged[:0:0]
	for _, h := range staged {
		if existing[h.ReleaseName] {
			kept = append(kept, h)
		}
	}
	return kept, nil
}

func (c *Crawler) obfStageAllowed(ctx context.Context, group string) bool {
	if IsStagingCore(group) {
		return true
	}
	if c.obfStageOK == nil {
		c.obfStageOK = map[string]bool{}
	}
	if v, ok := c.obfStageOK[group]; ok {
		return v
	}
	ok := false
	if c.Index != nil {
		named, err := c.Index.GroupHasNamedRelease(ctx, group)
		ok = err == nil && named
	}
	c.obfStageOK[group] = ok
	return ok
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
	c.curMaxPosted = 0
	c.curEmpty = false
	for _, r := range rows {
		if r.DateUnix > c.curMaxPosted {
			c.curMaxPosted = r.DateUnix
		}
	}
	var gaps [][2]int
	if opts.CaptureGaps {
		gaps = missingRanges(rows, a, b)
		c.curGaps = len(gaps)
	}
	headers := c.buildHeaders(group, rows)

	c.curStaged, c.curSkipObfCap = 0, 0
	var staged []HeaderRow
	if c.NameFixEnabled {
		var all []HeaderRow
		headers, all = splitStaged(headers)
		staged, err = c.capStaged(ctx, group, all)
		if err != nil {
			return 0, err
		}
		c.curStaged, c.curSkipObfCap = len(staged), len(all)-len(staged)
	}

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
	} else {
		kept = c.dropWrongLanguage(candidates)
	}
	kept = append(kept, c.dropWrongLanguage(direct)...)
	kept = append(kept, staged...)
	c.curEmpty = len(rows) > 0 && len(kept) == 0
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
