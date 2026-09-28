package usenet

import (
	"bytes"
	"encoding/binary"
	"regexp"
	"strings"

	"github.com/labbersanon/sakms/internal/config"
)

var embeddedVideoTok = regexp.MustCompile(`(?i)[\w.\-]+\.(?:mkv|mp4|avi|m4v|wmv|mov|ts|m2ts|mpg|mpeg|iso|img|vob|webm)`)

// archiveMemberNames returns filenames advertised in a ZIP or RAR prefix.
// Truncated first-segment blobs may yield a partial list.
func archiveMemberNames(data []byte) []string {
	if bytes.HasPrefix(data, []byte("PK")) {
		return zipLocalNames(data)
	}
	if bytes.HasPrefix(data, []byte("Rar!\x1a\x07\x00")) {
		return rar4Names(data)
	}
	if bytes.HasPrefix(data, []byte("Rar!\x1a\x07\x01\x00")) {
		return embeddedVideoTok.FindAllString(string(data[:min(len(data), 8192)]), -1)
	}
	return nil
}

func zipLocalNames(data []byte) []string {
	var names []string
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
		nameLen := int(binary.LittleEndian.Uint16(data[i+26:]))
		extraLen := int(binary.LittleEndian.Uint16(data[i+28:]))
		compSize := int(binary.LittleEndian.Uint32(data[i+18:]))
		if i+30+nameLen > len(data) {
			break
		}
		name := string(data[i+30 : i+30+nameLen])
		if name != "" {
			names = append(names, name)
		}
		next := i + 30 + nameLen + extraLen + compSize
		if next <= i {
			break
		}
		i = next
	}
	return names
}

func rar4Names(data []byte) []string {
	var names []string
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
			nameSize := int(binary.LittleEndian.Uint16(data[i+26:]))
			nameOff := 32
			if flags&0x100 != 0 {
				nameOff += 8
			}
			if nameSize > 0 && nameOff+nameSize <= size {
				names = append(names, string(data[i+nameOff:i+nameOff+nameSize]))
			}
			break
		}
		i += size
		if typ == 0x74 {
			break
		}
	}
	return names
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
