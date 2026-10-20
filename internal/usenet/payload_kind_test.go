package usenet

import "testing"

func TestPayloadKind(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"PAR2\x00PKT", "par2"},
		{"Rar!\x1a\x07\x00xxxx", "rar4"},
		{"Rar!\x1a\x07\x01\x00xxxx", "rar5"},
		{"PK\x03\x04xxxx", "zip"},
		{string(sevenZMagic) + "xxxx", "7z"},
		{"=ybegin line=128", "ybegin"},
		{"Some.Movie.mkv 0053CA13\n", "sfv"},
		{"", ""},
		{"\x00\x01\x02", "unknown"},
	}
	for _, c := range cases {
		if got := PayloadKind([]byte(c.in)); got != c.want {
			t.Fatalf("PayloadKind(%q)=%q want %q", c.in, got, c.want)
		}
	}
}
