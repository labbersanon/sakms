package disc

import "testing"

func TestSearchQueries_StripsDiscEdition(t *testing.T) {
	got := SearchQueries("LOONEY_TUNES_GOLDEN_V5_D1", "/media/looneytnsgldnv5d1.iso")
	if len(got) == 0 {
		t.Fatal("empty")
	}
	var hasClean, hasVolume bool
	for _, q := range got {
		if q == "LOONEY TUNES GOLDEN" {
			hasClean = true
		}
		if q == "LOONEY TUNES GOLDEN Volume 5" {
			hasVolume = true
		}
	}
	if !hasClean {
		t.Fatalf("queries = %v, want stripped volume", got)
	}
	if !hasVolume {
		t.Fatalf("queries = %v, want Volume 5 first-class query", got)
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

func TestParseEdition_GluedVD(t *testing.T) {
	vol, discN := ParseEdition("LOONEY_TUNES_GOLDEN_V5D1", "/media/looneytunesgoldenv5d1.iso")
	if vol != 5 || discN != 1 {
		t.Fatalf("glued volume/filename = %d/%d, want 5/1", vol, discN)
	}
	vol, discN = ParseEdition("LOONEY_TUNES_GOLDEN_V5D2", "/media/looneytunesgoldenv5d2.iso")
	if vol != 5 || discN != 2 {
		t.Fatalf("v5d2 = %d/%d, want 5/2", vol, discN)
	}
	vol, discN = ParseEdition("", "/media/inytoonsv5d4.iso")
	if vol != 5 || discN != 4 {
		t.Fatalf("filename-only v5d4 = %d/%d, want 5/4", vol, discN)
	}
}

func TestWikiQuery_GluedVD(t *testing.T) {
	got := WikiQuery("LOONEY_TUNES_GOLDEN_V5D1", "/media/looneytunesgoldenv5d1.iso")
	if got != "LOONEY TUNES GOLDEN Volume 5" {
		t.Fatalf("WikiQuery glued = %q", got)
	}
}

func TestVolumeTitleScore(t *testing.T) {
	if VolumeTitleScore("Looney Tunes Golden Collection, Vol. 5", 5) != 1 {
		t.Fatal("Vol. 5 must match volume 5")
	}
	if VolumeTitleScore("Looney Tunes Golden Collection, Vol. 1", 5) != 0 {
		t.Fatal("Vol. 1 must not match volume 5")
	}
	if VolumeTitleScore("Irreverent Imagination: The Golden Age of the Looney Tunes", 5) != 0 {
		t.Fatal("unnumbered title is not a volume match")
	}
}
