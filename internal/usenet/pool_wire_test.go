package usenet

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/mnightingale/rapidyenc"
)

func TestArticleWireReader_RestuffsLeadingDots(t *testing.T) {
	// nntp.Conn.Body() already unstuffed; lines arrive with bare LF (or CRLF
	// if the server never used CRLF). rapidyenc must see a restuffed "..".
	tests := []struct {
		name, in string
	}{
		{"bare LF (bodyReader)", "hello\n.secret\nworld\n"},
		{"CRLF", "hello\r\n.secret\r\nworld\r\n"},
	}
	want := []byte("hello\r\n..secret\r\nworld\r\n.\r\n")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := io.ReadAll(newArticleWireReader(strings.NewReader(tt.in)))
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestArticleWireReader_YEncLeadingDotRoundTrip(t *testing.T) {
	// 0x04 yEnc-encodes to '.'. A poster that skips yEnc line-start escaping
	// emits a 128-byte line of dots. bodyReader unstuffs the NNTP extra dot;
	// without restuff, rapidyenc unstuffs again and decoded len is 127.
	const n = 128
	raw := bytes.Repeat([]byte{0x04}, n)
	body := strings.Join([]string{
		"=ybegin part=1 total=1 line=128 size=128 name=t.bin",
		"=ypart begin=1 end=128",
		strings.Repeat(".", n),
		"=yend size=128 part=1",
		"",
	}, "\n")

	dec := rapidyenc.NewDecoder(newArticleWireReader(strings.NewReader(body)), rapidyenc.WithStatusLineAlreadyRead())
	resp, err := dec.Next()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if int64(len(resp.Data)) != resp.Metadata.PartSize {
		t.Errorf("len(data)=%d PartSize=%d", len(resp.Data), resp.Metadata.PartSize)
	}
	if !bytes.Equal(resp.Data, raw) {
		t.Errorf("decoded mismatch: got %d bytes, want %d", len(resp.Data), n)
	}
}
