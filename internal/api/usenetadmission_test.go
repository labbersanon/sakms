package api

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
)

// Claude 2026-10-15: Step 4 coverage for NewNNTPAdmissionPolicy's Snapshot —
// mode defaults always present, a resolvable title override merges onto its
// mode default (locked decision #3), and an unresolvable (blank-Title) movie
// override is absent from Titles rather than appearing as a phantom entry
// (locked decision #5).
func TestNNTPAdmissionPolicy_Snapshot(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, _, libStore, _, _, _, _, _ := testStores(t)

	if err := settingsStore.Set(ctx, qualityTiersKey(mode.Movies), `["medium","high","lossless"]`); err != nil {
		t.Fatal(err)
	}
	if err := libStore.SetTitleQualityPrefs(ctx, library.TitleQualityPrefs{
		Mode: mode.Movies, TMDBID: 438631, Tiers: []quality.Tier{quality.High},
		MinResolution: 1080, MinResolutionSet: true,
		Title: "Dune", Year: 2021,
	}); err != nil {
		t.Fatal(err)
	}
	// Unresolvable at PUT time (Title left blank) — must be skipped.
	if err := libStore.SetTitleQualityPrefs(ctx, library.TitleQualityPrefs{
		Mode: mode.Movies, TMDBID: 999, Tiers: []quality.Tier{quality.Medium},
	}); err != nil {
		t.Fatal(err)
	}

	policy := NewNNTPAdmissionPolicy(libStore, settingsStore)
	snap, err := policy.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	moviesDef := snap.Modes["movies"]
	if moviesDef.Policy != "mode" || len(moviesDef.Tiers) != 3 {
		t.Fatalf("movies mode default = %+v, want 3 tiers from settings", moviesDef)
	}

	entries := snap.Titles["dune"]
	if len(entries) != 1 {
		t.Fatalf("Titles[\"dune\"] = %+v, want exactly 1 entry", entries)
	}
	got := entries[0]
	if got.TMDBID != 438631 || got.Year != 2021 || got.MinRes != 1080 {
		t.Fatalf("dune entry = %+v, want tmdbID=438631 year=2021 minRes=1080", got)
	}

	for key, entries := range snap.Titles {
		for _, e := range entries {
			if e.TMDBID == 999 {
				t.Fatalf("unresolvable movie override (tmdb 999) leaked into Titles[%q]: %+v", key, e)
			}
		}
	}
}

// TestNNTPAdmissionPolicy_Snapshot_NilLibrary confirms a nil *library.Store
// (defensive — should never happen once wired in main.go) still returns mode
// defaults instead of erroring.
func TestNNTPAdmissionPolicy_Snapshot_NilLibrary(t *testing.T) {
	ctx := context.Background()
	_, _, settingsStore, _, _, _, _, _, _, _ := testStores(t)
	policy := NewNNTPAdmissionPolicy(nil, settingsStore)
	snap, err := policy.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Modes) != 3 {
		t.Fatalf("Modes = %+v, want 3 entries (movies/series/adult)", snap.Modes)
	}
	if len(snap.Titles) != 0 {
		t.Fatalf("Titles = %+v, want empty with nil library", snap.Titles)
	}
}
