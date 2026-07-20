package main

import (
	"sort"
	"testing"

	"github.com/labbersanon/sakms/internal/nodes"
)

func TestRemap(t *testing.T) {
	cases := []struct {
		name    string
		entries []PathMapEntry
		input   string
		want    string
	}{
		{
			name: "linux to linux",
			entries: []PathMapEntry{
				{Server: "/mnt/Adult-NAS", Local: "/data/Adult"},
			},
			input: "/mnt/Adult-NAS/foo.mkv",
			want:  "/data/Adult/foo.mkv",
		},
		{
			name: "linux to macOS style",
			entries: []PathMapEntry{
				{Server: "/mnt/Adult-NAS", Local: "/Volumes/Adult-NAS"},
			},
			input: "/mnt/Adult-NAS/foo.mkv",
			want:  "/Volumes/Adult-NAS/foo.mkv",
		},
		{
			name: "linux to windows style",
			entries: []PathMapEntry{
				{Server: "/mnt/Adult-NAS", Local: `Z:\Adult-NAS`},
			},
			input: "/mnt/Adult-NAS/foo.mkv",
			want:  `Z:\Adult-NAS\foo.mkv`,
		},
		{
			name: "no match returns original",
			entries: []PathMapEntry{
				{Server: "/mnt/Adult-NAS", Local: "/data/Adult"},
			},
			input: "/mnt/Movies/foo.mkv",
			want:  "/mnt/Movies/foo.mkv",
		},
		{
			name: "longest prefix wins",
			entries: []PathMapEntry{
				{Server: "/mnt", Local: "/short"},
				{Server: "/mnt/Adult-NAS", Local: "/data/Adult"},
			},
			input: "/mnt/Adult-NAS/foo.mkv",
			want:  "/data/Adult/foo.mkv",
		},
		{
			name: "longest prefix wins reversed entry order",
			entries: []PathMapEntry{
				{Server: "/mnt/Adult-NAS", Local: "/data/Adult"},
				{Server: "/mnt", Local: "/short"},
			},
			input: "/mnt/Adult-NAS/foo.mkv",
			want:  "/data/Adult/foo.mkv",
		},
		{
			name: "boundary: /mnt must not match /mnt-other",
			entries: []PathMapEntry{
				{Server: "/mnt", Local: "/local"},
			},
			input: "/mnt-other/foo.mkv",
			want:  "/mnt-other/foo.mkv",
		},
		{
			name: "exact prefix match (no trailing path)",
			entries: []PathMapEntry{
				{Server: "/mnt/Adult-NAS", Local: "/data/Adult"},
			},
			input: "/mnt/Adult-NAS",
			want:  "/data/Adult",
		},
		{
			name:    "empty entries returns original",
			entries: nil,
			input:   "/mnt/foo.mkv",
			want:    "/mnt/foo.mkv",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Remap(tc.entries, tc.input)
			if got != tc.want {
				t.Errorf("Remap(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// sortedByServer returns a copy of entries sorted by Server, so tests can
// compare map-derived (unordered) results deterministically.
func sortedByServer(entries []PathMapEntry) []PathMapEntry {
	out := append([]PathMapEntry(nil), entries...)
	sort.Slice(out, func(i, j int) bool { return out[i].Server < out[j].Server })
	return out
}

func TestMergePathMap(t *testing.T) {
	t.Run("incoming key not in existing is added", func(t *testing.T) {
		existing := []PathMapEntry{{Server: "/mnt/movies", Local: "/data/movies"}}
		incoming := []nodes.PathMapping{{Server: "/mnt/series", Local: "/data/series"}}
		got := sortedByServer(mergePathMap(existing, incoming))
		want := []PathMapEntry{
			{Server: "/mnt/movies", Local: "/data/movies"},
			{Server: "/mnt/series", Local: "/data/series"},
		}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("incoming key already in existing is replaced", func(t *testing.T) {
		existing := []PathMapEntry{{Server: "/mnt/movies", Local: "/old/movies"}}
		incoming := []nodes.PathMapping{{Server: "/mnt/movies", Local: "/new/movies"}}
		got := mergePathMap(existing, incoming)
		if len(got) != 1 || got[0].Local != "/new/movies" {
			t.Fatalf("got %+v, want one entry with Local=/new/movies", got)
		}
	})

	t.Run("existing key NOT in incoming is left untouched — the core merge guarantee", func(t *testing.T) {
		existing := []PathMapEntry{
			{Server: "/mnt/movies", Local: "/data/movies"},
			{Server: "/mnt/adult", Local: "/data/adult"},
		}
		// incoming only carries movies — adult's row is unconfigured/disabled
		// on the server side and was never included in the push.
		incoming := []nodes.PathMapping{{Server: "/mnt/movies", Local: "/data/movies-v2"}}
		got := sortedByServer(mergePathMap(existing, incoming))
		if len(got) != 2 {
			t.Fatalf("got %d entries, want 2 (adult's entry must survive)", len(got))
		}
		if got[0].Server != "/mnt/adult" || got[0].Local != "/data/adult" {
			t.Errorf("adult entry changed: got %+v, want unchanged {/mnt/adult /data/adult}", got[0])
		}
		if got[1].Server != "/mnt/movies" || got[1].Local != "/data/movies-v2" {
			t.Errorf("movies entry: got %+v, want updated {/mnt/movies /data/movies-v2}", got[1])
		}
	})

	t.Run("empty incoming leaves existing entirely untouched", func(t *testing.T) {
		existing := []PathMapEntry{{Server: "/mnt/movies", Local: "/data/movies"}}
		got := mergePathMap(existing, nil)
		if len(got) != 1 || got[0] != existing[0] {
			t.Fatalf("got %+v, want existing unchanged: %+v", got, existing)
		}
	})

	t.Run("empty existing just adopts incoming (fresh node case)", func(t *testing.T) {
		incoming := []nodes.PathMapping{{Server: "/mnt/movies", Local: "/data/movies"}}
		got := mergePathMap(nil, incoming)
		if len(got) != 1 || got[0].Server != "/mnt/movies" || got[0].Local != "/data/movies" {
			t.Fatalf("got %+v, want one entry matching incoming", got)
		}
	})
}
