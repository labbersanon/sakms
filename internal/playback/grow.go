package playback

import (
	"context"
	"io"
	"os"
	"time"
)

const (
	growPoll   = 200 * time.Millisecond
	maxYencGap = 8192
)

// GrowReader is a follow-reader: Read returns newly appended bytes, and true
// EOF only after stillGrowing is false and the current size has been consumed.
// Truncate-up usenet files keep Stat size at FileSize while holes remain;
// Read waits for a populated run at the current offset instead of feeding NULs.
type GrowReader struct {
	ctx          context.Context
	stillGrowing func() bool
	f            *os.File
	off          int64
}

func NewGrowReader(ctx context.Context, path string, stillGrowing func() bool) (*GrowReader, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &GrowReader{ctx: ctx, stillGrowing: stillGrowing, f: f}, nil
}

func (g *GrowReader) Read(p []byte) (int, error) {
	if g == nil || g.f == nil {
		return 0, io.EOF
	}
	for {
		st, err := g.f.Stat()
		if err != nil {
			return 0, err
		}
		size := st.Size()
		growing := g.stillGrowing != nil && g.stillGrowing()
		if g.off >= size {
			if !growing {
				return 0, io.EOF
			}
			if err := g.wait(); err != nil {
				return 0, err
			}
			continue
		}
		limit := int64(len(p))
		remain := size - g.off
		if remain < limit {
			limit = remain
		}
		end, isData, known := extentAt(g.f, g.off, size)
		if known {
			run := end - g.off
			if run < 0 {
				run = 0
			}
			if !isData && growing && run > maxYencGap {
				if err := g.wait(); err != nil {
					return 0, err
				}
				continue
			}
			if run > 0 && run < limit {
				limit = run
			}
		}
		n, err := g.f.ReadAt(p[:limit], g.off)
		if n > 0 {
			g.off += int64(n)
			if err == io.EOF {
				err = nil
			}
			return n, err
		}
		if !growing {
			return 0, io.EOF
		}
		if err := g.wait(); err != nil {
			return 0, err
		}
	}
}

func (g *GrowReader) wait() error {
	select {
	case <-g.ctx.Done():
		return g.ctx.Err()
	case <-time.After(growPoll):
		return nil
	}
}

// LeadingPopulatedBytes is how much real payload exists from offset 0.
// Truncate-up hollow files return 0 even when Stat size is the whole film.
func LeadingPopulatedBytes(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() <= 0 {
		return 0
	}
	buf := make([]byte, min(int64(4096), st.Size()))
	got, _ := f.ReadAt(buf, 0)
	if got == 0 {
		return 0
	}
	for _, b := range buf[:got] {
		if b != 0 {
			end, isData, known := extentAt(f, 0, st.Size())
			if !known {
				return st.Size()
			}
			if !isData {
				return 0
			}
			return end
		}
	}
	return 0
}

func (g *GrowReader) Close() error {
	if g == nil || g.f == nil {
		return nil
	}
	err := g.f.Close()
	g.f = nil
	return err
}
