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
	if err := s.SetTitleQualityPrefs(ctx, TitleQualityPrefs{
		Mode: mode.Series, TMDBID: 4589, Tiers: tiers,
		MinResolution: 1080, MinResolutionSet: true,
	}); err != nil {
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

	if err := s.SetTitleQualityPrefs(ctx, TitleQualityPrefs{
		Mode: mode.Series, TMDBID: 4589, Tiers: tiers,
	}); err != nil {
		t.Fatalf("clear min: %v", err)
	}
	got, err = s.GetTitleQualityPrefs(ctx, mode.Series, 4589)
	if err != nil {
		t.Fatalf("get after clear min: %v", err)
	}
	if got.MinResolutionSet {
		t.Fatalf("min should inherit, got set=%v val=%d", got.MinResolutionSet, got.MinResolution)
	}

	floors := map[int]quality.Tier{480: quality.Low, 720: quality.Medium, 1080: quality.Medium, 2160: quality.High}
	feat := true
	if err := s.SetTitleQualityPrefs(ctx, TitleQualityPrefs{
		Mode: mode.Series, TMDBID: 4589, Tiers: tiers,
		MinResolution: 1080, MinResolutionSet: true,
		MaxResolution: 1080, MaxResolutionSet: true,
		ResolutionFloors: floors,
		CodecRank:        []string{"x265", "av1", "x264"},
		FeatureUpgrade:   &feat,
	}); err != nil {
		t.Fatalf("set floors: %v", err)
	}
	got, err = s.GetTitleQualityPrefs(ctx, mode.Series, 4589)
	if err != nil {
		t.Fatalf("get floors: %v", err)
	}
	if got.ResolutionFloors[480] != quality.Low || got.ResolutionFloors[1080] != quality.Medium || got.ResolutionFloors[2160] != quality.High {
		t.Fatalf("resolution floors=%v", got.ResolutionFloors)
	}
	if !got.MaxResolutionSet || got.MaxResolution != 1080 {
		t.Fatalf("max resolution=%v set=%v", got.MaxResolution, got.MaxResolutionSet)
	}
	if len(got.CodecRank) != 3 || got.CodecRank[0] != "x265" {
		t.Fatalf("codec rank=%v", got.CodecRank)
	}
	if got.FeatureUpgrade == nil || !*got.FeatureUpgrade {
		t.Fatalf("feature upgrade=%v", got.FeatureUpgrade)
	}

	if err := s.DeleteTitleQualityPrefs(ctx, mode.Series, 4589); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = s.GetTitleQualityPrefs(ctx, mode.Series, 4589)
	if err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
}
