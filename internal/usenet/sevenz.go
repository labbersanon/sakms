package usenet

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claude 2026-10-20: 7z inner CRC lives in the end header, not the first
//   256KiB of .7z.001 (packed data).
// Reason: live hash.par2 BODYed ngPost 7z.001 (magic=377abcaf271c0004,
//   crcs=0). Split volumes put NextHeader at 32+NextHeaderOffset, which is
//   the last bytes of the last .7z.NNN volume. Encoded headers need LZMA;
//   the image already has 7z (p7zip-full / 7-Zip). A sparse file with the
//   original start header and the footer at the real offset lets `7z l -slt`
//   print Path/CRC without the packed streams.
// Troubleshooting: namefix crc magic=377abcaf271c0004 crcs=0 → last .7z.NNN
//   segment was not fetched, NextHeaderSize exceeded the tail, or 7z missing.
// Review if: a pure-Go LZMA header parser replaces the 7z spawn.
// Related files: archive_names.go, namefix.go pickBodyTargets

var sevenZMagic = []byte("7z\xbc\xaf\x27\x1c")

const (
	max7zPackedOffset = 1 << 40 // 1 TiB sparse apparent size
	max7zHeaderSize   = 4 << 20
	sevenZListTimeout = 5 * time.Second
)

// SevenZStart is the 32-byte 7z signature header.
type SevenZStart struct {
	Offset uint64
	Size   uint64
	CRC    uint32
}

// Parse7zStart reads NextHeaderOffset/Size from a 7z signature prefix.
func Parse7zStart(data []byte) (SevenZStart, bool) {
	if len(data) < 32 || !bytes.HasPrefix(data, sevenZMagic) {
		return SevenZStart{}, false
	}
	st := SevenZStart{
		Offset: binary.LittleEndian.Uint64(data[12:20]),
		Size:   binary.LittleEndian.Uint64(data[20:28]),
		CRC:    binary.LittleEndian.Uint32(data[28:32]),
	}
	if st.Size == 0 || st.Size > max7zHeaderSize || st.Offset > max7zPackedOffset {
		return SevenZStart{}, false
	}
	if crc32.ChecksumIEEE(data[12:32]) != binary.LittleEndian.Uint32(data[8:12]) {
		return SevenZStart{}, false
	}
	return st, true
}

func sevenZFiles(data []byte) []ArchiveFileCRC {
	st, ok := Parse7zStart(data)
	if !ok {
		return nil
	}
	end := 32 + st.Offset + st.Size
	if st.Offset+st.Size < st.Offset || end > uint64(len(data)) {
		return nil
	}
	return sevenZListFiles(data[:32], data[32+st.Offset:end])
}

// ArchiveFileCRCsFrom7zParts lists inner-file CRC32 values from a 7z start
// header plus either a complete archive blob or a last-volume tail.
func ArchiveFileCRCsFrom7zParts(start, tail []byte) []ArchiveFileCRC {
	st, ok := Parse7zStart(start)
	if !ok {
		return nil
	}
	if _, complete := Parse7zStart(tail); complete {
		return sevenZFiles(tail)
	}
	if uint64(len(tail)) < st.Size {
		return nil
	}
	hdr := tail[uint64(len(tail))-st.Size:]
	if st.CRC != 0 && crc32.ChecksumIEEE(hdr) != st.CRC {
		return nil
	}
	return sevenZListFiles(start[:32], hdr)
}

func sevenZListFiles(start, header []byte) []ArchiveFileCRC {
	st, ok := Parse7zStart(start)
	if !ok || uint64(len(header)) != st.Size {
		return nil
	}
	bin, err := lookPath("7z")
	if err != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "sakms-7z-crc-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "a.7z")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil
	}
	if _, err := f.Write(start[:32]); err != nil {
		f.Close()
		return nil
	}
	if err := f.Truncate(int64(32 + st.Offset + st.Size)); err != nil {
		f.Close()
		return nil
	}
	if _, err := f.WriteAt(header, int64(32+st.Offset)); err != nil {
		f.Close()
		return nil
	}
	if err := f.Close(); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), sevenZListTimeout)
	defer cancel()
	cmd := unpackCommand(ctx, bin, "l", "-slt", "-bd", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	return parse7zSLT(string(out))
}

func parse7zSLT(out string) []ArchiveFileCRC {
	const sep = "----------"
	i := strings.Index(out, sep)
	if i < 0 {
		return nil
	}
	var files []ArchiveFileCRC
	var cur ArchiveFileCRC
	enc := false
	flush := func() {
		if !enc && cur.CRC != "" && cur.CRC != "00000000" {
			files = append(files, cur)
		}
		cur = ArchiveFileCRC{}
		enc = false
	}
	for _, line := range strings.Split(out[i+len(sep):], "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "Path = "):
			if cur.Name != "" || cur.CRC != "" || enc {
				flush()
			}
			cur.Name = strings.TrimPrefix(line, "Path = ")
		case strings.HasPrefix(line, "CRC = "):
			cur.CRC = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(line, "CRC = ")))
		case strings.HasPrefix(line, "Encrypted = "):
			enc = strings.TrimSpace(strings.TrimPrefix(line, "Encrypted = ")) == "+"
		}
		if len(files) >= 4 {
			break
		}
	}
	flush()
	return files
}
