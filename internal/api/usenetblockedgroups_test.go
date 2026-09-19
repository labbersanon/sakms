package api

import (
	"testing"

	"github.com/labbersanon/sakms/internal/prowlarr"
)

func TestFilterBlockedReleaseGroups_DropsTupaCAndPasswordTitles(t *testing.T) {
	in := []prowlarr.Release{
		{Title: "Love.Is.Blind.S07E12.1080p.WEB.h264-EDITH"},
		{Title: "Love.Is.Blind.S07E12.1080p.NF.WEB-DL.DUAL-TupaC"},
		{Title: "Some.Show.S01E01.PASSWORD.PROTECTED-FOO"},
		{Title: "Love.Is.Blind.S06E02.1080p.WEB-DL-RiPER"},
	}
	out := filterBlockedReleaseGroups(in, []string{"TupaC"})
	if len(out) != 2 {
		t.Fatalf("len=%d want 2: %+v", len(out), titlesOf(out))
	}
	if out[0].Title != in[0].Title || out[1].Title != in[3].Title {
		t.Fatalf("got %+v", titlesOf(out))
	}
}

func TestNormalizeBlockedGroups_Dedupes(t *testing.T) {
	got := normalizeBlockedGroups([]string{" TupaC ", "tupac", "EDITH", ""})
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
}

func titlesOf(rs []prowlarr.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	return out
}
