package usenet

import "testing"

func TestParseListActiveName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"alt.binaries.movies 100 1 y", "alt.binaries.movies"},
		{"  alt.binaries.tv 5 1 n  ", "alt.binaries.tv"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := ParseListActiveName(tc.in); got != tc.want {
			t.Errorf("ParseListActiveName(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
