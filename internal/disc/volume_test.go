package disc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeVolume(t *testing.T) {
	got := sanitizeVolume("LOONEY_TUNES_GOLDEN_V5_D1")
	if got != "LOONEY_TUNES_GOLDEN_V5_D1" {
		t.Fatalf("got %q", got)
	}
	got = sanitizeVolume("bad:name/foo")
	if got != "bad_name_foo" {
		t.Fatalf("got %q", got)
	}
	if sanitizeVolume("   ") != "DVD" {
		t.Fatalf("empty = %q", sanitizeVolume("   "))
	}
}

func TestVolumeFromPVD(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "disc.iso")
	buf := make([]byte, 32768+80)
	copy(buf[32769:32774], "CD001")
	copy(buf[32808:32808+32], []byte("ANIMANIACS_VOLUME_3_DISC_1     "))
	if err := os.WriteFile(src, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	got := volumeFromPVD(src)
	if got != "ANIMANIACS_VOLUME_3_DISC_1" {
		t.Fatalf("pvd = %q", got)
	}
}

func TestSidecarPath(t *testing.T) {
	got := SidecarPath("/media/Looney/foo.iso")
	if !strings.HasSuffix(got, "foo.disc.json") {
		t.Fatalf("got %q", got)
	}
}

func TestIsDiscImage(t *testing.T) {
	if !IsDiscImage("a.ISO") || !IsDiscImage("b.img") {
		t.Fatal("expected iso/img")
	}
	if IsDiscImage("a.mkv") || IsDiscImage("VIDEO_TS") {
		t.Fatal("rejected non-images")
	}
}
