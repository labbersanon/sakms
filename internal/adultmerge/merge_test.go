package adultmerge

import (
	"testing"

	"github.com/labbersanon/sakms/internal/stashbox"
	"github.com/labbersanon/sakms/internal/tpdbrest"
)

// TestNormalizeGender is the exact-match table test for normalizeGender's
// documented contract (see that function's doc comment): only "female"/"male"
// (case-insensitive, trimmed) normalize to themselves; everything else — unset,
// empty, non-binary, transgender, intersex, "unknown", or any unrecognized
// token — normalizes to "" (excluded), by fail-safe design, never a guess.
func TestNormalizeGender(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lowercase female", "female", "female"},
		{"uppercase FEMALE", "FEMALE", "female"},
		{"padded mixed-case Female", " Female ", "female"},
		{"lowercase male", "male", "male"},
		{"uppercase MALE", "MALE", "male"},
		// The substring trap: "transgender_female" and "TRANSGENDER_MALE" both
		// CONTAIN "female"/"male" as substrings. normalizeGender must use exact
		// match after lower+trim, never strings.Contains — a Contains-based
		// implementation would wrongly bucket these as female/male.
		{"SubstringTrap_TransgenderFemale_ExcludedNotFemale", "transgender_female", ""},
		{"transgender male excluded", "TRANSGENDER_MALE", ""},
		{"intersex excluded", "intersex", ""},
		{"non_binary excluded", "non_binary", ""},
		{"non-binary (hyphen) excluded", "non-binary", ""},
		{"trans female (space) excluded", "trans female", ""},
		{"empty string excluded", "", ""},
		{"unknown excluded", "unknown", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeGender(c.in); got != c.want {
				t.Errorf("normalizeGender(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestMergePerformersGenderStamp proves MergePerformers' Gender-stamping
// contract (see that function's doc comment): a paired card normalizes
// firstNonEmpty(stash.Gender, tpdb.Gender) — StashDB's raw value wins the
// pick even when it itself normalizes to "" (excluded), honoring StashDB's
// call rather than falling through to a TPDB value that would have
// normalized to something else. An unpaired card just normalizes its own
// source's raw gender.
func TestMergePerformersGenderStamp(t *testing.T) {
	t.Run("paired: StashDB=MALE wins over TPDB=Female", func(t *testing.T) {
		tpdb := []tpdbrest.Performer{{ID: "t1", Name: "Riley Reid", Gender: "Female"}}
		stash := []stashbox.Performer{{ID: "s1", Name: "Riley Reid", Gender: "MALE"}}
		out := MergePerformers(tpdb, stash)
		if len(out) != 1 {
			t.Fatalf("expected 1 merged card, got %d: %+v", len(out), out)
		}
		if out[0].Gender != "male" {
			t.Errorf("expected StashDB gender to win (male), got %+v", out[0])
		}
	})

	t.Run("paired: StashDB=NON_BINARY excludes even though TPDB=Female", func(t *testing.T) {
		tpdb := []tpdbrest.Performer{{ID: "t1", Name: "Riley Reid", Gender: "Female"}}
		stash := []stashbox.Performer{{ID: "s1", Name: "Riley Reid", Gender: "NON_BINARY"}}
		out := MergePerformers(tpdb, stash)
		if len(out) != 1 {
			t.Fatalf("expected 1 merged card, got %d: %+v", len(out), out)
		}
		if out[0].Gender != "" {
			t.Errorf("expected the card excluded (StashDB's non-binary call honored, never falling back to TPDB's Female), got %+v", out[0])
		}
	})

	t.Run("paired: StashDB empty falls back to TPDB=Female", func(t *testing.T) {
		tpdb := []tpdbrest.Performer{{ID: "t1", Name: "Riley Reid", Gender: "Female"}}
		stash := []stashbox.Performer{{ID: "s1", Name: "Riley Reid", Gender: ""}}
		out := MergePerformers(tpdb, stash)
		if len(out) != 1 {
			t.Fatalf("expected 1 merged card, got %d: %+v", len(out), out)
		}
		if out[0].Gender != "female" {
			t.Errorf("expected fallback to TPDB's gender (female) when StashDB's is empty, got %+v", out[0])
		}
	})

	t.Run("unpaired TPDB card carries normalizeGender(t.Gender)", func(t *testing.T) {
		tpdb := []tpdbrest.Performer{{ID: "t1", Name: "Only Tpdb Person", Gender: "Male"}}
		out := MergePerformers(tpdb, nil)
		if len(out) != 1 || out[0].Source != "tpdb" {
			t.Fatalf("expected 1 unpaired TPDB card, got %+v", out)
		}
		if out[0].Gender != "male" {
			t.Errorf("expected the unpaired TPDB card's own gender normalized (male), got %+v", out[0])
		}
	})

	t.Run("unpaired StashDB card carries normalizeGender(s.Gender)", func(t *testing.T) {
		stash := []stashbox.Performer{{ID: "s1", Name: "Only Stashdb Person", Gender: "FEMALE"}}
		out := MergePerformers(nil, stash)
		if len(out) != 1 || out[0].Source != "stashdb" {
			t.Fatalf("expected 1 unpaired StashDB card, got %+v", out)
		}
		if out[0].Gender != "female" {
			t.Errorf("expected the unpaired StashDB card's own gender normalized (female), got %+v", out[0])
		}
	})
}
