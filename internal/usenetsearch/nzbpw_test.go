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

func TestNZBSearchPoster_ParsesFrom(t *testing.T) {
	got := nzbSearchPoster(`AbCdEfGhIjKlM@ngPost.com`)
	if got != "AbCdEfGhIjKlM@ngPost.com" {
		t.Fatalf("bare: %q", got)
	}
	got = nzbSearchPoster(`ngPost <AbCdEfGhIjKlM@ngPost.com>`)
	if got != "AbCdEfGhIjKlM@ngPost.com" {
		t.Fatalf("angle: %q", got)
	}
}

func TestArchiveSearchPosted_MinPositive(t *testing.T) {
	rows := []HeaderRow{
		{PostedAt: 200},
		{PostedAt: 0},
		{PostedAt: 150},
	}
	if got := archiveSearchPosted(rows); got != 150 {
		t.Fatalf("got %d", got)
	}
}

func TestArchiveSearchPoster_PrefersNgPost(t *testing.T) {
	rows := []HeaderRow{
		{From: "other@example.com"},
		{From: "AbCdEfGhIjKlM@ngPost.com"},
	}
	if got := archiveSearchPoster(rows); got != "AbCdEfGhIjKlM@ngPost.com" {
		t.Fatalf("got %q", got)
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
