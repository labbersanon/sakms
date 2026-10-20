package usenetsearch

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
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

// par2File is one File Description packet: inner name, MD5 of the first
// 16 KiB (hash_16K), and claimed file length.
type par2File struct {
	Name    string
	Hash16K string // lowercase 32-hex; empty when the 16-byte field is zeros
	Length  int64
}

// par2FileNames returns the filenames from File Description packets found in
// data (an index .par2 or the head of a vol file). Truncated or corrupt
// packets are skipped; it never panics on malformed input.
func par2FileNames(data []byte) []string {
	var out []string
	for _, f := range par2FileDescs(data) {
		if f.Name != "" {
			out = append(out, f.Name)
		}
	}
	return out
}

// par2FileDescs returns FileDesc name + hash_16K + length from data.
// Claude 2026-10-20: keep hash_16K, not only the filename.
// Reason: NNTmux matches MD5(first 16KiB) when FileDesc names are also hashes.
// Troubleshooting: parhash stored=0 after par2=true BODY → short packet or zero hash field.
// Review if: PAR 3.0 packets appear on Usenet.
func par2FileDescs(data []byte) []par2File {
	var out []par2File
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
		var f par2File
		if len(body) >= 48 {
			var h [16]byte
			copy(h[:], body[32:48])
			if h != [16]byte{} {
				f.Hash16K = hex.EncodeToString(h[:])
			}
		}
		if len(body) >= par2FileDescMin {
			f.Length = int64(binary.LittleEndian.Uint64(body[48:56]))
			if len(body) > par2FileDescMin {
				f.Name = strings.TrimRight(string(body[par2FileDescMin:]), "\x00")
			}
		}
		key := f.Hash16K + "\x00" + strings.ToLower(f.Name)
		if (f.Name != "" || f.Hash16K != "") && !seen[key] {
			seen[key] = true
			out = append(out, f)
		}
		if end >= uint64(len(data)) {
			break
		}
		i = int(end)
	}
	return out
}
