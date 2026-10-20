package usenetsearch

import (
	"strings"

	"github.com/labbersanon/sakms/internal/usenet"
)

// Claude 2026-10-20: classify obfuscation + compression before BODY.
// Reason: namefix treated ngPost hash.7z, RAR5, and hash.par2 as one
//   path. .7z.001 is packed data (no CRC); the footer is AES or LZMA.
//   A detector labels the set and caps the first 7z volume at 64 bytes.
// Troubleshooting: namefix pattern obf= pack= missing → this not logged.
//   payload=7z-aes with crcs=0 → empty password miss, not a parser miss.
// Review if: a new poster family (nzb.su random + rar) needs a pack tag.
// Related files: namefix.go pickBodyTargets, sevenz.go SevenZHeaderKind

// namePattern is the posting's obfuscation scheme and archive family.
type namePattern struct {
	Obf  string // hash-7z, hash-rar, hash-par2, quoted, ident, mixed
	Pack string // 7z-split, rar-split, zip-split, 7z-enc, par2, none
	Ext  string // unique suffixes, comma-joined
}

func detectNamePattern(rows []HeaderRow) namePattern {
	var has7z, has7zSplit, has7zEnc, hasRar, hasRarSplit, hasZip, hasSFV, hasPar2 bool
	obf, ident, quoted := 0, 0, 0
	seen := map[string]bool{}
	var ext []string
	addExt := func(e string) {
		if seen[e] {
			return
		}
		seen[e] = true
		ext = append(ext, e)
	}
	for _, r := range rows {
		n := r.Filename
		if n == "" {
			n = r.Subject
		}
		low := strings.ToLower(n)
		switch {
		case strings.Contains(low, ".7z.enc"):
			has7zEnc = true
			addExt("7z.enc")
		case reSplitVol.MatchString(low) && strings.Contains(low, ".7z."):
			has7z, has7zSplit = true, true
			addExt("7z")
		case strings.Contains(low, ".7z"):
			has7z = true
			addExt("7z")
		}
		if strings.Contains(low, ".rar") || reRarFollow.MatchString(low) {
			hasRar = true
			addExt("rar")
			if archivePartNum(low) > 0 || reRarFollow.MatchString(low) {
				hasRarSplit = true
			}
		}
		if strings.Contains(low, ".zip") {
			hasZip = true
			addExt("zip")
		}
		if strings.Contains(low, ".sfv") {
			hasSFV = true
			addExt("sfv")
		}
		if strings.Contains(low, ".par2") {
			hasPar2 = true
			addExt("par2")
		}
		if strings.Contains(low, ".nfo") {
			addExt("nfo")
		}
		stem := obfSkeleton(n)
		switch {
		case quotedReleaseName(stem) || quotedReleaseName(n):
			quoted++
		case tokenLooksObfuscated(stem) || looksObfuscated(stem) || hashCore(stem) != "" || len(hashTokens(n)) > 0:
			obf++
		case stem != "":
			ident++
		}
	}
	p := namePattern{Ext: strings.Join(ext, ",")}
	switch {
	case has7zEnc && !has7zSplit && !has7z:
		p.Pack = "7z-enc"
	case has7zSplit || has7z:
		p.Pack = "7z-split"
	case hasRarSplit || hasRar:
		p.Pack = "rar-split"
	case hasZip:
		p.Pack = "zip-split"
	case hasSFV:
		p.Pack = "sfv"
	case hasPar2:
		p.Pack = "par2"
	default:
		p.Pack = "none"
	}
	switch {
	case obf > 0 && ident == 0 && quoted == 0 && p.Pack == "7z-split":
		p.Obf = "hash-7z"
	case obf > 0 && ident == 0 && quoted == 0 && (p.Pack == "rar-split" || p.Pack == "zip-split"):
		p.Obf = "hash-rar"
	case obf > 0 && ident == 0 && quoted == 0 && p.Pack == "par2":
		p.Obf = "hash-par2"
	case quoted > 0 && obf == 0:
		p.Obf = "quoted"
	case ident > 0 && obf == 0:
		p.Obf = "ident"
	case obf > 0 && ident == 0 && quoted == 0:
		p.Obf = "hash"
	default:
		p.Obf = "mixed"
	}
	return p
}

func payloadKind(data, zstart []byte, zsize uint64) string {
	kind := usenet.PayloadKind(data)
	if zsize == 0 || uint64(len(data)) < zsize {
		return kind
	}
	if k := usenet.SevenZHeaderKind(data[uint64(len(data))-zsize:]); k != "" && k != "7z-packed" {
		return k
	}
	if kind == "unknown" && len(zstart) == 32 {
		return "7z-packed"
	}
	return kind
}
