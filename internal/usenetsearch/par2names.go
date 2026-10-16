package usenetsearch

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// PAR2 packet layout (spec 2.0): 64-byte header = magic "PAR2\0PKT" (8),
// packet length (8, LE, includes header), packet md5 (16), recovery set id
// (16), packet type (16); then the body. A File Description body is
// file id (16), file md5 (16), first-16k md5 (16), file length (8), then the
// filename padded with NULs to a multiple of 4.
var (
	par2Magic    = []byte("PAR2\x00PKT")
	par2FileDesc = []byte("PAR 2.0\x00FileDesc")
)

const (
	par2HeaderLen   = 64
	par2FileDescMin = 16 + 16 + 16 + 8
)

// par2FileNames returns the filenames from File Description packets found in
// data (an index .par2 or the head of a vol file). Truncated or corrupt
// packets are skipped; it never panics on malformed input.
func par2FileNames(data []byte) []string {
	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(data); {
		idx := bytes.Index(data[i:], par2Magic)
		if idx < 0 {
			break
		}
		i += idx
		if i+par2HeaderLen > len(data) {
			break
		}
		length := binary.LittleEndian.Uint64(data[i+8 : i+16])
		if length < par2HeaderLen || length%4 != 0 {
			i += len(par2Magic)
			continue
		}
		if !bytes.Equal(data[i+48:i+64], par2FileDesc) {
			if length > uint64(len(data)-i) {
				break
			}
			i += int(length)
			continue
		}
		end := uint64(i) + length
		if end > uint64(len(data)) {
			end = uint64(len(data))
		}
		body := data[i+par2HeaderLen : int(end)]
		if len(body) > par2FileDescMin {
			name := strings.TrimRight(string(body[par2FileDescMin:]), "\x00")
			if name != "" && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
		if end >= uint64(len(data)) {
			break
		}
		i = int(end)
	}
	return out
}
