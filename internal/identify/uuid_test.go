package identify

import "testing"

func TestExtractUUID_FoundInStem(t *testing.T) {
	uuid, ok := ExtractUUID("2011-04-25 - Happy B-Day Sunny Lane - Sunny Lane - StashDB [818735c0-c7bd-49e3-aff9-4f12a80d7e70]")
	if !ok {
		t.Fatal("expected a UUID to be found")
	}
	if uuid != "818735c0-c7bd-49e3-aff9-4f12a80d7e70" {
		t.Fatalf("got %q", uuid)
	}
}

func TestExtractUUID_CaseInsensitive(t *testing.T) {
	uuid, ok := ExtractUUID("stuff-A29768DB-B3CD-4A71-A75E-4294373207BB-more")
	if !ok || uuid != "A29768DB-B3CD-4A71-A75E-4294373207BB" {
		t.Fatalf("got uuid=%q ok=%v", uuid, ok)
	}
}

func TestExtractUUID_NotFound(t *testing.T) {
	_, ok := ExtractUUID("Some Studio - A Normal Filename (2024) [1080p]")
	if ok {
		t.Fatal("expected no UUID to be found in a normal filename")
	}
}

func TestExtractUUID_MalformedNotMatched(t *testing.T) {
	// Missing a segment / wrong length shouldn't match.
	_, ok := ExtractUUID("818735c0-c7bd-49e3-4f12a80d7e70")
	if ok {
		t.Fatal("expected malformed UUID-like string not to match")
	}
}
