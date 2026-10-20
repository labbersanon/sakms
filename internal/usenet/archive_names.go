package usenet

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/labbersanon/sakms/internal/config"
)

// Claude 2026-10-20: regex name scrape replaced by rar5Files (CRC + name).
// var embeddedVideoTok = regexp.MustCompile(`(?i)[\w.\-]+\.(?:mkv|mp4|avi|m4v|wmv|mov|ts|m2ts|mpg|mpeg|iso|img|vob|webm)`)

// ArchiveFileCRC is an inner-file CRC32 advertised in a ZIP or RAR prefix.
// RAR4 FILE_CRC, RAR5 Data CRC32, and ZIP local CRC32 are of the unpacked file
// (srrdb archive-crc).
type ArchiveFileCRC struct {
	Name string
	CRC  string // 8 uppercase hex; empty when the header left it zero
}

// archiveMemberNames returns filenames advertised in a ZIP or RAR prefix.
// Truncated first-segment blobs may yield a partial list.
func archiveMemberNames(data []byte) []string {
	var names []string
	for _, f := range archiveFiles(data) {
		if f.Name != "" {
			names = append(names, f.Name)
		}
	}
	return names
}

// archiveFileCRCs returns inner-file CRC32 values from a ZIP or RAR prefix.
func archiveFileCRCs(data []byte) []ArchiveFileCRC {
	var out []ArchiveFileCRC
	for _, f := range archiveFiles(data) {
		if f.CRC == "" {
			continue
		}
		out = append(out, f)
	}
	return out
}

func archiveFiles(data []byte) []ArchiveFileCRC {
	if bytes.HasPrefix(data, []byte("PK")) {
		return zipLocalFiles(data)
	}
	if bytes.HasPrefix(data, []byte("Rar!\x1a\x07\x00")) {
		return rar4Files(data)
	}
	if bytes.HasPrefix(data, []byte("Rar!\x1a\x07\x01\x00")) {
		return rar5Files(data)
	}
	if bytes.HasPrefix(data, sevenZMagic) {
		return sevenZFiles(data)
	}
	return nil
}

func zipLocalFiles(data []byte) []ArchiveFileCRC {
	var files []ArchiveFileCRC
	i := 0
	for i+30 <= len(data) {
		idx := bytes.Index(data[i:], []byte("PK\x03\x04"))
		if idx < 0 {
			break
		}
		i += idx
		if i+30 > len(data) {
			break
		}
		crc := binary.LittleEndian.Uint32(data[i+14:])
		nameLen := int(binary.LittleEndian.Uint16(data[i+26:]))
		extraLen := int(binary.LittleEndian.Uint16(data[i+28:]))
		compSize := int(binary.LittleEndian.Uint32(data[i+18:]))
		if i+30+nameLen > len(data) {
			break
		}
		name := string(data[i+30 : i+30+nameLen])
		f := ArchiveFileCRC{Name: name}
		if crc != 0 {
			f.CRC = fmt.Sprintf("%08X", crc)
		}
		if name != "" || f.CRC != "" {
			files = append(files, f)
		}
		next := i + 30 + nameLen + extraLen + compSize
		if next <= i {
			break
		}
		i = next
		if len(files) >= 4 {
			break
		}
	}
	return files
}

func rar4Files(data []byte) []ArchiveFileCRC {
	var files []ArchiveFileCRC
	i := 7
	for i+7 <= len(data) {
		typ := data[i+2]
		flags := binary.LittleEndian.Uint16(data[i+3:])
		size := int(binary.LittleEndian.Uint16(data[i+5:]))
		if size < 7 {
			break
		}
		if i+size > len(data) {
			break
		}
		if typ == 0x74 && size >= 32 {
			crc := binary.LittleEndian.Uint32(data[i+16:])
			nameSize := int(binary.LittleEndian.Uint16(data[i+26:]))
			nameOff := 32
			if flags&0x100 != 0 {
				nameOff += 8
			}
			f := ArchiveFileCRC{}
			if nameSize > 0 && nameOff+nameSize <= size {
				f.Name = string(data[i+nameOff : i+nameOff+nameSize])
			}
			if crc != 0 {
				f.CRC = fmt.Sprintf("%08X", crc)
			}
			if f.Name != "" || f.CRC != "" {
				files = append(files, f)
			}
			break
		}
		i += size
		if typ == 0x74 {
			break
		}
	}
	return files
}

func readRAR5Vint(data []byte, i int) (uint64, int, bool) {
	var val uint64
	var shift uint
	for n := 0; n < 10 && i < len(data); n++ {
		b := data[i]
		i++
		val |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return val, i, true
		}
		shift += 7
	}
	return 0, i, false
}

func rar5Files(data []byte) []ArchiveFileCRC {
	// Claude 2026-10-20: RAR5 Data CRC32 is srrdb archive-crc on a complete
	//   volume. Split volumes set flag 0x0010 on every part except the last;
	//   that CRC is packed-this-volume and must not be looked up.
	// Reason: live part001.rar BODY after #228 was RAR5, crcs=0 (regex names
	//   only). RAR 5.0 technote: file flag 0x0004 + uint32 Data CRC32.
	// Troubleshooting: namefix crc magic=526172211a070100 crcs=0 → vint walk
	//   broke or encryption header (type 4). Review if: BLAKE2 extra-area
	//   checksums replace CRC32.
	var files []ArchiveFileCRC
	i := 8
	for i+8 <= len(data) && len(files) < 4 {
		i += 4 // header CRC32
		size, next, ok := readRAR5Vint(data, i)
		if !ok || size == 0 || size > 1<<21 {
			break
		}
		hdrStart := next
		hdrEnd := hdrStart + int(size)
		if hdrEnd > len(data) {
			break
		}
		typ, next, ok := readRAR5Vint(data, hdrStart)
		if !ok {
			break
		}
		flags, next, ok := readRAR5Vint(data, next)
		if !ok {
			break
		}
		if flags&0x0001 != 0 {
			if _, next, ok = readRAR5Vint(data, next); !ok {
				break
			}
		}
		dataSize := uint64(0)
		if flags&0x0002 != 0 {
			dataSize, next, ok = readRAR5Vint(data, next)
			if !ok {
				break
			}
		}
		if typ == 4 || typ == 5 {
			break
		}
		if typ == 2 || typ == 3 {
			ff, next, ok := readRAR5Vint(data, next)
			if !ok {
				break
			}
			if _, next, ok = readRAR5Vint(data, next); !ok { // unpacked size
				break
			}
			if _, next, ok = readRAR5Vint(data, next); !ok { // attributes
				break
			}
			if ff&0x0002 != 0 {
				if next+4 > hdrEnd {
					break
				}
				next += 4
			}
			var crc uint32
			if ff&0x0004 != 0 {
				if next+4 > hdrEnd {
					break
				}
				crc = binary.LittleEndian.Uint32(data[next:])
				next += 4
			}
			if _, next, ok = readRAR5Vint(data, next); !ok { // compression
				break
			}
			if _, next, ok = readRAR5Vint(data, next); !ok { // host OS
				break
			}
			nlen, next, ok := readRAR5Vint(data, next)
			if !ok {
				break
			}
			name := ""
			if nlen > 0 && next+int(nlen) <= hdrEnd {
				name = string(data[next : next+int(nlen)])
			}
			splitAfter := flags&0x0010 != 0
			f := ArchiveFileCRC{Name: name}
			if crc != 0 && !splitAfter {
				f.CRC = fmt.Sprintf("%08X", crc)
			}
			if f.Name != "" || f.CRC != "" {
				files = append(files, f)
			}
			if typ == 2 {
				break
			}
		}
		i = hdrEnd
		if flags&0x0002 != 0 {
			if uint64(i)+dataSize > uint64(len(data)) {
				break
			}
			i += int(dataSize)
		}
	}
	return files
}

// PayloadKind classifies a BODY prefix: par2, rar4, rar5, rar5-enc, zip,
// 7z, sfv, ybegin, or unknown. 7z NextHeader kinds are SevenZHeaderKind.
func PayloadKind(data []byte) string {
	switch {
	case len(data) == 0:
		return ""
	case bytes.HasPrefix(data, []byte("PAR2\x00PKT")):
		return "par2"
	case bytes.HasPrefix(data, []byte("Rar!\x1a\x07\x00")):
		return "rar4"
	case bytes.HasPrefix(data, []byte("Rar!\x1a\x07\x01\x00")):
		if rar5Encrypted(data) {
			return "rar5-enc"
		}
		return "rar5"
	case bytes.HasPrefix(data, []byte("PK\x03\x04")), bytes.HasPrefix(data, []byte("PK\x05\x06")):
		return "zip"
	case bytes.HasPrefix(data, sevenZMagic):
		return "7z"
	case bytes.HasPrefix(bytes.TrimSpace(data), []byte("=ybegin")):
		return "ybegin"
	case looksSFVPayload(data):
		return "sfv"
	default:
		return "unknown"
	}
}

func rar5Encrypted(data []byte) bool {
	i := 8
	for n := 0; n < 8 && i+8 <= len(data); n++ {
		i += 4
		size, next, ok := readRAR5Vint(data, i)
		if !ok || size == 0 || size > 1<<21 {
			return false
		}
		hdrStart := next
		hdrEnd := hdrStart + int(size)
		if hdrEnd > len(data) {
			return false
		}
		typ, _, ok := readRAR5Vint(data, hdrStart)
		if !ok {
			return false
		}
		if typ == 4 || typ == 5 {
			return true
		}
		i = hdrEnd
	}
	return false
}

func looksSFVPayload(data []byte) bool {
	n := len(data)
	if n > 4096 {
		n = 4096
	}
	lines := 0
	for _, line := range strings.Split(string(data[:n]), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		if i < 1 || i+1+8 > len(line) {
			return false
		}
		crc := line[i+1:]
		if len(crc) != 8 {
			return false
		}
		for _, c := range crc {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		lines++
		if lines >= 1 {
			return true
		}
	}
	return false
}

func archiveBaseName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		return name[i+1:]
	}
	return name
}

func nameLooksVideo(name string) bool {
	return config.IsVideoFile(archiveBaseName(name))
}

func nameLooksArchive(name string) bool {
	return archiveKind(archiveBaseName(name)) != ""
}

func nameLooksJunk(name string) bool {
	base := strings.ToLower(archiveBaseName(name))
	switch {
	case strings.HasSuffix(base, ".nfo"), strings.HasSuffix(base, ".sfv"),
		strings.HasSuffix(base, ".srr"), strings.HasSuffix(base, ".txt"),
		strings.HasSuffix(base, ".jpg"), strings.HasSuffix(base, ".jpeg"),
		strings.HasSuffix(base, ".png"), strings.HasSuffix(base, ".gif"),
		strings.HasSuffix(base, ".mp3"), strings.HasSuffix(base, ".flac"),
		strings.HasSuffix(base, ".wav"), strings.HasSuffix(base, ".m4a"),
		strings.HasSuffix(base, ".nzb"):
		return true
	}
	return false
}
