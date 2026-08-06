package rename

import "testing"

func TestIsJunkRenameFilename(t *testing.T) {
	junk := []string{
		"RED_SKELTON_Title1.mp4",
		"RED_SKELTON_Title4.mp4",
		"Title.mkv",
		"Movie2.mp4",
		"Video.avi",
		"Clip9.mkv",
		"Track1.mp4",
	}
	for _, n := range junk {
		if !IsJunkRenameFilename(n) {
			t.Errorf("expected junk: %q", n)
		}
	}
	ok := []string{
		"Red Skelton More Funny Faces.mp4",
		"Red Skelton Funny Faces III - AV1.mp4",
		"A Beautiful Mind (2001).mkv",
	}
	for _, n := range ok {
		if IsJunkRenameFilename(n) {
			t.Errorf("expected not junk: %q", n)
		}
	}
}

func TestHasTitleTokenOverlap(t *testing.T) {
	if !HasTitleTokenOverlap("Red Skelton More Funny Faces.mp4", "More Funny Faces") {
		t.Fatal("expected overlap on Funny/Faces/More/Skelton/Red")
	}
	if HasTitleTokenOverlap("RED_SKELTON_Title1.mp4", "Some Random Film") {
		t.Fatal("Title1 must not overlap invented title")
	}
	if !HasTitleTokenOverlap("JoJo Dancer 2007.mkv", "Jo Jo Dancer, Your Life Is Calling") {
		t.Fatal("expected JoJo / Dancer overlap")
	}
}

func TestWebAuthorityTMDBID_StableNegative(t *testing.T) {
	a := WebAuthorityTMDBID("More Funny Faces", 1982)
	b := WebAuthorityTMDBID("More Funny Faces", 1982)
	c := WebAuthorityTMDBID("More Funny Faces", 1984)
	if a >= 0 || b >= 0 {
		t.Fatalf("expected negative ids, got %d %d", a, b)
	}
	if a != b {
		t.Fatalf("expected stable id, got %d vs %d", a, b)
	}
	if a == c {
		t.Fatalf("different years should differ")
	}
}
