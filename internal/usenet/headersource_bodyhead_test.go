package usenet

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestBodyHead_ReturnsYencNameAndTruncates(t *testing.T) {
	payload := bytes.Repeat([]byte("Rar!\x1a\x07\x00abcdefgh"), 100)
	srv := newFakeNNTP(t)
	srv.add("first@x", yencPart(t, "My.Show.S01E01.1080p.WEB-DL.x264-GRP.rar", int64(len(payload)), 1, 1, 0, payload))

	m := New(Config{Servers: []ServerConfig{srv.cfgWith(1)}, StagingDir: t.TempDir()})
	hs := m.HeaderSource()
	s, err := hs.BodyHead(context.Background(), "<first@x>", 64)
	if err != nil {
		t.Fatalf("BodyHead: %v", err)
	}
	if s.YencName != "My.Show.S01E01.1080p.WEB-DL.x264-GRP.rar" {
		t.Fatalf("YencName = %q", s.YencName)
	}
	if s.YencFileSize != int64(len(payload)) {
		t.Fatalf("YencFileSize = %d", s.YencFileSize)
	}
	if len(s.Data) != 64 || !bytes.Equal(s.Data, payload[:64]) {
		t.Fatalf("Data len=%d, want truncated 64-byte prefix", len(s.Data))
	}
	if s.WireBytes != int64(len(payload)) {
		t.Fatalf("WireBytes = %d, want %d", s.WireBytes, len(payload))
	}
}

func TestBodyHead_NotFoundKeepsConn(t *testing.T) {
	srv := newFakeNNTP(t)
	srv.addStatus("gone@x", 430)
	m := New(Config{Servers: []ServerConfig{srv.cfgWith(1)}, StagingDir: t.TempDir()})
	hs := m.HeaderSource()
	_, err := hs.BodyHead(context.Background(), "gone@x", 100)
	if !errors.Is(err, ErrArticleNotFound) {
		t.Fatalf("err = %v, want ErrArticleNotFound", err)
	}
}

func TestBodyHead_BusyYields(t *testing.T) {
	srv := newFakeNNTP(t)
	p := newPool(srv.cfgWith(1))
	busy, err := p.get()
	if err != nil {
		t.Fatalf("prime: %v", err)
	}
	t.Cleanup(func() { p.put(busy, false) })

	m := New(Config{Servers: []ServerConfig{srv.cfgWith(1)}, StagingDir: t.TempDir()})
	m.pools = []*pool{p}
	_, err = m.HeaderSource().BodyHead(context.Background(), "x@x", 100)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if got := srv.bodyCount.Load(); got != 0 {
		t.Fatalf("BODY issued %d times while busy", got)
	}
}

func TestArchiveMemberNames_Exported(t *testing.T) {
	if got := ArchiveMemberNames([]byte("not an archive")); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}
