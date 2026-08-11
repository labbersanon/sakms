package api

import (
	"testing"
)

// TestAdultSceneKey covers the composite key derivation:
// - box+sceneId form when both are present
// - title fallback when either is absent
// - punctuation/case normalization on the fallback
func TestAdultSceneKey(t *testing.T) {
	cases := []struct {
		name    string
		box     string
		sceneID string
		title   string
		want    string
	}{
		{
			name:    "box+sceneId form — catalog card",
			box:     "stashdb",
			sceneID: "abc-123",
			title:   "Some Scene",
			want:    "stashdb:abc-123",
		},
		{
			name:    "title fallback — sceneId empty",
			box:     "stashdb",
			sceneID: "",
			title:   "Wild Scene Title",
			want:    "title:wild scene title",
		},
		{
			name:    "title fallback — box empty",
			box:     "",
			sceneID: "abc-123",
			title:   "Wild Scene Title",
			want:    "title:wild scene title",
		},
		{
			name:    "title fallback — both empty",
			box:     "",
			sceneID: "",
			title:   "Wild Scene Title",
			want:    "title:wild scene title",
		},
		{
			name:    "punctuation normalization on title fallback",
			box:     "",
			sceneID: "",
			title:   "Cathy's Craving: Hot Scene!",
			want:    "title:cathys craving hot scene",
		},
		{
			name:    "case-insensitive title fallback",
			box:     "",
			sceneID: "",
			title:   "UPPER CASE TITLE",
			want:    "title:upper case title",
		},
		{
			name:    "box+sceneId takes priority over non-empty title",
			box:     "tpdb",
			sceneID: "xyz-789",
			title:   "This Title Should Be Ignored",
			want:    "tpdb:xyz-789",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := adultSceneKey(tc.box, tc.sceneID, tc.title)
			if got != tc.want {
				t.Errorf("adultSceneKey(%q, %q, %q) = %q, want %q", tc.box, tc.sceneID, tc.title, got, tc.want)
			}
		})
	}
}

// TestAdultIdentityWeak covers the weak-identity predicate:
// - empty+empty ⇒ weak (nothing to check against)
// - studio appears in release title ⇒ strong
// - performer appears in release title (studio absent) ⇒ strong
// - neither studio nor any performer appears ⇒ weak
func TestAdultIdentityWeak(t *testing.T) {
	cases := []struct {
		name         string
		studio       string
		performers   []string
		releaseTitle string
		want         bool // true = weak
	}{
		{
			name:         "empty studio and empty performers — weak by clause (a)",
			studio:       "",
			performers:   nil,
			releaseTitle: "SomeStudio.Wild.Scene.1080p-GROUP",
			want:         true,
		},
		{
			name:         "empty studio and empty slice — weak by clause (a)",
			studio:       "",
			performers:   []string{},
			releaseTitle: "SomeStudio.Wild.Scene.1080p-GROUP",
			want:         true,
		},
		{
			name:         "studio appears in release title — strong",
			studio:       "Cathys Craving",
			performers:   nil,
			releaseTitle: "CathysCraving.Teasing.Cheerleader.XXX.1080p-GROUP",
			want:         false,
		},
		{
			name:         "performer appears in release title, no studio — strong",
			studio:       "",
			performers:   []string{"Pixie Smalls"},
			releaseTitle: "CathysCraving.Pixie.Smalls.Teasing.Cheerleader.XXX.1080p",
			want:         false,
		},
		{
			name:         "neither studio nor performer in title — weak by clause (b)",
			studio:       "Cathy's Craving",
			performers:   []string{"Jane Doe"},
			releaseTitle: "CompletelyUnrelated.Release.Name.2020-GROUP",
			want:         true,
		},
		{
			name:         "one performer matches out of multiple — strong",
			studio:       "",
			performers:   []string{"Unknown Person", "Pixie Smalls"},
			releaseTitle: "CathysCraving.Pixie.Smalls.Cheerleader.1080p",
			want:         false,
		},
		{
			name:         "studio alone matches without performers — strong",
			studio:       "Cathys Craving",
			performers:   []string{"Someone Else"},
			releaseTitle: "CathysCraving.SomeScene.1080p-GROUP",
			want:         false,
		},
		{
			name:         "single-word studio matches (singleWordTitleMatches fallback)",
			studio:       "Wicked",
			performers:   nil,
			releaseTitle: "Wicked.Wild.Scene.1080p-GROUP",
			want:         false,
		},
		{
			name:         "blank performer in slice is skipped gracefully",
			studio:       "",
			performers:   []string{"", ""},
			releaseTitle: "SomeStudio.Wild.Scene.1080p-GROUP",
			want:         true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := adultIdentityWeak(tc.studio, tc.performers, tc.releaseTitle)
			if got != tc.want {
				t.Errorf("adultIdentityWeak(%q, %v, %q) = %v, want %v",
					tc.studio, tc.performers, tc.releaseTitle, got, tc.want)
			}
		})
	}
}
