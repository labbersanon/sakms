package usenet

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"unicode/utf16"

	"github.com/ulikunitz/xz/lzma"
)

// Claude 2026-10-20: parse 7z kHeader / kEncodedHeader in-process (no 7z l).
// Reason: live nid=0x17 mini=374 still `7z l` 255. After LZMA decode, the
//   kHeader PackPos still describes the 23GiB main streams, so 7z seeks
//   holes. CRC32 and UTF-16 names live in that header; we never need the
//   packed file data.
// Troubleshooting: namefix crc crcs=0 with `7z header miss nid=` → AES
//   header (-mhe), packLen > last-segment tail, or LZMA props we don't
//   decode. Review if: encoded headers start using a non-LZMA coder.
// Related files: archive_names.go, namefix.go pickBodyTargets

var sevenZMagic = []byte("7z\xbc\xaf\x27\x1c")

const (
	max7zPackedOffset = 1 << 40 // 1 TiB apparent packed size
	max7zHeaderSize   = 4 << 20
	max7zLZMADict     = 16 << 20
	max7zParseNIDs    = 32
)

var (
	sevenZLZMA  = []byte{0x03, 0x01, 0x01}
	sevenZLZMA2 = []byte{0x21}
	sevenZCopy  = []byte{0x00}
	sevenZAES   = []byte{0x06, 0xf1, 0x07, 0x01}
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
	hdr := data[32+st.Offset : end]
	if st.CRC != 0 && crc32.ChecksumIEEE(hdr) != st.CRC {
		return nil
	}
	return sevenZFilesFromHeader(hdr, data[32:32+st.Offset])
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
	files := sevenZFilesFromHeader(hdr, tail[:uint64(len(tail))-st.Size])
	if len(files) == 0 {
		sevenZMiss(st, tail, hdr)
	}
	return files
}

func sevenZMiss(st SevenZStart, tail, hdr []byte) {
	n := 80
	if len(hdr) < n {
		n = len(hdr)
	}
	nid := byte(0)
	if len(hdr) > 0 {
		nid = hdr[0]
	}
	log.Printf("usenet: 7z header miss off=%d sz=%d tail=%d nid=0x%x parse hdr=%x",
		st.Offset, st.Size, len(tail), nid, hdr[:n])
}

func sevenZFilesFromHeader(hdr, packedBefore []byte) []ArchiveFileCRC {
	if len(hdr) == 0 {
		return nil
	}
	switch hdr[0] {
	case 0x01:
		return parse7zPlainHeader(hdr)
	case 0x17:
		decoded := decode7zEncodedHeader(hdr, packedBefore)
		if len(decoded) == 0 {
			return nil
		}
		return parse7zPlainHeader(decoded)
	default:
		return nil
	}
}

func decode7zEncodedHeader(hdr, packedBefore []byte) []byte {
	si, _, ok := parse7zStreams(hdr, 1)
	if !ok || len(si.folders) != 1 {
		return nil
	}
	var packLen uint64
	for _, sz := range si.packSizes {
		packLen += sz
	}
	if packLen == 0 || packLen > max7zHeaderSize || uint64(len(packedBefore)) < packLen {
		log.Printf("usenet: 7z encoded packLen=%d before=%d folders=%d", packLen, len(packedBefore), len(si.folders))
		return nil
	}
	packed := packedBefore[uint64(len(packedBefore))-packLen:]
	out, err := unpack7zFolder(si.folders[0], packed)
	if err != nil {
		log.Printf("usenet: 7z encoded lzma pack=%d unpack=%d method=%x err=%v",
			len(packed), si.folders[0].unpackSize, si.folders[0].method, err)
		return nil
	}
	return out
}

func unpack7zFolder(f sevenZFolder, packed []byte) ([]byte, error) {
	if f.encrypted {
		plain, err := decrypt7zAESEmpty(f.aesProps, packed)
		if err != nil {
			return nil, err
		}
		if len(f.unpackSizes) >= 2 && f.unpackSizes[0] > 0 && f.unpackSizes[0] <= uint64(len(plain)) {
			plain = plain[:f.unpackSizes[0]]
		}
		packed = plain
		if len(f.method) == 0 {
			if f.unpackSize == 0 || f.unpackSize > uint64(len(packed)) {
				return packed, nil
			}
			return packed[:f.unpackSize], nil
		}
	}
	if f.unpackSize == 0 || f.unpackSize > max7zHeaderSize {
		return nil, fmt.Errorf("unpack size %d", f.unpackSize)
	}
	switch {
	case bytes.Equal(f.method, sevenZCopy):
		if uint64(len(packed)) < f.unpackSize {
			return nil, io.ErrUnexpectedEOF
		}
		return packed[:f.unpackSize], nil
	case bytes.Equal(f.method, sevenZLZMA):
		if len(f.props) < 5 {
			return nil, fmt.Errorf("lzma props %d", len(f.props))
		}
		hdr := make([]byte, lzma.HeaderLen)
		copy(hdr[:5], f.props[:5])
		binary.LittleEndian.PutUint64(hdr[5:], f.unpackSize)
		r, err := lzma.ReaderConfig{DictCap: lzmaDictCap(f.props)}.NewReader(
			io.MultiReader(bytes.NewReader(hdr), bytes.NewReader(packed)))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(io.LimitReader(r, int64(f.unpackSize)))
	case bytes.Equal(f.method, sevenZLZMA2):
		if len(f.props) < 1 {
			return nil, fmt.Errorf("lzma2 props")
		}
		dict := lzma2DictCap(f.props[0])
		r, err := lzma.Reader2Config{DictCap: dict}.NewReader2(bytes.NewReader(packed))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(io.LimitReader(r, int64(f.unpackSize)))
	default:
		return nil, fmt.Errorf("method %x", f.method)
	}
}

func lzmaDictCap(props []byte) int {
	n := 4096
	if len(props) >= 5 {
		if d := int(binary.LittleEndian.Uint32(props[1:5])); d > n {
			n = d
		}
	}
	if n > max7zLZMADict {
		return max7zLZMADict
	}
	return n
}

func lzma2DictCap(p byte) int {
	if p == 40 {
		return max7zLZMADict
	}
	n := int((uint32(2 | (p & 1))) << (p/2 + 11))
	if n < 4096 {
		return 4096
	}
	if n > max7zLZMADict {
		return max7zLZMADict
	}
	return n
}

type sevenZFolder struct {
	method      []byte
	props       []byte
	aesProps    []byte
	unpackSizes []uint64
	unpackSize  uint64
	crc         uint32
	crcOK       bool
	encrypted   bool
	outStreams  int
}

type sevenZStreams struct {
	packSizes []uint64
	folders   []sevenZFolder
	fileCRCs  []uint32
}

func parse7zStreams(b []byte, i int) (sevenZStreams, int, bool) {
	var si sevenZStreams
	nids := 0
	for i < len(b) && nids < max7zParseNIDs {
		nids++
		nid := b[i]
		i++
		switch nid {
		case 0x00:
			return si, i, true
		case 0x06:
			var ok bool
			si.packSizes, i, ok = parse7zPackInfo(b, i)
			if !ok {
				return sevenZStreams{}, i, false
			}
		case 0x07:
			var ok bool
			si.folders, i, ok = parse7zUnpackInfo(b, i)
			if !ok {
				return sevenZStreams{}, i, false
			}
		case 0x08:
			var ok bool
			si.fileCRCs, i, ok = parse7zSubStreams(b, i, si.folders)
			if !ok {
				return sevenZStreams{}, i, false
			}
		default:
			return sevenZStreams{}, i, false
		}
	}
	return sevenZStreams{}, i, false
}

func parse7zPackInfo(b []byte, i int) ([]uint64, int, bool) {
	_, i, ok := read7zUint64(b, i) // PackPos
	if !ok {
		return nil, i, false
	}
	num, i, ok := read7zUint64(b, i)
	if !ok || num == 0 || num > 8 {
		return nil, i, false
	}
	var sizes []uint64
	for n := 0; n < max7zParseNIDs && i < len(b); n++ {
		nid := b[i]
		i++
		switch nid {
		case 0x00:
			if sizes == nil {
				return nil, i, false
			}
			return sizes, i, true
		case 0x09:
			sizes = make([]uint64, num)
			for k := uint64(0); k < num; k++ {
				sz, ni, ok := read7zUint64(b, i)
				// Claude 2026-10-20: main-stream sizes are tens of GiB.
				// Reason: this parser also walks kHeader PackInfo after
				//   LZMA-decoding kEncodedHeader; capping at 4MiB (header
				//   packed size) dropped live ngPost CRCs. Encoded-header
				//   packLen is capped in decode7zEncodedHeader.
				// Troubleshooting: 7z header miss after nid=0x17 decode →
				//   this bound. Review if: max7zPackedOffset changes.
				if !ok || sz > max7zPackedOffset {
					return nil, i, false
				}
				sizes[k] = sz
				i = ni
			}
		case 0x0A:
			_, _, ni, ok := read7zDigests(b, i, int(num))
			if !ok {
				return nil, i, false
			}
			i = ni
		default:
			return nil, i, false
		}
	}
	return nil, i, false
}

func parse7zUnpackInfo(b []byte, i int) ([]sevenZFolder, int, bool) {
	if i >= len(b) || b[i] != 0x0B {
		return nil, i, false
	}
	i++
	nfold, i, ok := read7zUint64(b, i)
	if !ok || nfold == 0 || nfold > 8 {
		return nil, i, false
	}
	if i >= len(b) {
		return nil, i, false
	}
	if b[i] != 0 { // External folders
		return nil, i, false
	}
	i++
	folders := make([]sevenZFolder, nfold)
	for f := range folders {
		var ok bool
		folders[f], i, ok = parse7zFolder(b, i)
		if !ok {
			return nil, i, false
		}
	}
	if i >= len(b) || b[i] != 0x0C {
		return nil, i, false
	}
	i++
	for f := range folders {
		nout := folders[f].outStreams
		if nout < 1 {
			nout = 1
		}
		for s := 0; s < nout; s++ {
			sz, ni, ok := read7zUint64(b, i)
			if !ok {
				return nil, i, false
			}
			folders[f].unpackSizes = append(folders[f].unpackSizes, sz)
			i = ni
		}
		folders[f].unpackSize = folders[f].unpackSizes[len(folders[f].unpackSizes)-1]
	}
	for n := 0; n < max7zParseNIDs && i < len(b); n++ {
		nid := b[i]
		i++
		switch nid {
		case 0x00:
			return folders, i, true
		case 0x0A:
			crcs, defined, ni, ok := read7zDigests(b, i, len(folders))
			if !ok {
				return nil, i, false
			}
			i = ni
			for k := range folders {
				if defined[k] {
					folders[k].crc = crcs[k]
					folders[k].crcOK = true
				}
			}
		default:
			return nil, i, false
		}
	}
	return nil, i, false
}

func parse7zFolder(b []byte, i int) (sevenZFolder, int, bool) {
	var f sevenZFolder
	nc, i, ok := read7zUint64(b, i)
	if !ok || nc == 0 || nc > 4 {
		return f, i, false
	}
	var totalIn, totalOut uint64
	for c := uint64(0); c < nc; c++ {
		if i >= len(b) {
			return f, i, false
		}
		flags := b[i]
		i++
		idSize := int(flags & 0x0F)
		complex := flags&0x10 != 0
		hasAttrs := flags&0x20 != 0
		if i+idSize > len(b) {
			return f, i, false
		}
		method := append([]byte(nil), b[i:i+idSize]...)
		i += idSize
		nin, nout := uint64(1), uint64(1)
		if complex {
			nin, i, ok = read7zUint64(b, i)
			if !ok {
				return f, i, false
			}
			nout, i, ok = read7zUint64(b, i)
			if !ok {
				return f, i, false
			}
		}
		var props []byte
		if hasAttrs {
			psz, ni, ok := read7zUint64(b, i)
			if !ok || psz > 64 {
				return f, i, false
			}
			i = ni
			if uint64(i)+psz > uint64(len(b)) {
				return f, i, false
			}
			props = append([]byte(nil), b[i:uint64(i)+psz]...)
			i += int(psz)
		}
		totalIn += nin
		totalOut += nout
		if bytes.HasPrefix(method, sevenZAES) {
			f.encrypted = true
			f.aesProps = props
			continue
		}
		f.method = method
		f.props = props
	}
	if totalOut == 0 {
		return f, i, false
	}
	f.outStreams = int(totalOut)
	nbind := totalOut - 1
	for k := uint64(0); k < nbind; k++ {
		_, i, ok = read7zUint64(b, i)
		if !ok {
			return f, i, false
		}
		_, i, ok = read7zUint64(b, i)
		if !ok {
			return f, i, false
		}
	}
	npack := totalIn - nbind
	if npack > 1 {
		for k := uint64(0); k < npack; k++ {
			_, i, ok = read7zUint64(b, i)
			if !ok {
				return f, i, false
			}
		}
	}
	return f, i, true
}

func parse7zSubStreams(b []byte, i int, folders []sevenZFolder) ([]uint32, int, bool) {
	numInFolders := make([]int, len(folders))
	for k := range numInFolders {
		numInFolders[k] = 1
	}
	haveNum := false
	var subCRCs []uint32
	var subDef []bool
	for n := 0; n < max7zParseNIDs && i < len(b); n++ {
		nid := b[i]
		i++
		switch nid {
		case 0x00:
			return assemble7zFileCRCs(folders, numInFolders, subCRCs, subDef), i, true
		case 0x0D:
			haveNum = true
			for k := range folders {
				ns, ni, ok := read7zUint64(b, i)
				if !ok || ns == 0 || ns > 64 {
					return nil, i, false
				}
				numInFolders[k] = int(ns)
				i = ni
			}
		case 0x09:
			for _, ns := range numInFolders {
				for j := 1; j < ns; j++ {
					_, ni, ok := read7zUint64(b, i)
					if !ok {
						return nil, i, false
					}
					i = ni
				}
			}
		case 0x0A:
			ncrc := 0
			for k, ns := range numInFolders {
				if ns != 1 || !folders[k].crcOK {
					ncrc += ns
				}
			}
			if !haveNum && ncrc == 0 {
				ncrc = len(folders)
			}
			var ok bool
			subCRCs, subDef, i, ok = read7zDigests(b, i, ncrc)
			if !ok {
				return nil, i, false
			}
		default:
			return nil, i, false
		}
	}
	return nil, i, false
}

func assemble7zFileCRCs(folders []sevenZFolder, numInFolders []int, subCRCs []uint32, subDef []bool) []uint32 {
	var out []uint32
	sub := 0
	for k, f := range folders {
		ns := 1
		if k < len(numInFolders) {
			ns = numInFolders[k]
		}
		for s := 0; s < ns; s++ {
			if ns == 1 && f.crcOK {
				out = append(out, f.crc)
				continue
			}
			if sub < len(subCRCs) && (subDef == nil || subDef[sub]) {
				out = append(out, subCRCs[sub])
			} else {
				out = append(out, 0)
			}
			sub++
		}
	}
	if len(out) == 0 {
		return subCRCs
	}
	return out
}

func parse7zPlainHeader(hdr []byte) []ArchiveFileCRC {
	if len(hdr) < 2 || hdr[0] != 0x01 {
		return nil
	}
	i := 1
	var names []string
	empty := []bool{}
	var fileCRCs []uint32
	for n := 0; n < max7zParseNIDs && i < len(hdr); n++ {
		nid := hdr[i]
		i++
		switch nid {
		case 0x00:
			return pair7zFiles(names, empty, fileCRCs)
		case 0x02:
			var ok bool
			i, ok = skip7zArchiveProps(hdr, i)
			if !ok {
				return nil
			}
		case 0x03:
			_, ni, ok := parse7zStreams(hdr, i)
			if !ok {
				return nil
			}
			i = ni
		case 0x04:
			si, ni, ok := parse7zStreams(hdr, i)
			if !ok {
				return nil
			}
			i = ni
			fileCRCs = si.fileCRCs
			if len(fileCRCs) == 0 {
				for _, f := range si.folders {
					if f.crcOK {
						fileCRCs = append(fileCRCs, f.crc)
					}
				}
			}
		case 0x05:
			var ok bool
			names, empty, i, ok = parse7zFilesInfo(hdr, i)
			if !ok {
				return nil
			}
		default:
			return nil
		}
	}
	return nil
}

func skip7zArchiveProps(b []byte, i int) (int, bool) {
	for n := 0; n < max7zParseNIDs && i < len(b); n++ {
		if b[i] == 0 {
			return i + 1, true
		}
		i++
		sz, ni, ok := read7zUint64(b, i)
		if !ok || uint64(ni)+sz > uint64(len(b)) {
			return i, false
		}
		i = ni + int(sz)
	}
	return i, false
}

func parse7zFilesInfo(b []byte, i int) ([]string, []bool, int, bool) {
	nfiles, i, ok := read7zUint64(b, i)
	if !ok || nfiles == 0 || nfiles > 64 {
		return nil, nil, i, false
	}
	n := int(nfiles)
	empty := make([]bool, n)
	var names []string
	for p := 0; p < max7zParseNIDs && i < len(b); p++ {
		nid := b[i]
		i++
		if nid == 0x00 {
			return names, empty, i, true
		}
		sz, ni, ok := read7zUint64(b, i)
		if !ok || uint64(ni)+sz > uint64(len(b)) {
			return nil, nil, i, false
		}
		i = ni
		data := b[i : uint64(i)+sz]
		i += int(sz)
		switch nid {
		case 0x0E:
			bits, _, ok := read7zBits(data, 0, n)
			if !ok {
				return nil, nil, i, false
			}
			copy(empty, bits)
		case 0x11:
			names = decode7zNames(data, n)
		}
	}
	return nil, nil, i, false
}

func decode7zNames(data []byte, nfiles int) []string {
	if len(data) < 3 || data[0] != 0 {
		return nil
	}
	var names []string
	var cur []uint16
	for i := 1; i+1 < len(data) && len(names) < nfiles; i += 2 {
		w := binary.LittleEndian.Uint16(data[i : i+2])
		if w == 0 {
			names = append(names, string(utf16.Decode(cur)))
			cur = cur[:0]
			continue
		}
		cur = append(cur, w)
	}
	return names
}

func pair7zFiles(names []string, empty []bool, crcs []uint32) []ArchiveFileCRC {
	var files []ArchiveFileCRC
	ci := 0
	n := len(names)
	if len(empty) > n {
		n = len(empty)
	}
	if n == 0 {
		for _, c := range crcs {
			if c == 0 {
				continue
			}
			files = append(files, ArchiveFileCRC{CRC: fmt.Sprintf("%08X", c)})
			if len(files) >= 4 {
				break
			}
		}
		return files
	}
	for i := 0; i < n; i++ {
		if i < len(empty) && empty[i] {
			continue
		}
		f := ArchiveFileCRC{}
		if i < len(names) {
			f.Name = names[i]
		}
		if ci < len(crcs) && crcs[ci] != 0 {
			f.CRC = fmt.Sprintf("%08X", crcs[ci])
		}
		ci++
		if f.CRC == "" && f.Name == "" {
			continue
		}
		files = append(files, f)
		if len(files) >= 4 {
			break
		}
	}
	return files
}

func read7zBits(b []byte, i, n int) ([]bool, int, bool) {
	if n < 0 {
		return nil, i, false
	}
	need := (n + 7) / 8
	if i+need > len(b) {
		return nil, i, false
	}
	out := make([]bool, n)
	for k := 0; k < n; k++ {
		out[k] = b[i+k/8]&(0x80>>uint(k%8)) != 0
	}
	return out, i + need, true
}

func read7zDigests(b []byte, i, n int) ([]uint32, []bool, int, bool) {
	if n < 0 || i >= len(b) {
		return nil, nil, i, false
	}
	all := b[i] != 0
	i++
	defined := make([]bool, n)
	if all {
		for k := range defined {
			defined[k] = true
		}
	} else {
		var ok bool
		defined, i, ok = read7zBits(b, i, n)
		if !ok {
			return nil, nil, i, false
		}
	}
	crcs := make([]uint32, n)
	for k := 0; k < n; k++ {
		if !defined[k] {
			continue
		}
		if i+4 > len(b) {
			return nil, nil, i, false
		}
		crcs[k] = binary.LittleEndian.Uint32(b[i : i+4])
		i += 4
	}
	return crcs, defined, i, true
}

func read7zUint64(b []byte, i int) (uint64, int, bool) {
	if i >= len(b) {
		return 0, i, false
	}
	first := b[i]
	i++
	mask := byte(0x80)
	var value uint64
	for n := 0; n < 8; n++ {
		if first&mask == 0 {
			high := uint64(first & (mask - 1))
			return value | high<<(8*n), i, true
		}
		if i >= len(b) {
			return 0, i, false
		}
		value |= uint64(b[i]) << (8 * n)
		i++
		mask >>= 1
	}
	return value, i, true
}
