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
