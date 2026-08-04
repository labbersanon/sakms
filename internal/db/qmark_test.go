package db

import "testing"

func TestRebind_basic(t *testing.T) {
	got := Rebind("SELECT a FROM t WHERE id = ? AND name = ?")
	want := "SELECT a FROM t WHERE id = $1 AND name = $2"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRebind_skipsQuoted(t *testing.T) {
	in := `SELECT '?' AS q, "cast" FROM t WHERE id = ?`
	got := Rebind(in)
	want := `SELECT '?' AS q, "cast" FROM t WHERE id = $1`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRebind_escapedQuestion(t *testing.T) {
	// Inside quotes ?? is left alone; outside, ?? → literal ?.
	got := Rebind("SELECT 'a??b', x ?? y, ?")
	want := "SELECT 'a??b', x ? y, $1"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRebind_comment(t *testing.T) {
	got := Rebind("SELECT ? -- trailing ?\nFROM t WHERE x = ?")
	want := "SELECT $1 -- trailing ?\nFROM t WHERE x = $2"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
