package usenetsearch

import (
	"encoding/hex"
	"testing"
)

func TestDetectNamePattern_Hash7z(t *testing.T) {
	rows := []HeaderRow{
		{Filename: "4A1Xwx6mlhVYpaDkl40NnPSVNcJZj7ib5XysItStQlH5BKdmCT.7z.001"},
		{Filename: "4A1Xwx6mlhVYpaDkl40NnPSVNcJZj7ib5XysItStQlH5BKdmCT.7z.046"},
		{Filename: "4A1Xwx6mlhVYpaDkl40NnPSVNcJZj7ib5XysItStQlH5BKdmCT.par2"},
	}
	p := detectNamePattern(rows)
	if p.Obf != "hash-7z" || p.Pack != "7z-split" {
		t.Fatalf("pattern = %+v, want obf=hash-7z pack=7z-split", p)
	}
}

func TestDetectNamePattern_HashPar2(t *testing.T) {
	rows := []HeaderRow{
		{Filename: nfHash + ".par2"},
		{Filename: nfHash + ".vol000+01.par2"},
	}
	p := detectNamePattern(rows)
	if p.Obf != "hash-par2" || p.Pack != "par2" {
		t.Fatalf("pattern = %+v, want hash-par2/par2", p)
	}
}

func TestDetectNamePattern_HashRar(t *testing.T) {
	rows := []HeaderRow{
		{Filename: nfHash + ".part01.rar"},
		{Filename: nfHash + ".part099.rar"},
		{Filename: nfHash + ".sfv"},
	}
	p := detectNamePattern(rows)
	if p.Obf != "hash-rar" || p.Pack != "rar-split" {
		t.Fatalf("pattern = %+v, want hash-rar/rar-split", p)
	}
}

func TestDetectNamePattern_Quoted(t *testing.T) {
	rows := []HeaderRow{
		{Subject: `[01/12] - "Some.Movie.2021.1080p.BluRay.x264-GRP.mkv" yEnc (1/1)`},
	}
	p := detectNamePattern(rows)
	if p.Obf != "quoted" {
		t.Fatalf("pattern = %+v, want quoted", p)
	}
}

func TestPayloadKind_7zAESFooter(t *testing.T) {
	const live = "1706f7a0e97fe7010980e000070b0100022406f107010a530734cdd1c5f369ebc523030101055d0010000001000c80da821e0a01fc3137330000"
	hdr := mustHex(t, live)
	got := payloadKind(hdr, make([]byte, 32), uint64(len(hdr)))
	if got != "7z-aes" {
		t.Fatalf("payload=%q, want 7z-aes", got)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
