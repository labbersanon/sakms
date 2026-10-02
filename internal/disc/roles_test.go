package disc

import "testing"

func titles(rows ...Title) []Title { return rows }

func TestAssignRoles_GoldenShortSiblings(t *testing.T) {
	// looneytnsgldnv5d1: play-all + 15 shorts + 2 extras.
	in := titles(
		Title{N: 1, DurationS: 6386, Chapters: 15},
		Title{N: 2, DurationS: 433},
		Title{N: 3, DurationS: 415},
		Title{N: 16, DurationS: 478},
		Title{N: 17, DurationS: 1783, Chapters: 4},
		Title{N: 18, DurationS: 1609, Chapters: 4},
	)
	got := AssignRoles(in)
	if got[0].Role != RolePlayAll || got[0].SplitChapters {
		t.Fatalf("title 1 = %+v, want playall without chapter split", got[0])
	}
	for _, i := range []int{1, 2, 3} {
		if got[i].Role != RoleFeature {
			t.Fatalf("title %d role = %q, want feature", got[i].N, got[i].Role)
		}
	}
	if got[4].Role != RoleExtra || got[5].Role != RoleExtra {
		t.Fatalf("extras = %q / %q", got[4].Role, got[5].Role)
	}
	works := PlanWorks(got)
	if len(works) != 3 {
		t.Fatalf("works = %d, want 3 shorts (play-all skipped)", len(works))
	}
	if works[0].Name != "t02" || works[0].ChapterStart != 0 {
		t.Fatalf("first work = %+v", works[0])
	}
}

func TestAssignRoles_AnimaniacsChapterSplit(t *testing.T) {
	in := titles(Title{N: 1, DurationS: 6353, Chapters: 23})
	got := AssignRoles(in)
	if got[0].Role != RolePlayAll || !got[0].SplitChapters {
		t.Fatalf("got %+v, want playall+split", got[0])
	}
	works := PlanWorks(got)
	if len(works) != 23 {
		t.Fatalf("works = %d, want 23 chapters", len(works))
	}
	if works[0].Name != "t01c01" || works[0].ChapterStart != 1 || works[0].ChapterEnd != 1 {
		t.Fatalf("first chapter work = %+v", works[0])
	}
	if works[22].Name != "t01c23" {
		t.Fatalf("last chapter work = %+v", works[22])
	}
}

func TestAssignRoles_MovieStaysOneFile(t *testing.T) {
	in := titles(Title{N: 1, DurationS: 5400, Chapters: 12})
	got := AssignRoles(in)
	if got[0].Role != RoleFeature || got[0].SplitChapters {
		t.Fatalf("got %+v, want single feature", got[0])
	}
	works := PlanWorks(got)
	if len(works) != 1 || works[0].Name != "t01" {
		t.Fatalf("works = %+v", works)
	}
}

func TestAssignRoles_MenuIgnored(t *testing.T) {
	in := titles(
		Title{N: 1, DurationS: 12, Chapters: 1},
		Title{N: 2, DurationS: 5400, Chapters: 8},
	)
	got := AssignRoles(in)
	if got[0].Role != RoleMenu {
		t.Fatalf("short title role = %q", got[0].Role)
	}
	if got[1].Role != RoleFeature {
		t.Fatalf("movie role = %q", got[1].Role)
	}
}
