package usenet

import (
	"context"
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

func TestPasswordsFromTitles(t *testing.T) {
	got := PasswordsFromTitles([]string{
		"Show.S01E01.1080p-GRP",
		"Show.S01E01.1080p-GRP {{hunter2}}",
		"Show.S01E01 password=roman",
		"Show.S01E01 {{password}}",
	})
	if len(got) != 2 || got[0] != "hunter2" || got[1] != "roman" {
		t.Fatalf("PasswordsFromTitles = %v", got)
	}
}

func TestDiscoverySearchQueryDropsEmbeddedPassword(t *testing.T) {
	got := DiscoverySearchQuery("Show.S01E01.1080p-GRP {{hunter2}}.nzb")
	if got != "Show.S01E01.1080p-GRP" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteStagingPasswordsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeStagingPasswords(dir, []string{" alpha ", "alpha", "beta"})
	got := stagingPasswords(dir)
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("got %v", got)
	}
}

func TestDiscoverJobPasswordsSkipsWhenSidecarPresent(t *testing.T) {
	dir := t.TempDir()
	writeStagingPasswords(dir, []string{"from-sidecar"})
	called := 0
	m := &Manager{
		downloads: map[string]*dlState{
			"nzb-1": {gid: "nzb-1", name: "Show.S01E01", stagingDir: dir},
		},
		discoverPasswords: func(context.Context, string) []string {
			called++
			return []string{"from-search"}
		},
	}
	m.discoverJobPasswords(context.Background(), "nzb-1")
	if called != 0 {
		t.Fatalf("discover called %d times, want 0 when sidecar exists", called)
	}
}

func TestDiscoverJobPasswordsWritesSidecar(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{
		downloads: map[string]*dlState{
			"nzb-1": {gid: "nzb-1", name: "Show.S01E01", stagingDir: dir},
		},
		discoverPasswords: func(_ context.Context, title string) []string {
			if title != "Show.S01E01" {
				t.Fatalf("title = %q", title)
			}
			return []string{"from-search"}
		},
	}
	m.discoverJobPasswords(context.Background(), "nzb-1")
	got := stagingPasswords(dir)
	if len(got) != 1 || got[0] != "from-search" {
		t.Fatalf("sidecar = %v", got)
	}
	if !m.hasUnpackPassword("nzb-1") {
		t.Fatal("expected hasUnpackPassword after discover")
	}
	m.discoverJobPasswords(context.Background(), "nzb-1")
}

func TestHasUnpackPasswordSeesSidecar(t *testing.T) {
	dir := t.TempDir()
	writeStagingPasswords(dir, []string{"sidecar"})
	m := &Manager{
		downloads: map[string]*dlState{
			"nzb-1": {gid: "nzb-1", stagingDir: dir},
		},
	}
	if !m.hasUnpackPassword("nzb-1") {
		t.Fatal("expected sidecar to count as a known password")
	}
}

func TestPreferredNZBName_PrefersDNZBOverPassedShowTitle(t *testing.T) {
	got := preferredNZBName("The Red Skelton Show", DNZBHeaders{Name: "Show.S04E30.720p.WEB-WEBTUBE"}, nil)
	if got != "Show.S04E30.720p.WEB-WEBTUBE" {
		t.Fatalf("got %q", got)
	}
}
