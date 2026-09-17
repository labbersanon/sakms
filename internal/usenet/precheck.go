package usenet

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Claude 2026-09-15: pre-download NNTP STAT availability gate (Leo / SABnzbd-like).
// Reason: dead NZBs burned ~10min of concurrent=1 before mid-download 430/PAR2 fail;
//   STAT sampling aborts before staging when payload articles are gone.
// Troubleshooting: journal "usenet precheck:"; AddNZB/RelaunchNZB return ErrArticlesUnavailable.
// Review if: assembleFile ever tolerates missing segments (abort threshold could become %).
// Related: docs/usenet-precheck.md, fetchSegmentAny, RunAutoGrab candidate loop.

// ErrArticlesUnavailable is returned when a pre-download STAT check finds
// payload articles missing on every configured subscription. Callers that have
// a ranked candidate list should try the next release; relaunch should park.
var ErrArticlesUnavailable = errors.New("usenet: too many articles are missing on the configured subscriptions")

// PrecheckResult is the diagnostic summary logged for every precheck pass/fail.
type PrecheckResult struct {
	PayloadSegments int
	Sampled         int
	SampleMissing   int
	Checked         int
	Missing         int
	WorstFile       string
	Escalated       bool
	Inconclusive    bool
	StatUnreliable  bool
}

type precheckPolicy struct {
	ExactThreshold   int
	SampleCap        int
	InteriorSamples  int
	LeaderCap        int
	AbortSampleRatio float64
	SampleTimeout    time.Duration
	EscalateTimeout  time.Duration
	MaxWorkers       int
}

// Fixed policy — always on, no settings UI (operator choice 2026-09-15).
// Escalation scope A: only files that missed in the sample (+ small re-sample).
var defaultPrecheckPolicy = precheckPolicy{
	ExactThreshold:   16,
	SampleCap:        48,
	InteriorSamples:  12,
	LeaderCap:        32,
	AbortSampleRatio: 0.25,
	SampleTimeout:    15 * time.Second,
	EscalateTimeout:  45 * time.Second,
	MaxWorkers:       4,
}

type sampledSeg struct {
	fileIdx int
	msgID   string
}

// precheckNZB STATs a sample of payload MsgIDs, escalating to the affected
// files when misses are sparse. skip MsgIDs are treated as already present
// (relaunch resume). Zero pools is a no-op so fixtures without NNTP stay green.
func (m *Manager) precheckNZB(ctx context.Context, nzb *NZB, skip map[string]bool) (PrecheckResult, error) {
	var res PrecheckResult
	if nzb == nil || len(m.currentPools()) == 0 {
		res.Inconclusive = true
		return res, nil
	}
	policy := defaultPrecheckPolicy

	payload := payloadFiles(nzb)
	if len(payload) == 0 {
		res.Inconclusive = true
		return res, nil
	}

	var all []sampledSeg
	for fi, f := range payload {
		for _, s := range f.Segs {
			id := strings.TrimSpace(s.MsgID)
			if id == "" || skip[id] {
				continue
			}
			all = append(all, sampledSeg{fileIdx: fi, msgID: id})
		}
	}
	res.PayloadSegments = len(all)
	if len(all) == 0 {
		return res, nil
	}

	// Process-lifetime STAT-unreliable: skip gate and proceed (BODY already proven).
	if m.isStatUnreliable() {
		res.StatUnreliable = true
		res.Inconclusive = true
		log.Printf("usenet precheck: skipped — STAT marked unreliable this process")
		return res, nil
	}

	sample := sampleSegments(payload, all, policy)
	res.Sampled = len(sample)

	sampleCtx, cancel := context.WithTimeout(ctx, policy.SampleTimeout)
	defer cancel()

	sampleMissing, sampleRemoved, sampleChecked, sampleInconc, err := m.statBatch(sampleCtx, sample)
	res.Checked = sampleChecked
	res.Inconclusive = sampleInconc
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		res.Inconclusive = true
		return res, nil
	}

	if sampleRemoved > 0 {
		res.Missing = sampleRemoved
		log.Printf("usenet precheck: abort — %d sampled payload article(s) removed (451)", sampleRemoved)
		return res, ErrArticlesUnavailable
	}

	sampleMissIDs := missingSegs(sample, sampleMissing)
	res.SampleMissing = len(sampleMissIDs)
	if len(sampleMissIDs) == 0 {
		log.Printf("usenet precheck: ok sampled=%d payload=%d", res.Sampled, res.PayloadSegments)
		return res, nil
	}

	// Mandatory BODY trust probe: some backends 430 STAT but serve BODY.
	probeID := sampleMissIDs[0].msgID
	if m.trustProbeBody(sampleCtx, probeID) {
		m.markStatUnreliable()
		res.StatUnreliable = true
		log.Printf("usenet precheck: STAT unreliable (BODY ok for %s) — proceeding", probeID)
		return res, nil
	}

	ratio := float64(len(sampleMissIDs)) / float64(len(sample))
	if ratio >= policy.AbortSampleRatio {
		res.Missing = len(sampleMissIDs)
		res.WorstFile = worstFileName(payload, sampleMissIDs)
		log.Printf("usenet precheck: abort — sample missing %.0f%% (%d/%d) worst=%q",
			ratio*100, len(sampleMissIDs), len(sample), res.WorstFile)
		return res, ErrArticlesUnavailable
	}

	// Escalate to every segment of the files that missed in the sample (option A).
	res.Escalated = true
	escalate := escalateSegments(all, sampleMissIDs)
	escCtx, escCancel := context.WithTimeout(ctx, policy.EscalateTimeout)
	defer escCancel()

	escMissing, escRemoved, escChecked, escInconc, escErr := m.statBatch(escCtx, escalate)
	res.Checked += escChecked
	if escInconc {
		res.Inconclusive = true
	}
	if escErr != nil && !errors.Is(escErr, context.DeadlineExceeded) && !errors.Is(escErr, context.Canceled) {
		res.Inconclusive = true
		return res, nil
	}
	if escRemoved > 0 || len(escMissing) > 0 {
		res.Missing = len(escMissing) + escRemoved
		res.WorstFile = worstFileName(payload, missingSegs(escalate, escMissing))
		log.Printf("usenet precheck: abort after escalate — missing=%d checked=%d worst=%q",
			res.Missing, res.Checked, res.WorstFile)
		return res, ErrArticlesUnavailable
	}

	log.Printf("usenet precheck: ok after escalate sampled_miss=%d checked=%d", len(sampleMissIDs), res.Checked)
	return res, nil
}

// precheckConcurrency leaves one connection of the budget for real downloads.
func (m *Manager) precheckConcurrency() int {
	n := concurrencyBudget(m.currentPools()) - 1
	return min(max(n, 1), defaultPrecheckPolicy.MaxWorkers)
}

func (m *Manager) statBatch(ctx context.Context, segs []sampledSeg) (missing map[string]bool, removed, checked int, inconclusive bool, err error) {
	missing = make(map[string]bool)
	if len(segs) == 0 {
		return missing, 0, 0, false, nil
	}
	workers := m.precheckConcurrency()
	type outcome struct {
		msgID   string
		missing bool
		removed bool
		inconc  bool
		err     error
	}
	jobs := make(chan sampledSeg, len(segs))
	out := make(chan outcome, len(segs))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				select {
				case <-ctx.Done():
					out <- outcome{msgID: s.msgID, inconc: true, err: ctx.Err()}
					continue
				default:
				}
				found, rem, serr := m.statArticleAny(ctx, s.msgID)
				switch {
				case serr != nil:
					out <- outcome{msgID: s.msgID, inconc: true, err: serr}
				case rem:
					out <- outcome{msgID: s.msgID, removed: true}
				case !found:
					out <- outcome{msgID: s.msgID, missing: true}
				default:
					out <- outcome{msgID: s.msgID}
				}
			}
		}()
	}
	for _, s := range segs {
		jobs <- s
	}
	close(jobs)
	go func() {
		wg.Wait()
		close(out)
	}()

	var firstErr error
	for o := range out {
		checked++
		if o.err != nil && firstErr == nil {
			firstErr = o.err
		}
		if o.inconc {
			inconclusive = true
		}
		if o.removed {
			removed++
		}
		if o.missing {
			missing[o.msgID] = true
		}
	}
	return missing, removed, checked, inconclusive, firstErr
}

// statArticleAny STATs msgID across pools in order. Protocol 430/451 keep the
// socket (put ok=true) so we do not dial-thrash.
//
// Claude 2026-09-17: bounded reconnect (maxStatAttemptsPerServer = 2).
// Reason: a stale-socket STAT failure (broken pipe on an idle connection)
//   previously poisoned a precheck sample, making a healthy NZB look
//   inconclusive. STAT is one round trip, so a single retry is cheap.
// Review if: maxStatAttemptsPerServer is exposed as a settings knob.
func (m *Manager) statArticleAny(ctx context.Context, msgID string) (found, removed bool, err error) {
	pools := m.currentPools()
	if len(pools) == 0 {
		return false, false, ErrNoSubscriptions
	}
	allNotFound := true
	sawRemoved := false
	var otherErr error

	for _, p := range pools {
		for attempt := 1; attempt <= maxStatAttemptsPerServer; attempt++ {
			if ctx.Err() != nil {
				return false, false, ctx.Err()
			}
			conn, gerr := p.getCtx(ctx)
			if gerr != nil {
				if isTransportError(gerr) && attempt < maxStatAttemptsPerServer && ctx.Err() == nil {
					sleepWithCtx(ctx, transportRetryDelay(attempt))
					continue
				}
				allNotFound = false
				if otherErr == nil {
					otherErr = gerr
				}
				break
			}
			_, _, serr := conn.Stat(ensureAngleMsgID(msgID))
			mapped := mapNNTPError(serr)
			// 430/451 are valid STAT answers — keep the connection.
			keep := mapped == nil || errors.Is(mapped, ErrArticleNotFound) || errors.Is(mapped, ErrArticleRemoved)
			p.put(conn, keep)
			if mapped == nil {
				return true, false, nil
			}
			switch {
			case errors.Is(mapped, ErrArticleNotFound):
				break // try next pool
			case errors.Is(mapped, ErrArticleRemoved):
				allNotFound = false
				sawRemoved = true
			default:
				if isTransportError(mapped) && attempt < maxStatAttemptsPerServer && ctx.Err() == nil {
					sleepWithCtx(ctx, transportRetryDelay(attempt))
					continue
				}
				allNotFound = false
				if otherErr == nil {
					otherErr = mapped
				}
			}
			break
		}
	}

	if allNotFound {
		return false, false, nil
	}
	if sawRemoved && otherErr == nil {
		return false, true, nil
	}
	return false, false, otherErr
}

// trustProbeBody reports whether any pool serves msgID's BODY, which means a
// preceding 430 from STAT was a lie and the precheck gate cannot be trusted.
func (m *Manager) trustProbeBody(ctx context.Context, msgID string) bool {
	for _, p := range m.currentPools() {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		conn, err := p.getCtx(ctx)
		if err != nil {
			continue
		}
		_, err = fetchSegment(conn, msgID)
		keep := err == nil || errors.Is(err, ErrArticleNotFound) || errors.Is(err, ErrArticleRemoved)
		p.put(conn, keep)
		if err == nil {
			return true
		}
	}
	return false
}

func (m *Manager) markStatUnreliable() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statUnreliable = true
}

func (m *Manager) isStatUnreliable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statUnreliable
}

func ensureAngleMsgID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return id
	}
	if !strings.HasPrefix(id, "<") {
		id = "<" + id
	}
	if !strings.HasSuffix(id, ">") {
		id += ">"
	}
	return id
}

func isMetaSubject(subject string) bool {
	name := strings.ToLower(filenameFromSubject(subject))
	if name == "" {
		name = strings.ToLower(subject)
	}
	switch filepath.Ext(name) {
	case ".par2", ".nfo", ".sfv", ".srr", ".jpg", ".jpeg", ".png", ".gif", ".txt":
		return true
	}
	if strings.Contains(name, ".vol") && strings.Contains(name, ".par2") {
		return true
	}
	return false
}

func payloadFiles(nzb *NZB) []NZBFile {
	var payload []NZBFile
	for _, f := range nzb.Files {
		if !isMetaSubject(f.Subject) {
			payload = append(payload, f)
		}
	}
	if len(payload) == 0 {
		return nzb.Files
	}
	return payload
}

func missingSegs(segs []sampledSeg, missing map[string]bool) []sampledSeg {
	var out []sampledSeg
	for _, s := range segs {
		if missing[s.msgID] {
			out = append(out, s)
		}
	}
	return out
}

// appendStride appends n evenly-spaced segments of all, skipping MsgIDs already
// in seen. Spreads probes across the release instead of clustering at one file.
func appendStride(out, all []sampledSeg, seen map[string]bool, n int) []sampledSeg {
	if n <= 0 || len(all) < 2 {
		return out
	}
	for i := 0; i < n; i++ {
		s := all[(i*(len(all)-1))/n]
		if seen[s.msgID] {
			continue
		}
		seen[s.msgID] = true
		out = append(out, s)
	}
	return out
}

func sampleSegments(payload []NZBFile, all []sampledSeg, policy precheckPolicy) []sampledSeg {
	if len(all) <= policy.ExactThreshold {
		return append([]sampledSeg(nil), all...)
	}

	seen := make(map[string]bool)
	var out []sampledSeg

	step := 1
	if len(payload) > policy.LeaderCap {
		step = (len(payload) + policy.LeaderCap - 1) / policy.LeaderCap
	}
	leaders := 0
	for fi := 0; fi < len(payload) && leaders < policy.LeaderCap; fi += step {
		f := payload[fi]
		if len(f.Segs) == 0 {
			continue
		}
		id := strings.TrimSpace(f.Segs[0].MsgID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, sampledSeg{fileIdx: fi, msgID: id})
		leaders++
	}

	out = appendStride(out, all, seen, min(policy.InteriorSamples, len(all)))

	if len(out) > policy.SampleCap {
		out = out[:policy.SampleCap]
	}
	return out
}

// escalateSpotChecks is how many extra evenly-spaced segments the escalation
// pass probes beyond the files that already missed in the sample.
const escalateSpotChecks = 24

func escalateSegments(all, sampleMiss []sampledSeg) []sampledSeg {
	missFiles := make(map[int]bool)
	for _, s := range sampleMiss {
		missFiles[s.fileIdx] = true
	}
	seen := make(map[string]bool)
	var out []sampledSeg
	for _, s := range all {
		if !missFiles[s.fileIdx] || seen[s.msgID] {
			continue
		}
		seen[s.msgID] = true
		out = append(out, s)
	}
	// Spot-check the rest of the release so a whole-NZB outage is caught even
	// when only one file missed in the sample.
	return appendStride(out, all, seen, min(escalateSpotChecks, len(all)))
}

func worstFileName(payload []NZBFile, miss []sampledSeg) string {
	counts := make(map[int]int)
	for _, s := range miss {
		counts[s.fileIdx]++
	}
	bestIdx, bestN := -1, 0
	for fi, n := range counts {
		if n > bestN {
			bestIdx, bestN = fi, n
		}
	}
	if bestIdx < 0 || bestIdx >= len(payload) {
		return ""
	}
	name := filenameFromSubject(payload[bestIdx].Subject)
	if name == "" {
		return fmt.Sprintf("file[%d]", bestIdx)
	}
	return name
}
