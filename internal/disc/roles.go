package disc

import "fmt"

// AssignRoles classifies titles: play-all + short siblings (Golden),
// chapter-split of one long high-chapter title (Animaniacs), or one feature
// (typical movie). Menus are under 60s.
func AssignRoles(titles []Title) []Title {
	out := make([]Title, len(titles))
	copy(out, titles)
	if len(out) == 0 {
		return out
	}

	var playIdx = -1
	var playDur float64
	for i, t := range out {
		if t.DurationS < menuMaxS {
			out[i].Role = RoleMenu
			continue
		}
		if t.DurationS > shortMaxS && t.Chapters >= 8 && t.DurationS >= playDur {
			playDur = t.DurationS
			playIdx = i
		}
	}

	var shortN int
	for i, t := range out {
		if t.Role == RoleMenu || i == playIdx {
			continue
		}
		if t.DurationS >= menuMaxS && t.DurationS <= shortMaxS {
			shortN++
		}
	}

	if playIdx >= 0 && shortN >= 2 {
		out[playIdx].Role = RolePlayAll
		for i, t := range out {
			if i == playIdx || t.Role == RoleMenu {
				continue
			}
			if t.DurationS >= menuMaxS && t.DurationS <= shortMaxS {
				out[i].Role = RoleFeature
				continue
			}
			if t.DurationS >= extraMinS {
				out[i].Role = RoleExtra
				continue
			}
			out[i].Role = RoleExtra
		}
		return out
	}

	if playIdx >= 0 {
		t := out[playIdx]
		avg := 0.0
		if t.Chapters > 0 {
			avg = t.DurationS / float64(t.Chapters)
		}
		if t.Chapters >= chSplitMinN && avg >= chSplitMinS && avg <= chSplitMaxS {
			out[playIdx].Role = RolePlayAll
			out[playIdx].SplitChapters = true
			for i := range out {
				if i == playIdx || out[i].Role == RoleMenu {
					continue
				}
				out[i].Role = RoleExtra
			}
			return out
		}
		out[playIdx].Role = RoleFeature
		for i := range out {
			if i == playIdx || out[i].Role == RoleMenu {
				continue
			}
			out[i].Role = RoleExtra
		}
		return out
	}

	best := -1
	for i, t := range out {
		if t.Role == RoleMenu {
			continue
		}
		if best < 0 || t.DurationS > out[best].DurationS {
			best = i
		}
	}
	for i := range out {
		if out[i].Role == RoleMenu {
			continue
		}
		if i == best {
			out[i].Role = RoleFeature
			continue
		}
		out[i].Role = RoleExtra
	}
	return out
}

// PlanWorks turns classified titles into extract jobs. Play-all is skipped
// unless SplitChapters is set. Extras and menus are skipped.
func PlanWorks(titles []Title) []Work {
	var works []Work
	for _, t := range titles {
		if t.SplitChapters && t.Chapters > 0 {
			for c := 1; c <= t.Chapters; c++ {
				works = append(works, Work{
					Title:        t.N,
					ChapterStart: c,
					ChapterEnd:   c,
					Name:         fmt.Sprintf("t%02dc%02d", t.N, c),
					Role:         RoleFeature,
				})
			}
			continue
		}
		if t.Role != RoleFeature {
			continue
		}
		works = append(works, Work{
			Title: t.N,
			Name:  fmt.Sprintf("t%02d", t.N),
			Role:  RoleFeature,
		})
	}
	return works
}
