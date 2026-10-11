package api

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/usenet"
	"github.com/labbersanon/sakms/internal/websearch"
)

func TestUniquePasswordsCapsAndDedupes(t *testing.T) {
	got := uniquePasswords([]string{" a ", "a", "b", "c", ""}, 2)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v", got)
	}
}

func TestPasswordsFromIndexerTitles(t *testing.T) {
	got := usenet.PasswordsFromTitles([]string{
		"Show.S01E01.1080p-Smad",
		"Show.S01E01.1080p-Smad {{hunter2}}",
	})
	if len(got) != 1 || got[0] != "hunter2" {
		t.Fatalf("got %v", got)
	}
}

type scriptedPasswordAI struct {
	resp map[string]any
	err  error
}

func (s scriptedPasswordAI) ChatJSON(context.Context, string) (map[string]any, error) {
	return s.resp, s.err
}

func TestExtractArchivePassword_RequiresExplicit(t *testing.T) {
	pw := extractArchivePassword(context.Background(), scriptedPasswordAI{resp: map[string]any{"password": "hunter2"}}, "Show", []websearch.Result{
		{Title: "nzb", Description: "password is hunter2", URL: "http://example"},
	})
	if pw != "hunter2" {
		t.Fatalf("got %q", pw)
	}
	declined := extractArchivePassword(context.Background(), scriptedPasswordAI{resp: map[string]any{"password": nil}}, "Show", []websearch.Result{
		{Title: "nzb", Description: "encrypted rar", URL: "http://example"},
	})
	if declined != "" {
		t.Fatalf("declined = %q", declined)
	}
}

func TestDiscoverySearchQuery_EmptyName(t *testing.T) {
	if got := usenet.DiscoverySearchQuery("   "); got != "" {
		t.Fatalf("got %q", got)
	}
}
