package usenet

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func parsePayloadNZB(t *testing.T, p testPayload) *NZB {
	t.Helper()
	nzb, err := ParseNZB([]byte(p.nzbXML))
	if err != nil {
		t.Fatalf("ParseNZB: %v", err)
	}
	return nzb
}

// Claude 2026-09-22: sample-cap assertion retired with full payload STAT.
// Reason: healthy NZBs now STAT every payload article, not ≤ SampleCap.
// func TestPrecheck_HealthyNZB_SamplesNotFull(t *testing.T) {
// 	p := makePayload(t, 20, 512)
// 	srv := newFakeNNTP(t)
// 	srv.serveAll(p)
// 	nzbHTTP := nzbServer(t, p)
// 	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: t.TempDir(), HTTPClient: nzbHTTP.Client()})
// 	gid, err := m.AddNZB(context.Background(), nzbHTTP.URL, "Healthy")
// 	if err != nil {
// 		t.Fatalf("AddNZB: %v", err)
// 	}
// 	_ = waitTerminal(t, m, gid)
// 	n := srv.statCount.Load()
// 	if n == 0 {
// 		t.Fatal("expected STAT probes")
// 	}
// 	if n > int64(defaultPrecheckPolicy.SampleCap) {
// 		t.Fatalf("statCount=%d exceeds SampleCap %d", n, defaultPrecheckPolicy.SampleCap)
// 	}
// }

func TestPrecheck_FullCheck_OkWhenAllPresent(t *testing.T) {
	p := makePayload(t, 20, 512)
	srv := newFakeNNTP(t)
	srv.serveAll(p)
	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: t.TempDir()})
	res, err := m.precheckNZB(context.Background(), parsePayloadNZB(t, p), nil, "")
	if err != nil {
		t.Fatalf("precheckNZB: %v", err)
	}
	if res.Checked != 20 {
		t.Fatalf("Checked=%d want 20", res.Checked)
	}
	if res.PayloadSegments != 20 {
		t.Fatalf("PayloadSegments=%d want 20", res.PayloadSegments)
	}
	if n := srv.statCount.Load(); n != 20 {
		t.Fatalf("statCount=%d want 20 (full payload STAT)", n)
	}
}

func TestPrecheck_FullCheck_AbortsOnSingleMiss(t *testing.T) {
	p := makePayload(t, 20, 512)
	srv := newFakeNNTP(t)
	srv.serveOnly(p, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19)
	staging := t.TempDir()
	nzbHTTP := nzbServer(t, p)
	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: staging, HTTPClient: nzbHTTP.Client()})
	gid, err := m.AddNZB(context.Background(), nzbHTTP.URL, "OneHole")
	if !errors.Is(err, ErrArticlesUnavailable) {
		t.Fatalf("err=%v want ErrArticlesUnavailable", err)
	}
	if gid != "" {
		t.Fatalf("gid=%q want empty", gid)
	}
	if ents, _ := os.ReadDir(staging); len(ents) != 0 {
		t.Fatalf("staging not empty: %v", ents)
	}
}

func TestPrecheck_AllMissing_AbortsWithoutStaging(t *testing.T) {
	p := makePayload(t, 4, 512)
	srv := newFakeNNTP(t) // no articles
	nzbHTTP := nzbServer(t, p)
	staging := t.TempDir()
	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: staging, HTTPClient: nzbHTTP.Client()})
	gid, err := m.AddNZB(context.Background(), nzbHTTP.URL, "Dead")
	if !errors.Is(err, ErrArticlesUnavailable) {
		t.Fatalf("err=%v want ErrArticlesUnavailable", err)
	}
	if gid != "" {
		t.Fatalf("gid=%q want empty", gid)
	}
	if ents, _ := os.ReadDir(staging); len(ents) != 0 {
		t.Fatalf("staging not empty: %v", ents)
	}
}

func TestPrecheck_ZeroPools_NoOp(t *testing.T) {
	p := makePayload(t, 2, 512)
	nzbHTTP := nzbServer(t, p)
	m := New(Config{StagingDir: t.TempDir(), HTTPClient: nzbHTTP.Client()})
	gid, err := m.AddNZB(context.Background(), nzbHTTP.URL, "No Pool")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	if gid == "" {
		t.Fatal("expected gid")
	}
}

func TestPrecheck_SemaphoreCapacityTracksMaxConcurrentDownloads(t *testing.T) {
	m := New(Config{MaxConcurrentDownloads: 2})
	if got, want := cap(m.currentPrecheckSemaphore()), 2; got != want {
		t.Fatalf("New precheck sem cap=%d want %d", got, want)
	}
	if got, want := cap(m.currentSemaphore()), 2; got != want {
		t.Fatalf("New download sem cap=%d want %d", got, want)
	}
	m.SetMaxConcurrentDownloads(4)
	if got, want := cap(m.currentPrecheckSemaphore()), 4; got != want {
		t.Fatalf("after Set precheck sem cap=%d want %d", got, want)
	}
	if got, want := cap(m.currentSemaphore()), 4; got != want {
		t.Fatalf("after Set download sem cap=%d want %d", got, want)
	}
	m.SetMaxConcurrentDownloads(0)
	if got, want := cap(m.currentPrecheckSemaphore()), DefaultMaxConcurrentDownloads; got != want {
		t.Fatalf("clamp precheck sem cap=%d want %d", got, want)
	}
}

func TestPrecheck_OverlappingBlockAtCapacity(t *testing.T) {
	p := makePayload(t, 4, 512)
	srv := newFakeNNTP(t)
	srv.serveAll(p)
	m := New(Config{
		Servers:                []ServerConfig{srv.cfg()},
		StagingDir:             t.TempDir(),
		MaxConcurrentDownloads: 1,
	})
	sem := m.currentPrecheckSemaphore()
	sem <- struct{}{}

	done := make(chan error, 1)
	go func() {
		_, err := m.precheckNZB(context.Background(), parsePayloadNZB(t, p), nil, "")
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("precheck returned while semaphore full: %v", err)
	case <-time.After(80 * time.Millisecond):
	}

	<-sem
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("precheck after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("precheck did not proceed after semaphore release")
	}
}

func TestPrecheck_StatUnreliable_StillSTATsEveryArticle(t *testing.T) {
	p := makePayload(t, 8, 512)
	srv := newFakeNNTP(t)
	srv.serveAll(p)
	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: t.TempDir()})
	m.markStatUnreliable()
	res, err := m.precheckNZB(context.Background(), parsePayloadNZB(t, p), nil, "")
	if err != nil {
		t.Fatalf("precheckNZB: %v", err)
	}
	if res.Checked != 8 {
		t.Fatalf("Checked=%d want 8 (gate must still STAT after unreliable latch)", res.Checked)
	}
	if n := srv.statCount.Load(); n != 8 {
		t.Fatalf("statCount=%d want 8", n)
	}
}

func TestPrecheck_LyingStatDoesNotWaiveRealHole(t *testing.T) {
	p := makePayload(t, 4, 512)
	srv := newFakeNNTP(t)
	srv.serveOnly(p, 1, 2, 3)
	srv.lieStatIDs(p.msgIDs[0])
	staging := t.TempDir()
	nzbHTTP := nzbServer(t, p)
	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: staging, HTTPClient: nzbHTTP.Client()})
	gid, err := m.AddNZB(context.Background(), nzbHTTP.URL, "LiePlusHole")
	if !errors.Is(err, ErrArticlesUnavailable) {
		t.Fatalf("err=%v want ErrArticlesUnavailable (seg4 is a real hole)", err)
	}
	if gid != "" {
		t.Fatalf("gid=%q want empty", gid)
	}
	if n := srv.bodyCount.Load(); n < 1 {
		t.Fatalf("bodyCount=%d want BODY confirm of STAT misses", n)
	}
}

func TestPrecheck_AllStatLiesButBodyPresent(t *testing.T) {
	p := makePayload(t, 4, 512)
	srv := newFakeNNTP(t)
	srv.serveAll(p)
	srv.lieStatIDs(p.msgIDs...)
	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: t.TempDir()})
	res, err := m.precheckNZB(context.Background(), parsePayloadNZB(t, p), nil, "")
	if err != nil {
		t.Fatalf("precheckNZB: %v", err)
	}
	if !res.StatUnreliable {
		t.Fatal("expected StatUnreliable after BODY confirmed every STAT miss")
	}
	if n := srv.bodyCount.Load(); n != 4 {
		t.Fatalf("bodyCount=%d want 4 (every STAT miss BODY-confirmed)", n)
	}
}

func TestPrecheck_IncludesPar2Articles(t *testing.T) {
	bin := makePayload(t, 2, 512)
	par2ID := "par2seg@test"
	nzbXML := `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="p@example.com" date="0" subject="release.bin (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
      <segment bytes="512" number="1">seg1@test</segment>
      <segment bytes="512" number="2">seg2@test</segment>
    </segments>
  </file>
  <file poster="p@example.com" date="0" subject="release.vol001+02.par2 (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments>
      <segment bytes="512" number="1">` + par2ID + `</segment>
    </segments>
  </file>
</nzb>`
	nzb, err := ParseNZB([]byte(nzbXML))
	if err != nil {
		t.Fatalf("ParseNZB: %v", err)
	}
	srv := newFakeNNTP(t)
	srv.add(bin.msgIDs[0], bin.parts[0])
	srv.add(bin.msgIDs[1], bin.parts[1])
	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: t.TempDir()})
	_, err = m.precheckNZB(context.Background(), nzb, nil, "")
	if !errors.Is(err, ErrArticlesUnavailable) {
		t.Fatalf("err=%v want ErrArticlesUnavailable (missing par2 article)", err)
	}
	if n := srv.statCount.Load(); n != 3 {
		t.Fatalf("statCount=%d want 3 (bin + par2)", n)
	}
}

func TestIsMetaSubject_Par2IsNotDecorative(t *testing.T) {
	if isMetaSubject(`"release.vol001+02.par2" yEnc (1/1)`) {
		t.Fatal("par2 must be included in precheck")
	}
	if isMetaSubject(`"316cef87b8ef42dc840681b2b2cf2c37.par2" yEnc`) {
		t.Fatal("obfuscated par2 subject must be included in precheck")
	}
	if !isMetaSubject(`"release.nfo" yEnc (1/1)`) {
		t.Fatal("nfo should stay decorative")
	}
}
