package usenetsearch

import (
	"context"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
)

func TestParseSubject_Scene(t *testing.T) {
	ps := ParseSubject(`[Group] Some.Movie.2024.1080p.BluRay.x264-GROUP - "some.movie.2024.1080p.bluray.x264-group.part01.rar" yEnc (1/120)`)
	if !ps.OK || ps.Obfuscated {
		t.Fatalf("expected usable parse, got %+v", ps)
	}
	if ps.PartN != 1 || ps.PartM != 120 {
		t.Fatalf("parts: %+v", ps)
	}
	if ps.Name != "[Group] Some.Movie.2024.1080p.BluRay.x264-GROUP" {
		t.Fatalf("name=%q", ps.Name)
	}
}

func TestParseSubject_MultiFileSameStem(t *testing.T) {
	a := ParseSubject(`[01/73] R.J.Decker.S02E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-RAWR - "r.j.decker.s02e05.1080p.amzn.web-dl.ddp5.1.h.264-rawr.part01.rar" yEnc (1/40)`)
	b := ParseSubject(`[48/73] R.J.Decker.S02E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-RAWR - "r.j.decker.s02e05.1080p.amzn.web-dl.ddp5.1.h.264-rawr.part48.rar" yEnc (12/40)`)
	c := ParseSubject(`[73/73] R.J.Decker.S02E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-RAWR - "r.j.decker.s02e05.1080p.amzn.web-dl.ddp5.1.h.264-rawr.vol00+01.par2" yEnc (1/1)`)
	if !a.OK || a.Name == "" || a.Name != b.Name || a.Name != c.Name {
		t.Fatalf("stems a=%q b=%q c=%q ok=%v", a.Name, b.Name, c.Name, a.OK)
	}
	if a.FileN != 1 || a.FileM != 73 || b.FileN != 48 || c.FileN != 73 || c.FileM != 73 {
		t.Fatalf("file counters a=%d/%d b=%d/%d c=%d/%d", a.FileN, a.FileM, b.FileN, b.FileM, c.FileN, c.FileM)
	}
	if a.PartN != 1 || a.PartM != 40 || b.PartN != 12 || b.PartM != 40 {
		t.Fatalf("yEnc parts a=%d/%d b=%d/%d", a.PartN, a.PartM, b.PartN, b.PartM)
	}
}

func TestParseSubject_ParenFileCountNotYenc(t *testing.T) {
	ps := ParseSubject(`Bhool.Bhulaiyaa.2007] (72/93) - "Bhool.Bhulaiyaa.2007.Untouched.Blu-ray.1080p.BD50.AVC.DD.DTS-HD.MA.5.1-D0NL1GHT.vol230+" yEnc (1/50)`)
	if !ps.OK {
		t.Fatalf("expected usable parse, got %+v", ps)
	}
	if ps.PartN != 1 || ps.PartM != 50 {
		t.Fatalf("yEnc parts: %+v", ps)
	}
	if ps.FileN != 72 || ps.FileM != 93 {
		t.Fatalf("file count: %+v", ps)
	}
	if strings.Contains(ps.Name, "72/93") || strings.Contains(ps.Name, "1/50") {
		t.Fatalf("stem still has counters: %q", ps.Name)
	}
	other := ParseSubject(`Bhool.Bhulaiyaa.2007] (1/93) - "Bhool.Bhulaiyaa.2007.Untouched.Blu-ray.1080p.BD50.AVC.DD.DTS-HD.MA.5.1-D0NL1GHT.vol001+" yEnc (3/50)`)
	if ps.Name != other.Name {
		t.Fatalf("stems split: %q vs %q", ps.Name, other.Name)
	}
}

func TestParseSubject_Obfuscated(t *testing.T) {
	ps := ParseSubject(`a1b2c3d4e5f6789012345678 yEnc (1/10)`)
	if !ps.Obfuscated {
		t.Fatalf("expected obfuscated, got %+v", ps)
	}
}

func TestParseSubject_Latin1BeforeYencDoesNotPanic(t *testing.T) {
	// Live panic: ToLower expanded latin1 0xe4 → U+FFFD (+2 bytes) so
	// core[:i] was [:80] on a 78-byte subject.
	subj := "Some.Movie.2024.1080p" + string([]byte{0xe4}) + " yEnc (1/10)"
	ps := ParseSubject(subj)
	if ps.PartN != 1 || ps.PartM != 10 {
		t.Fatalf("parts: %+v", ps)
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
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
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
	if _, err := idx.RecomputeDirty(context.Background(), 100); err != nil {
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
