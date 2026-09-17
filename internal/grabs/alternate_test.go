package grabs

import (
	"context"
	"testing"
	"time"
)

// TestReleaseKeys_Deterministic asserts that identical inputs produce identical
// outputs and that the raw URL/title never appear in the output.
func TestReleaseKeys_Deterministic(t *testing.T) {
	url := "https://indexer.example/nzb?id=secret-api-key"
	title := "Show.Name.S01E01.1080p.BluRay"
	k1 := ReleaseKeys(url, title)
	k2 := ReleaseKeys(url, title)
	if len(k1) != 2 {
		t.Fatalf("want 2 keys, got %d: %v", len(k1), k1)
	}
	for i, k := range k1 {
		if k != k2[i] {
			t.Errorf("key[%d] not deterministic: %q != %q", i, k, k2[i])
		}
	}
	for _, k := range k1 {
		if contains(k, url) || contains(k, "secret-api-key") {
			t.Errorf("key %q contains raw URL or credential", k)
		}
		if contains(k, title) {
			t.Errorf("key %q contains raw title", k)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || (len(s) > 0 && len(sub) > 0 && stringContains(s, sub)))
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestReleaseKeys_TitleNormalisation asserts that the title key is case- and
// whitespace-insensitive.
func TestReleaseKeys_TitleNormalisation(t *testing.T) {
	a := ReleaseKeys("", "Show.Name.S01E01")
	b := ReleaseKeys("", "SHOW.NAME.S01E01")
	c := ReleaseKeys("", "  show.name.s01e01  ")
	if len(a) != 1 || len(b) != 1 || len(c) != 1 {
		t.Fatalf("unexpected key counts: a=%d b=%d c=%d", len(a), len(b), len(c))
	}
	if a[0] != b[0] || a[0] != c[0] {
		t.Errorf("normalisation diverged: %q %q %q", a[0], b[0], c[0])
	}
}

// TestReleaseKeys_EmptyInputsSkipped asserts that empty URL/title produce no
// keys (not empty-string keys).
func TestReleaseKeys_EmptyInputsSkipped(t *testing.T) {
	if got := ReleaseKeys("", ""); got != nil {
		t.Errorf("empty inputs: want nil, got %v", got)
	}
	urlOnly := ReleaseKeys("https://example.com/nzb", "")
	if len(urlOnly) != 1 || urlOnly[0][:2] != "u:" {
		t.Errorf("URL-only keys: want [u:…], got %v", urlOnly)
	}
	titleOnly := ReleaseKeys("", "Some Title")
	if len(titleOnly) != 1 || titleOnly[0][:2] != "t:" {
		t.Errorf("title-only keys: want [t:…], got %v", titleOnly)
	}
}

// TestAlternateAttempts counts only "u:" entries.
func TestAlternateAttempts(t *testing.T) {
	for _, tc := range []struct {
		keys []string
		want int
	}{
		{nil, 0},
		{[]string{"u:abc123"}, 1},
		{[]string{"u:abc123", "t:def456"}, 1},
		{[]string{"u:abc123", "t:def456", "u:ghi789", "t:jkl012"}, 2},
	} {
		if got := AlternateAttempts(tc.keys); got != tc.want {
			t.Errorf("AlternateAttempts(%v) = %d, want %d", tc.keys, got, tc.want)
		}
	}
}

// TestParseFormatTriedReleaseKeys round-trips the storage format.
func TestParseFormatTriedReleaseKeys(t *testing.T) {
	keys := []string{"u:deadbeef00112233", "t:cafebabe00112233", "u:11223344aabbccdd"}
	formatted := FormatTriedReleaseKeys(keys)
	parsed := ParseTriedReleaseKeys(formatted)
	if len(parsed) != len(keys) {
		t.Fatalf("round-trip len: got %d, want %d", len(parsed), len(keys))
	}
	for i := range keys {
		if parsed[i] != keys[i] {
			t.Errorf("[%d] %q != %q", i, parsed[i], keys[i])
		}
	}
	// Empty string round-trips to nil.
	if got := ParseTriedReleaseKeys(""); got != nil {
		t.Errorf("ParseTriedReleaseKeys(\"\") = %v, want nil", got)
	}
}

// TestParkForAlternateRelease_AppendsDedupesAndSetsFields is the store-method
// acceptance test: park write sets due-now, clears GID, zeroes
// transport_retry_count, leaves retry_count and next_search_scope untouched.
func TestParkForAlternateRelease_AppendsDedupesAndSetsFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	g, err := s.Create(ctx, Grab{
		Mode:           "movies",
		Title:          "Test Movie",
		TMDBID:         1,
		Indexer:        "I",
		Protocol:       "usenet",
		DownloadClient: "usenet",
		RootFolderPath: "/movies",
		DownloadURL:    "https://indexer.example/nzb?id=1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Simulate an in-flight dispatch.
	if err := s.SetDownloadGID(ctx, g.ID, "nzb-abc"); err != nil {
		t.Fatalf("setGID: %v", err)
	}
	// Advance retry_count so we can assert it is preserved.
	if err := s.SetPendingRetry(ctx, g.ID, time.Now().Add(-time.Hour), "prior failure"); err != nil {
		t.Fatalf("prior park: %v", err)
	}
	// Re-dispatch to get a GID again.
	if err := s.SetDownloadGID(ctx, g.ID, "nzb-abc"); err != nil {
		t.Fatalf("re-set GID: %v", err)
	}
	// Reload to get current state.
	current, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	priorRetryCount := current.RetryCount

	before := time.Now()
	keys := []string{"u:aabbccdd00112233", "t:eeff00112233aabb"}
	if err := s.ParkForAlternateRelease(ctx, g.ID, time.Now(), "unpacked failed", keys); err != nil {
		t.Fatalf("ParkForAlternateRelease: %v", err)
	}
	after := time.Now().UTC()

	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after park: %v", err)
	}
	// status = pending_retry
	if got.Status != PendingRetry {
		t.Errorf("status = %q, want %q", got.Status, PendingRetry)
	}
	// download_gid cleared
	if got.DownloadGID != "" {
		t.Errorf("download_gid = %q, want ''", got.DownloadGID)
	}
	// transport_retry_count = 0
	if got.TransportRetryCount != 0 {
		t.Errorf("transport_retry_count = %d, want 0", got.TransportRetryCount)
	}
	// retry_count UNCHANGED
	if got.RetryCount != priorRetryCount {
		t.Errorf("retry_count = %d, want %d (unchanged)", got.RetryCount, priorRetryCount)
	}
	// next_search_scope untouched (empty)
	if got.NextSearchScope != "" {
		t.Errorf("next_search_scope = %q, want ''", got.NextSearchScope)
	}
	// retry_after ≈ now (due-now)
	ra, err := ParseTime(got.RetryAfter)
	if err != nil {
		t.Fatalf("parsing retry_after %q: %v", got.RetryAfter, err)
	}
	if ra.Before(before.Add(-time.Second)) || ra.After(after.Add(10*time.Second)) {
		t.Errorf("retry_after = %v, want ≈ now (%v..%v)", ra, before, after)
	}
	// tried_release_keys contains both new keys
	parsed := ParseTriedReleaseKeys(got.TriedReleaseKeys)
	if len(parsed) != 2 {
		t.Errorf("tried_release_keys has %d entries, want 2: %v", len(parsed), parsed)
	}

	// Second park appends (no duplicates).
	moreKeys := []string{"u:11223344aabbccdd", keys[0]} // one new + one duplicate
	if err := s.ParkForAlternateRelease(ctx, g.ID, time.Now(), "unpack failed again", moreKeys); err != nil {
		t.Fatalf("second ParkForAlternateRelease: %v", err)
	}
	got2, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get after second park: %v", err)
	}
	parsed2 := ParseTriedReleaseKeys(got2.TriedReleaseKeys)
	// original 2 + 1 new (duplicate dropped)
	if len(parsed2) != 3 {
		t.Errorf("after second park: tried_release_keys has %d entries, want 3: %v", len(parsed2), parsed2)
	}
}

// TestSetPendingRetry_ClearsTriedReleaseKeys asserts days-ladder fallback ends
// the alternate-release episode by clearing tried_release_keys.
func TestSetPendingRetry_ClearsTriedReleaseKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	g, err := s.Create(ctx, Grab{
		Mode: "movies", Title: "Movie", TMDBID: 2,
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies", DownloadURL: "https://i.example/nzb",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetDownloadGID(ctx, g.ID, "nzb-xyz"); err != nil {
		t.Fatalf("setGID: %v", err)
	}
	keys := []string{"u:aabbccdd00112233", "t:eeff00112233aabb"}
	if err := s.ParkForAlternateRelease(ctx, g.ID, time.Now(), "bad unpack", keys); err != nil {
		t.Fatalf("ParkForAlternateRelease: %v", err)
	}
	// Days ladder park should clear the keys.
	if err := s.SetPendingRetry(ctx, g.ID, time.Now().Add(24*time.Hour), "no match"); err != nil {
		t.Fatalf("SetPendingRetry: %v", err)
	}
	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TriedReleaseKeys != "" {
		t.Errorf("tried_release_keys = %q after days-ladder park, want ''", got.TriedReleaseKeys)
	}
}

// TestRelaunch_PreservesTriedReleaseKeys asserts that dispatching an alternate
// release preserves the exclusion list so two bad NZBs cannot alternate forever.
func TestRelaunch_PreservesTriedReleaseKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	g, err := s.Create(ctx, Grab{
		Mode: "movies", Title: "Movie", TMDBID: 3,
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies", DownloadURL: "https://i.example/nzb",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetDownloadGID(ctx, g.ID, "nzb-first"); err != nil {
		t.Fatalf("setGID: %v", err)
	}
	keys := []string{"u:aabbccdd00112233", "t:eeff00112233aabb"}
	if err := s.ParkForAlternateRelease(ctx, g.ID, time.Now(), "bad unpack", keys); err != nil {
		t.Fatalf("ParkForAlternateRelease: %v", err)
	}
	// Relaunch with a different NZB URL.
	d := Dispatch{
		Indexer: "I", Protocol: "usenet", DownloadClient: "usenet",
		RootFolderPath: "/movies",
		DownloadURL:    "https://i.example/nzb2",
		GID:            "nzb-second",
	}
	if err := s.Relaunch(ctx, g.ID, d); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	got, err := s.Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Keys must be preserved — clearing here would let the two NZBs alternate.
	parsed := ParseTriedReleaseKeys(got.TriedReleaseKeys)
	if len(parsed) != 2 {
		t.Errorf("tried_release_keys after Relaunch: got %d entries, want 2: %v", len(parsed), parsed)
	}
}
