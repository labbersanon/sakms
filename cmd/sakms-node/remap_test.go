package main

import "testing"

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
			name: "empty entries returns original",
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
