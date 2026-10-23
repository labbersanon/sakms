package usenet

import (
	"fmt"
	"testing"
)

func TestAssessNZB_SequentialRAR(t *testing.T) {
	nzb := &NZB{Files: []NZBFile{
		subjectFile("Movie.2021.part001.rar", 2),
		subjectFile("Movie.2021.part002.rar", 2),
		subjectFile("Movie.2021.vol001+02.par2", 1),
	}}
	a := AssessNZB(nzb)
	if !a.OK {
		t.Fatalf("want streamable: %+v", a)
	}
	if len(a.Plan.Play) != 2 || a.Plan.Play[0].Name != "Movie.2021.part001.rar" {
		t.Fatalf("play=%+v", a.Plan.Play)
	}
	if len(a.Plan.Par2) != 1 {
		t.Fatalf("par2=%d", len(a.Plan.Par2))
	}
}

func TestAssessNZB_RawMKV(t *testing.T) {
	a := AssessNZB(&NZB{Files: []NZBFile{subjectFile("Movie.2021.mkv", 3)}})
	if !a.OK {
		t.Fatalf("mkv: %+v", a)
	}
}

func TestAssessNZB_Rejects7zAndGaps(t *testing.T) {
	seven := AssessNZB(&NZB{Files: []NZBFile{subjectFile("Movie.2021.7z", 4)}})
	if seven.OK || seven.Reason != "7z archive" {
		t.Fatalf("7z=%+v", seven)
	}
	gappy := subjectFile("Movie.2021.mkv", 2)
	gappy.Segs[1].Number = 3
	gap := AssessNZB(&NZB{Files: []NZBFile{gappy}})
	if gap.OK || gap.Reason != "gapped segments" {
		t.Fatalf("gap=%+v", gap)
	}
}

func TestAssessNZB_Par2Only(t *testing.T) {
	a := AssessNZB(&NZB{Files: []NZBFile{subjectFile("Movie.2021.vol001+02.par2", 1)}})
	if a.OK {
		t.Fatalf("par2-only must reject: %+v", a)
	}
}

func subjectFile(name string, n int) NZBFile {
	segs := make([]NZBSegment, n)
	for i := 0; i < n; i++ {
		segs[i] = NZBSegment{Number: i + 1, Bytes: 1, MsgID: fmt.Sprintf("<%d.%s@x>", i+1, name)}
	}
	return NZBFile{Subject: `"` + name + `" yEnc (1/1)`, Segs: segs}
}
