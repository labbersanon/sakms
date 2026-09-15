package usenet

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestPrecheck_HealthyNZB_SamplesNotFull(t *testing.T) {
	p := makePayload(t, 20, 512)
	srv := newFakeNNTP(t)
	srv.serveAll(p)
	nzbHTTP := nzbServer(t, p)
	m := New(Config{Servers: []ServerConfig{srv.cfg()}, StagingDir: t.TempDir(), HTTPClient: nzbHTTP.Client()})
	gid, err := m.AddNZB(context.Background(), nzbHTTP.URL, "Healthy")
	if err != nil {
		t.Fatalf("AddNZB: %v", err)
	}
	_ = waitTerminal(t, m, gid)
	n := srv.statCount.Load()
	if n == 0 {
		t.Fatal("expected STAT probes")
	}
	if n > int64(defaultPrecheckPolicy.SampleCap) {
		t.Fatalf("statCount=%d exceeds SampleCap %d", n, defaultPrecheckPolicy.SampleCap)
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
