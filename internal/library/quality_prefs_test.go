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

func TestTitleQualityPrefs_TitleYearRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetTitleQualityPrefs(ctx, TitleQualityPrefs{
		Mode: mode.Movies, TMDBID: 438631, Tiers: []quality.Tier{quality.High},
		Title: "Dune", Year: 2021,
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.GetTitleQualityPrefs(ctx, mode.Movies, 438631)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Dune" || got.Year != 2021 {
		t.Fatalf("got title=%q year=%d, want Dune/2021", got.Title, got.Year)
	}
}

func TestListTitleQualityPrefs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetTitleQualityPrefs(ctx, TitleQualityPrefs{
		Mode: mode.Movies, TMDBID: 11, Tiers: []quality.Tier{quality.Low}, Title: "Dune", Year: 1984,
	}); err != nil {
		t.Fatalf("set 1: %v", err)
	}
	if err := s.SetTitleQualityPrefs(ctx, TitleQualityPrefs{
		Mode: mode.Movies, TMDBID: 438631, Tiers: []quality.Tier{quality.High}, MinResolution: 1080, MinResolutionSet: true, Title: "Dune", Year: 2021,
	}); err != nil {
		t.Fatalf("set 2: %v", err)
	}
	// A movie override with no resolvable library title (Title left blank).
	if err := s.SetTitleQualityPrefs(ctx, TitleQualityPrefs{
		Mode: mode.Movies, TMDBID: 999, Tiers: []quality.Tier{quality.Medium},
	}); err != nil {
		t.Fatalf("set 3: %v", err)
	}

	got, err := s.ListTitleQualityPrefs(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("list len=%d, want 3", len(got))
	}
	byID := map[int]TitleQualityPrefs{}
	for _, p := range got {
		byID[p.TMDBID] = p
	}
	if byID[11].Title != "Dune" || byID[11].Year != 1984 {
		t.Fatalf("tmdb 11 = %+v, want Dune/1984", byID[11])
	}
	if byID[438631].Title != "Dune" || byID[438631].Year != 2021 || !byID[438631].MinResolutionSet {
		t.Fatalf("tmdb 438631 = %+v, want Dune/2021/MinResolutionSet", byID[438631])
	}
	if byID[999].Title != "" {
		t.Fatalf("tmdb 999 title=%q, want blank (not resolvable at PUT)", byID[999].Title)
	}
}
