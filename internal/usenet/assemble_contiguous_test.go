package usenet

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestYencBeginOffsets_FillStrideGaps proves write positions use yEnc begin
// offsets (DirectWrite-style) and Truncate uses =ybegin FileSize, leaving NUL
// windows in the stride gaps — not a packed decoded-length layout.
func TestYencBeginOffsets_FillStrideGaps(t *testing.T) {
	// Three segments whose yEnc begins advance by 100 but decoded lengths are
	// 90/95/88; FileSize is 300 (3×stride). DirectWrite layout keeps the holes.
	type seg struct {
		yencBegin int64
		data      []byte
	}
	const fileSize int64 = 300
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

	var maxEnd, yencSize int64
	yencSize = fileSize
	for _, s := range segs {
		if _, err := f.WriteAt(s.data, s.yencBegin); err != nil {
			t.Fatal(err)
		}
		if end := s.yencBegin + int64(len(s.data)); end > maxEnd {
			maxEnd = end
		}
	}
	finalSize := maxEnd
	if yencSize > finalSize {
		finalSize = yencSize
	}
	if err := f.Truncate(finalSize); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(got)) != fileSize {
		t.Fatalf("size=%d, want FileSize %d (not packed %d)", len(got), fileSize, 90+95+88)
	}
	expect := make([]byte, fileSize)
	copy(expect[0:], segs[0].data)
	copy(expect[100:], segs[1].data)
	copy(expect[200:], segs[2].data)
	if !bytes.Equal(got, expect) {
		t.Fatalf("yEnc-begin assemble mismatch: len got=%d want=%d", len(got), len(expect))
	}
	if bytes.IndexByte(got[90:100], 0) < 0 {
		t.Fatal("missing NUL window between part 1 and 2")
	}
	if bytes.IndexByte(got[195:200], 0) < 0 {
		t.Fatal("missing NUL window between part 2 and 3")
	}
	if bytes.IndexByte(got[288:300], 0) < 0 {
		t.Fatal("missing trailing NUL pad after last decoded byte up to FileSize")
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

// makeStridedPayload is like makePayload but yEnc offsets advance by stride
// while each part's decoded length is shorter, matching stride posters.
func makeStridedPayload(t *testing.T, decoded []int, stride int, fileSize int64) testPayload {
	t.Helper()
	p := testPayload{filename: "release.bin"}
	p.full = make([]byte, fileSize)
	var segs strings.Builder
	for i, n := range decoded {
		msgID := fmt.Sprintf("seg%d@test", i+1)
		off := int64(i * stride)
		chunk := make([]byte, n)
		for j := range chunk {
			chunk[j] = byte((i+1)*17 + j%251)
		}
		copy(p.full[off:], chunk)
		p.msgIDs = append(p.msgIDs, msgID)
		p.parts = append(p.parts, yencPart(t, p.filename, fileSize, i+1, len(decoded), off, chunk))
		fmt.Fprintf(&segs, `      <segment bytes="%d" number="%d">%s</segment>`+"\n", n, i+1, msgID)
	}
	p.nzbXML = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p@example.com" date="0" subject="release.bin (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
` + segs.String() + `    </segments>
  </file>
</nzb>`
	return p
}

func TestAssembleFile_YencStrideGaps(t *testing.T) {
	const stride = 100
	const fileSize int64 = 300
	decoded := []int{90, 95, 88}
	p := makeStridedPayload(t, decoded, stride, fileSize)
	srv := newFakeNNTP(t)
	srv.serveAll(p)
	nzbHTTP := nzbServer(t, p)
	staging := t.TempDir()
	m := New(Config{
		Servers:       []ServerConfig{srv.cfgWith(2)},
		StagingDir:    staging,
		HTTPClient:    nzbHTTP.Client(),
		SegmentResume: true,
	})
	gid, err := m.AddNZB(context.Background(), nzbHTTP.URL, "Yenc Stride")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	d := waitTerminal(t, m, gid)
	if d.Status != "complete" {
		t.Fatalf("status=%q err=%q", d.Status, d.ErrorMessage)
	}
	got, err := os.ReadFile(filepath.Join(staging, gid, p.filename))
	if err != nil {
		t.Fatalf("read assembled: %v", err)
	}
	if int64(len(got)) != fileSize {
		t.Fatalf("assembled size=%d, want FileSize %d (packed would be %d)", len(got), fileSize, 90+95+88)
	}
	if !bytes.Equal(got, p.full) {
		t.Fatalf("assembled mismatch vs yEnc-begin layout")
	}
	if bytes.IndexByte(got[90:100], 0) < 0 || bytes.IndexByte(got[195:200], 0) < 0 {
		t.Fatal("expected NUL stride windows in assembled file")
	}
}
