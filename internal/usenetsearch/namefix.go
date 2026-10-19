package usenetsearch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/release"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-18: obfuscated-set name recovery (plan D2/D3 + PreDB reverse).
// Reason: obfuscated posts carry no release name; a staged set is renamed from
//   a local PreDB hash (md5/md5md5/sha1 in subject or filename), PreDB
//   filename / IRC [FN:], a scene-shaped header filename, or first-segment
//   PAR2/RAR/yEnc/NFO sampling, then the same admission gate + STAT as crawl.
// Troubleshooting: sets never renamed → o2cli grep "namefix pass"; check
//   body_fetches (0 = nothing due, or PreDB-only exhausted yield), hash_hits,
//   unresolved, and namefix_body_mb_per_hour. Undo: docs/usenet-nntp-native.md.
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
}

const (
	// Claude 2026-10-16: 40 (was 20) so one pass can drain yield-group due sets.
	// Reason: 25 due anime/highspeed/tv were split across loops while boneless ate slots.
	// Troubleshooting: namefix pass sets=20 with yield due leftover → bump this.
	// Review if: BODY budget (namefix_body_mb_per_hour) starts hitting BudgetStop.
	namefixSetsPerPass  = 40
	namefixBodiesPerSet = 3
	// namefixMinPayload: smaller sets are finalized without any BODY fetch.
	namefixMinPayload = 100 << 20
	// namefixHeadBytes caps the decoded bytes kept from one BODY.
	namefixHeadBytes = 256 << 10
	// DefaultNameFixBodyMBPerHour caps BODY volume spent on name recovery.
	DefaultNameFixBodyMBPerHour = 512
	namefixSizeTolerance        = 0.15
	namefixMaxCandidates        = 6
	exhaustedPreDBRetry         = time.Hour
)

// Name sources / confidences (usenet_nntp_releases.name_source/name_confidence).
const (
	sourcePreDB  = "predb"
	sourcePar2   = "par2"
	sourceRar    = "rar"
	sourceYenc   = "ybegin"
	sourceNFO    = "nfo"
	confPreDB    = "predb_exact"
	confMedium   = "medium"
	verdictNamed = "namefix"
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
	Groups  []string
	Budget  *bodyBudget

	UnnamedTTL time.Duration
}

// Run does one pass: TTL/attempt sweep, then up to namefixSetsPerPass due sets.
// ErrBusy or an exhausted BODY budget stops the pass without charging attempts.
func (n *NameFixer) Run(ctx context.Context) (NameFixResult, error) {
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
	exhausted, err := n.Index.DueExhaustedYield(ctx, n.Groups, namefixSetsPerPass)
	if err != nil {
		return res, err
	}
	sets, err := n.Index.DuePendingNames(ctx, n.Groups, namefixSetsPerPass)
	if err != nil {
		return res, err
	}
	for _, set := range append(exhausted, sets...) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		stop, err := n.fixSet(ctx, set, &res, &statBudget)
		if err != nil {
			return res, err
		}
		if stop {
			break
		}
	}
	if res.Sets > 0 || res.Finalized > 0 {
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
	rows, err := n.Index.SetRows(ctx, set.Group, set.Key)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		_, err := n.Index.skipAndDelete(ctx, []releaseKey{{set.Group, set.Key}}, skipReasonOrphan)
		return false, err
	}
	res.Sets++

	if hit, ok := n.lookupPreDB(ctx, set, rows); ok {
		res.HashHits++
		return n.promote(ctx, set, rows, hit.Title, sourcePreDB, confPreDB, "hit", res, statBudget)
	}
	for _, cand := range headerNameCandidates(rows) {
		name, source, conf, predb, ok := n.accept(ctx, cand.name, cand.source, set.PayloadBytes)
		if ok {
			return n.promote(ctx, set, rows, name, source, conf, predb, res, statBudget)
		}
	}
	if set.PreDBOnly {
		return false, n.Index.ScheduleNameRetry(ctx, set.Group, set.Key, exhaustedPreDBRetry)
	}

	if set.PayloadBytes < namefixMinPayload {
		res.Finalized++
		return false, n.finalize(ctx, set)
	}

	for _, pick := range pickBodyTargets(rows) {
		if !n.Budget.fits(pick.Bytes) {
			res.BudgetStop = true
			return true, nil
		}
		sample, berr := n.Src.BodyHead(ctx, pick.MsgID, namefixHeadBytes)
		if errors.Is(berr, usenet.ErrBusy) {
			res.Busy = true
			return true, nil
		}
		res.BodyFetches++
		if berr != nil {
			continue
		}
		n.Budget.add(sample.WireBytes)
		res.BodyBytes += sample.WireBytes
		for _, cand := range candidatesFromSample(sample, pick.NFO) {
			name, source, conf, predb, ok := n.accept(ctx, cand.name, cand.source, set.PayloadBytes)
			if ok {
				return n.promote(ctx, set, rows, name, source, conf, predb, res, statBudget)
			}
		}
	}

	res.Unresolved++
	return false, n.Index.BumpNameAttempt(ctx, set.Group, set.Key, set.Attempts)
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

// accept normalizes a recovered name and applies the D2 acceptance rules:
// PreDB exact (size within ±15%) is always accepted; par2/rar/ybegin names that
// parse as title + (year|episode) + (resolution|source) are accepted without a
// PreDB match; NFO-only names need a PreDB hit.
func (n *NameFixer) accept(ctx context.Context, raw, source string, payload int64) (name, src, conf, predb string, ok bool) {
	norm := normalizeRecovered(raw)
	if norm == "" || hashCore(norm) != "" || looksObfuscated(norm) || tokenLooksObfuscated(norm) {
		return "", "", "", "", false
	}
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
	if source != sourceNFO && parsesAsRelease(norm) {
		return norm, source, confMedium, predb, true
	}
	return "", "", "", "", false
}

// promote runs an accepted name through the admission gate and STAT (when
// admission is on), then renames the set. A gate reject or confirmed-missing
// STAT deletes the set (same irreversibility as crawl admission).
func (n *NameFixer) promote(ctx context.Context, set PendingSet, rows []HeaderRow, name, source, conf, predb string, res *NameFixResult, statBudget *int) (bool, error) {
	busy := false
	if c := n.Crawler; c != nil && !c.languageOK(name) {
		res.RejectGate++
		n.markRejected(set)
		_, err := n.Index.skipAndDelete(ctx, []releaseKey{{set.Group, set.Key}}, skipReasonRejected)
		return false, err
	}
	if c := n.Crawler; c != nil && c.AdmissionEnabled && c.Admitter != nil {
		snap, err := c.Admitter.Snapshot(ctx)
		if err != nil {
			return false, err
		}
		if snap != nil {
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
				res.RejectGate++
				n.markRejected(set)
				_, err := n.Index.skipAndDelete(ctx, []releaseKey{{set.Group, set.Key}}, skipReasonRejected)
				return false, err
			}
			var st chunkStats
			keep, _, _ := c.statVerdict(ctx, set.Group, name, rows, false, statBudget, &busy, &st)
			if !keep {
				res.RejectStat++
				n.markRejected(set)
				_, err := n.Index.skipAndDelete(ctx, []releaseKey{{set.Group, set.Key}}, skipReasonRejected)
				return false, err
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
	if busy {
		res.Busy = true
	}
	return busy, nil
}

// lookupPreDB tries nZEDb-style reverse lookups: md5/sha1/md5md5 tokens from
// every header's subject and filename, then PreDB.filename / IRC [FN:].
func (n *NameFixer) lookupPreDB(ctx context.Context, set PendingSet, rows []HeaderRow) (PreDBHit, bool) {
	if n.PreDB == nil {
		return PreDBHit{}, false
	}
	okHit := func(hit PreDBHit, ok bool) bool {
		return ok && hit.Title != "" && sizeWithin(hit.SizeBytes, set.PayloadBytes)
	}
	for _, h := range hashCandidates(rows) {
		if hit, ok := n.PreDB.ByHash(ctx, h); okHit(hit, ok) {
			return hit, true
		}
	}
	for _, fn := range filenameCandidates(rows) {
		if hit, ok := n.PreDB.ByFilename(ctx, fn); okHit(hit, ok) {
			return hit, true
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

func filenameCandidates(rows []HeaderRow) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		key := strings.ToLower(s)
		if seen[key] || hashCore(key) != "" {
			return
		}
		seen[key] = true
		out = append(out, s)
	}
	for _, r := range rows {
		add(r.Filename)
		add(normalizeRecovered(r.Filename))
	}
	return out
}

func headerNameCandidates(rows []HeaderRow) []nameCand {
	seen := map[string]bool{}
	var out []nameCand
	for _, r := range rows {
		raw := r.Filename
		key := strings.ToLower(normalizeRecovered(raw))
		if key == "" || hashCore(key) != "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, nameCand{raw, sourceYenc})
	}
	return out
}

// sizeWithin reports whether payload is within ±15% of a PreDB-reported size
// (unknown on either side passes).
func sizeWithin(preSize, payload int64) bool {
	if preSize <= 0 || payload <= 0 {
		return true
	}
	diff := float64(payload - preSize)
	if diff < 0 {
		diff = -diff
	}
	return diff/float64(preSize) <= namefixSizeTolerance
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
		for _, nm := range usenet.ArchiveMemberNames(s.Data) {
			raw = append(raw, nameCand{nm, sourceRar})
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
	MsgID string
	Bytes int64
	NFO   bool
}

// pickBodyTargets chooses up to namefixBodiesPerSet first-segment articles:
// (a) the smallest file (likely PAR2 index or NFO), (b) the first payload file
// by FileN/name (RAR/7z header and =ybegin name), (c) an NFO if identifiable.
func pickBodyTargets(rows []HeaderRow) []bodyPick {
	type file struct {
		key   string
		first HeaderRow
		total int64
		meta  bool
		nfo   bool
		fileN int
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
			f = &file{key: key, first: r, meta: r.IsMeta || IsMetaSubject(r.Subject, r.Filename), nfo: strings.Contains(low, ".nfo"), fileN: r.FileN}
			files[key] = f
			order = append(order, key)
		}
		f.total += r.Bytes
		if r.PartN > 0 && (r.PartN < f.first.PartN || f.first.PartN <= 0) {
			f.first = r
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
	var picks []bodyPick
	seen := map[string]bool{}
	add := func(f *file) {
		if f == nil || seen[f.first.MsgID] || len(picks) >= namefixBodiesPerSet {
			return
		}
		seen[f.first.MsgID] = true
		picks = append(picks, bodyPick{MsgID: f.first.MsgID, Bytes: f.first.Bytes, NFO: f.nfo})
	}
	add(sorted[0])

	var payload []*file
	for _, f := range sorted {
		if !f.meta {
			payload = append(payload, f)
		}
	}
	sort.SliceStable(payload, func(i, j int) bool {
		if payload[i].fileN != payload[j].fileN {
			return payload[i].fileN < payload[j].fileN
		}
		return payload[i].key < payload[j].key
	})
	if len(payload) > 0 {
		add(payload[0])
	}
	for _, f := range sorted {
		if f.nfo {
			add(f)
			break
		}
	}
	return picks
}
