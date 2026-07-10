package videophash

import (
	"context"
	"errors"
	"image"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// cannedFrames builds FrameCount same-dimensioned frames (distinct colors) that
// assembleCollage accepts and PerceptionHash can hash.
func cannedFrames() []image.Image {
	frames := make([]image.Image, FrameCount)
	for i := range frames {
		frames[i] = solidFrame(4, 2, tagColor(i))
	}
	return frames
}

// newTestHasher builds a Hasher with injected fakes and no real ffmpeg.
func newTestHasher(probe durationProber, run frameRunner) *Hasher {
	return &Hasher{probe: probe, run: run, timeout: time.Minute}
}

func TestHashDeterministic(t *testing.T) {
	probe := func(ctx context.Context, path string) (float64, error) { return 100.0, nil }
	run := func(ctx context.Context, path string, ts []float64) ([]image.Image, error) {
		return cannedFrames(), nil
	}
	h := newTestHasher(probe, run)

	first, err := h.Hash(context.Background(), "fake.mp4")
	if err != nil {
		t.Fatalf("first Hash: %v", err)
	}
	second, err := h.Hash(context.Background(), "fake.mp4")
	if err != nil {
		t.Fatalf("second Hash: %v", err)
	}
	if first != second {
		t.Errorf("Hash not deterministic: %q != %q", first, second)
	}
	// Encoding invariant: bare lowercase hex, parseable, no scheme prefix.
	if _, err := strconv.ParseUint(first, 16, 64); err != nil {
		t.Errorf("Hash output %q is not valid unpadded hex: %v", first, err)
	}
}

func TestHashFrameCountGuard(t *testing.T) {
	probe := func(ctx context.Context, path string) (float64, error) { return 100.0, nil }
	run := func(ctx context.Context, path string, ts []float64) ([]image.Image, error) {
		return cannedFrames()[:FrameCount-1], nil // 24 frames
	}
	h := newTestHasher(probe, run)

	if _, err := h.Hash(context.Background(), "fake.mp4"); err == nil {
		t.Fatal("expected error when runner returns wrong frame count, got nil")
	}
}

func TestHashRequestsCorrectTimestamps(t *testing.T) {
	const dur = 73.0
	var captured []float64
	probe := func(ctx context.Context, path string) (float64, error) { return dur, nil }
	run := func(ctx context.Context, path string, ts []float64) ([]image.Image, error) {
		captured = ts
		return cannedFrames(), nil
	}
	h := newTestHasher(probe, run)

	if _, err := h.Hash(context.Background(), "fake.mp4"); err != nil {
		t.Fatalf("Hash: %v", err)
	}
	want := collageTimestamps(dur)
	if !reflect.DeepEqual(captured, want) {
		t.Errorf("runner received timestamps %v, want %v (proves no off-by-one in collageTimestamps)", captured, want)
	}
}

func TestHashProbeErrorPropagates(t *testing.T) {
	sentinel := errors.New("probe failed")
	probe := func(ctx context.Context, path string) (float64, error) { return 0, sentinel }
	run := func(ctx context.Context, path string, ts []float64) ([]image.Image, error) {
		t.Fatal("runner should not be called when probe fails")
		return nil, nil
	}
	h := newTestHasher(probe, run)

	_, err := h.Hash(context.Background(), "fake.mp4")
	if !errors.Is(err, sentinel) {
		t.Errorf("Hash error = %v, want %v", err, sentinel)
	}
}

func TestHashNonPositiveDuration(t *testing.T) {
	probe := func(ctx context.Context, path string) (float64, error) { return 0, nil }
	run := func(ctx context.Context, path string, ts []float64) ([]image.Image, error) {
		t.Fatal("runner should not be called for non-positive duration")
		return nil, nil
	}
	h := newTestHasher(probe, run)

	if _, err := h.Hash(context.Background(), "fake.mp4"); err == nil {
		t.Fatal("expected error for non-positive duration, got nil")
	}
}

func TestHashRunnerErrorPropagates(t *testing.T) {
	sentinel := errors.New("frame extraction failed")
	probe := func(ctx context.Context, path string) (float64, error) { return 100.0, nil }
	run := func(ctx context.Context, path string, ts []float64) ([]image.Image, error) {
		return nil, sentinel
	}
	h := newTestHasher(probe, run)

	_, err := h.Hash(context.Background(), "fake.mp4")
	if !errors.Is(err, sentinel) {
		t.Errorf("Hash error = %v, want %v", err, sentinel)
	}
}
