package predb

import (
	"context"
	"testing"
)

func TestStore_SearchFilenameAndTitle(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const title = "Named.From.File.2020.1080p.BluRay.x264-GRP"
	const fn = "v1aaqzdh11lfeaan9yuztj.part01.rar"
	if err := s.Upsert(ctx, Entry{Title: title, Filename: fn, SizeBytes: 100 << 20}, SourceDump); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Search(ctx, "V1AAqzDH11LfEAAN9yuzTJ.part01.rar")
	if err != nil || !ok || got.Title != title {
		t.Fatalf("filename Search = %+v ok=%v err=%v", got, ok, err)
	}
	got, ok, err = s.Search(ctx, title+".mkv")
	if err != nil || !ok || got.Title != title {
		t.Fatalf("cleaned title Search = %+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, err := s.Search(ctx, "LDfYSlzgpWrggjClQRtBfk.part047.rar"); err != nil || ok {
		t.Fatalf("unknown ngPost stem must miss, ok=%v err=%v", ok, err)
	}
}
