//go:build integration

// This is the tier-3 live identify integration test for Adult's SAK-computed
// phash (spec §8.7, plan §6). Unlike the unit cascade tests (fake boxes) and
// internal/videophash's own live cross-validation (hash FIDELITY vs a local
// Stash, Hamming 0), NEITHER of those proves that a SAK-computed hash actually
// RESOLVES to the right scene against the LIVE community DB. That is Unvalidated
// Assumption #1, and this test is its only coverage: compute this package's
// production videophash for a real Adult file, run it through the real
// identify.LookupFingerprints cascade against a configured StashDB, and confirm
// it resolves to the expected scene.
//
// t.Skip()s cleanly whenever ffmpeg/ffprobe are absent or the env is unset, so
// CI stays green with no live dependency. Measure-first: the resolved scene id
// is logged before any assertion.
//
// Env:
//
//	SAK_STASHDB_URL           stash-box GraphQL endpoint (e.g. https://stashdb.org/graphql)
//	SAK_STASHDB_APIKEY        StashDB API key
//	SAK_STASHDB_TEST_FILE     path to a real Adult video file, readable locally
//	SAK_STASHDB_EXPECT_SCENE  the stash-box scene id SAK's hash should resolve to
package rename

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/identify"
	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/throttle"
	"github.com/labbersanon/sakms/internal/videophash"
)

func TestLiveIdentify_SAKHashResolvesInStashDB(t *testing.T) {
	url := os.Getenv("SAK_STASHDB_URL")
	apiKey := os.Getenv("SAK_STASHDB_APIKEY")
	testFile := os.Getenv("SAK_STASHDB_TEST_FILE")
	expectScene := os.Getenv("SAK_STASHDB_EXPECT_SCENE")

	if url == "" || apiKey == "" || testFile == "" || expectScene == "" {
		t.Skip("SAK_STASHDB_URL/SAK_STASHDB_APIKEY/SAK_STASHDB_TEST_FILE/SAK_STASHDB_EXPECT_SCENE not all set — skipping live identify test")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH — skipping live identify test")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH — skipping live identify test")
	}
	if _, err := os.Stat(testFile); err != nil {
		t.Skipf("SAK_STASHDB_TEST_FILE %q not readable: %v", testFile, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Compute SAK's own StashDB-compatible hash for the file — the exact hasher
	// production Adult identification now uses.
	hash, err := videophash.New().Hash(ctx, testFile)
	if err != nil {
		t.Fatalf("videophash.Hash(%q): %v", testFile, err)
	}
	t.Logf("SAK computed phash = %s for %s", hash, testFile)

	// Run it through the real cascade against the live StashDB, the same
	// identify.LookupFingerprints path identifyAdultFiles drives.
	box := stashbox.New(stashbox.Config{Endpoint: url, APIKey: apiKey, HasVoteField: true}, &http.Client{Timeout: 30 * time.Second})
	ident := &identify.Identifier{
		GiveBack: identify.NewGiveBack(map[string]*stashbox.Client{"stashdb": box}),
		Throttle: throttle.New(0),
	}

	matches, err := ident.LookupFingerprints(ctx, []string{hash})
	if err != nil {
		t.Fatalf("LookupFingerprints: %v", err)
	}
	match, ok := matches[hash]
	if !ok || match == nil {
		t.Fatalf("SAK's computed phash %s resolved to no scene in the live StashDB — expected scene %s", hash, expectScene)
	}
	t.Logf("resolved to scene id=%s title=%q box=%s", match.SceneID, match.Title, match.Box)

	if match.SceneID != expectScene {
		t.Errorf("expected SAK's hash to resolve to scene %s, got %s (%q)", expectScene, match.SceneID, match.Title)
	}
}
