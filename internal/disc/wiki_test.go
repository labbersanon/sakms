package disc

import "testing"

const goldenWiki = `'''''Looney Tunes Golden Collection: Volume 5''''' is a set.

==Disc 1: Bugs Bunny and Daffy Duck==
:''Note.''
{|class="wikitable sortable"
|-
!#
!Title
!Year
|-
|1
|''[[14 Carrot Rabbit]]''
|1952
|-
|2
|''[[Ali Baba Bunny]]''
|1957
|-
|3
|''[[Transylvania 6-5000 (1963 film)|Transylvania 6-5000]]''
|1963
|-
|4
|''[[Bacall to Arms]]''{{refn|group=n|Co-directed.}}
|1946
|}

===Special features===
* Vault extra

==Disc 2: Fun-Filled Fairy Tales==
{|class="wikitable sortable"
|-
!#
!Title
|-
|1
|''[[Bewitched Bunny]]''
|-
|2
|''[[Paying the Piper]]''
|}
`

func TestParseDiscTOC_GoldenDisc1Order(t *testing.T) {
	got := ParseDiscTOC(goldenWiki, 1)
	want := []string{
		"14 Carrot Rabbit",
		"Ali Baba Bunny",
		"Transylvania 6-5000",
		"Bacall to Arms",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %q, want %q", i+1, got[i], want[i])
		}
	}
}

func TestParseDiscTOC_PicksDisc2(t *testing.T) {
	got := ParseDiscTOC(goldenWiki, 2)
	if len(got) != 2 || got[0] != "Bewitched Bunny" || got[1] != "Paying the Piper" {
		t.Fatalf("disc 2 = %v", got)
	}
}

func TestParseDiscTOC_IgnoresVaultLists(t *testing.T) {
	got := ParseDiscTOC(goldenWiki, 1)
	for _, n := range got {
		if n == "Vault extra" {
			t.Fatalf("vault leaked into TOC: %v", got)
		}
	}
}
