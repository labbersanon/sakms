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

func TestWindow_RollingSixtySeconds(t *testing.T) {
	var w Window
	start := time.Unix(0, 0)
	// Steady 1 MB/s for 60s, then one more second with no progress — oldest
	// second drops so rate stays near 1 MB/s over the remaining ~60s span.
	for i := 0; i <= 60; i++ {
		w.Add(start.Add(time.Duration(i)*time.Second), int64(i)*1_000_000)
	}
	got := w.Add(start.Add(61*time.Second), 60_000_000)
	if got < 900_000 || got > 1_100_000 {
		t.Fatalf("speed=%d want ~900k–1M B/s over rolling 60s window", got)
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
