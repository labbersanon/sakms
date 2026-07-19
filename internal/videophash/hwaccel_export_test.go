package videophash

import (
	"context"
	"testing"
)

// TestProbeHWAccelDelegatesToProbeHWAccel proves the exported wrapper is a
// pure delegate to the unexported probe: same input, same output. Comparing
// the two calls (rather than a fixed value) keeps the test deterministic on
// any machine — with or without ffmpeg or a GPU present.
func TestProbeHWAccelDelegatesToProbeHWAccel(t *testing.T) {
	if got, want := ProbeHWAccel(context.Background()), probeHWAccel(context.Background()); got != want {
		t.Fatalf("ProbeHWAccel = %q, probeHWAccel = %q; wrapper must delegate", got, want)
	}
}
