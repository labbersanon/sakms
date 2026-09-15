package usenet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeObfuscatedPar2Names_RenamesMatroska(t *testing.T) {
	dir := t.TempDir()
	// EBML / Matroska magic
	fake := filepath.Join(dir, "show.vol-01.par2")
	payload := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("fake-matroska-body")...)
	if err := os.WriteFile(fake, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := verifyAndRepair(dir, []string{fake})
	if err != nil {
		t.Fatalf("verifyAndRepair: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("files = %v", got)
	}
	if !strings.HasSuffix(got[0], ".mkv") {
		t.Fatalf("want .mkv rename, got %q", got[0])
	}
	if _, err := os.Stat(got[0]); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}
	if _, err := os.Stat(fake); !os.IsNotExist(err) {
		t.Fatal("original .par2 path should be gone after rename")
	}
}

func TestNormalizeObfuscatedPar2Names_KeepsRealPar2(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "set.par2")
	// Minimal PAR2 magic — Parse will fail later, but classification must keep it.
	if err := os.WriteFile(real, []byte("PAR2\x00PKT incomplete"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isPar2Payload(real) {
		t.Fatal("expected isPar2Payload true for PAR2 magic")
	}
	got := normalizeObfuscatedPar2Names([]string{real})
	if len(got) != 1 || got[0] != real {
		t.Fatalf("real par2 should be unchanged, got %v", got)
	}
}
