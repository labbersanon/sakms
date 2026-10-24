package playback

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGrowReader_WaitsThenEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mkv")
	if err := os.WriteFile(path, []byte("aaaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	growing := true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := NewGrowReader(ctx, path, func() bool { return growing })
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	buf := make([]byte, 8)
	n, err := r.Read(buf)
	if n != 4 || err != nil {
		t.Fatalf("first read n=%d err=%v", n, err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = f.WriteString("bbbb")
		f.Close()
		time.Sleep(50 * time.Millisecond)
		growing = false
	}()
	n, err = r.Read(buf)
	if n != 4 || string(buf[:n]) != "bbbb" || err != nil {
		t.Fatalf("second read n=%d buf=%q err=%v", n, buf[:n], err)
	}
	n, err = r.Read(buf)
	if n != 0 || err != io.EOF {
		t.Fatalf("eof n=%d err=%v", n, err)
	}
}

func TestLeadingPopulatedBytes_Populated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mkv")
	if err := os.WriteFile(path, []byte("mkv-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := LeadingPopulatedBytes(path); n < 9 {
		t.Fatalf("populated = %d", n)
	}
}

func TestLeadingPopulatedBytes_HollowTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(2 << 20); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if n := LeadingPopulatedBytes(path); n != 0 {
		t.Fatalf("hollow = %d, want 0", n)
	}
}

func TestGrowReader_DoesNotFeedSparseHole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(1 << 20); err != nil {
		t.Fatal(err)
	}
	prefix := bytes.Repeat([]byte("a"), 16*1024)
	if _, err := f.WriteAt(prefix, 0); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := NewGrowReader(ctx, path, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	buf := make([]byte, 64*1024)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("read err=%v", err)
	}
	if n > 32*1024 {
		t.Fatalf("read %d from 16KiB prefix — fed the Truncate hole", n)
	}
	if n < 16 || string(buf[:16]) != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("n=%d buf=%q", n, buf[:min(n, 16)])
	}
}
