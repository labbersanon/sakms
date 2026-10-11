package usenet

import (
	"bytes"
	"encoding/binary"
)

// rarLooksEncrypted reports whether a RAR prefix advertises header/file
// encryption. Used by the first-segment peek so we can abort before BODY when
// no password is known (SABnzbd pause-on-encrypted).
//
// Fail-open: a truncated peek that cannot be parsed is treated as not
// encrypted; unrar still fails closed after assemble.
func rarLooksEncrypted(data []byte) bool {
	if bytes.HasPrefix(data, []byte("Rar!\x1a\x07\x00")) {
		return rar4LooksEncrypted(data)
	}
	if bytes.HasPrefix(data, []byte("Rar!\x1a\x07\x01")) {
		return rar5LooksEncrypted(data)
	}
	return false
}

func rar4LooksEncrypted(data []byte) bool {
	i := 7
	for i+7 <= len(data) {
		typ := data[i+2]
		flags := binary.LittleEndian.Uint16(data[i+3:])
		size := int(binary.LittleEndian.Uint16(data[i+5:]))
		if size < 7 {
			return false
		}
		if i+size > len(data) {
			// Truncated header: honour flags we already have.
			if typ == 0x73 && flags&0x0080 != 0 {
				return true
			}
			if typ == 0x74 && flags&0x0004 != 0 {
				return true
			}
			return false
		}
		if typ == 0x73 { // main archive header; MHD_PASSWORD
			return flags&0x0080 != 0
		}
		if typ == 0x74 { // file header; LHD_PASSWORD
			return flags&0x0004 != 0
		}
		i += size
	}
	return false
}

func rar5LooksEncrypted(data []byte) bool {
	i := 8 // marker including trailing 0x00
	if i+4 > len(data) {
		return false
	}
	i += 4 // header CRC32
	_, i, ok := readRar5Vint(data, i)
	if !ok {
		return false
	}
	typ, i, ok := readRar5Vint(data, i)
	if !ok {
		return false
	}
	flags, i, ok := readRar5Vint(data, i)
	if !ok {
		return false
	}
	const extraPresent uint64 = 0x0001
	if flags&extraPresent == 0 {
		return false
	}
	extraSize, i, ok := readRar5Vint(data, i)
	if !ok || extraSize == 0 {
		return false
	}
	end := i + int(extraSize)
	if end > len(data) {
		end = len(data)
	}
	// Main header type 1; encryption extra type 1. File header type 2 can
	// also carry encryption extras — treat any encryption extra as encrypted.
	_ = typ
	for i < end {
		recSize, ni, ok := readRar5Vint(data, i)
		if !ok || recSize == 0 {
			break
		}
		recType, ni, ok := readRar5Vint(data, ni)
		if !ok {
			break
		}
		if recType == 1 { // encryption
			return true
		}
		i = i + int(recSize)
		if i < ni {
			i = ni
		}
	}
	return false
}

func readRar5Vint(data []byte, i int) (val uint64, next int, ok bool) {
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
