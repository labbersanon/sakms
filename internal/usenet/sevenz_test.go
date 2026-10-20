package usenet

import (
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSevenZUint64RoundTrip(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 255, 256, 0xFFFF, 26635170624} {
		enc := append7zUint64(nil, v)
		got, n, ok := read7zUint64(enc, 0)
		if !ok || got != v || n != len(enc) {
			t.Fatalf("v=%d enc=%x got=%d n=%d ok=%v", v, enc, got, n, ok)
		}
	}
}

func TestEncodedPackLenAndPackPosPatch(t *testing.T) {
	hdr := []byte{0x17, 0x06, 0x00, 0x01, 0x09, 0x64, 0x00}
	packLen, ok := encodedPackLen(hdr)
	if !ok || packLen != 0x64 {
		t.Fatalf("packLen=%d ok=%v", packLen, ok)
	}
	wide := append([]byte{0x17, 0x06}, append7zUint64(nil, 26635170624)...)
	wide = append(wide, 0x01, 0x09, 0x64, 0x00)
	patched := patchEncodedPackPos(wide)
	if patched == nil || patched[0] != 0x17 || patched[1] != 0x06 || patched[2] != 0 {
		t.Fatalf("patched=%x", patched)
	}
	packLen, ok = encodedPackLen(patched)
	if !ok || packLen != 0x64 {
		t.Fatalf("patched packLen=%d ok=%v", packLen, ok)
	}
}

func TestParse7zStart_RejectsGarbage(t *testing.T) {
	if _, ok := Parse7zStart([]byte("Rar!\x1a\x07\x00")); ok {
		t.Fatal("rar prefix must not parse as 7z")
	}
	if _, ok := Parse7zStart(append(append([]byte{}, sevenZMagic...), make([]byte, 26)...)); ok {
		t.Fatal("zero size/offset must be rejected")
	}
}

func TestArchiveFileCRCs_7zSplitTail(t *testing.T) {
	start, tail, crc, member := makeSevenZSplit(t)
	st, ok := Parse7zStart(start)
	if !ok {
		t.Fatal("start header")
	}
	if st.Offset == 0 || st.Size == 0 {
		t.Fatalf("start = %+v", st)
	}
	if got := ArchiveFileCRCs(start); len(got) != 0 {
		t.Fatalf("first-volume prefix must not list CRCs, got %+v", got)
	}
	got := ArchiveFileCRCsFrom7zParts(start, tail)
	if len(got) != 1 || got[0].CRC != crc || !strings.Contains(got[0].Name, member) {
		t.Fatalf("tail crc = %+v, want %s %s", got, crc, member)
	}
}

func makeSevenZSplit(t *testing.T) (start, tail []byte, crc, member string) {
	t.Helper()
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not on PATH")
	}
	dir := t.TempDir()
	member = "Some.Movie.2021.1080p.BluRay.x264-GRP.mkv"
	payload := make([]byte, 200_000)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, member), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("7z", "a", "-bd", "-v64k", filepath.Join(dir, "split.7z"), filepath.Join(dir, member))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("7z a: %v (%s)", err, out)
	}
	vols, err := filepath.Glob(filepath.Join(dir, "split.7z.*"))
	if err != nil || len(vols) < 2 {
		t.Fatalf("volumes = %v err=%v", vols, err)
	}
	first, err := os.ReadFile(vols[0])
	if err != nil {
		t.Fatal(err)
	}
	last, err := os.ReadFile(vols[len(vols)-1])
	if err != nil {
		t.Fatal(err)
	}
	st, ok := Parse7zStart(first)
	if !ok {
		t.Fatalf("parse %s", vols[0])
	}
	if uint64(len(last)) < st.Size {
		t.Fatalf("last volume %d < header %d", len(last), st.Size)
	}
	listed := ArchiveFileCRCsFrom7zParts(first[:32], last)
	if len(listed) == 0 || listed[0].CRC == "" {
		t.Fatalf("fixture list = %+v (need 7z l)", listed)
	}
	head := first
	if len(head) > 256 {
		head = head[:256]
	}
	return head, last[uint64(len(last))-st.Size:], listed[0].CRC, member
}
