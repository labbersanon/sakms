package usenet

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Claude 2026-10-11: SAB/NZBGet-compatible password sources for unrar.
// Reason: password RARs (Smad hash sets) burned a full BODY then stalled on
//   stdin; when the indexer/title already carries the password, unpack should
//   use it instead of parking.
// Troubleshooting: journal "password-protected archive"; Requests password reason.
// Review if: a UI per-job password field is added.
// Related: unpack.go runUnrar -p; nzb.go X-DNZB-Password + <meta type="password">.

var (
	titleBracePW    = regexp.MustCompile(`\{\{([^{}]+)\}\}`)
	titlePasswordEq = regexp.MustCompile(`(?i)(?:^|[\s.])password=(\S+)`)
)

// SplitTitlePassword pulls a SAB-style password out of an NZB/job name.
// Title is the leftover display name. Password is empty when none is embedded.
//
// Recognised forms: `Name {{pw}}.nzb`, `Name password=pw`, `Name / pw`.
// The Newznab placeholder `{{password}}` is not a real password.
func SplitTitlePassword(name string) (title, password string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ""
	}
	if m := titleBracePW.FindStringSubmatch(name); m != nil {
		pw := strings.TrimSpace(m[1])
		if pw != "" && !strings.EqualFold(pw, "password") {
			rest := strings.TrimSpace(titleBracePW.ReplaceAllString(name, " "))
			return trimJobName(rest), pw
		}
	}
	if m := titlePasswordEq.FindStringSubmatch(name); m != nil {
		pw := strings.TrimSuffix(m[1], ".nzb")
		pw = strings.Trim(pw, ".")
		rest := strings.TrimSpace(titlePasswordEq.ReplaceAllString(name, " "))
		return trimJobName(rest), pw
	}
	if i := strings.LastIndex(name, " / "); i >= 0 {
		pw := strings.TrimSpace(name[i+3:])
		if pw != "" && !strings.Contains(strings.ToLower(pw), "password") {
			return strings.TrimSpace(name[:i]), pw
		}
	}
	return name, ""
}

func trimJobName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".nzb")
	s = strings.TrimSuffix(s, ".NZB")
	s = strings.Trim(s, ".-_ ")
	return strings.Join(strings.Fields(s), " ")
}

// nzbMetaValue returns the first NZB <meta type="..."> value, or "".
func nzbMetaValue(nzb *NZB, typ string) string {
	if nzb == nil {
		return ""
	}
	want := strings.ToLower(strings.TrimSpace(typ))
	for _, m := range nzb.Meta {
		if strings.EqualFold(strings.TrimSpace(m.Type), want) {
			if v := strings.TrimSpace(m.Value); v != "" {
				return v
			}
		}
	}
	return ""
}

func collectJobPasswords(dnzb DNZBHeaders, nzb *NZB, name string, extra []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(pw string) {
		pw = strings.TrimSpace(pw)
		if pw == "" || seen[pw] {
			return
		}
		seen[pw] = true
		out = append(out, pw)
	}
	add(dnzb.Password)
	add(nzbMetaValue(nzb, "password"))
	if _, pw := SplitTitlePassword(name); pw != "" {
		add(pw)
	}
	if t := nzbMetaValue(nzb, "title"); t != "" {
		if _, pw := SplitTitlePassword(t); pw != "" {
			add(pw)
		}
	}
	if dnzb.Name != "" {
		if _, pw := SplitTitlePassword(dnzb.Name); pw != "" {
			add(pw)
		}
	}
	for _, pw := range extra {
		add(pw)
	}
	return out
}

func stagingPasswords(dir string) []string {
	var out []string
	for _, name := range []string{"password.txt", "pass.txt", "passwords.txt"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			out = append(out, line)
		}
	}
	return out
}

func mergePasswords(groups ...[]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, g := range groups {
		for _, pw := range g {
			pw = strings.TrimSpace(pw)
			if pw == "" || seen[pw] {
				continue
			}
			seen[pw] = true
			out = append(out, pw)
		}
	}
	return out
}

func preferredNZBName(passed string, dnzb DNZBHeaders, nzb *NZB) string {
	if s := strings.TrimSpace(dnzb.Name); s != "" {
		title, _ := SplitTitlePassword(s)
		if title != "" {
			return title
		}
		return s
	}
	if s := nzbMetaValue(nzb, "title"); s != "" {
		title, _ := SplitTitlePassword(s)
		if title != "" {
			return title
		}
		return s
	}
	if s := strings.TrimSpace(passed); s != "" {
		title, _ := SplitTitlePassword(s)
		if title != "" {
			return title
		}
		return s
	}
	return "usenet-download"
}
