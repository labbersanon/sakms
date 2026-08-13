package vmaf

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// genClip writes a tiny synthetic clip at path from an ffmpeg lavfi source
// expression (e.g. "testsrc" / "testsrc2"), at the given square size and 10fps,
// mirroring internal/phash/integration_test.go's pattern — no committed video
// fixtures. Kept tiny (2s) so a real VMAF decode of two of them is cheap.
func genClip(t *testing.T, path, source string, size int) {
	t.Helper()
	cmd := exec.Command("ffmpeg",
		"-y",
		"-f", "lavfi",
		"-i", source+"=duration=2:size="+itoa(size)+"x"+itoa(size)+":rate=10",
		"-pix_fmt", "yuv420p",
		path,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generating clip %s from %q: %v\n%s", path, source, err, stderr.String())
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestCompute_RealFFmpeg drives the REAL ffmpeg+libvmaf path against synthetic
// clips generated at test time. It is deliberately NOT build-tagged: it runs
// under a plain `go test ./internal/vmaf/...` and skips cleanly (with a clear
// message) when this machine's ffmpeg lacks the libvmaf filter — which ffmpeg
// build ends up in the deployment image is a separate packaging concern, so a
// dev box without libvmaf must not turn this into a red suite.
//
// It is measure-first (prints every score before asserting) and asserts only
// plausible ranges + ordering, never an exact value:
//   - an identical-content candidate scores high,
//   - a visibly-different candidate scores low,
//   - a DIFFERENT-RESOLUTION candidate computes at all (proves the mandatory
//     scale2ref stage — libvmaf errors outright on mismatched input sizes, the
//     common Dedup case of the same title at two qualities).
func TestCompute_RealFFmpeg(t *testing.T) {
	ctx := context.Background()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH — skipping real libvmaf computation test")
	}
	if !Available(ctx) {
		t.Skip("this ffmpeg build has no libvmaf filter compiled in — skipping real VMAF " +
			"computation (which ffmpeg ships in the deployment image is a separate packaging concern)")
	}

	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.mkv")          // reference: testsrc, 128x128
	sameContent := filepath.Join(dir, "same.mkv") // identical content, 128x128
	diffContent := filepath.Join(dir, "diff.mkv") // different pattern, 128x128
	lowerRes := filepath.Join(dir, "small.mkv")   // same pattern, 64x64 (res mismatch)
	genClip(t, ref, "testsrc", 128)
	genClip(t, sameContent, "testsrc", 128)
	genClip(t, diffContent, "testsrc2", 128)
	genClip(t, lowerRes, "testsrc", 64)

	same, err := Compute(ctx, sameContent, ref)
	if err != nil {
		t.Fatalf("Compute(same-content vs ref): %v", err)
	}
	diff, err := Compute(ctx, diffContent, ref)
	if err != nil {
		t.Fatalf("Compute(different-content vs ref): %v", err)
	}
	scaled, err := Compute(ctx, lowerRes, ref)
	if err != nil {
		t.Fatalf("Compute(lower-resolution vs ref) — scale2ref should make this succeed, not error: %v", err)
	}

	t.Logf("VMAF(same content, 128 vs 128) = %.4f", same)
	t.Logf("VMAF(different content, 128 vs 128) = %.4f", diff)
	t.Logf("VMAF(same content, 64 scaled to 128) = %.4f", scaled)

	for name, s := range map[string]float64{"same": same, "diff": diff, "scaled": scaled} {
		if s < 0 || s > 100 {
			t.Errorf("VMAF %s score %.4f is outside the plausible [0,100] range", name, s)
		}
	}
	// Identical content re-encoded scores high; a genuinely different pattern
	// scores low. Wide margins so codec/version drift never makes this flaky.
	if same < 60 {
		t.Errorf("expected identical-content VMAF to be high (>60), got %.4f", same)
	}
	if diff > 60 {
		t.Errorf("expected different-content VMAF to be low (<60), got %.4f", diff)
	}
	if same < diff {
		t.Errorf("expected identical-content score (%.4f) to exceed different-content score (%.4f)", same, diff)
	}
}

// TestCompute_SharedSemaphoreBoundsCombinedConcurrency proves the single
// package-level semaphore actually caps concurrent computations (plan AC7).
//
// Compute is the ONE entry point every caller funnels through — the on-demand
// HTTP view path and the eager scheduled fan-out both call it, and it is the
// sole acquirer of vmafSem — so exercising Compute concurrently IS the combined
// on-demand+eager test AC7 asks for; there is no second entry point to also
// drive. We inject a deterministic stub for computeFunc (AC7's required
// observation seam) so the assertion rests on the semaphore, never on real
// ffmpeg subprocess timing.
func TestCompute_SharedSemaphoreBoundsCombinedConcurrency(t *testing.T) {
	orig := computeFunc
	origDuration := durationFunc
	defer func() {
		computeFunc = orig
		durationFunc = origDuration
	}()
	durationFunc = func(ctx context.Context, path string) (time.Duration, error) {
		return time.Minute, nil
	}

	const callers = maxConcurrentVMAF * 4

	var mu sync.Mutex
	current, maxObserved := 0, 0
	// One Compute now runs several samples; keep the observation channel large
	// enough that post-release sample calls cannot block the stub during wg.Wait.
	entered := make(chan struct{}, callers*defaultSampleCount)
	release := make(chan struct{})

	computeFunc = func(ctx context.Context, a, b string, sample sampleWindow) (float64, error) {
		mu.Lock()
		current++
		if current > maxObserved {
			maxObserved = current
		}
		mu.Unlock()
		entered <- struct{}{}
		<-release // hold the semaphore slot until the test lets go
		mu.Lock()
		current--
		mu.Unlock()
		return 100, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Compute(context.Background(), "cand", "ref"); err != nil {
				t.Errorf("Compute returned unexpected error: %v", err)
			}
		}()
	}

	// Exactly maxConcurrentVMAF stubs may be inside at once; the rest must be
	// parked on the semaphore. Wait for the first admitted batch...
	for i := 0; i < maxConcurrentVMAF; i++ {
		<-entered
	}
	// ...then give any wrongly-admitted extra caller a beat to slip in and bump
	// maxObserved before we assert (release is still closed-off, so nothing has
	// freed a slot yet).
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	observed := maxObserved
	mu.Unlock()
	if observed > maxConcurrentVMAF {
		close(release)
		wg.Wait()
		t.Fatalf("semaphore breached: observed %d concurrent computations, cap is %d", observed, maxConcurrentVMAF)
	}

	close(release)
	wg.Wait()

	if maxObserved != maxConcurrentVMAF {
		t.Errorf("expected the semaphore to admit exactly %d concurrent computations under load, observed max %d",
			maxConcurrentVMAF, maxObserved)
	}
}

func TestCompute_AveragesAtLeastThreeSuccessfulSamples(t *testing.T) {
	orig := computeFunc
	origDuration := durationFunc
	defer func() {
		computeFunc = orig
		durationFunc = origDuration
	}()
	durationFunc = func(ctx context.Context, path string) (time.Duration, error) {
		return time.Minute, nil
	}

	calls := 0
	computeFunc = func(ctx context.Context, a, b string, sample sampleWindow) (float64, error) {
		calls++
		switch calls {
		case 1:
			return 90, nil
		case 2:
			return 80, nil
		case 3, 4:
			return 0, context.DeadlineExceeded
		default:
			return 70, nil
		}
	}

	got, err := Compute(context.Background(), "candidate.mkv", "reference.mkv")
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if calls != defaultSampleCount {
		t.Fatalf("computeFunc calls = %d, want %d", calls, defaultSampleCount)
	}
	if got != 80 {
		t.Fatalf("score = %.2f, want average 80", got)
	}
}

func TestCompute_FailsWhenFewerThanThreeSamplesSucceed(t *testing.T) {
	orig := computeFunc
	origDuration := durationFunc
	defer func() {
		computeFunc = orig
		durationFunc = origDuration
	}()
	durationFunc = func(ctx context.Context, path string) (time.Duration, error) {
		return time.Minute, nil
	}

	calls := 0
	computeFunc = func(ctx context.Context, a, b string, sample sampleWindow) (float64, error) {
		calls++
		if calls <= 2 {
			return 90, nil
		}
		return 0, context.DeadlineExceeded
	}

	_, err := Compute(context.Background(), "candidate.mkv", "reference.mkv")
	if err == nil {
		t.Fatal("expected Compute to fail with only 2 successful samples")
	}
}
