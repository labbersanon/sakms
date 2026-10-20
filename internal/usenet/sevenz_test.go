package usenet

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ulikunitz/xz/lzma"
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

func TestParse7zPackInfoHuge(t *testing.T) {
	b := []byte{0x06}
	b = append7zUint64(b, 23951834384)
	b = append(b, 0x01, 0x09)
	b = append7zUint64(b, 23951834384)
	b = append(b, 0x00, 0x00)
	si, n, ok := parse7zStreams(b, 0)
	if !ok || n != len(b) || len(si.packSizes) != 1 || si.packSizes[0] != 23951834384 {
		t.Fatalf("si=%+v n=%d ok=%v", si, n, ok)
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

func TestArchiveFileCRCs_7zEncodedHeader(t *testing.T) {
	start, tail, crc, member := makeSevenZSplit(t)
	st, ok := Parse7zStart(start)
	if !ok {
		t.Fatal("start header")
	}
	plain := tail[uint64(len(tail))-st.Size:]
	if plain[0] != 0x01 {
		if got := ArchiveFileCRCsFrom7zParts(start, tail); len(got) != 1 || got[0].CRC != crc {
			t.Fatalf("fixture already encoded: %+v", got)
		}
		return
	}
	props, packed := lzmaPack(t, plain)
	enc := wrapEncodedHeader(t, packed, uint64(len(plain)), props, 23951834384)
	fake := make7zStart(t, 23951834384, uint64(len(enc)), enc)
	junk := bytes.Repeat([]byte{0xAA}, 64)
	got := ArchiveFileCRCsFrom7zParts(fake, append(append(junk, packed...), enc...))
	if len(got) != 1 || got[0].CRC != crc || !strings.Contains(got[0].Name, member) {
		t.Fatalf("encoded crc = %+v, want %s %s", got, crc, member)
	}
}

func lzmaPack(t *testing.T, raw []byte) (props, packed []byte) {
	t.Helper()
	var buf bytes.Buffer
	w, err := lzma.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if len(data) <= lzma.HeaderLen {
		t.Fatalf("lzma packed %d", len(data))
	}
	return data[:5], data[lzma.HeaderLen:]
}

func wrapEncodedHeader(t *testing.T, packed []byte, unpack uint64, props []byte, packPos uint64) []byte {
	t.Helper()
	if len(props) < 5 {
		t.Fatal("lzma props")
	}
	out := []byte{0x17, 0x06}
	out = append7zUint64(out, packPos)
	out = append(out, 0x01, 0x09)
	out = append7zUint64(out, uint64(len(packed)))
	out = append(out, 0x00, 0x07, 0x0B, 0x01, 0x00, 0x01, 0x23, 0x03, 0x01, 0x01, 0x05)
	out = append(out, props[:5]...)
	out = append(out, 0x0C)
	out = append7zUint64(out, unpack)
	out = append(out, 0x00, 0x00)
	return out
}

func make7zStart(t *testing.T, off, size uint64, hdr []byte) []byte {
	t.Helper()
	start := make([]byte, 32)
	copy(start, sevenZMagic)
	start[6] = 0
	start[7] = 4
	binary.LittleEndian.PutUint64(start[12:20], off)
	binary.LittleEndian.PutUint64(start[20:28], size)
	binary.LittleEndian.PutUint32(start[28:32], crc32.ChecksumIEEE(hdr))
	binary.LittleEndian.PutUint32(start[8:12], crc32.ChecksumIEEE(start[12:32]))
	return start
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
	crc, listed := sltInnerCRC(t, vols[0])
	if crc == "" || !strings.Contains(listed, member) {
		t.Fatalf("7z l oracle crc=%q name=%q", crc, listed)
	}
	head := first
	if len(head) > 256 {
		head = head[:256]
	}
	return head, last, crc, member
}

func sltInnerCRC(t *testing.T, vol001 string) (crc, name string) {
	t.Helper()
	cmd := exec.Command("7z", "l", "-slt", "-bd", vol001)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("7z l: %v (%s)", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "Path = "):
			name = strings.TrimPrefix(line, "Path = ")
		case strings.HasPrefix(line, "CRC = "):
			crc = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(line, "CRC = ")))
		}
	}
	return crc, name
}

func append7zUint64(dst []byte, value uint64) []byte {
	first := byte(0)
	mask := byte(0x80)
	var i int
	for i = 0; i < 8; i++ {
		if value < uint64(1)<<(7*(i+1)) {
			first |= byte(value >> (8 * i))
			break
		}
		first |= mask
		mask >>= 1
	}
	dst = append(dst, first)
	for n := 0; n < i; n++ {
		dst = append(dst, byte(value))
		value >>= 8
	}
	return dst
}
