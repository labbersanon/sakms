package rename

import (
	"path/filepath"
	"testing"
)

// TestSeriesGate_Allows exercises seriesGate.allows against the plan's own
// test matrix (autopilot-impl-title-collision-fix.md §5.1) — the real
// failure titles ("The Path" vs "The Oath"/"The Secret Path"/a duplicate
// "The Path" catalog entry), plus the corrected rows the critic/architect
// review pass added (zero vs negative pin, empty-token-set-on-both-sides,
// and the §3.4b short-circuit).
func TestSeriesGate_Allows(t *testing.T) {
	cases := []struct {
		name           string
		gate           seriesGate
		candidateTitle string
		candidateID    int
		want           bool
	}{
		{
			name:           "1_title_overlap_catches_The_Oath",
			gate:           seriesGate{titleSeed: "The Path"},
			candidateTitle: "The Oath", candidateID: 100,
			want: false,
		},
		{
			name:           "2_title_overlap_alone_not_enough_for_The_Secret_Path",
			gate:           seriesGate{titleSeed: "The Path"},
			candidateTitle: "The Secret Path", candidateID: 100,
			want: true,
		},
		{
			name:           "3_pinned_id_closes_the_gap_left_by_case_2",
			gate:           seriesGate{titleSeed: "The Path", pinnedTMDBID: 65227},
			candidateTitle: "The Secret Path", candidateID: 100,
			want: false,
		},
		{
			name:           "4_duplicate_catalog_entry_same_title_different_id",
			gate:           seriesGate{titleSeed: "The Path", pinnedTMDBID: 65227},
			candidateTitle: "The Path", candidateID: 999999,
			want: false,
		},
		{
			name:           "5_correct_show_never_rejected",
			gate:           seriesGate{titleSeed: "The Path", pinnedTMDBID: 65227},
			candidateTitle: "The Path", candidateID: 65227,
			want: true,
		},
		{
			name:           "6_empty_seed_fails_open",
			gate:           seriesGate{titleSeed: ""},
			candidateTitle: "Anything", candidateID: 0,
			want: true,
		},
		{
			name:           "7_web_authority_synthetic_negative_id_exempt",
			gate:           seriesGate{pinnedTMDBID: 65227},
			candidateTitle: "The Path", candidateID: -12345,
			want: true,
		},
		{
			name:           "8a_hash_seed_rejects_without_a_pin",
			gate:           seriesGate{titleSeed: "6c9038580261273f37fee109d678acbf"},
			candidateTitle: "The Path", candidateID: 65227,
			want: false,
		},
		{
			name:           "8b_pin_short_circuits_the_odd_hash_seed",
			gate:           seriesGate{titleSeed: "6c9038580261273f37fee109d678acbf", pinnedTMDBID: 65227},
			candidateTitle: "The Path", candidateID: 65227,
			want: true,
		},
		{
			name:           "9a_zero_pin_disables_Check_B_only",
			gate:           seriesGate{titleSeed: "The Path", pinnedTMDBID: 0},
			candidateTitle: "The Oath", candidateID: 100,
			want: false,
		},
		{
			name:           "9b_negative_pin_fails_open_per_the_lte_rule",
			gate:           seriesGate{titleSeed: "The Path", pinnedTMDBID: -55},
			candidateTitle: "The Path", candidateID: 65227,
			want: true,
		},
		{
			name:           "10a_seed_tokenizes_to_empty_set_on_a_non_empty_string",
			gate:           seriesGate{titleSeed: "9-1-1"},
			candidateTitle: "9-1-1: Lone Star", candidateID: 200,
			want: true,
		},
		{
			name:           "10b_candidate_side_one_rune_title_tokenizes_to_nothing",
			gate:           seriesGate{titleSeed: "The Path"},
			candidateTitle: "V", candidateID: 300,
			want: true,
		},
		{
			// 11a — SITE 4 COUPLING LOCK (plan §2.2/§6.6). allowsTitle's seed
			// side (site 4) MUST use the same tokenizer HasTitleTokenOverlap
			// uses for its argument 1 (site 2). Both are filenameTokens today,
			// so filenameTokens(FromName("vs")) is EMPTY and the guard fails
			// open with no basis to reject.
			//
			// This row is the entire point of the coupling: leave site 4 on
			// titleTokens while site 2 moves and titleTokens("vs") = {vs} is
			// non-empty, so the emptiness pre-check passes, Check A runs, the
			// overlap call then tokenizes the seed to nothing on its own side,
			// and the candidate is REJECTED — inverting this guard's fail-open
			// contract. This row goes false and the test fails, which is what
			// it is here to do.
			name:           "11a_release_scene_only_seed_fails_open_site4_coupling",
			gate:           seriesGate{titleSeed: "vs"},
			candidateTitle: "The Path", candidateID: 100,
			want: true,
		},
		{
			// 11b — the CANDIDATE side of the same coupling (site 5, plan §3).
			// candidateTitle keeps using titleTokens, so titleTokens("Extended")
			// is now {extended} — non-empty — Check A stays ACTIVE and rejects a
			// candidate sharing no token with the seed. Pre-fix "extended" was
			// in the one shared stop list, this tokenized to the empty set, and
			// the guard failed open and ALLOWED.
			name:           "11b_release_scene_only_candidate_keeps_check_A_active",
			gate:           seriesGate{titleSeed: "The Path"},
			candidateTitle: "Extended", candidateID: 100,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := tc.gate
			got := g.allows(tc.candidateTitle, tc.candidateID)
			if got != tc.want {
				t.Errorf("seriesGate{titleSeed:%q pinnedTMDBID:%d}.allows(%q, %d) = %v, want %v",
					tc.gate.titleSeed, tc.gate.pinnedTMDBID, tc.candidateTitle, tc.candidateID, got, tc.want)
			}
		})
	}
}

// TestSeriesGate_Allows_IncrementsSharedRejectedCounter proves the rejected
// counter is a pointer shared across withSeed copies (§4.1's correctness
// requirement for §4.3's distinct Unmatched reason) — a rejection on a
// withSeed-derived gate must be visible on the original.
func TestSeriesGate_Allows_IncrementsSharedRejectedCounter(t *testing.T) {
	rejected := 0
	g := &seriesGate{titleSeed: "The Path", rejected: &rejected}
	seeded := g.withSeed("The Path")
	if seeded.rejected != g.rejected {
		t.Fatalf("expected withSeed to copy the rejected pointer, got a different pointer")
	}
	if seeded.allows("The Oath", 100) {
		t.Fatalf("expected the seeded copy to reject a non-overlapping title")
	}
	if rejected != 1 {
		t.Errorf("expected the shared counter to be incremented via the withSeed copy, got %d", rejected)
	}
	if g.allows("The Oath", 100) {
		t.Fatalf("expected the original gate to also reject a non-overlapping title")
	}
	if rejected != 2 {
		t.Errorf("expected both rejections to accumulate on the shared counter, got %d", rejected)
	}
}

func TestShowFolderKey(t *testing.T) {
	roots := []string{filepath.FromSlash("/media/Media Library/Series")}
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "plain_show_folder",
			path: filepath.FromSlash("/media/Media Library/Series/The Path/Season 1/The.Path.S01E02.2160p.WEB.h265-NiXON/hash.mp4"),
			want: "thepath",
		},
		{
			name: "jellyfin_decorated_folder_normalizes_the_same_key",
			path: filepath.FromSlash("/media/Media Library/Series/The Path (2016) [tmdbid-65227]/Season 01/The Path S01E01.mp4"),
			want: "thepath",
		},
		{
			name: "file_sits_directly_in_root_no_show_folder",
			path: filepath.FromSlash("/media/Media Library/Series/loose.mkv"),
			want: "",
		},
		{
			name: "path_outside_every_root",
			path: filepath.FromSlash("/somewhere/else/Show/ep.mkv"),
			want: "",
		},
		{
			name: "sibling_root_sharing_a_prefix_is_not_matched_by_bare_HasPrefix",
			path: filepath.FromSlash("/media/Media Library/Series2/Show/ep.mkv"),
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := showFolderKey(tc.path, roots)
			if got != tc.want {
				t.Errorf("showFolderKey(%q, %v) = %q, want %q", tc.path, roots, got, tc.want)
			}
		})
	}
}

// TestShowFolderKey_KidsRootTakesPrecedenceWhenNested proves roots are
// matched longest-first: a nested Kids root must key its own files under
// its own segment, not be shadowed by the shorter parent root matching
// first and reporting the Kids root's own directory name as the "show".
func TestShowFolderKey_KidsRootTakesPrecedenceWhenNested(t *testing.T) {
	seriesRoot := filepath.FromSlash("/media/Series")
	kidsRoot := filepath.FromSlash("/media/Series/Kids")
	roots := []string{seriesRoot, kidsRoot}

	got := showFolderKey(filepath.FromSlash("/media/Series/Kids/Bluey/Season 1/ep.mkv"), roots)
	if got != "bluey" {
		t.Errorf("expected the nested Kids root to win and key under its own show folder, got %q", got)
	}
}

func TestPinFolderID(t *testing.T) {
	t.Run("two_different_ids_go_sticky_ambiguous", func(t *testing.T) {
		m := map[string]int{}
		pinFolderID(m, "thepath", 111)
		pinFolderID(m, "thepath", 222)
		if m["thepath"] != 0 {
			t.Fatalf("expected the key to go ambiguous (0) after two different ids, got %d", m["thepath"])
		}
		pinFolderID(m, "thepath", 111) // a third write must not resurrect it
		if m["thepath"] != 0 {
			t.Errorf("expected a third write to leave the sticky-ambiguous 0 in place, got %d", m["thepath"])
		}
	})

	t.Run("empty_key_never_written", func(t *testing.T) {
		m := map[string]int{}
		pinFolderID(m, "", 65227)
		if _, ok := m[""]; ok {
			t.Errorf("expected an empty key to never enter the map, got an entry: %v", m)
		}
	})

	t.Run("non_positive_id_never_written", func(t *testing.T) {
		m := map[string]int{}
		pinFolderID(m, "thepath", 0)
		if _, ok := m["thepath"]; ok {
			t.Fatalf("expected a non-positive id to never enter the map, got an entry: %v", m)
		}
		pinFolderID(m, "thepath", 65227)
		if m["thepath"] != 65227 {
			t.Errorf("expected the real id to win outright (not sticky-ambiguous), got %d", m["thepath"])
		}
	})

	t.Run("negative_id_never_written", func(t *testing.T) {
		m := map[string]int{}
		pinFolderID(m, "thepath", -1)
		if _, ok := m["thepath"]; ok {
			t.Fatalf("expected a negative id to never enter the map, got an entry: %v", m)
		}
	})
}
