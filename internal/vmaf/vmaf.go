// Package vmaf computes VMAF (Video Multi-Method Assessment Fusion)
// perceptual-quality scores by shelling out to ffmpeg's libvmaf filter, one
// candidate file measured against one reference (a Dedup group's primary).
//
// Execution locus (vmaf-backend plan, "VMAF execution locus" decision, Option
// A): the ffmpeg subprocess runs IN-PROCESS in the sakms server, the same
// exec.CommandContext pattern internal/phash uses — it is deliberately NOT
// dispatched through internal/nodes' dispatcher, so it is NOT covered by the
// node CPU governor. This package's only resource bound is the single
// package-level concurrency semaphore below plus a per-computation timeout;
// that limit is honest, not borrowed from a governor that does not apply here.
package vmaf

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxConcurrentVMAF bounds how many VMAF computations may run in-process at
// once, across EVERY caller — both the on-demand HTTP view path (Stage 2) and
// the eager scheduled fan-out (Stage 3) call Compute, and Compute is the sole
// acquirer of the single vmafSem below, so combined concurrency across both
// paths can never exceed this cap (plan AC7). It is intentionally low: a VMAF
// computation is a full-decode of two files and is CPU-heavy.
const maxConcurrentVMAF = 2

// computeTimeout bounds a single candidate-vs-reference computation. A pair
// that cannot finish within it is failed (context-cancelled ffmpeg), so one
// pathological file never wedges a slot forever.
const computeTimeout = 15 * time.Minute

const (
	defaultSampleCount    = 5
	defaultSampleDuration = 10 * time.Second
	minSuccessfulSamples  = 3
)

// vmafSem is the ONE shared concurrency limiter for the whole package. It is a
// package-level singleton on purpose (plan AC7 / Critic round 1): making it
// per-call or per-path would let each path stay within its own bound while
// their combined load silently exceeded the cap. Every caller funnels through
// Compute, which is the only code that acquires it.
var vmafSem = make(chan struct{}, maxConcurrentVMAF)

// computeFunc runs the real ffmpeg+libvmaf computation for one sampled window.
// It is a package var rather than a direct call ONLY so tests can substitute a
// deterministic stub (plan AC7's required observation seam): the semaphore is
// acquired inside Compute BEFORE computeFunc runs, so a test that injects a
// blocking stub recording max-observed concurrency and launches many
// concurrent Compute calls can assert the semaphore bound holds without
// depending on real ffmpeg subprocess timing. Production always uses
// runFFmpegVMAF.
var computeFunc = runFFmpegVMAF

var durationFunc = ffprobeDuration

type sampleWindow struct {
	Start    time.Duration
	Duration time.Duration
}

// Compute returns the VMAF score of candidatePath measured against
// referencePath, a value in roughly [0, 100] where higher is closer to the
// reference. It acquires the shared package semaphore (blocking, or returning
// ctx.Err() if ctx is cancelled while waiting) and applies its own
// computeTimeout on top of the caller's ctx, so callers need not add either.
func Compute(ctx context.Context, candidatePath, referencePath string) (float64, error) {
	select {
	case vmafSem <- struct{}{}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	defer func() { <-vmafSem }()

	cctx, cancel := context.WithTimeout(ctx, computeTimeout)
	defer cancel()
	return computeSampled(cctx, candidatePath, referencePath)
}

// vmafScoreRE matches libvmaf's aggregate (pooled-mean) score line, which the
// filter prints once to stderr, e.g. "[Parsed_libvmaf_0 @ 0x..] VMAF score:
// 98.597452". This single line is the pooled mean across all frames — the
// value callers want — so scraping it is sufficient; the per-frame JSON log is
// not needed.
var vmafScoreRE = regexp.MustCompile(`VMAF score:\s*([0-9]+(?:\.[0-9]+)?)`)

// runFFmpegVMAF shells out to ffmpeg's libvmaf filter for one pair and parses
// the pooled-mean score off stderr.
//
// The scale2ref stage is mandatory, not cosmetic: Dedup candidates are the
// same content at DIFFERENT qualities, so a resolution mismatch is the common
// case, and libvmaf errors outright ("Error reinitializing filters!") when its
// two inputs differ in size. scale2ref scales the candidate (input 0) up/down
// to the reference's (input 1) dimensions first, then libvmaf compares them.
//
// Known limitation (documented, not silently handled): VMAF pairs frames 1:1,
// so a frame-rate / frame-count mismatch between candidate and reference skews
// the score. The common Dedup case is a matching frame rate; normalising fps
// is left as a deliberate follow-up rather than guessed at here.
func computeSampled(ctx context.Context, candidatePath, referencePath string) (float64, error) {
	candidateDuration, err := durationFunc(ctx, candidatePath)
	if err != nil {
		return 0, err
	}
	referenceDuration, err := durationFunc(ctx, referencePath)
	if err != nil {
		return 0, err
	}
	windows := fixedSampleWindows(minDuration(candidateDuration, referenceDuration), defaultSampleCount, defaultSampleDuration)

	var sum float64
	successes := 0
	var failures []string
	for i, w := range windows {
		score, err := computeFunc(ctx, candidatePath, referencePath, w)
		if err != nil {
			failures = append(failures, fmt.Sprintf("sample %d/%d: %v", i+1, len(windows), err))
			continue
		}
		successes++
		sum += score
	}
	if successes < minSuccessfulSamples {
		return 0, fmt.Errorf("vmaf: only %d/%d samples succeeded for %s vs %s (need %d): %s",
			successes, len(windows), candidatePath, referencePath, minSuccessfulSamples, strings.Join(failures, "; "))
	}
	return sum / float64(successes), nil
}

// Claude 2026-08-12: sampled VMAF replaces full-file VMAF.
// Reason: full-file 4K/HDR/DV comparisons hit the 15-minute command timeout and
// returned "signal: killed"; five fixed short windows preserve quality signal
// while keeping operator-facing Dedup responsive.
// Troubleshooting: repeated "VMAF…" followed by unavailable/error after 15 min.
// Review if: VMAF moves to a node/offline queue or gets per-mode tunables.
func fixedSampleWindows(total time.Duration, count int, duration time.Duration) []sampleWindow {
	if count <= 0 || total <= 0 {
		return nil
	}
	if duration <= 0 || duration > total {
		duration = total
	}
	out := make([]sampleWindow, 0, count)
	for i := 1; i <= count; i++ {
		center := time.Duration(float64(total) * float64(i) / float64(count+1))
		start := center - duration/2
		if start < 0 {
			start = 0
		}
		if maxStart := total - duration; start > maxStart {
			start = maxStart
		}
		out = append(out, sampleWindow{Start: start, Duration: duration})
	}
	return out
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func ffprobeDuration(ctx context.Context, path string) (time.Duration, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	raw, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("vmaf: ffprobe duration of %s: %w", path, err)
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("vmaf: invalid duration %q for %s", strings.TrimSpace(string(raw)), path)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func runFFmpegVMAF(ctx context.Context, candidatePath, referencePath string, sample sampleWindow) (float64, error) {
	const filter = "[0:v][1:v]scale2ref=flags=bicubic[dist][ref];[dist][ref]libvmaf"
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin",
		"-ss", seconds(sample.Start),
		"-t", seconds(sample.Duration),
		"-i", candidatePath,
		"-ss", seconds(sample.Start),
		"-t", seconds(sample.Duration),
		"-i", referencePath,
		"-lavfi", filter,
		"-f", "null",
		"-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("vmaf: computing %s vs %s: %w\n%s",
			candidatePath, referencePath, err, tailLines(stderr.String(), 8))
	}
	m := vmafScoreRE.FindStringSubmatch(stderr.String())
	if m == nil {
		return 0, fmt.Errorf("vmaf: no VMAF score in ffmpeg output for %s vs %s\n%s",
			candidatePath, referencePath, tailLines(stderr.String(), 8))
	}
	score, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("vmaf: parsing score %q for %s vs %s: %w",
			m[1], candidatePath, referencePath, err)
	}
	return score, nil
}

func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}

// Available reports whether this ffmpeg build actually has the libvmaf filter
// compiled in — distinct from ffmpeg merely being on PATH. The deployment
// image's ffmpeg package is a separately-resolved concern (some Debian /
// jellyfin-ffmpeg builds ship without libvmaf), so callers and tests use this
// to fail/skip cleanly with a clear message instead of an opaque filter error.
func Available(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-filters").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "libvmaf")
}

// tailLines returns at most the last n non-empty lines of s, so an error wraps
// ffmpeg's actual complaint without dumping its full multi-hundred-line log.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		out = append([]string{lines[i]}, out...)
	}
	return strings.Join(out, "\n")
}
