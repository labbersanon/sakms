package vmaf

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/labbersanon/sakms/internal/proposals"
)

func TestReferenceIndex_LargestSizeNotWinner(t *testing.T) {
	got := ReferenceIndex([]proposals.Candidate{
		{Path: "/keep.mkv", Size: 100, Winner: true},
		{Path: "/big.mkv", Size: 500, Winner: false},
		{Path: "/mid.mkv", Size: 200, Winner: false},
	})
	if got != 1 {
		t.Fatalf("ReferenceIndex = %d, want 1 (largest Size, not Winner)", got)
	}
}

func TestReferenceIndex_SizeTieKeepsLowestIndex(t *testing.T) {
	got := ReferenceIndex([]proposals.Candidate{
		{Path: "/a.mkv", Size: 500, Winner: false},
		{Path: "/b.mkv", Size: 500, Winner: true},
	})
	if got != 0 {
		t.Fatalf("ReferenceIndex = %d, want 0 (tie keeps lowest index)", got)
	}
}

func TestReferenceIndex_AllZeroFallsBackToWinner(t *testing.T) {
	got := ReferenceIndex([]proposals.Candidate{
		{Path: "/missing-a.mkv", Size: 0, Winner: false},
		{Path: "/missing-b.mkv", Size: 0, Winner: true},
	})
	if got != 1 {
		t.Fatalf("ReferenceIndex = %d, want 1 (Winner fallback when all sizes 0)", got)
	}
}

func TestReferenceIndex_AllZeroNoWinnerReturnsMinusOne(t *testing.T) {
	got := ReferenceIndex([]proposals.Candidate{
		{Path: "/missing-a.mkv"},
		{Path: "/missing-b.mkv"},
	})
	if got != -1 {
		t.Fatalf("ReferenceIndex = %d, want -1 (no size, no Winner)", got)
	}
}

func TestReferenceIndex_StatsPathWhenSizeMissing(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.mkv")
	large := filepath.Join(dir, "large.mkv")
	if err := os.WriteFile(small, make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(large, make([]byte, 200), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReferenceIndex([]proposals.Candidate{
		{Path: small, Winner: true},
		{Path: large, Winner: false},
	})
	if got != 1 {
		t.Fatalf("ReferenceIndex = %d, want 1 (stat fills missing Size)", got)
	}
}

func TestReferenceIndex_Empty(t *testing.T) {
	if got := ReferenceIndex(nil); got != -1 {
		t.Fatalf("ReferenceIndex(nil) = %d, want -1", got)
	}
}
