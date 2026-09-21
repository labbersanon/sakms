package ratesmooth

import (
	"testing"
	"time"
)

func TestWindow_NeedsTwoSamples(t *testing.T) {
	var w Window
	if got := w.Add(time.Unix(1, 0), 100); got != 0 {
		t.Fatalf("one sample speed=%d want 0", got)
	}
}

func TestWindow_RollingTenSeconds(t *testing.T) {
	var w Window
	start := time.Unix(0, 0)
	// 10 MB over 10s → 1 MB/s; then add another 1s with no progress — oldest
	// drops so rate should stay near 1 MB/s from the remaining span.
	for i := 0; i <= 10; i++ {
		w.Add(start.Add(time.Duration(i)*time.Second), int64(i)*1_000_000)
	}
	got := w.Add(start.Add(11*time.Second), 10_000_000)
	// Window keeps ~10s: from t=1 (1MB) to t=11 (10MB) → 9MB/10s = 900_000
	// or from t=0 if prune keeps two: after prune cutoff=t1, samples from t=1..11.
	if got < 800_000 || got > 1_100_000 {
		t.Fatalf("speed=%d want ~900k–1M B/s over rolling window", got)
	}
}

func TestWindow_IgnoresBackwardOrFlat(t *testing.T) {
	var w Window
	start := time.Unix(0, 0)
	w.Add(start, 1000)
	if got := w.Add(start.Add(time.Second), 1000); got != 0 {
		t.Fatalf("flat progress speed=%d want 0", got)
	}
}

func TestWindow_Reset(t *testing.T) {
	var w Window
	start := time.Unix(0, 0)
	w.Add(start, 0)
	w.Add(start.Add(time.Second), 1000)
	w.Reset()
	if got := w.Add(start.Add(2*time.Second), 2000); got != 0 {
		t.Fatalf("after reset one sample speed=%d want 0", got)
	}
}
