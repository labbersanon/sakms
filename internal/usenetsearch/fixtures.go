package usenetsearch

// Claude 2026-10-16: golden collection-stem fixtures gate extra RE2 patterns.
// Reason: auto-applied drift regexes must not re-split Decker/Bhool/teevee/hash
//   tokens. fixturesHold runs on a candidate extra set before publish.
// Troubleshooting: a proposed pattern rejected with "fixture <name>" → it
//   changed a locked stem; do not weaken the fixture, skip the pattern.
// Review if: a new ParseSubject test is added — copy it here.
// Related files: subject.go, drift.go, subject_test.go

type stemFixture struct {
	name      string
	subjects  []string
	wantName  string
	wantObf   bool
	wantFileM int
	wantPartM int
}

var goldenStemFixtures = []stemFixture{
	{
		name: "scene",
		subjects: []string{
			`[Group] Some.Movie.2024.1080p.BluRay.x264-GROUP - "some.movie.2024.1080p.bluray.x264-group.part01.rar" yEnc (1/120)`,
		},
		wantName:  "Some.Movie.2024.1080p.BluRay.x264-GROUP",
		wantFileM: 0,
		wantPartM: 120,
	},
	{
		name: "decker-multifile",
		subjects: []string{
			`[01/73] R.J.Decker.S02E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-RAWR - "r.j.decker.s02e05.1080p.amzn.web-dl.ddp5.1.h.264-rawr.part01.rar" yEnc (1/40)`,
			`[48/73] R.J.Decker.S02E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-RAWR - "r.j.decker.s02e05.1080p.amzn.web-dl.ddp5.1.h.264-rawr.part48.rar" yEnc (12/40)`,
			`[73/73] R.J.Decker.S02E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-RAWR - "r.j.decker.s02e05.1080p.amzn.web-dl.ddp5.1.h.264-rawr.vol00+01.par2" yEnc (1/1)`,
		},
		wantName:  "R.J.Decker.S02E05.1080p.AMZN.WEB-DL.DDP5.1.H.264-RAWR",
		wantFileM: 73,
	},
	{
		name: "bhool-paren",
		subjects: []string{
			`Bhool.Bhulaiyaa.2007] (72/93) - "Bhool.Bhulaiyaa.2007.Untouched.Blu-ray.1080p.BD50.AVC.DD.DTS-HD.MA.5.1-D0NL1GHT.vol230+" yEnc (1/50)`,
			`Bhool.Bhulaiyaa.2007] (1/93) - "Bhool.Bhulaiyaa.2007.Untouched.Blu-ray.1080p.BD50.AVC.DD.DTS-HD.MA.5.1-D0NL1GHT.vol001+" yEnc (3/50)`,
		},
		wantName:  "Bhool.Bhulaiyaa.2007]",
		wantFileM: 93,
		wantPartM: 50,
	},
	{
		name: "teevee-wrapper",
		subjects: []string{
			`[#a.b.teevee] Show.S01E01.720p.HDTV.x264-GRP - [01/30] - "show.s01e01.720p.hdtv.x264-grp.part01.rar" yEnc (1/20)`,
			`[#a.b.teevee] Show.S01E01.720p.HDTV.x264-GRP - [30/30] - "show.s01e01.720p.hdtv.x264-grp.vol00+01.par2" yEnc (1/1)`,
		},
		wantName:  "Show.S01E01.720p.HDTV.x264-GRP",
		wantFileM: 30,
	},
	{
		name: "hex-obfuscated",
		subjects: []string{
			`a1b2c3d4e5f6789012345678 yEnc (1/10)`,
		},
		wantObf:   true,
		wantPartM: 10,
	},
	{
		name: "lead-hash-token",
		subjects: []string{
			`ah63jka93jf0jh26ahjas558 - [01/22] - "random01.part01.rar" yEnc (1/40)`,
			`ah63jka93jf0jh26ahjas558 - [22/22] - "other.vol00+01.par2" yEnc (1/1)`,
		},
		wantObf:   true,
		wantFileM: 22,
	},
	{
		name: "lead-hash-glued",
		subjects: []string{
			`t2EI3CdWdF0hi5b8L9tkx[08/52] - "aaa.rar" yEnc (1/9)`,
			`t2EI3CdWdF0hi5b8L9tkx[52/52] - "zzz.par2" yEnc (1/1)`,
		},
		wantObf:   true,
		wantFileM: 52,
	},
}

func fixturesHold(extra []compiledExtra) (ok bool, why string) {
	for _, f := range goldenStemFixtures {
		var keys []string
		for _, subj := range f.subjects {
			ps := parseSubjectWith(subj, extra)
			if f.wantObf {
				if !ps.Obfuscated {
					return false, f.name + ": expected obfuscated"
				}
				keys = append(keys, obfKeyWith("g", "p@x", subj, extra))
			} else {
				if !ps.OK || ps.Obfuscated || ps.Name != f.wantName {
					return false, f.name + ": name=" + ps.Name
				}
				keys = append(keys, ps.Name)
			}
			if f.wantFileM > 0 && ps.FileM != f.wantFileM {
				return false, f.name + ": file_m mismatch"
			}
			if f.wantPartM > 0 && ps.PartM != f.wantPartM {
				return false, f.name + ": part_m mismatch"
			}
		}
		for i := 1; i < len(keys); i++ {
			if keys[i] != keys[0] {
				return false, f.name + ": subjects split"
			}
		}
	}
	return true, ""
}
