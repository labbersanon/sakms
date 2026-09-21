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

func TestUniqueOutputName_ClaimsBaseThenParts(t *testing.T) {
	used := map[string]struct{}{}
	if got := uniqueOutputName("hash.par2", used); got != "hash.par2" {
		t.Fatalf("first: got %q", got)
	}
	if got := uniqueOutputName("hash.par2", used); got != "hash.part002.par2" {
		t.Fatalf("second: got %q", got)
	}
	if got := uniqueOutputName("hash.par2", used); got != "hash.part003.par2" {
		t.Fatalf("third: got %q", got)
	}
	if got := uniqueOutputName("other.bin", used); got != "other.bin" {
		t.Fatalf("distinct: got %q", got)
	}
}

func TestPriorFile_RequiresFirstMsgMatch(t *testing.T) {
	dir := t.TempDir()
	tr := loadResumeTracker(dir, "gid", false)
	if err := tr.markSegment("a.bin", "msg-a@x", 1, 0, 100, 100); err != nil {
		t.Fatal(err)
	}
	name, _, done := tr.priorFile("msg-missing@x")
	if name != "" || done != 0 {
		t.Fatalf("want no match for missing msg, got name=%q done=%d", name, done)
	}
	name, _, done = tr.priorFile("msg-a@x")
	if name != "a.bin" || done != 1 {
		t.Fatalf("want a.bin/1, got %q/%d", name, done)
	}
}

// TestDownloadAll_UniquifiesCollidingYencNames is the Love Is Blind / RiPER
// obfuscation regression: every NZB file shares one yEnc =ybegin name. Without
// uniquify, parts smash one path and PAR2 reports thousands of missing slices.
func TestDownloadAll_UniquifiesCollidingYencNames(t *testing.T) {
	const sharedName = "316cef87b8ef42dc840681b2b2cf2c37.par2"
	partSize := 512
	segCount := 2

	type part struct {
		label string
		full  []byte
		ids   []string
		parts [][]byte
	}
	mk := func(label string, fill byte) part {
		p := part{label: label, full: make([]byte, segCount*partSize)}
		for i := range p.full {
			p.full[i] = fill
		}
		for i := 0; i < segCount; i++ {
			id := fmt.Sprintf("%s-seg%d@test", label, i+1)
			off := int64(i * partSize)
			p.ids = append(p.ids, id)
			p.parts = append(p.parts, yencPart(t, sharedName, int64(len(p.full)), i+1, segCount, off, p.full[off:off+int64(partSize)]))
		}
		return p
	}
	a := mk("a", 0x11)
	b := mk("b", 0x22)

	var segsA, segsB strings.Builder
	for i := 0; i < segCount; i++ {
		fmt.Fprintf(&segsA, `      <segment bytes="%d" number="%d">%s</segment>`+"\n", partSize, i+1, a.ids[i])
		fmt.Fprintf(&segsB, `      <segment bytes="%d" number="%d">%s</segment>`+"\n", partSize, i+1, b.ids[i])
	}
	nzbXML := `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p@example.com" date="0" subject="[1/2] &quot;Show.S01E01.part01.mkv&quot; yEnc (1/2)">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
` + segsA.String() + `    </segments>
  </file>
  <file poster="p@example.com" date="0" subject="[2/2] &quot;Show.S01E01.part02.mkv&quot; yEnc (1/2)">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
` + segsB.String() + `    </segments>
  </file>
</nzb>`

	srv := newFakeNNTP(t)
	for i := range a.ids {
		srv.add(a.ids[i], a.parts[i])
		srv.add(b.ids[i], b.parts[i])
	}
	nzbHTTP := nzbServer(t, testPayload{nzbXML: nzbXML})
	staging := t.TempDir()
	m := New(Config{
		Servers:       []ServerConfig{srv.cfgWith(2)},
		StagingDir:    staging,
		HTTPClient:    nzbHTTP.Client(),
		SegmentResume: true,
	})

	gid, err := m.AddNZB(context.Background(), nzbHTTP.URL, "Colliding Yenc")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	final := waitTerminal(t, m, gid)
	if final.Status != "complete" {
		t.Fatalf("status=%q err=%q", final.Status, final.ErrorMessage)
	}

	dir := filepath.Join(staging, gid)
	pathA := filepath.Join(dir, "Show.S01E01.part01.mkv")
	pathB := filepath.Join(dir, "Show.S01E01.part02.mkv")
	gotA, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("read part1: %v (dir=%v)", err, listNames(t, dir))
	}
	gotB, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatalf("read part2: %v (dir=%v)", err, listNames(t, dir))
	}
	if !bytes.Equal(gotA, a.full) {
		t.Fatalf("part1 bytes mismatch")
	}
	if !bytes.Equal(gotB, b.full) {
		t.Fatalf("part2 bytes mismatch")
	}

	// Resume keys must stay separate — no merged Done map with duplicate n values.
	tr := loadResumeTracker(dir, gid, false)
	if len(tr.snap.Files) != 2 {
		t.Fatalf("resume files = %d, want 2: %#v", len(tr.snap.Files), tr.snap.Files)
	}
	for name, rf := range tr.snap.Files {
		nums := map[int]int{}
		for _, seg := range rf.Done {
			nums[seg.Number]++
		}
		for n, c := range nums {
			if c != 1 {
				t.Fatalf("%s: segment number %d appears %d times", name, n, c)
			}
		}
		if len(rf.Done) != segCount {
			t.Fatalf("%s: done=%d want %d", name, len(rf.Done), segCount)
		}
	}
}

// When subject quotes the same hash.par2 for every file, uniqueOutputName still
// separates staging paths (safety net when subject offers no distinct part name).
func TestDownloadAll_UniquifiesWhenSubjectAlsoCollides(t *testing.T) {
	const sharedName = "316cef87b8ef42dc840681b2b2cf2c37.par2"
	partSize := 256
	segCount := 1

	mkBody := func(label string, fill byte) (id string, body []byte, full []byte) {
		full = bytes.Repeat([]byte{fill}, partSize)
		id = label + "-seg1@test"
		body = yencPart(t, sharedName, int64(len(full)), 1, 1, 0, full)
		return id, body, full
	}
	idA, bodyA, fullA := mkBody("a", 0x33)
	idB, bodyB, fullB := mkBody("b", 0x44)

	nzbXML := `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p@example.com" date="0" subject="[1/2] &quot;` + sharedName + `&quot; yEnc (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
      <segment bytes="` + fmt.Sprint(partSize) + `" number="1">` + idA + `</segment>
    </segments>
  </file>
  <file poster="p@example.com" date="0" subject="[2/2] &quot;` + sharedName + `&quot; yEnc (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
      <segment bytes="` + fmt.Sprint(partSize) + `" number="1">` + idB + `</segment>
    </segments>
  </file>
</nzb>`

	srv := newFakeNNTP(t)
	srv.add(idA, bodyA)
	srv.add(idB, bodyB)
	nzbHTTP := nzbServer(t, testPayload{nzbXML: nzbXML})
	staging := t.TempDir()
	m := New(Config{
		Servers:    []ServerConfig{srv.cfgWith(2)},
		StagingDir: staging,
		HTTPClient: nzbHTTP.Client(),
	})
	gid, err := m.AddNZB(context.Background(), nzbHTTP.URL, "Subject Collides Too")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	final := waitTerminal(t, m, gid)
	if final.Status != "complete" {
		t.Fatalf("status=%q err=%q", final.Status, final.ErrorMessage)
	}
	dir := filepath.Join(staging, gid)
	gotA, err := os.ReadFile(filepath.Join(dir, sharedName))
	if err != nil {
		t.Fatalf("part1: %v dir=%v", err, listNames(t, dir))
	}
	gotB, err := os.ReadFile(filepath.Join(dir, "316cef87b8ef42dc840681b2b2cf2c37.part002.par2"))
	if err != nil {
		t.Fatalf("part2: %v dir=%v", err, listNames(t, dir))
	}
	if !bytes.Equal(gotA, fullA) || !bytes.Equal(gotB, fullB) {
		t.Fatal("assembled bytes mismatch")
	}
	_ = segCount
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}
