package usenet

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMatroskaPayload writes a file whose leading bytes are EBML/Matroska
// magic, so sniffMediaExt classifies it as .mkv regardless of its name.
func writeMatroskaPayload(t *testing.T, path string) {
	t.Helper()
	payload := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, "fake-matroska-body"...)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeObfuscatedPar2Names_RenamesMatroska(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "show.vol-01.par2")
	writeMatroskaPayload(t, fake)

	got, err := verifyAndRepair(dir, []string{fake}, nil)
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

func TestVerifyAndRepair_ProgressCallback(t *testing.T) {
	dir := t.TempDir()
	par2a := filepath.Join(dir, "set.par2")
	par2b := filepath.Join(dir, "set.vol.par2")
	data := filepath.Join(dir, "set.rar")
	for _, p := range []string{par2a, par2b} {
		if err := os.WriteFile(p, []byte("PAR2\x00PKT incomplete"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(data, []byte("rar-payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	var calls [][2]int64
	_, err := verifyAndRepair(dir, []string{par2a, par2b, data}, func(done, total int64) {
		calls = append(calls, [2]int64{done, total})
	})
	if err == nil {
		t.Fatal("want parse error on junk PAR2")
	}
	if len(calls) < 2 {
		t.Fatalf("want progress after each par2 ReadFile, got %v", calls)
	}
	wantTotal := int64(2 + 1 + 1 + 1) // par2 + data + verify + rewrite bound
	for i, c := range calls {
		if c[1] != wantTotal {
			t.Fatalf("call %d total=%d want %d", i, c[1], wantTotal)
		}
		if c[0] > c[1] {
			t.Fatalf("done > total: %v", c)
		}
		if i > 0 && c[0] <= calls[i-1][0] {
			t.Fatalf("done not increasing: %v", calls)
		}
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

// TestNormalizeObfuscatedPar2Names_DedupesDuplicatePaths is the Ancient Aliens
// regression guard: assembleFile can list the same obfuscated .par2 path many
// times; after the first rename, later copies must not spam "skipping non-PAR2".
func TestNormalizeObfuscatedPar2Names_DedupesDuplicatePaths(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "show.vol-01.par2")
	writeMatroskaPayload(t, fake)

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	got := normalizeObfuscatedPar2Names([]string{fake, fake, fake})
	if len(got) != 1 {
		t.Fatalf("want 1 deduped path, got %v", got)
	}
	if !strings.HasSuffix(got[0], ".mkv") {
		t.Fatalf("want .mkv, got %q", got[0])
	}
	out := buf.String()
	if strings.Count(out, "renamed obfuscated payload") != 1 {
		t.Fatalf("want exactly one rename log, got:\n%s", out)
	}
	if strings.Contains(out, "skipping non-PAR2") {
		t.Fatalf("duplicate paths must not spam skipping logs:\n%s", out)
	}
}

func TestNormalizeObfuscatedPar2Names_AdoptsAlreadyRenamedTarget(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "show.vol-01.par2")
	newPath := filepath.Join(dir, "show.vol-01.mkv")
	writeMatroskaPayload(t, newPath)

	// Source gone, target present — second appearance after a prior rename.
	got := normalizeObfuscatedPar2Names([]string{old})
	if len(got) != 1 || got[0] != newPath {
		t.Fatalf("want adopt %q, got %v", newPath, got)
	}
}

func TestNormalizeObfuscatedPar2Names_DropsNonexistentPath(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "missing.bin")
	got := normalizeObfuscatedPar2Names([]string{gone})
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}

func TestNormalizeObfuscatedPar2Names_RefusesClobberExistingTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "show.vol-01.par2")
	dst := filepath.Join(dir, "show.vol-01.mkv")
	writeMatroskaPayload(t, src)
	if err := os.WriteFile(dst, []byte("already-there"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := normalizeObfuscatedPar2Names([]string{src})
	if len(got) != 1 || got[0] != src {
		t.Fatalf("want keep source when target exists, got %v", got)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "already-there" {
		t.Fatalf("target must not be clobbered: %v %q", err, data)
	}
}

func TestNormalizeObfuscatedPar2Names_PreservesFirstAppearanceOrder(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.vol.par2")
	if err := os.WriteFile(a, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMatroskaPayload(t, b)

	got := normalizeObfuscatedPar2Names([]string{a, b, a})
	if len(got) != 2 {
		t.Fatalf("want [a, renamed-b], got %v", got)
	}
	if got[0] != a {
		t.Fatalf("first path should stay first: %v", got)
	}
	if !strings.HasSuffix(got[1], ".mkv") {
		t.Fatalf("second should be renamed mkv: %v", got)
	}
}
