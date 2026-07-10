//go:build integration

// This is the tier-3 real-ffmpeg integration test plus the tier-1 live-Stash
// cross-validation gate (spec §5 / plan §3). Unlike videophash_test.go (which
// fakes both seams with canned images) this drives the REAL runFFprobeDuration
// + runFFmpegFrames path end to end.
//
// Two independent checks:
//  1. Determinism: a synthetic lavfi-generated clip hashes identically twice
//     through real ffmpeg decode. Always runs when ffmpeg/ffprobe are on PATH.
//  2. Live-Stash cross-validation (the actual compatibility gate): given
//     SAK_STASH_URL, SAK_STASH_APIKEY, SAK_STASH_TEST_FILE (and optionally
//     SAK_STASH_SCENE_PATH if the file's path as SAK sees it differs from how
//     Stash indexed it), fetch Stash's own already-computed phash for that file
//     via the same stashapi client SAK's Adult identification already uses,
//     independently compute this package's hash for the same file, and report
//     the Hamming distance between them. t.Skip()s cleanly if ffmpeg is absent
//     or any env var is unset, so CI stays green with no live dependency.
//
// Measure-first: every distance is logged BEFORE any assertion, so the real
// numbers are visible in -v output regardless of pass/fail.
package videophash

import (
	"bytes"
	"context"
	"math/bits"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/stashapi"
)

func genClip(t *testing.T, path, source string) {
	t.Helper()
	cmd := exec.Command("ffmpeg",
		"-y",
		"-f", "lavfi",
		"-i", source+"=duration=6:size=320x240:rate=10",
		"-pix_fmt", "yuv420p",
		path,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generating clip %s from %q: %v\n%s", path, source, err, stderr.String())
	}
}

func TestHash_RealFFmpegDeterminism(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH — skipping real-ffmpeg integration test")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH — skipping real-ffmpeg integration test")
	}

	dir := t.TempDir()
	clip := filepath.Join(dir, "testsrc.mkv")
	// 6s duration so the 5%-95% sampling window (collageTimestamps) spans
	// enough real time to pull 25 genuinely distinct frames from lavfi's
	// deterministic pattern generator, not near-duplicate adjacent frames.
	genClip(t, clip, "testsrc")

	h := New()
	ctx := context.Background()

	hash1, err := h.Hash(ctx, clip)
	if err != nil {
		t.Fatalf("hashing clip: %v", err)
	}
	hash2, err := h.Hash(ctx, clip)
	if err != nil {
		t.Fatalf("re-hashing clip: %v", err)
	}

	u1, err := strconv.ParseUint(hash1, 16, 64)
	if err != nil {
		t.Fatalf("parsing hash1 %q: %v", hash1, err)
	}
	u2, err := strconv.ParseUint(hash2, 16, 64)
	if err != nil {
		t.Fatalf("parsing hash2 %q: %v", hash2, err)
	}
	dist := Hamming(u1, u2)
	t.Logf("hash1=%s hash2=%s Hamming=%d bits", hash1, hash2, dist)

	if hash1 != hash2 {
		t.Errorf("expected the same clip to hash identically twice through real decode, got %q vs %q (Hamming %d)",
			hash1, hash2, dist)
	}
}

// stashGraphQLPHash fetches the phash fingerprint Stash already computed for
// the file at scenePath, via the existing stashapi.Client (the same client
// SAK's Adult identification path uses). Returns "" if no phash fingerprint
// exists for that path.
func stashGraphQLPHash(t *testing.T, ctx context.Context, client *stashapi.Client, scenePath string) string {
	t.Helper()
	files, err := client.FindSceneInfoByPaths(ctx, []string{scenePath})
	if err != nil {
		t.Fatalf("FindSceneInfoByPaths(%q): %v", scenePath, err)
	}
	f, ok := files[scenePath]
	if !ok || f == nil {
		return ""
	}
	return f.PHash
}

func TestHash_LiveStashCrossValidation(t *testing.T) {
	url := os.Getenv("SAK_STASH_URL")
	apiKey := os.Getenv("SAK_STASH_APIKEY")
	testFile := os.Getenv("SAK_STASH_TEST_FILE")
	scenePath := os.Getenv("SAK_STASH_SCENE_PATH")
	if scenePath == "" {
		scenePath = testFile
	}

	if url == "" || apiKey == "" || testFile == "" {
		t.Skip("SAK_STASH_URL/SAK_STASH_APIKEY/SAK_STASH_TEST_FILE not all set — skipping live-Stash cross-validation")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH — skipping live-Stash cross-validation")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH — skipping live-Stash cross-validation")
	}
	if _, err := os.Stat(testFile); err != nil {
		t.Skipf("SAK_STASH_TEST_FILE %q not readable: %v", testFile, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := stashapi.New(stashapi.Config{URL: url, APIKey: apiKey}, &http.Client{Timeout: 30 * time.Second})
	stashHex := stashGraphQLPHash(t, ctx, client, scenePath)
	if stashHex == "" {
		t.Skipf("Stash reports no phash fingerprint for %q — skipping (file may not be indexed yet)", scenePath)
	}

	sakHex, err := New().Hash(ctx, testFile)
	if err != nil {
		t.Fatalf("videophash.Hash(%q): %v", testFile, err)
	}

	stashU, err := strconv.ParseUint(stashHex, 16, 64)
	if err != nil {
		t.Fatalf("parsing Stash's phash %q as hex uint64: %v", stashHex, err)
	}
	sakU, err := strconv.ParseUint(sakHex, 16, 64)
	if err != nil {
		t.Fatalf("parsing SAK's phash %q as hex uint64: %v", sakHex, err)
	}

	dist := Hamming(stashU, sakU)
	t.Logf("file=%s", testFile)
	t.Logf("Stash phash = %s (%d of 64 bits set)", stashHex, bits.OnesCount64(stashU))
	t.Logf("SAK   phash = %s (%d of 64 bits set)", sakHex, bits.OnesCount64(sakU))
	t.Logf("Hamming distance = %d / 64 bits", dist)

	// Measure-first: report the real distance regardless of outcome. Stash's
	// own local Scene Duplicate Checker treats 0 as identical and 1-10 as
	// "very similar" (spec §1e) — use that as the informative threshold here,
	// not a hard pass/fail gate, since this is the FIRST live measurement of
	// this pipeline's real-world fidelity, not a pre-validated constant.
	if dist == 0 {
		t.Logf("RESULT: byte-identical to Stash's own computed phash.")
	} else if dist <= 10 {
		t.Logf("RESULT: within Stash's own \"very similar\" band (<=10 bits) — consistent with expected ffmpeg pipeline variance.")
	} else {
		t.Errorf("RESULT: Hamming distance %d exceeds Stash's own \"very similar\" band (10 bits) — "+
			"this indicates a real algorithm/pipeline discrepancy, not just minor ffmpeg variance. "+
			"Investigate before treating this hasher as StashDB-compatible.", dist)
	}
}

