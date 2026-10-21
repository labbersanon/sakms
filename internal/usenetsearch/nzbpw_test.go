package usenetsearch

import "testing"

func TestNZBSearchStem_StripsSplit7z(t *testing.T) {
	got := nzbSearchStem(`o6938TxyzABCDEF.7z.001`)
	if got != "o6938TxyzABCDEF" {
		t.Fatalf("got %q", got)
	}
	got = nzbSearchStem(`[1/9] - "o6938TxyzABCDEF.7z.001" yEnc (1/50)`)
	if got != "o6938TxyzABCDEF" {
		t.Fatalf("quoted got %q", got)
	}
	if nzbSearchStem("abc") != "" {
		t.Fatal("short stem")
	}
}

func TestArchiveSearchStem_Prefers7z(t *testing.T) {
	rows := []HeaderRow{
		{Filename: "aaaaaaaa.par2", Subject: `"aaaaaaaa.par2"`},
		{Filename: "o6938TxyzABCDEF.7z.001", Subject: `"o6938TxyzABCDEF.7z.001"`},
	}
	if got := archiveSearchStem(rows); got != "o6938TxyzABCDEF" {
		t.Fatalf("got %q", got)
	}
}
