package usenet

import (
	"testing"
)

func TestIsTextPasswordFile(t *testing.T) {
	for _, tc := range []struct {
		sub  string
		want bool
	}{
		{`[9/9] "Show.S01E01.nfo" yEnc (1/1)`, true},
		{`[8/9] "password.txt" yEnc (1/1)`, true},
		{`release.nfo`, true},
		{`[1/9] "Show.S01E01.part01.rar" yEnc (1/50)`, false},
		{`[2/9] "folder.jpg" yEnc (1/1)`, false},
		{`[3/9] "Show.sfv" yEnc (1/1)`, false},
	} {
		if got := isTextPasswordFile(tc.sub); got != tc.want {
			t.Errorf("isTextPasswordFile(%q) = %v, want %v", tc.sub, got, tc.want)
		}
	}
}

func TestTextPasswordTargetsSkipsRAR(t *testing.T) {
	nzb := &NZB{Files: []NZBFile{
		{Subject: `[1/2] "a.part01.rar" yEnc`, Segs: []NZBSegment{{Bytes: 1000, MsgID: "rar"}}},
		{Subject: `[2/2] "a.nfo" yEnc (1/1)`, Segs: []NZBSegment{{Bytes: 400, MsgID: "nfo1"}}},
	}}
	got := textPasswordTargets(nzb)
	if len(got) != 1 || got[0].msgID != "nfo1" {
		t.Fatalf("got %+v", got)
	}
}

func TestPasswordsFromArticleText(t *testing.T) {
	got := passwordsFromArticleText([]byte("Show.S01E01\r\nPassword: hunter2\r\nPosted by foo\n"))
	if len(got) != 1 || got[0] != "hunter2" {
		t.Fatalf("got %v", got)
	}
	got = passwordsFromArticleText([]byte("password is roman\n"))
	if len(got) != 1 || got[0] != "roman" {
		t.Fatalf("got %v", got)
	}
	got = passwordsFromArticleText([]byte("Show {{secret}} packed\n"))
	if len(got) != 1 || got[0] != "secret" {
		t.Fatalf("got %v", got)
	}
	if n := passwordsFromArticleText([]byte("this release is password protected\n")); len(n) != 0 {
		t.Fatalf("protected sentence = %v", n)
	}
	if n := passwordsFromArticleText([]byte("Rar!\x1a\x07\x01 encrypted")); len(n) != 0 {
		t.Fatalf("rar payload = %v", n)
	}
}

func TestPasswordsFromPasswordFile(t *testing.T) {
	got := passwordsFromPasswordFile([]byte("# ignore\n\nhunter2\n"))
	if len(got) != 1 || got[0] != "hunter2" {
		t.Fatalf("got %v", got)
	}
}

func TestCleanExtractedPasswordRejectsJunk(t *testing.T) {
	if got := cleanExtractedPassword("password"); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := cleanExtractedPassword("http://example"); got != "" {
		t.Fatalf("got %q", got)
	}
}
