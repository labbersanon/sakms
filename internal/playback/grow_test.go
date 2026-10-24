package playback

import (
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
