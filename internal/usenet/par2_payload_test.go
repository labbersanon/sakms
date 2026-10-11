package usenet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsPar2Payload_MagicAtOffset(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "set.vol-07.par2")
	body := append([]byte{0x00, 0x00, 0xFF, 0x00}, []byte("PAR2\x00PKT rest")...)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if !isPar2Payload(p) {
		t.Fatal("offset magic must count as PAR2")
	}
	if !looksPar2VolumeName(p) {
		t.Fatal("vol-07.par2 should look like a recovery volume")
	}
}

func TestTrimPar2Magic(t *testing.T) {
	data := append([]byte{0xAA, 0xBB}, []byte("PAR2\x00PKT")...)
	got := trimPar2Magic(data)
	if string(got[:8]) != "PAR2\x00PKT" {
		t.Fatalf("trim = %q", got[:8])
	}
}

func TestLooksPar2VolumeName(t *testing.T) {
	if !looksPar2VolumeName("a.vol00+01.par2") || !looksPar2VolumeName("a.vol-07.par2") {
		t.Fatal("standard vol names")
	}
	if looksPar2VolumeName("a.par2") || looksPar2VolumeName("a.rar") {
		t.Fatal("index/rar must not look like volumes")
	}
}
