package usenet

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"regexp"
	"strings"

	"github.com/labbersanon/sakms/internal/config"
)

var embeddedVideoTok = regexp.MustCompile(`(?i)[\w.\-]+\.(?:mkv|mp4|avi|m4v|wmv|mov|ts|m2ts|mpg|mpeg|iso|img|vob|webm)`)

// ArchiveFileCRC is an inner-file CRC32 advertised in a ZIP or RAR4 prefix.
// RAR FILE_CRC and ZIP local CRC32 are of the unpacked file (srrdb archive-crc).
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

// archiveFileCRCs returns inner-file CRC32 values from a ZIP or RAR4 prefix.
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
		var out []ArchiveFileCRC
		for _, n := range embeddedVideoTok.FindAllString(string(data[:min(len(data), 8192)]), -1) {
			out = append(out, ArchiveFileCRC{Name: n})
		}
		return out
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
			// Claude 2026-10-20: FILE_CRC at header+16 (RAR 4.x file header).
			// Reason: srrdb archive-crc is the unpacked inner-file CRC32;
			//   RAR FILE_CRC is that value even when the block is compressed.
			// Troubleshooting: CRC lookups miss on a live .rar BODY → this
			//   offset drifted or the first article is RAR5.
			// Review if: RAR5 FILE_CRC (variable-int) is added.
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
