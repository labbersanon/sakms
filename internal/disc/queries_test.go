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

func TestWikiQuery_KeepsVolumeDropsDisc(t *testing.T) {
	got := WikiQuery("LOONEY_TUNES_GOLDEN_V5_D1", "/media/looneytnsgldnv5d1.iso")
	if got != "LOONEY TUNES GOLDEN Volume 5" {
		t.Fatalf("WikiQuery = %q", got)
	}
}

func TestDiscNumber_FromVolumeD1(t *testing.T) {
	if n := DiscNumber("LOONEY_TUNES_GOLDEN_V5_D1", "x.iso"); n != 1 {
		t.Fatalf("disc = %d", n)
	}
	if n := DiscNumber("SHOW DISC 2", "x.iso"); n != 2 {
		t.Fatalf("disc = %d", n)
	}
}
