package usenetsearch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
)

func TestParseSubject_SevenZSplitQuotedFilename(t *testing.T) {
	ps := ParseSubject(`[01/38] - "NOmSbr4NWyZ7Cq1K3tUnmfcYON3VyPTBb4R2lKG78d2AHuIBdc.7z.001" yEnc (1/20)`)
	if ps.Filename != "NOmSbr4NWyZ7Cq1K3tUnmfcYON3VyPTBb4R2lKG78d2AHuIBdc.7z.001" {
		t.Fatalf("filename=%q, want quoted .7z.001 (not truncated to .7z)", ps.Filename)
	}
	tok := ParseSubject(`NOmSbr4NWyZ7Cq1K3tUnmfcYON3VyPTBb4R2lKG78d2AHuIBdc.7z.038 yEnc (12/20)`)
	if !strings.HasSuffix(tok.Filename, ".7z.038") {
		t.Fatalf("unquoted filename=%q, want .7z.038", tok.Filename)
	}
}

func TestParseSubject_Scene(t *testing.T) {
	ps := ParseSubject(`[Group] Some.Movie.2024.1080p.BluRay.x264-GROUP - "some.movie.2024.1080p.bluray.x264-group.part01.rar" yEnc (1/120)`)
	if !ps.OK || ps.Obfuscated {
		t.Fatalf("expected usable parse, got %+v", ps)
	}
	if ps.PartN != 1 || ps.PartM != 120 {
		t.Fatalf("parts: %+v", ps)
	}
	if ps.Name != "Some.Movie.2024.1080p.BluRay.x264-GROUP" {
		t.Fatalf("name=%q", ps.Name)
	}
}

func TestParseSubject_TeeveeWrapperSameStem(t *testing.T) {
	a := ParseSubject(`[#a.b.teevee] Show.S01E01.720p.HDTV.x264-GRP - [01/30] - "show.s01e01.720p.hdtv.x264-grp.part01.rar" yEnc (1/20)`)
	b := ParseSubject(`[#a.b.teevee] Show.S01E01.720p.HDTV.x264-GRP - [30/30] - "show.s01e01.720p.hdtv.x264-grp.vol00+01.par2" yEnc (1/1)`)
	if !a.OK || a.Name != b.Name || a.Name != "Show.S01E01.720p.HDTV.x264-GRP" {
		t.Fatalf("stems a=%q b=%q", a.Name, b.Name)
	}
	if strings.Contains(a.Name, "teevee") || strings.Contains(a.Name, "[#") {
		t.Fatalf("wrapper left in stem: %q", a.Name)
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

func TestParseSubject_LeadTokenSpaceForm(t *testing.T) {
	const par2Subj = `5JSoQ-2rqpfGRs5hg0JjXx2j7o27 [3/10] "p4v0O75-2qNMayAm3QhP_iIbuaK6.par2" yEnc (58/97)`
	a := ParseSubject(par2Subj)
	b := ParseSubject(`5JSoQ-2rqpfGRs5hg0JjXx2j7o27 [7/10] "p4v0O75-2qNMayAm3QhP_iIbuaK6.part01.rar" yEnc (1/50)`)
	if !a.Obfuscated || a.OK || a.Name != "" {
		t.Fatalf("space-form token must be obfuscated, got %+v", a)
	}
	if !b.Obfuscated || b.Name != "" {
		t.Fatalf("same collection later file: %+v", b)
	}
	ka := ObfKey("alt.binaries.teevee", "p@t", par2Subj)
	kb := ObfKey("alt.binaries.teevee", "p@t", `5JSoQ-2rqpfGRs5hg0JjXx2j7o27 [7/10] "zzzzzzzzzzzzzzzzzzzz.part01.rar" yEnc (1/50)`)
	if ka != kb || !strings.HasPrefix(ka, obfKeyPrefix) {
		t.Fatalf("keys a=%s b=%s, want one obf: key (lead token groups random filenames)", ka, kb)
	}
}

func TestParseSubject_LeadTokenUnderscore14Char(t *testing.T) {
	ps := ParseSubject(`_1Pfe68F_aYXOyD [3/31] "cc57PmW5-Xk5p2b" yEnc (32/69)`)
	if !ps.Obfuscated || ps.Name != "" {
		t.Fatalf("14-char token with underscore must be obfuscated, got %+v", ps)
	}
}

func TestParseSubject_LowChurnLiveTeeveeToken(t *testing.T) {
	// Live 2026-10-18: admitted as named because churn was 3/19.
	for _, subj := range []string{
		`Njabzsard2xrgxGUQIT [1/17] "ghFYYdm8wzKU4p5qpqK.rar" yEnc (67/77)`,
		`YqhgxTKELhBTzNPdC [2/12] "0G6IJMEdZG_f-Evmq.par2" yEnc (13/21)`,
		`GPmdeCQPiAYfvskRv [5/7] "6NHr2KAtKsF0O82q4.rar" yEnc (5/72)`,
		`_KpudSiWIppvqepljgyXbMXsP [2/3] "J6QcK4_JgLGk2dRCirRQ49Zsi.rar" yEnc (44/88)`,
	} {
		ps := ParseSubject(subj)
		if !ps.Obfuscated || ps.OK || ps.Name != "" {
			t.Fatalf("%q: want obfuscated unnamed, got %+v", subj, ps)
		}
		h := parseHeader("alt.binaries.teevee", subj, "p@t", "m@x", 1, 1, 1000, true)
		if !isObfKey(h.ReleaseName) {
			t.Fatalf("%q staged as %q, want obf:", subj, h.ReleaseName)
		}
	}
	if tokenLooksObfuscated("AmericanHorrorStory") {
		t.Fatal("CamelCase title without a digit must not look obfuscated")
	}
}

func TestParseSubject_LeadToken15CharAndLeadingUnderscore(t *testing.T) {
	short := ParseSubject(`KLoSR1zOTNoaex3 [10/15] "0AOugJ-kXbvFN0r" yEnc (122/138)`)
	if !short.Obfuscated || short.Name != "" {
		t.Fatalf("15-char space token must be obfuscated, got %+v", short)
	}
	lead := `_eZL_Gt5Lei3_2pVVtuYg-nzLtYDY [3/18] "PPBdjTAfd_hl0KNGCnkO5-xEujZht" yEnc (52/165)`
	ps := ParseSubject(lead)
	if !ps.Obfuscated || ps.Name != "" {
		t.Fatalf("leading-underscore token must be obfuscated, got %+v", ps)
	}
	ka := ObfKey("alt.binaries.teevee", "p@t", lead)
	kb := ObfKey("alt.binaries.teevee", "p@t", `_eZL_Gt5Lei3_2pVVtuYg-nzLtYDY [18/18] "zzzz.par2" yEnc (1/1)`)
	if ka != kb {
		t.Fatalf("keys split on file index: %s vs %s", ka, kb)
	}
}

func TestParseSubject_SpaceLeadTokenNotScene(t *testing.T) {
	ps := ParseSubject(`Some_Show_Name_S01E01 [01/20] "some.show.s01e01.part01.rar" yEnc (1/50)`)
	if ps.Obfuscated {
		t.Fatalf("episode token must not be obfuscated: %+v", ps)
	}
	year := ParseSubject(`Andor-2025 [1/5] "andor.2025.part01.rar" yEnc (1/10)`)
	if year.Obfuscated {
		t.Fatalf("year token must not be obfuscated: %+v", year)
	}
}

func TestParseSubject_EFNetRequestWrapper(t *testing.T) {
	a := ParseSubject(`[23051509]-[FULL]-[#a.b.teevee@EFNet]-[ Greys.Anatomy.S21E03.720p.HDTV.x264-GROUP ]-[01/30] - "greys.anatomy.s21e03.part01.rar" yEnc (1/50)`)
	b := ParseSubject(`[23051509]-[FULL]-[#a.b.teevee@EFNet]-[ Greys.Anatomy.S21E03.720p.HDTV.x264-GROUP ]-[30/30] - "greys.anatomy.s21e03.vol00+01.par2" yEnc (1/1)`)
	if !a.OK || a.Obfuscated || a.Name != "Greys.Anatomy.S21E03.720p.HDTV.x264-GROUP" {
		t.Fatalf("a=%+v", a)
	}
	if a.Name != b.Name {
		t.Fatalf("split %q vs %q", a.Name, b.Name)
	}
	if strings.Contains(a.Name, "FULL") || strings.Contains(a.Name, "EFNet") || strings.Contains(a.Name, "23051509") {
		t.Fatalf("wrapper left in stem: %q", a.Name)
	}
}

func TestParseSubject_BinReqWrapper(t *testing.T) {
	ps := ParseSubject(`www.Bin-Req.net Presents: #23051508 - Precure.S01E01.720p.HDTV.x264-GRP - "precure.s01e01.part01.rar" yEnc (1/20)`)
	if !ps.OK || ps.Obfuscated || ps.Name != "Precure.S01E01.720p.HDTV.x264-GRP" {
		t.Fatalf("%+v", ps)
	}
	if strings.Contains(strings.ToLower(ps.Name), "bin-req") || strings.Contains(ps.Name, "23051508") {
		t.Fatalf("wrapper left in stem: %q", ps.Name)
	}
}

func TestParseSubject_RequestIDOnlyIsObfuscated(t *testing.T) {
	a := ParseSubject(`[23051509]-[FULL]-[#a.b.teevee@EFNet]-[ ]-[01/10] - "abc.part01.rar" yEnc (1/50)`)
	b := ParseSubject(`[23051509]-[FULL]-[#a.b.teevee@EFNet]-[ ]-[10/10] - "zzz.par2" yEnc (1/1)`)
	if !a.Obfuscated || a.OK || a.Name != "" {
		t.Fatalf("want obfuscated unnamed, got %+v", a)
	}
	ka := ObfKey("alt.binaries.teevee", "p@t", `[23051509]-[FULL]-[#a.b.teevee@EFNet]-[ ]-[01/10] - "abc.part01.rar" yEnc (1/50)`)
	kb := ObfKey("alt.binaries.teevee", "p@t", `[23051509]-[FULL]-[#a.b.teevee@EFNet]-[ ]-[10/10] - "zzz.par2" yEnc (1/1)`)
	if ka != kb || !strings.HasPrefix(ka, obfKeyPrefix) {
		t.Fatalf("request id must group files: %s vs %s", ka, kb)
	}
	if b.Obfuscated != a.Obfuscated {
		t.Fatalf("b=%+v", b)
	}
	junk := ParseSubject(`23051509 ] - "file.part01.rar" yEnc (1/10)`)
	if !junk.Obfuscated || junk.OK || junk.Name != "" {
		t.Fatalf("numeric leftover must not be named: %+v", junk)
	}
}

func TestParseSubject_ImplausibleYencPartsIgnored(t *testing.T) {
	ps := ParseSubject(`"UrHfhOEuTZTAP6SWX2HhDYJ6N" yEnc (730696/967646)`)
	if ps.PartN != 0 || ps.PartM != 0 {
		t.Fatalf("parts %+v, want 0/0 (unique-ID pair is not a yEnc total)", ps)
	}
	h := parseHeader("alt.binaries.hdtv.x264", `"UrHfhOEuTZTAP6SWX2HhDYJ6N" yEnc (730696/967646)`,
		"p@x", "m@x", 1, 1, 1000, true)
	if isObfKey(h.ReleaseName) {
		t.Fatalf("staged %q, want skip_unparsed", h.ReleaseName)
	}
	ok := ParseSubject(`a1b2c3d4e5f6789012345678 yEnc (1/80)`)
	if ok.PartN != 1 || ok.PartM != 80 {
		t.Fatalf("plausible yEnc still parsed: %+v", ok)
	}
}

func TestStageObfuscated_FileCounterWithoutYenc(t *testing.T) {
	const g = "alt.binaries.teevee"
	const tok = "Njabzsard2xrgxGUQIT"
	a := parseHeader(g, tok+` [10/15] "zzzzzzzzzzzzzzzzzzzz.part01.rar"`, "p@t", "a@t", 1, 1, 1000, true)
	b := parseHeader(g, tok+` [11/15] "zzzzzzzzzzzzzzzzzzzz.part02.rar"`, "p@t", "b@t", 2, 1, 1000, true)
	if !isObfKey(a.ReleaseName) || a.ReleaseName != b.ReleaseName {
		t.Fatalf("want one staged key, got a=%q b=%q", a.ReleaseName, b.ReleaseName)
	}
	if a.FileM != 15 || a.PartM != 0 {
		t.Fatalf("FileM=%d PartM=%d (do not copy [k/N] into yEnc parts)", a.FileM, a.PartM)
	}
}

func TestStageObfuscated_MooveeYencLessTrailingParts(t *testing.T) {
	h := parseHeader("alt.binaries.moovee",
		`[38/38] - "H3tSMujaxxxxxxxx.vol03+04.par2" (53/166)`,
		"p@t", "m@t", 1, 1, 1000, true)
	if !isObfKey(h.ReleaseName) {
		t.Fatalf("want staged, got name=%q partM=%d fileM=%d", h.ReleaseName, h.PartM, h.FileM)
	}
	if h.PartM != 166 {
		t.Fatalf("partM=%d, want 166 from trailing (n/m)", h.PartM)
	}
}

func TestParseSubject_QuotedFansubFilename(t *testing.T) {
	clover := `[02/11] - "[UQW] Black Clover - Ep170 [BDRip 1080p HEVC-YUV420P10 FLAC].par2" yEnc (1/1)`
	ps := ParseSubject(clover)
	if !strings.Contains(ps.Filename, "Black Clover") || !ps.Meta {
		t.Fatalf("quoted fansub filename=%q meta=%v", ps.Filename, ps.Meta)
	}
	juu := `[3/8] - "[Onalrie] Juuou Mujin Dandivine - S01E02 [1080p WEBRip AV1].vol00+01.par2" yEnc (2/2)`
	ps = ParseSubject(juu)
	if ps.Filename == "01.par2" || !strings.Contains(ps.Filename, "Dandivine") {
		t.Fatalf("vol+ quoted filename=%q, want full title not 01.par2", ps.Filename)
	}
}

func TestParseSubject_XxxHashStemIsObfuscated(t *testing.T) {
	// Live 2026-10-19: xxx backfill admitted hyphenated hash stems as named.
	subj := `V-DtfXhDFjheDo7MLtjcb4LKQBjf2DsCzWn0_eMjneFNs0yDtdeBAJl0ub9oLloKyWSsyY9V6Lnyu9-jZ1gYHTjRWp8`
	ps := ParseSubject(subj)
	if !ps.Obfuscated || ps.OK || ps.Name != "" {
		t.Fatalf("partless xxx hash must be obfuscated unnamed, got %+v", ps)
	}
	h := parseHeader("alt.binaries.xxx", subj, "p@x", "m@x", 1, 1, 1000, true)
	if isObfKey(h.ReleaseName) || h.ReleaseName != "" {
		t.Fatalf("no [k/N]/yEnc: want skip_unparsed, got %q", h.ReleaseName)
	}
	scene := ParseSubject(`Some.Movie.2021.1080p.WEB-DL.x264-GRP yEnc (1/10)`)
	if scene.Obfuscated || !scene.OK || scene.Name != "Some.Movie.2021.1080p.WEB-DL.x264-GRP" {
		t.Fatalf("scene name with dots must stay named, got %+v", scene)
	}
}

func TestStageObfuscated_HdtvQuotedHashMkv(t *testing.T) {
	h := parseHeader("alt.binaries.hdtv.x264", `"htgdyhl2tbpvyqihkd.mkv" yEnc (3837/3928) 2750952218`,
		"p@x", "m@x", 1, 1, 1000, true)
	if !isObfKey(h.ReleaseName) {
		t.Fatalf("want staged, got %q", h.ReleaseName)
	}
}

func TestParseHeader_UnderscoreFansub1080pIsKey(t *testing.T) {
	const title = "[Fancutfags]_Puella_Magi_Madoka_Magica_-_03_[BD-Fancut][h264-1080p_FLAC][245C3BA1]"
	h := parseHeader("alt.binaries.multimedia.anime.highspeed",
		`[01/27] - "`+title+`.vol001+002.par2" yEnc (1/1)`, "p@x", "m@x", 1, 1, 1000, true)
	if isObfKey(h.ReleaseName) || h.ReleaseName != title {
		t.Fatalf("release=%q, want underscore 1080p fansub title", h.ReleaseName)
	}
}

func TestParseHeader_QuotedTitleWithoutResIsKey(t *testing.T) {
	h := parseHeader("alt.binaries.multimedia.erotica",
		`"MilfAF - London River.mkv.part2.rar" yEnc (yEnc) (01/82)`, "p@x", "m@x", 1, 1, 1000, true)
	if isObfKey(h.ReleaseName) || h.ReleaseName != "MilfAF - London River" {
		t.Fatalf("release=%q, want quoted title without resolution", h.ReleaseName)
	}
	hash := parseHeader("alt.binaries.anime",
		`[4/10] - "nBRXqe48CSFbDwIghsuUcWr.vol001+002.par2" yEnc (1/1)`, "p@x", "m2@x", 2, 1, 1000, true)
	if !isObfKey(hash.ReleaseName) {
		t.Fatalf("hash par2 must stay staged, got %q", hash.ReleaseName)
	}
}

func TestParseHeader_QuotedIdentEpisodeIsKey(t *testing.T) {
	const title = "WOR-HAZ Chivalry of a Failed Knight S01E09 BD HEVC 1080p FLAC"
	subj := `[9/12] - "` + title + `.mkv" yEnc (2269/3248)`
	h := parseHeader("alt.binaries.multimedia.anime.highspeed", subj, "p@x", "m@x", 1, 1, 1000, true)
	if isObfKey(h.ReleaseName) || h.ReleaseName != title {
		t.Fatalf("release=%q, want quoted episode title", h.ReleaseName)
	}
	par2 := parseHeader("alt.binaries.multimedia.anime.highspeed",
		`[9/12] - "`+title+`.par2" yEnc (1/1)`, "p@x", "m2@x", 2, 1, 1000, true)
	if par2.ReleaseName != h.ReleaseName {
		t.Fatalf("par2 key=%q mkv key=%q", par2.ReleaseName, h.ReleaseName)
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
		Name:     "Rel",
		Group:    "alt.binaries.movies",
		Complete: true,
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
	c.Complete = false
	if _, err := ToNZB(c); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("incomplete ToNZB err=%v, want ErrIncomplete", err)
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

func TestSkipPayload_SampleNotMetaPar2(t *testing.T) {
	if !skipPayload(`"Movie.2021.1080p.sample.mkv" yEnc (1/1)`, "Movie.2021.1080p.sample.mkv", false) {
		t.Fatal("sample mkv must skip payload")
	}
	if skipPayload(`"Movie.2021.1080p.mkv" yEnc (1/50)`, "Movie.2021.1080p.mkv", false) {
		t.Fatal("main mkv is payload")
	}
	if skipPayload(`"The.Sampler.2021.1080p.mkv" yEnc (1/10)`, "The.Sampler.2021.1080p.mkv", false) {
		t.Fatal("Sampler in a title is not a trailer file")
	}
	if !skipPayload(`"hash.par2" yEnc (1/1)`, "hash.par2", false) {
		t.Fatal("par2 is meta")
	}
}
