package library

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
)

func TestTitleQualityPrefs_RoundTripAndDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetTitleQualityPrefs(ctx, mode.Series, 4589)
	if err != ErrNotFound {
		t.Fatalf("empty get: %v, want ErrNotFound", err)
	}

	tiers := []quality.Tier{quality.High, quality.Lossless}
	if err := s.SetTitleQualityPrefs(ctx, mode.Series, 4589, tiers, 1080, true); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.GetTitleQualityPrefs(ctx, mode.Series, 4589)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.HasOverride || !got.MinResolutionSet || got.MinResolution != 1080 {
		t.Fatalf("got %+v", got)
	}
	if len(got.Tiers) != 2 || got.Tiers[0] != quality.High || got.Tiers[1] != quality.Lossless {
		t.Fatalf("tiers=%v", got.Tiers)
	}

	if err := s.SetTitleQualityPrefs(ctx, mode.Series, 4589, tiers, 0, false); err != nil {
		t.Fatalf("clear min: %v", err)
	}
	got, err = s.GetTitleQualityPrefs(ctx, mode.Series, 4589)
	if err != nil {
		t.Fatalf("get after clear min: %v", err)
	}
	if got.MinResolutionSet {
		t.Fatalf("min should inherit, got set=%v val=%d", got.MinResolutionSet, got.MinResolution)
	}

	if err := s.DeleteTitleQualityPrefs(ctx, mode.Series, 4589); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = s.GetTitleQualityPrefs(ctx, mode.Series, 4589)
	if err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
}
