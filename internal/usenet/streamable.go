package usenet

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// FilePlan is one NZB file in download order (message-IDs only, no URLs).
type FilePlan struct {
	Name   string   `json:"name"`
	MsgIDs []string `json:"msgids"`
}

// SequencePlan is the On Demand BODY order: playable files first, PAR2 last.
type SequencePlan struct {
	Play []FilePlan `json:"play"`
	Par2 []FilePlan `json:"par2"`
}

// Assessment is XML-only streamability: sequential RAR or a raw video file.
type Assessment struct {
	OK       bool
	Reason   string
	Plan     SequencePlan
	Files    int
	Segments int
	Bytes    int64
}

// AssessNZB decides whether an NZB can start-while-downloading.
// 7z-solid, par2-only, and gapped segment numbers are rejected.
func AssessNZB(nzb *NZB) Assessment {
	var out Assessment
	if nzb == nil {
		out.Reason = "empty nzb"
		return out
	}
	payload := payloadFiles(nzb)
	if len(payload) == 0 {
		out.Reason = "no payload files"
		return out
	}
	var play, par2 []FilePlan
	var names []string
	has7z := false
	hasRar := false
	hasVideo := false
	for _, f := range payload {
		plan, segs := filePlan(f)
		out.Segments += segs
		for _, s := range f.Segs {
			out.Bytes += s.Bytes
		}
		if plan.Name == "" || len(plan.MsgIDs) == 0 {
			continue
		}
		if !contiguousMsgIDs(f) {
			out.Reason = "gapped segments"
			return out
		}
		out.Files++
		lower := strings.ToLower(plan.Name)
		switch {
		case strings.HasSuffix(lower, ".par2"):
			par2 = append(par2, plan)
		case archiveKind(plan.Name) == "7z" || strings.Contains(lower, ".7z."):
			has7z = true
		case archiveKind(plan.Name) == "rar":
			hasRar = true
			play = append(play, plan)
			names = append(names, plan.Name)
		case isVideoName(plan.Name):
			hasVideo = true
			play = append(play, plan)
		}
	}
	out.Plan = SequencePlan{Play: sortPlayPlans(play), Par2: par2}
	if has7z && !hasRar && !hasVideo {
		out.Reason = "7z archive"
		return out
	}
	if hasVideo {
		out.OK = true
		return out
	}
	if hasRar {
		leaders := archiveLeaders(names)
		for _, leader := range leaders {
			if rarSetScore(leader, names) >= 2 {
				out.OK = true
				return out
			}
		}
		if rarBarePlusR00(names) {
			out.OK = true
			return out
		}
		out.Reason = "incomplete rar set"
		return out
	}
	out.Reason = "no sequential video archive"
	return out
}

// ReorderNZB puts SequencePlan play files first and PAR2 last. On Demand Play
// uses this so BODY fetches the video/RAR volumes before repair files.
func ReorderNZB(nzb *NZB) {
	if nzb == nil || len(nzb.Files) < 2 {
		return
	}
	plan := AssessNZB(nzb).Plan
	if len(plan.Play) == 0 && len(plan.Par2) == 0 {
		return
	}
	playWant := make(map[string]int, len(plan.Play))
	for i, p := range plan.Play {
		playWant[p.Name] = i
	}
	par2Want := make(map[string]bool, len(plan.Par2))
	for _, p := range plan.Par2 {
		par2Want[p.Name] = true
	}
	play := make([]NZBFile, len(plan.Play))
	playHit := make([]bool, len(plan.Play))
	var other, par2 []NZBFile
	for _, f := range nzb.Files {
		name, _ := filePlan(f)
		if i, ok := playWant[name.Name]; ok {
			play[i] = f
			playHit[i] = true
			continue
		}
		if par2Want[name.Name] {
			par2 = append(par2, f)
			continue
		}
		other = append(other, f)
	}
	var ordered []NZBFile
	for i, f := range play {
		if playHit[i] {
			ordered = append(ordered, f)
		}
	}
	nzb.Files = append(append(ordered, other...), par2...)
}

// nextRarVolume is the volume unrar -vp will ask for after name.
func nextRarVolume(name string) string {
	lower := strings.ToLower(name)
	if m := rarPartRE.FindStringSubmatch(lower); m != nil {
		digits := m[1]
		n := 0
		fmt.Sscanf(digits, "%d", &n)
		repl := fmt.Sprintf(".part%0*d.rar", len(digits), n+1)
		return name[:len(name)-len(m[0])] + repl
	}
	if rarVolumeRE.MatchString(lower) {
		ext := filepath.Ext(name)
		numStr := strings.TrimPrefix(strings.ToLower(ext), ".r")
		n := 0
		fmt.Sscanf(numStr, "%d", &n)
		return strings.TrimSuffix(name, ext) + fmt.Sprintf(".r%02d", n+1)
	}
	if strings.HasSuffix(lower, ".rar") {
		return strings.TrimSuffix(name, filepath.Ext(name)) + ".r00"
	}
	return ""
}

func filePlan(f NZBFile) (FilePlan, int) {
	name := filenameFromSubject(f.Subject)
	if name == "" {
		name = sanitizeName(f.Subject)
	}
	var ids []string
	for _, s := range sortedSegments(f) {
		id := strings.TrimSpace(s.MsgID)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return FilePlan{Name: name, MsgIDs: ids}, len(ids)
}

func contiguousMsgIDs(f NZBFile) bool {
	segs := sortedSegments(f)
	if len(segs) == 0 {
		return false
	}
	expect := segs[0].Number
	if expect != 1 {
		return false
	}
	for _, s := range segs {
		if s.Number != expect {
			return false
		}
		if strings.TrimSpace(s.MsgID) == "" {
			return false
		}
		expect++
	}
	return true
}

func sortedSegments(f NZBFile) []NZBSegment {
	segs := append([]NZBSegment(nil), f.Segs...)
	sort.Slice(segs, func(i, j int) bool { return segs[i].Number < segs[j].Number })
	return segs
}

func isVideoName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".mov", ".ts", ".m2ts", ".webm":
		return true
	default:
		return false
	}
}

func rarBarePlusR00(names []string) bool {
	hasRar, hasR00 := false, false
	for _, name := range names {
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".rar") && !rarPartRE.MatchString(lower) {
			hasRar = true
		}
		if rarVolumeRE.MatchString(lower) {
			hasR00 = true
		}
	}
	return hasRar && hasR00
}

func sortPlayPlans(play []FilePlan) []FilePlan {
	sort.SliceStable(play, func(i, j int) bool {
		return playVolumeOrder(play[i].Name) < playVolumeOrder(play[j].Name)
	})
	return play
}

func playVolumeOrder(name string) int {
	lower := strings.ToLower(name)
	if m := rarPartRE.FindStringSubmatch(lower); m != nil {
		n := strings.TrimLeft(m[1], "0")
		if n == "" {
			return 0
		}
		var num int
		for _, r := range n {
			if r < '0' || r > '9' {
				break
			}
			num = num*10 + int(r-'0')
		}
		return num
	}
	if strings.HasSuffix(lower, ".rar") {
		return 1
	}
	if rarVolumeRE.MatchString(lower) {
		ext := strings.TrimPrefix(filepath.Ext(lower), ".r")
		var num int
		for _, r := range ext {
			if r < '0' || r > '9' {
				break
			}
			num = num*10 + int(r-'0')
		}
		return num + 2
	}
	return 0
}
