package api

import (
	"slices"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
)

func TestFormatDownloadAddedAt(t *testing.T) {
	if got := formatDownloadAddedAt(time.Time{}); got != "" {
		t.Fatalf("zero = %q, want empty", got)
	}
	ts := time.Date(2026, 9, 20, 4, 0, 0, 0, time.UTC)
	if got := formatDownloadAddedAt(ts); got != "2026-09-20T04:00:00Z" {
		t.Fatalf("got %q", got)
	}
}

func TestMergedDownloadsSort_ByAddedAtThenGID(t *testing.T) {
	rows := []apidto.Download{
		{GID: "b", AddedAt: "2026-09-20T05:00:00Z"},
		{GID: "a", AddedAt: "2026-09-20T04:00:00Z"},
		{GID: "c", AddedAt: "2026-09-20T04:00:00Z"},
	}
	sortDownloadsOldestFirst(rows)
	got := []string{rows[0].GID, rows[1].GID, rows[2].GID}
	if !slices.Equal(got, []string{"a", "c", "b"}) {
		t.Fatalf("order = %v, want a,c,b", got)
	}
}
