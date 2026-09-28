package usenet

import "bytes"

// payloadKind is the container/type of a yEnc-decoded first segment.
type payloadKind int

const (
	kindUnknown payloadKind = iota
	kindVideo
	kindArchive
	kindPAR2
	kindJunk
)

// sniffMediaExtFrom returns a file extension for common payload magics, or ""
// when unrecognized. Same set sniffMediaExt uses after assemble.
func sniffMediaExtFrom(head []byte) string {
	switch sniffPayloadKind(head) {
	case kindVideo:
		if len(head) >= 8 && string(head[4:8]) == "ftyp" {
			return ".mp4"
		}
		if len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "AVI " {
			return ".avi"
		}
		return ".mkv"
	case kindArchive:
		if bytes.HasPrefix(head, []byte("Rar!")) {
			return ".rar"
		}
		if bytes.HasPrefix(head, []byte("PK")) {
			return ".zip"
		}
		return ""
	default:
		return ""
	}
}

func sniffPayloadKind(data []byte) payloadKind {
	if len(data) < 2 {
		return kindUnknown
	}
	if hasVideoMagic(data) {
		return kindVideo
	}
	if bytes.HasPrefix(data, []byte("PAR2")) {
		return kindPAR2
	}
	if bytes.HasPrefix(data, []byte("Rar!")) || bytes.HasPrefix(data, []byte("PK")) ||
		bytes.HasPrefix(data, []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}) {
		return kindArchive
	}
	if isJunkMagic(data) {
		return kindJunk
	}
	return kindUnknown
}

func hasVideoMagic(data []byte) bool {
	if bytes.HasPrefix(data, []byte{0x1A, 0x45, 0xDF, 0xA3}) {
		return true
	}
	if len(data) >= 8 && string(data[4:8]) == "ftyp" {
		return true
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "AVI " {
		return true
	}
	if bytes.HasPrefix(data, []byte{0x00, 0x00, 0x01, 0xBA}) {
		return true
	}
	// ISO 9660 primary volume descriptor.
	if len(data) > 32769+5 && string(data[32769:32769+5]) == "CD001" {
		return true
	}
	return false
}

func isJunkMagic(data []byte) bool {
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return true
	}
	if bytes.HasPrefix(data, []byte("\x89PNG")) {
		return true
	}
	if bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")) {
		return true
	}
	if bytes.HasPrefix(data, []byte("ID3")) {
		return true
	}
	if len(data) >= 2 && data[0] == 0xFF && (data[1] == 0xFB || data[1] == 0xF3 || data[1] == 0xF2) {
		return true
	}
	if bytes.HasPrefix(data, []byte("%PDF")) {
		return true
	}
	return false
}

// videoMagicIn reports Matroska/MP4/AVI magic anywhere in data (RAR/ZIP bodies
// often place the inner file immediately after a short header).
func videoMagicIn(data []byte) bool {
	if hasVideoMagic(data) {
		return true
	}
	if bytes.Contains(data, []byte{0x1A, 0x45, 0xDF, 0xA3}) {
		return true
	}
	if i := bytes.Index(data, []byte("ftyp")); i >= 4 {
		return true
	}
	return false
}
