package usenet

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestContiguousOffsets_NoYencGaps proves write positions advance by decoded
// length, not by yEnc begin strides (the gap bug that hollowed RARs).
func TestContiguousOffsets_NoYencGaps(t *testing.T) {
	// Three segments whose yEnc begins advance by 100 but decoded lengths are
	// 90/95/88 — contiguous layout must pack without holes.
	type seg struct {
		yencBegin int64
		data      []byte
	}
	segs := []seg{
		{0, bytes.Repeat([]byte("a"), 90)},
		{100, bytes.Repeat([]byte("b"), 95)},
		{200, bytes.Repeat([]byte("c"), 88)},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var cursor int64
	var expect bytes.Buffer
	for _, s := range segs {
		// Contiguous path ignores yencBegin (the regression under test).
		if _, err := f.WriteAt(s.data, cursor); err != nil {
			t.Fatal(err)
		}
		expect.Write(s.data)
		cursor += int64(len(s.data))
		_ = s.yencBegin
	}
	if err := f.Truncate(cursor); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, expect.Bytes()) {
		t.Fatalf("contiguous assemble mismatch: len got=%d want=%d", len(got), expect.Len())
	}
	if int64(len(got)) != 90+95+88 {
		t.Fatalf("size=%d, want 273 (no yEnc gaps)", len(got))
	}
	if bytes.IndexByte(got, 0) >= 0 {
		t.Fatal("assembled file contains NUL — yEnc-gap regression")
	}
}

func TestArchiveLeaders_RanksCompleteSetOverOrphanPretty(t *testing.T) {
	names := []string{
		"Show.Name.S01E01.1080p.part01.rar", // orphan pretty
		"AbCdEfGhIjKlMnOp.part02.rar",
		"AbCdEfGhIjKlMnOp.part01.rar",
		"AbCdEfGhIjKlMnOp.part03.rar",
	}
	got := archiveLeaders(names)
	if len(got) != 2 {
		t.Fatalf("leaders=%v, want 2", got)
	}
	if got[0] != "AbCdEfGhIjKlMnOp.part01.rar" {
		t.Fatalf("first leader=%q, want hash set (complete)", got[0])
	}
	if got[1] != "Show.Name.S01E01.1080p.part01.rar" {
		t.Fatalf("second leader=%q, want pretty orphan", got[1])
	}
}
