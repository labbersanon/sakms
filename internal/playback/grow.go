package playback

import (
	"context"
	"io"
	"os"
	"time"
)

const growPoll = 200 * time.Millisecond

// GrowReader is a follow-reader: Read returns newly appended bytes, and true
// EOF only after stillGrowing is false and the current size has been consumed.
type GrowReader struct {
	ctx          context.Context
	path         string
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
	return &GrowReader{ctx: ctx, path: path, stillGrowing: stillGrowing, f: f}, nil
}

func (g *GrowReader) Read(p []byte) (int, error) {
	if g == nil || g.f == nil {
		return 0, io.EOF
	}
	for {
		n, err := g.f.ReadAt(p, g.off)
		if n > 0 {
			g.off += int64(n)
			if err == io.EOF {
				err = nil
			}
			return n, err
		}
		growing := g.stillGrowing != nil && g.stillGrowing()
		if !growing {
			st, statErr := g.f.Stat()
			if statErr == nil && g.off < st.Size() {
				continue
			}
			return 0, io.EOF
		}
		select {
		case <-g.ctx.Done():
			return 0, g.ctx.Err()
		case <-time.After(growPoll):
		}
	}
}

func (g *GrowReader) Close() error {
	if g == nil || g.f == nil {
		return nil
	}
	err := g.f.Close()
	g.f = nil
	return err
}
