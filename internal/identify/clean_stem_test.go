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
		{
			// The 2026-08-11 bug: a title ALREADY truncated mid-word by
			// whatever recorded it ("Cheerlea"), with a SPACE-separated date
			// (dateTokenPattern is literal-dot only) and no trailing tech
			// marker. Nothing here is recognizable to the cleaner, so it
			// passes through byte-for-byte and Prowlarr gets a query that
			// cannot match. Pinned deliberately as an UNCHANGED pass-through:
			// the cleaner is not the fix (there is no signal here to clean
			// on) — ReleaseTitleLacksSceneStructure detecting exactly this
			// shape, and autoGrabSearch's 0-result Studio+Title fallback
			// firing on it, is. See TestReleaseTitleLacksSceneStructure below
			// and TestDiscoverAvailabilityHandler_Adult_ZeroResultsFallsBackToStudioTitle.
			name: "midword_truncated_space_date_no_tech_marker",
			in:   "CathysCraving 26 02 08 Scene 1000 Pixie Smalls Teasing Cheerlea",
			want: "CathysCraving 26 02 08 Scene 1000 Pixie Smalls Teasing Cheerlea",
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

// TestReleaseTitleLacksSceneStructure is the other half of the 2026-08-11
// truncated-title bug: the cleaner cannot rescue a title it recognizes nothing
// in, so the recovery is a broader Studio+Title retry — and this predicate is
// the gate that decides which titles earn one. The two dotted fixtures below
// are the exact release titles the two pinned Adult availability handler
// regression tests send; both must report FALSE, because a fallback firing for
// them would issue a second Prowlarr query and break those tests' assertion
// that the cleaned releaseTitle is the query that went out.
func TestReleaseTitleLacksSceneStructure(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			// The bug: mid-word truncation, space-separated date, no tech
			// marker — the cleaner strips nothing, so a 0-result answer to
			// this query proves nothing about the scene's availability.
			name: "midword_truncated_space_date_no_tech_marker",
			in:   "CathysCraving 26 02 08 Scene 1000 Pixie Smalls Teasing Cheerlea",
			want: true,
		},
		{
			// TestDiscoverAvailabilityHandler_Adult_ReleaseTitleQueryCleaned's
			// releaseTitle: dotted date + XXX + resolution + codec + group all
			// recognized and stripped, so the cleaned query is trustworthy.
			name: "dotted_xxx_group_PRT",
			in:   "TabooHeat.26.07.18.Cory.Chase.In.Step.Mom.Has.One.Wish.BBC.Gangbang.XXX.720p.HEVC.x265.PRT",
			want: false,
		},
		{
			// TestDiscoverAvailabilityHandler_Adult_ReleaseTitlePreferredOverStudioTitle's
			// releaseTitle.
			name: "dotted_xxx_resolution_group",
			in:   "Step.Siblings.Caught.26.06.01.Poppy.Applegate.XXX.1080p-GROUP",
			want: false,
		},
		{
			// Brackets alone are structure the cleaner acts on.
			name: "bracketed_variant",
			in:   "[TabooHeat] Cory Chase In Step Mom Has One Wish BBC Gangbang (26.07.18)[720p][x264][xFans].mp4",
			want: false,
		},
		{
			// An already-clean title is indistinguishable from a truncated
			// one from here, and reports true for the same reason: nothing
			// about it can confirm a 0-result query was answered correctly.
			name: "already_clean_is_also_unverifiable",
			in:   "Cory Chase Step Mom Has One Wish BBC Gangbang",
			want: true,
		},
		{
			// Dash-separated but otherwise clean: separator normalization
			// alone must not count as structure.
			name: "dash_separated_no_markers",
			in:   "Cory-Chase-Step-Mom-Has-One-Wish",
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ReleaseTitleLacksSceneStructure(c.in); got != c.want {
				t.Errorf("ReleaseTitleLacksSceneStructure(%q) = %v, want %v (cleaned: %q)", c.in, got, c.want, CleanReleaseTitleForSearch(c.in))
			}
		})
	}
}
