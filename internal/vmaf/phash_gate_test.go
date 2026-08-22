package vmaf

import (
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/phash"
)

func seededHash(hexPrefix string) string {
	return "pdq256/5f:" + hexPrefix + strings.Repeat("0", 320-len(hexPrefix))
}

func TestShouldScoreVMAF(t *testing.T) {
	ref := seededHash("")
	near := seededHash("0f") // 4 bits, well inside Movies 64 and Series 40
	far := seededHash(strings.Repeat("f", 320))

	tests := []struct {
		name                 string
		candidate, reference string
		perFrameThreshold    int
		want                 bool
	}{
		{"identical hashes match", ref, ref, phash.DefaultMoviesThreshold, true},
		{"near hashes match at movies default", near, ref, phash.DefaultMoviesThreshold, true},
		{"near hashes match at series default", near, ref, phash.DefaultThreshold, true},
		{"far hashes skip at movies default", far, ref, phash.DefaultMoviesThreshold, false},
		{"empty candidate skips", "", ref, phash.DefaultMoviesThreshold, false},
		{"empty reference skips", ref, "", phash.DefaultMoviesThreshold, false},
		{"both empty skip", "", "", phash.DefaultMoviesThreshold, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldScoreVMAF(tc.candidate, tc.reference, tc.perFrameThreshold); got != tc.want {
				t.Fatalf("ShouldScoreVMAF(...) = %v, want %v", got, tc.want)
			}
		})
	}
}
