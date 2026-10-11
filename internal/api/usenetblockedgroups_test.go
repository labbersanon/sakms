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
		{Title: "Hot.Wheels.S03E01.1080p.WEB-DL-Smad"},
		{Title: "Show.S01E01.1080p.WEB password=secret-OK"},
	}
	out := filterBlockedReleaseGroups(in, []string{"TupaC", "Smad"})
	if len(out) != 3 {
		t.Fatalf("len=%d want 3: %+v", len(out), titlesOf(out))
	}
	if out[0].Title != in[0].Title || out[1].Title != in[3].Title || out[2].Title != in[5].Title {
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
