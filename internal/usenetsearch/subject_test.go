package usenetsearch

import (
	"context"
	"testing"
)

func TestParseSubject_Scene(t *testing.T) {
	ps := ParseSubject(`[Group] Some.Movie.2024.1080p.BluRay.x264-GROUP - "some.movie.2024.1080p.bluray.x264-group.part01.rar" yEnc (1/120)`)
	if !ps.OK || ps.Obfuscated {
		t.Fatalf("expected usable parse, got %+v", ps)
	}
	if ps.PartN != 1 || ps.PartM != 120 {
		t.Fatalf("parts: %+v", ps)
	}
}

func TestParseSubject_Obfuscated(t *testing.T) {
	ps := ParseSubject(`a1b2c3d4e5f6789012345678 yEnc (1/10)`)
	if !ps.Obfuscated {
		t.Fatalf("expected obfuscated, got %+v", ps)
	}
}

func TestLocatorRoundTrip(t *testing.T) {
	raw := EncodeLocator("alt.binaries.movies", "abc@example.com", "Some.Release", 100, 12345)
	p, ok, err := DecodeLocator(raw)
	if err != nil || !ok {
		t.Fatalf("decode: ok=%v err=%v", ok, err)
	}
	if p.Group != "alt.binaries.movies" || p.SeedMsgID != "abc@example.com" || p.Name != "Some.Release" {
		t.Fatalf("parts: %+v", p)
	}
	if !IsLocator(raw) {
		t.Fatal("IsLocator")
	}
	if IsLocator("https://example.com/x.nzb") {
		t.Fatal("http should not be locator")
	}
}

func TestToNZB(t *testing.T) {
	c := Candidate{
		Name:  "Rel",
		Group: "alt.binaries.movies",
		Files: []CandidateFile{{
			Subject:  "Rel.part01.rar",
			Filename: "Rel.part01.rar",
			Segs:     []CandidateSeg{{Bytes: 100, Number: 1, MsgID: "m1@x"}, {Bytes: 100, Number: 2, MsgID: "m2@x"}},
		}},
	}
	nzb, err := ToNZB(c)
	if err != nil || len(nzb.Files) != 1 || len(nzb.Files[0].Segs) != 2 {
		t.Fatalf("nzb=%+v err=%v", nzb, err)
	}
}

func TestOpenIndexSearch(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	err = idx.Upsert(context.Background(), []HeaderRow{
		{Group: "g", MsgNum: 1, MsgID: "a@x", Subject: "Foo.Bar.2024.mkv yEnc (1/2)", ReleaseName: "Foo.Bar.2024", PartN: 1, PartM: 2, Filename: "Foo.Bar.2024.mkv", Bytes: 1000, PostedAt: 1700000000},
		{Group: "g", MsgNum: 2, MsgID: "b@x", Subject: "Foo.Bar.2024.mkv yEnc (2/2)", ReleaseName: "Foo.Bar.2024", PartN: 2, PartM: 2, Filename: "Foo.Bar.2024.mkv", Bytes: 1000, PostedAt: 1700000000},
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := idx.SearchReleases(context.Background(), Query{Terms: []string{"Foo"}, Groups: []string{"g"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].SegmentCount != 2 {
		t.Fatalf("got %+v", cs)
	}
}
