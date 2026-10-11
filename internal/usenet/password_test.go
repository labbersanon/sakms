package usenet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSplitTitlePassword(t *testing.T) {
	for _, tc := range []struct {
		in, title, pw string
	}{
		{"Show.S01E01 {{secret}}.nzb", "Show.S01E01", "secret"},
		{"Show.S01E01 password=roman", "Show.S01E01", "roman"},
		{"Show.S01E01 password=roman.nzb", "Show.S01E01", "roman"},
		{"Show.S01E01 / hunter2", "Show.S01E01", "hunter2"},
		{"Show.S01E01 {{password}}", "Show.S01E01 {{password}}", ""},
		{"Show.S01E01.PASSWORD.PROTECTED-FOO", "Show.S01E01.PASSWORD.PROTECTED-FOO", ""},
		{"plain-title", "plain-title", ""},
		{"", "", ""},
	} {
		title, pw := SplitTitlePassword(tc.in)
		if title != tc.title || pw != tc.pw {
			t.Errorf("SplitTitlePassword(%q) = (%q, %q), want (%q, %q)", tc.in, title, pw, tc.title, tc.pw)
		}
	}
}

func TestNZBMetaPassword(t *testing.T) {
	nzb := &NZB{Meta: []NZBMeta{
		{Type: "title", Value: "Show.S01E01"},
		{Type: "password", Value: " from-meta "},
	}}
	dnzb := DNZBHeaders{Name: "Show.S01E01 {{ignored}}", Password: "from-header"}
	got := collectJobPasswords(dnzb, nzb, "Show.S01E01", nil)
	if len(got) < 2 || got[0] != "from-header" || got[1] != "from-meta" {
		t.Fatalf("collectJobPasswords = %v, want header then meta first", got)
	}
}

func TestStagingPasswords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "password.txt")
	if err := os.WriteFile(path, []byte("# comment\n\nalpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := stagingPasswords(dir)
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("stagingPasswords = %v", got)
	}
}

func TestPreferredNZBName_PrefersDNZBOverPassedShowTitle(t *testing.T) {
	got := preferredNZBName("The Red Skelton Show", DNZBHeaders{Name: "Show.S04E30.720p.WEB-WEBTUBE"}, nil)
	if got != "Show.S04E30.720p.WEB-WEBTUBE" {
		t.Fatalf("got %q", got)
	}
}
