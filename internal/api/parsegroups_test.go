package api

import "testing"

func TestParseGroupList(t *testing.T) {
	got := parseGroupList("a\nb, c\n\na")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("parseGroupList = %v", got)
	}
}
