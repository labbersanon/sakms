package usenet

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	par2lib "github.com/go-newsgroups/par2"
)

func TestClassifyPeek_MatroskaMagic(t *testing.T) {
	data := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, "body"...)
	if got := classifyPeek(data, "hash.par2", `"hash.par2" yEnc`, 0); got != evidenceVideo {
		t.Fatalf("got %v want evidenceVideo", got)
	}
}

func TestClassifyPeek_JPEGIsJunk(t *testing.T) {
	data := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	if got := classifyPeek(data, "photo.jpg", `"photo.jpg" yEnc`, 0); got != evidenceJunk {
		t.Fatalf("got %v want evidenceJunk", got)
	}
}

func TestClassifyPeek_NamedMKVWithoutMagicStillCounts(t *testing.T) {
	data := []byte{1, 2, 3, 4, 5}
	if got := classifyPeek(data, "release.mkv", `release.mkv (1/1)`, 0); got != evidenceVideo {
		t.Fatalf("got %v want evidenceVideo (claimed video ext)", got)
	}
}

func TestClassifyPeek_ZipWithMKVMember(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("Show.S01E01.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x00}); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if got := classifyPeek(buf.Bytes(), "hash.rar", `"hash.rar" yEnc`, 0); got != evidenceVideo {
		t.Fatalf("got %v want evidenceVideo from zip member", got)
	}
}

func TestClassifyPeek_PAR2FileDescVideo(t *testing.T) {
	blob := par2WithNames(t, "movie.mkv")
	if got := classifyPeek(blob, "set.par2", `"set.par2" yEnc`, 0); got != evidenceVideo {
		t.Fatalf("got %v want evidenceVideo from FileDesc", got)
	}
}

func TestClassifyPeek_PAR2FileDescJunkOnly(t *testing.T) {
	blob := par2WithNames(t, "release.nfo", "folder.jpg")
	if got := classifyPeek(blob, "set.par2", `"set.par2" yEnc`, 0); got != evidenceJunk {
		t.Fatalf("got %v want evidenceJunk from FileDesc", got)
	}
}

func TestPrecheckVideo_JPEGAborts(t *testing.T) {
	p := makeNamedPayload(t, "photo.jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, 1, 64)
	p.nzbXML = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p@example.com" date="0" subject="&quot;photo.jpg&quot; yEnc (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
      <segment bytes="64" number="1">` + p.msgIDs[0] + `</segment>
    </segments>
  </file>
</nzb>`
	srv := newFakeNNTP(t)
	srv.serveAll(p)
	staging := t.TempDir()
	nzbHTTP := nzbServer(t, p)
	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: staging, HTTPClient: nzbHTTP.Client()})
	gid, err := m.AddNZB(context.Background(), nzbHTTP.URL, "JPEG Dump")
	if !errors.Is(err, ErrNoVideoUnpacked) {
		t.Fatalf("err=%v want ErrNoVideoUnpacked", err)
	}
	if gid != "" {
		t.Fatalf("gid=%q want empty", gid)
	}
	if ents, _ := os.ReadDir(staging); len(ents) != 0 {
		t.Fatalf("staging not empty: %v", ents)
	}
}

func TestPrecheckVideo_ObfuscatedPar2MatroskaProceeds(t *testing.T) {
	prefix := append([]byte{0x1A, 0x45, 0xDF, 0xA3}, bytes.Repeat([]byte{0x11}, 60)...)
	p := makeNamedPayload(t, "316cef87b8ef42dc840681b2b2cf2c37.par2", prefix, 1, len(prefix))
	p.nzbXML = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p@example.com" date="0" subject="&quot;316cef87b8ef42dc840681b2b2cf2c37.par2&quot; yEnc (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
      <segment bytes="64" number="1">` + p.msgIDs[0] + `</segment>
    </segments>
  </file>
</nzb>`
	srv := newFakeNNTP(t)
	srv.serveAll(p)
	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: t.TempDir()})
	_, err := m.precheckNZB(context.Background(), parsePayloadNZB(t, p), nil, "")
	if err != nil {
		t.Fatalf("precheckNZB: %v", err)
	}
}

func TestPrecheckVideo_ResumeSkipDoesNotPeek(t *testing.T) {
	p := makeNamedPayload(t, "photo.jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, 2, 64)
	srv := newFakeNNTP(t)
	srv.serveAll(p)
	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: t.TempDir()})
	skip := map[string]bool{p.msgIDs[0]: true}
	_, err := m.precheckNZB(context.Background(), parsePayloadNZB(t, p), skip, "")
	if err != nil {
		t.Fatalf("resume skip should skip video peek: %v", err)
	}
}

func TestIsPrecheckReject(t *testing.T) {
	if !IsPrecheckReject(ErrArticlesUnavailable) {
		t.Fatal("430 must be a precheck reject")
	}
	if !IsPrecheckReject(fmt.Errorf("%w: peek", ErrNoVideoUnpacked)) {
		t.Fatal("no-video must be a precheck reject")
	}
	if IsPrecheckReject(ErrTransport) {
		t.Fatal("transport must not be a precheck reject")
	}
}

func makeNamedPayload(t *testing.T, filename string, full []byte, segCount, partSize int) testPayload {
	t.Helper()
	if len(full) < segCount*partSize {
		padded := make([]byte, segCount*partSize)
		copy(padded, full)
		full = padded
	}
	p := testPayload{filename: filename, full: full[:segCount*partSize]}
	var segs strings.Builder
	for i := 0; i < segCount; i++ {
		msgID := fmt.Sprintf("seg%d@test", i+1)
		off := int64(i * partSize)
		p.msgIDs = append(p.msgIDs, msgID)
		p.parts = append(p.parts, yencPart(t, filename, int64(len(p.full)), i+1, segCount, off, p.full[off:off+int64(partSize)]))
		fmt.Fprintf(&segs, `      <segment bytes="%d" number="%d">%s</segment>`+"\n", partSize, i+1, msgID)
	}
	p.nzbXML = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p@example.com" date="0" subject="` + filename + ` (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
` + segs.String() + `    </segments>
  </file>
</nzb>`
	return p
}

func par2WithNames(t *testing.T, names ...string) []byte {
	t.Helper()
	set := [16]byte{9, 8, 7}
	ids := make([][16]byte, len(names))
	var blob []byte
	for i, name := range names {
		ids[i] = md5.Sum([]byte(name))
	}
	blob = append(blob, par2Pkt(set, par2TypeMain, par2MainBody(4, ids...))...)
	for i, name := range names {
		blob = append(blob, par2Pkt(set, par2TypeFileDesc, par2FileDescBody(ids[i], name, 10))...)
	}
	if _, err := par2lib.Parse(blob); err != nil {
		t.Fatalf("fixture Parse: %v", err)
	}
	return blob
}

var (
	par2TypeMain     = []byte("PAR 2.0\x00Main\x00\x00\x00\x00")
	par2TypeFileDesc = []byte("PAR 2.0\x00FileDesc")
)

func par2Pkt(setID [16]byte, ptype, body []byte) []byte {
	for len(body)%4 != 0 {
		body = append(body, 0)
	}
	buf := make([]byte, 64+len(body))
	copy(buf[0:8], []byte("PAR2\x00PKT"))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(len(buf)))
	copy(buf[32:48], setID[:])
	copy(buf[48:64], ptype)
	copy(buf[64:], body)
	sum := md5.Sum(buf[32:])
	copy(buf[16:32], sum[:])
	return buf
}

func par2MainBody(sliceSize uint64, ids ...[16]byte) []byte {
	b := make([]byte, 12)
	binary.LittleEndian.PutUint64(b[0:8], sliceSize)
	binary.LittleEndian.PutUint32(b[8:12], uint32(len(ids)))
	for _, id := range ids {
		b = append(b, id[:]...)
	}
	return b
}

func par2FileDescBody(id [16]byte, name string, length uint64) []byte {
	b := make([]byte, 56)
	copy(b[0:16], id[:])
	for i := 16; i < 48; i++ {
		b[i] = byte(i)
	}
	binary.LittleEndian.PutUint64(b[48:56], length)
	return append(b, name...)
}
