package usenet

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const (
	postPasswordPeekTimeout = 10 * time.Second
	maxPostPasswordFiles    = 6
	maxPostPasswordSegs     = 3
	maxPostPasswordBytes    = 128 << 10
)

// Claude 2026-10-11: read THIS NZB's nfo/txt articles for an unpack password.
// Reason: the poster usually puts the password on the Usenet post (subject or
//
//	a small plaintext companion), not on a sibling indexer title. Discover is
//	the fallback after this collection's own articles.
//
// Troubleshooting: journal "usenet: post articles yielded N unpack password
//
//	candidate(s)"; nfo/txt BODY before encrypted-RAR abort.
//
// Review if: a per-job UI password field replaces article peek.
func (m *Manager) peekPostPasswords(ctx context.Context, nzb *NZB, gid string) {
	if m == nil || nzb == nil || len(m.currentPools()) == 0 {
		return
	}
	if gid != "" {
		m.mu.Lock()
		dl := m.downloads[gid]
		hasJob := dl != nil && (len(dl.passwords) > 0 || len(stagingPasswords(dl.stagingDir)) > 0)
		m.mu.Unlock()
		if hasJob {
			return
		}
	}
	targets := textPasswordTargets(nzb)
	if len(targets) == 0 {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, postPasswordPeekTimeout)
	defer cancel()
	var found []string
	for _, t := range targets {
		if pctx.Err() != nil {
			break
		}
		seg, err := m.fetchSegmentAny(pctx, t.msgID)
		if err != nil {
			continue
		}
		found = append(found, passwordsFromArticleText(seg.data)...)
		name := strings.ToLower(t.name)
		if strings.Contains(name, "password") {
			found = append(found, passwordsFromPasswordFile(seg.data)...)
		}
	}
	found = mergePasswords(found)
	if len(found) == 0 {
		return
	}
	dir := ""
	m.mu.Lock()
	if dl := m.downloads[gid]; dl != nil {
		dl.passwords = mergePasswords(dl.passwords, found)
		dir = dl.stagingDir
	}
	m.mu.Unlock()
	writeStagingPasswords(dir, found)
	log.Printf("usenet: post articles yielded %d unpack password candidate(s) for %s", len(found), gid)
}

type textPasswordTarget struct {
	msgID string
	name  string
}

func textPasswordTargets(nzb *NZB) []textPasswordTarget {
	if nzb == nil {
		return nil
	}
	var out []textPasswordTarget
	for _, f := range nzb.Files {
		if !isTextPasswordFile(f.Subject) {
			continue
		}
		if len(f.Segs) == 0 || len(f.Segs) > maxPostPasswordSegs {
			continue
		}
		var bytes int64
		for _, s := range f.Segs {
			bytes += s.Bytes
		}
		if bytes > maxPostPasswordBytes {
			continue
		}
		id := strings.TrimSpace(f.Segs[0].MsgID)
		if id == "" {
			continue
		}
		name := filenameFromSubject(f.Subject)
		if name == "" {
			name = f.Subject
		}
		out = append(out, textPasswordTarget{msgID: id, name: name})
		if len(out) >= maxPostPasswordFiles {
			break
		}
	}
	return out
}

func isTextPasswordFile(subject string) bool {
	name := strings.ToLower(filenameFromSubject(subject))
	if name == "" {
		name = strings.ToLower(subject)
	}
	base := filepath.Base(name)
	switch {
	case strings.HasSuffix(base, ".nfo"), strings.HasSuffix(base, ".txt"):
		return true
	case strings.Contains(base, "password") && !strings.HasSuffix(base, ".rar") && !strings.HasSuffix(base, ".zip"):
		return true
	default:
		return false
	}
}

func passwordsFromArticleText(data []byte) []string {
	if looksBinaryPayload(data) {
		return nil
	}
	text := strings.ToValidUTF8(string(data), "")
	var out []string
	if _, pw := SplitTitlePassword(text); pw != "" {
		out = append(out, pw)
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.Trim(line, "\r"))
		if line == "" {
			continue
		}
		if _, pw := SplitTitlePassword(line); pw != "" {
			out = append(out, pw)
		}
		if m := articlePasswordRE.FindStringSubmatch(line); m != nil {
			if pw := cleanExtractedPassword(m[1]); pw != "" {
				out = append(out, pw)
			}
		}
	}
	return mergePasswords(out)
}

func passwordsFromPasswordFile(data []byte) []string {
	if looksBinaryPayload(data) {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.Trim(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsAny(line, " \t") && len(line) > 40 {
			continue
		}
		if pw := cleanExtractedPassword(line); pw != "" {
			out = append(out, pw)
		}
	}
	return mergePasswords(out)
}

func cleanExtractedPassword(pw string) string {
	pw = strings.TrimSpace(pw)
	pw = strings.Trim(pw, `"'.,;:`)
	if pw == "" || len(pw) > 128 {
		return ""
	}
	low := strings.ToLower(pw)
	if low == "password" || strings.Contains(low, "protect") || strings.Contains(low, "encrypt") {
		return ""
	}
	if strings.Contains(low, "http") {
		return ""
	}
	for _, r := range pw {
		if r < 32 || r == 127 || (!unicode.IsPrint(r) && !unicode.IsSpace(r)) {
			return ""
		}
	}
	return pw
}

func looksBinaryPayload(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	if bytes.HasPrefix(data, []byte("Rar!")) || bytes.HasPrefix(data, []byte("PAR2")) || bytes.HasPrefix(data, []byte("PK")) {
		return true
	}
	n := len(data)
	if n > 2048 {
		n = 2048
	}
	nuls := 0
	for _, b := range data[:n] {
		if b == 0 {
			nuls++
		}
	}
	return nuls > n/10
}
