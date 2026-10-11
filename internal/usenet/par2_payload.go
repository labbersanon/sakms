package usenet

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

const par2PktMagic = "PAR2\x00PKT"

// isPar2Payload reports whether path is a real PAR2 set (extension + magic).
// Magic may sit after a short pad — WEBTUBE vol-*.par2 files were skipped
// when only bytes 0:4 were checked, so recovery packets never loaded.
func isPar2Payload(path string) bool {
	if !strings.HasSuffix(strings.ToLower(path), ".par2") {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 64*1024)
	n, err := f.Read(head)
	if err != nil || n < 4 {
		return false
	}
	return par2MagicOffset(head[:n]) >= 0
}

func par2MagicOffset(head []byte) int {
	if bytes.HasPrefix(head, []byte("PAR2")) {
		return 0
	}
	if i := bytes.Index(head, []byte(par2PktMagic)); i >= 0 {
		return i
	}
	return -1
}

func trimPar2Magic(data []byte) []byte {
	off := par2MagicOffset(data)
	if off > 0 && off < len(data) {
		return data[off:]
	}
	return data
}

// looksPar2VolumeName reports QuickPar/MultiPar recovery volume names
// (set.vol00+01.par2, set.vol-07.par2) so verifyAndRepair still tries them
// when the prefix sniff misses.
func looksPar2VolumeName(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if !strings.HasSuffix(base, ".par2") {
		return false
	}
	return strings.Contains(base, ".vol")
}

func isPar2Candidate(path string) bool {
	return isPar2Payload(path) || looksPar2VolumeName(path)
}
