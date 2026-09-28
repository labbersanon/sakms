package adultnewest

import "testing"

// TestNormalizeAdultQuery is the regression test for a real "Adult downloads
// never resolve" report: Prowlarr returned 0 raw releases for nearly every
// scene tried, because the raw studio+title query (colons, commas,
// asterisks, apostrophes and all) rarely appears verbatim in how trackers
// actually name Adult releases.
func TestNormalizeAdultQuery(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{
			"Private Classics Franky Knight: Curvy And Horny, Looking For A Stallion",
			"Private Classics Franky Knight Curvy And Horny Looking For A Stallion",
		},
		{"Cruel Handjobs Little Trick.", "Cruel Handjobs Little Trick"},
		{"CzechAR Stepmommy Finally Admits She Fantasizes About You Too *4k", "CzechAR Stepmommy Finally Admits She Fantasizes About You Too 4k"},
		{"Gloryhole Secrets Satine Summers' Seventh Interview", "Gloryhole Secrets Satine Summers Seventh Interview"},
		{"  extra   whitespace   here  ", "extra whitespace here"},
		{"", ""},
		{"(parens) [brackets] {braces}", "parens brackets braces"},
	}
	for _, tc := range cases {
		if got := NormalizeAdultQuery(tc.raw); got != tc.want {
			t.Errorf("NormalizeAdultQuery(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
