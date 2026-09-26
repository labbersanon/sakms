package downloader

import (
	"testing"

	torrentlib "github.com/anacrolix/torrent"
)

func TestCompressHeatmap_BucketsCompleteRun(t *testing.T) {
	runs := torrentlib.PieceStateRuns{{Length: 8}}
	runs[0].Complete = true
	heat := compressHeatmap(runs, 8)
	if len(heat) != 8 {
		t.Fatalf("heatmap len=%d want 8", len(heat))
	}
	for i := 0; i < len(heat); i++ {
		if heat[i] != '9' {
			t.Fatalf("heat[%d]=%c want 9 (%q)", i, heat[i], heat)
		}
	}
}

func TestSetFilePriority_RejectsBadPriority(t *testing.T) {
	m := NewForTesting(t.TempDir())
	if err := m.SetFilePriority("g1", "a.mkv", "urgent"); err == nil {
		t.Fatal("want error for unknown priority")
	}
}
