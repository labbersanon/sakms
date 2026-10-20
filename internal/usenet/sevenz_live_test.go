package usenet

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestParseLiveEncodedAESHeader(t *testing.T) {
	// Live ngPost hash.7z.065 NextHeader (off=33948691072 sz=58).
	h, err := hex.DecodeString("1706f7a0e97fe7010980e000070b0100022406f107010a530734cdd1c5f369ebc523030101055d0010000001000c80da821e0a01fc3137330000")
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 58 {
		t.Fatalf("len %d", len(h))
	}
	si, n, ok := parse7zStreams(h, 1)
	if !ok || n != len(h) {
		t.Fatalf("ok=%v n=%d/%d", ok, n, len(h))
	}
	if len(si.packSizes) != 1 || si.packSizes[0] != 224 {
		t.Fatalf("pack %+v", si.packSizes)
	}
	if len(si.folders) != 1 || !si.folders[0].encrypted {
		t.Fatalf("folders %+v", si.folders)
	}
	f := si.folders[0]
	if !bytes.Equal(f.method, sevenZLZMA) || f.unpackSize != 542 || f.unpackSizes[0] != 218 {
		t.Fatalf("folder method=%x unpack=%d sizes=%v", f.method, f.unpackSize, f.unpackSizes)
	}
	if SevenZHeaderKind(h) != "7z-aes" {
		t.Fatalf("kind=%q", SevenZHeaderKind(h))
	}
}
