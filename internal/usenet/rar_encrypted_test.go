package usenet

import (
	"encoding/binary"
	"testing"
)

func TestRar4LooksEncrypted_MainHeaderFlag(t *testing.T) {
	data := makeRAR4(t, 0x0080, 0x73)
	if !rarLooksEncrypted(data) {
		t.Fatal("want encrypted from MHD_PASSWORD")
	}
}

func TestRar4LooksEncrypted_ClearMainHeader(t *testing.T) {
	data := makeRAR4(t, 0x0001, 0x73)
	if rarLooksEncrypted(data) {
		t.Fatal("volume flag alone is not encryption")
	}
}

func TestRarLooksEncrypted_NotRAR(t *testing.T) {
	if rarLooksEncrypted([]byte("PK\x03\x04")) {
		t.Fatal("zip is not rar-encrypted")
	}
}

func makeRAR4(t *testing.T, flags uint16, typ byte) []byte {
	t.Helper()
	data := append([]byte("Rar!\x1a\x07\x00"), make([]byte, 7)...)
	data[7+2] = typ
	binary.LittleEndian.PutUint16(data[7+3:], flags)
	binary.LittleEndian.PutUint16(data[7+5:], 7)
	return data
}
