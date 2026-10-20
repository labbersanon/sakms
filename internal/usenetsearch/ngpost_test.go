package usenetsearch

import "testing"

func TestLooksLikeNgPost(t *testing.T) {
	if !looksLikeNgPostFrom("7f2TvDLqciSVg@ngPost.com") {
		t.Fatal("live GEN_FROM")
	}
	if looksLikeNgPostFrom("p@x") || looksLikeNgPostFrom("someone@example.com") {
		t.Fatal("non-ngPost From")
	}
	if !looksLikeNgPostMsgID("76d3cb044ad64a81b4385bf073ee12e8@ngPost") {
		t.Fatal("live Id128 msgid")
	}
	if !looksLikeNgPostMsgID("<abc@ngPost.com>") {
		t.Fatal("msgid with .com")
	}
	rows := []HeaderRow{{From: "7f2TvDLqciSVg@ngPost.com"}}
	if !looksLikeNgPost(rows) {
		t.Fatal("rows From")
	}
}

func TestObfKey_NgPostOmitsGroup(t *testing.T) {
	const from = "7f2TvDLqciSVg@ngPost.com"
	subj := `[001/136] "o6938TnxIec0VaRtCFRJc9SzSq20qBNU6KL2bwpkOIahgpQgsf.7z.001" (10/732)`
	a := ObfKey("alt.binaries.teevee", from, subj)
	b := ObfKey("alt.binaries.moovee", from, subj)
	if a != b || !isObfKey(a) {
		t.Fatalf("ALL-policy dups must share a key: %s vs %s", a, b)
	}
	other := ObfKey("alt.binaries.teevee", "other@ngPost.com", subj)
	if other == a {
		t.Fatal("different GEN_FROM must separate posts")
	}
	plain := `[01/10] - "0123456789abcdef0123456789abcdef.part01.rar" yEnc (1/80)`
	if ObfKey("g", "p@x", plain) == ObfKey("g2", "p@x", plain) {
		t.Fatal("non-ngPost posters still mix group into ObfKey")
	}
}

func TestParseHeader_NgPostUUIDStaysUnparsed(t *testing.T) {
	h := parseHeader("alt.binaries.teevee", "76d3cb044ad64a81b4385bf073ee12e8",
		"k9j8h7g6f5d4s3a2@ngPost.com", "76d3cb044ad64a81b4385bf073ee12e8@ngPost",
		1, 1, 716800, true)
	if isObfKey(h.ReleaseName) {
		t.Fatalf("-x UUID subject cannot cluster from OVERVIEW, got %q", h.ReleaseName)
	}
}
