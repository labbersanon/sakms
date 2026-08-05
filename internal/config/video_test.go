package config

import "testing"

func TestIsVideoExt_JellyfinParity(t *testing.T) {
	wantTrue := []string{".mkv", "MKV", ".mk3d", ".m2ts", ".mpg", ".mpeg", ".iso", ".img", ".mp4", "ts"}
	for _, ext := range wantTrue {
		if !IsVideoExt(ext) {
			t.Errorf("IsVideoExt(%q) = false, want true", ext)
		}
	}
	wantFalse := []string{".plexmatch", ".nfo", ".srr", ".jpg", "", "320 - 10x10", ".trickplay"}
	for _, ext := range wantFalse {
		if IsVideoExt(ext) {
			t.Errorf("IsVideoExt(%q) = true, want false", ext)
		}
	}
	if !IsVideoFile("/media/Movie.mkv") {
		t.Error("IsVideoFile(.mkv) = false")
	}
	if IsVideoFile("/media/show/.plexmatch") {
		t.Error("IsVideoFile(.plexmatch) = true")
	}
	if IsVideoFile("/media/trickplay/320 - 10x10") {
		t.Error("IsVideoFile(extensionless) = true")
	}
}
