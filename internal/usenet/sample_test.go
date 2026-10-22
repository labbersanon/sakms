package usenet

import "testing"

func TestIsSampleName(t *testing.T) {
	yes := []string{
		"Movie.2021.1080p.sample.mkv",
		"movie-sample.mkv",
		"sample.mkv",
		"Show.S01E01.sample",
		`[03/10] - "hash.sample.mp4" yEnc (1/1)`,
	}
	no := []string{
		"Movie.2021.1080p.mkv",
		"The.Sampler.2021.1080p.BluRay.x264-GRP",
		"samples.bonus.mkv",
		"hash.part01.rar",
		"hash.par2",
	}
	for _, s := range yes {
		if !IsSampleName(s) {
			t.Errorf("%q want sample", s)
		}
	}
	for _, s := range no {
		if IsSampleName(s) {
			t.Errorf("%q must not be sample", s)
		}
	}
}
