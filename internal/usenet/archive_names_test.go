package usenet

import (
	"encoding/binary"
	"testing"
)

func TestArchiveFileCRCs_RAR4AndZIP(t *testing.T) {
	member := "Some.Show.S01E02.720p.HDTV.x264-GRP.mkv"
	rar := rar4WithCRC(member, 0x0053CA13)
	got := ArchiveFileCRCs(rar)
	if len(got) != 1 || got[0].Name != member || got[0].CRC != "0053CA13" {
		t.Fatalf("rar4 = %+v", got)
	}
	if names := ArchiveMemberNames(rar); len(names) != 1 || names[0] != member {
		t.Fatalf("rar4 names = %v", names)
	}

	zip := zipLocalWithCRC(member, 0xAABBCCDD)
	got = ArchiveFileCRCs(zip)
	if len(got) != 1 || got[0].Name != member || got[0].CRC != "AABBCCDD" {
		t.Fatalf("zip = %+v", got)
	}
	zero := rar4WithCRC(member, 0)
	if crcs := ArchiveFileCRCs(zero); len(crcs) != 0 {
		t.Fatalf("zero CRC must be dropped, got %+v", crcs)
	}

	rar5 := rar5WithCRC(member, 0x0053CA13, false)
	got = ArchiveFileCRCs(rar5)
	if len(got) != 1 || got[0].Name != member || got[0].CRC != "0053CA13" {
		t.Fatalf("rar5 = %+v", got)
	}
	split := ArchiveFileCRCs(rar5WithCRC(member, 0x0053CA13, true))
	if len(split) != 0 {
		t.Fatalf("RAR5 split-after CRC is packed-this-volume, got %+v", split)
	}
}

func rar4WithCRC(member string, crc uint32) []byte {
	b := []byte("Rar!\x1a\x07\x00")
	b = append(b, 0, 0, 0x73, 0, 0, 13, 0, 0, 0, 0, 0, 0, 0)
	hdr := make([]byte, 32)
	hdr[2] = 0x74
	binary.LittleEndian.PutUint16(hdr[5:], uint16(32+len(member)))
	binary.LittleEndian.PutUint32(hdr[16:], crc)
	binary.LittleEndian.PutUint16(hdr[26:], uint16(len(member)))
	return append(append(b, hdr...), member...)
}

func zipLocalWithCRC(member string, crc uint32) []byte {
	hdr := make([]byte, 30+len(member))
	copy(hdr[0:], []byte("PK\x03\x04"))
	binary.LittleEndian.PutUint32(hdr[14:], crc)
	binary.LittleEndian.PutUint16(hdr[26:], uint16(len(member)))
	copy(hdr[30:], member)
	return hdr
}

func appendRAR5Vint(b []byte, v uint64) []byte {
	for {
		x := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, x|0x80)
			continue
		}
		return append(b, x)
	}
}

func rar5WithCRC(member string, crc uint32, splitAfter bool) []byte {
	var body []byte
	body = appendRAR5Vint(body, 2)
	flags := uint64(0)
	if splitAfter {
		flags |= 0x0010
	}
	body = appendRAR5Vint(body, flags)
	body = appendRAR5Vint(body, 0x0004)
	body = appendRAR5Vint(body, 1)
	body = appendRAR5Vint(body, 0)
	var crcBuf [4]byte
	binary.LittleEndian.PutUint32(crcBuf[:], crc)
	body = append(body, crcBuf[:]...)
	body = appendRAR5Vint(body, 0)
	body = appendRAR5Vint(body, 0)
	body = appendRAR5Vint(body, uint64(len(member)))
	body = append(body, member...)
	hdr := []byte{0, 0, 0, 0}
	hdr = appendRAR5Vint(hdr, uint64(len(body)))
	hdr = append(hdr, body...)
	return append([]byte("Rar!\x1a\x07\x01\x00"), hdr...)
}
