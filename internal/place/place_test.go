package place

import "testing"

func TestQualityKeyGreater_Resolution(t *testing.T) {
	high := NewQualityKey(1080, 0, "h264", 1000)
	low := NewQualityKey(720, 0, "h264", 5000)
	if !high.Greater(low) {
		t.Error("higher resolution should win regardless of bitrate")
	}
	if low.Greater(high) {
		t.Error("lower resolution should not win")
	}
}

func TestQualityKeyGreater_SourceRankTiebreak(t *testing.T) {
	remux := NewQualityKey(1080, 5, "h264", 1000)
	webdl := NewQualityKey(1080, 3, "h264", 20000)
	if !remux.Greater(webdl) {
		t.Error("higher source rank should win at equal resolution regardless of bitrate")
	}
}

func TestQualityKeyGreater_AV1Tiebreak(t *testing.T) {
	av1 := NewQualityKey(1080, 0, "av1", 1000)
	h264 := NewQualityKey(1080, 0, "h264", 5000)
	if !av1.Greater(h264) {
		t.Error("AV1 should win at equal resolution/source regardless of bitrate")
	}
}

func TestQualityKeyGreater_BitRateFinalTiebreak(t *testing.T) {
	a := NewQualityKey(1080, 0, "h264", 8000)
	b := NewQualityKey(1080, 0, "h264", 4000)
	if !a.Greater(b) {
		t.Error("higher bitrate should win when everything else is equal")
	}
}

func TestQualityKeyGreater_UnknownSourceRankOnlyLosesToKnown(t *testing.T) {
	untrackedA := NewQualityKey(1080, 0, "h264", 8000)
	untrackedB := NewQualityKey(1080, 0, "h264", 4000)
	// Both unknown source (0): falls through to codec/bitrate, not a forced loss.
	if !untrackedA.Greater(untrackedB) {
		t.Error("two unknown-source keys should still compare via bitrate")
	}

	trackedKnown := NewQualityKey(1080, 4, "h264", 1000)
	if !trackedKnown.Greater(untrackedA) {
		t.Error("a known source rank should beat unknown even at lower bitrate")
	}
}

func TestUniquePath_NoCollision(t *testing.T) {
	exists := func(string) bool { return false }
	got, err := UniquePath("/media/Movies/Foo.mkv", exists)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/media/Movies/Foo.mkv" {
		t.Errorf("expected unchanged path, got %q", got)
	}
}

func TestUniquePath_OneCollision(t *testing.T) {
	taken := map[string]bool{"/media/Movies/Foo.mkv": true}
	exists := func(p string) bool { return taken[p] }
	got, err := UniquePath("/media/Movies/Foo.mkv", exists)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/media/Movies/Foo.2.mkv" {
		t.Errorf("expected .2 suffix, got %q", got)
	}
}

func TestUniquePath_MultipleCollisions(t *testing.T) {
	taken := map[string]bool{
		"/media/Movies/Foo.mkv":   true,
		"/media/Movies/Foo.2.mkv": true,
		"/media/Movies/Foo.3.mkv": true,
	}
	exists := func(p string) bool { return taken[p] }
	got, err := UniquePath("/media/Movies/Foo.mkv", exists)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/media/Movies/Foo.4.mkv" {
		t.Errorf("expected .4 suffix, got %q", got)
	}
}

func TestUniquePath_Exhausted(t *testing.T) {
	exists := func(string) bool { return true } // everything taken, forever
	_, err := UniquePath("/media/Movies/Foo.mkv", exists)
	if err == nil {
		t.Error("expected an error when no unique path can be found")
	}
}
