package disc

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

var volumeIDRe = regexp.MustCompile(`(?m)^VolumeId:\s*(.+?)\s*$`)

func volumeOf(ctx context.Context, src string) string {
	if v := volumeFrom7z(ctx, src); v != "" {
		return sanitizeVolume(v)
	}
	if v := volumeFromPVD(src); v != "" {
		return sanitizeVolume(v)
	}
	return sanitizeVolume(strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)))
}

func volumeFrom7z(ctx context.Context, src string) string {
	bin, err := lookPath("7z")
	if err != nil {
		return ""
	}
	cmd := runCmd(ctx, bin, "l", src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	m := volumeIDRe.FindSubmatch(out)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

func volumeFromPVD(src string) string {
	f, err := os.Open(src)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 80)
	if _, err := f.ReadAt(buf, 32768); err != nil {
		return ""
	}
	if len(buf) < 45 || string(buf[1:6]) != "CD001" {
		return ""
	}
	return strings.TrimSpace(strings.TrimRight(string(buf[40:72]), "\x00 "))
}

func sanitizeVolume(raw string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			b.WriteByte('_')
		default:
			if unicode.IsControl(r) {
				continue
			}
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	s = strings.Trim(s, ".")
	if s == "" {
		return "DVD"
	}
	return s
}
