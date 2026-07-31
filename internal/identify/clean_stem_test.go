package identify

import "testing"

// TestCleanReleaseTitleForSearch covers the 2026-07-31 Cory Chase bug and its
// generalization: a raw scene-release title must lose its date, XXX tag,
// resolution/codec, and trailing release group (whichever separator style the
// indexer used), while an already-clean title passes through untouched and a
// technical marker never truncates a descriptive title mid-way.
func TestCleanReleaseTitleForSearch(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// The exact bug: dotted release, glued studio, embedded date,
			// XXX/resolution/codec suffix, dot-separated release group (PRT).
			name: "dotted_xxx_group_PRT",
			in:   "TabooHeat.26.07.18.Cory.Chase.In.Step.Mom.Has.One.Wish.BBC.Gangbang.XXX.720p.HEVC.x265.PRT",
			want: "TabooHeat Cory Chase In Step Mom Has One Wish BBC Gangbang",
		},
		{
			// Same scene, 2160p, dash-separated release group (Narcos).
			name: "dotted_xxx_group_dash_Narcos",
			in:   "TabooHeat.26.07.18.Cory.Chase.In.Step.Mom.Has.One.Wish.BBC.Gangbang.XXX.2160p.MP4-Narcos",
			want: "TabooHeat Cory Chase In Step Mom Has One Wish BBC Gangbang",
		},
		{
			// Bracketed variant: studio + technical data in brackets, date in
			// parens, trailing container tag — brackets removed, studio goes
			// with them, mp4 mopped up.
			name: "bracketed_variant",
			in:   "[TabooHeat] Cory Chase In Step Mom Has One Wish BBC Gangbang (26.07.18)[720p][x264][xFans].mp4",
			want: "Cory Chase In Step Mom Has One Wish BBC Gangbang",
		},
		{
			// No XXX tag, dash release group — resolution is the boundary that
			// still drops the group.
			name: "no_xxx_resolution_boundary_dash_group",
			in:   "Step.Siblings.Caught.26.06.01.Poppy.Applegate.1080p.x265-KTR",
			want: "Step Siblings Caught Poppy Applegate",
		},
		{
			// Already-clean query: only separator/whitespace normalization,
			// no words dropped.
			name: "already_clean_unchanged",
			in:   "Cory Chase Step Mom Has One Wish BBC Gangbang",
			want: "Cory Chase Step Mom Has One Wish BBC Gangbang",
		},
		{
			// A trailing number that is NOT a resolution (no "p") must not
			// trigger truncation — the title's own words survive.
			name: "trailing_number_not_resolution",
			in:   "Amiee Cambridge Road Trip With My Busty Step Mom 2",
			want: "Amiee Cambridge Road Trip With My Busty Step Mom 2",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CleanReleaseTitleForSearch(c.in); got != c.want {
				t.Errorf("CleanReleaseTitleForSearch(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
