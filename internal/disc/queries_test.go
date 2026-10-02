package disc

import "testing"

func TestSearchQueries_StripsDiscEdition(t *testing.T) {
	got := SearchQueries("LOONEY_TUNES_GOLDEN_V5_D1", "/media/looneytnsgldnv5d1.iso")
	if len(got) == 0 {
		t.Fatal("empty")
	}
	var hasClean bool
	for _, q := range got {
		if q == "LOONEY TUNES GOLDEN" {
			hasClean = true
		}
	}
	if !hasClean {
		t.Fatalf("queries = %v, want stripped volume", got)
	}
}
