package predb

import "testing"

func TestCleanForMatchingStripsParts(t *testing.T) {
	got := CleanForMatching(`path/LDfYSlzgpWrggjClQRtBfk.part047.rar`)
	if got != "LDfYSlzgpWrggjClQRtBfk" {
		t.Fatalf("cleaned=%q", got)
	}
	scene := CleanForMatching("Show.Name.S01E02.1080p.WEB-DL.x264-GRP.mkv")
	if scene != "Show.Name.S01E02.1080p.WEB-DL.x264-GRP" {
		t.Fatalf("scene=%q", scene)
	}
}

func TestLooksLikeHashed(t *testing.T) {
	if !LooksLikeHashed("LDfYSlzgpWrggjClQRtBfk.part047.rar") {
		t.Fatal("ngPost stem must be hashed")
	}
	if !LooksLikeHashed("abcdef0123456789abcdef0123456789.mkv") {
		t.Fatal("md5 filename must be hashed")
	}
	if LooksLikeHashed("Show.Name.S01E02.1080p.WEB-DL.x264-GRP.mkv") {
		t.Fatal("scene mkv must not look hashed")
	}
}
