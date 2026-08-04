package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/downloader"
	"github.com/labbersanon/sakms/internal/usenet"
)

// Protocol/seed-count/upload-speed DTO-boundary tests (implementing plan §4,
// G-8…G-11).

// G-8 — the torrent mapper emits protocol: "torrent" and passes SeedCount and
// UploadSpeed straight through.
func TestToDTODownload_SetsTorrentProtocol(t *testing.T) {
	got := toDTODownload(downloader.Download{
		GID: "g1", Status: "active", Filename: "movie.mkv",
		TotalLength: 100, CompletedLength: 50, DownloadSpeed: 10,
		SeedCount: 4, UploadSpeed: 20,
	})
	if got.Protocol != apidto.DownloadProtocolTorrent {
		t.Fatalf("Protocol = %q, want %q", got.Protocol, apidto.DownloadProtocolTorrent)
	}
	if got.SeedCount != 4 {
		t.Fatalf("SeedCount = %d, want 4 (passthrough)", got.SeedCount)
	}
	if got.UploadSpeed != 20 {
		t.Fatalf("UploadSpeed = %d, want 20 (passthrough)", got.UploadSpeed)
	}
}

// G-9 — the usenet mapper emits protocol: "usenet" and leaves the torrent-
// only fields at their zero value: usenet has no seeder/upload concept at
// all, and the frontend hides both fields entirely for this protocol.
func TestToUsenetDTODownload_SetsUsenetProtocolAndOmitsTorrentOnlyFields(t *testing.T) {
	got := toUsenetDTODownload(usenet.Download{
		GID: "g2", Status: "active", Filename: "episode.mkv",
		TotalLength: 100, CompletedLength: 50, DownloadSpeed: 30,
	})
	if got.Protocol != apidto.DownloadProtocolUsenet {
		t.Fatalf("Protocol = %q, want %q", got.Protocol, apidto.DownloadProtocolUsenet)
	}
	if got.SeedCount != 0 {
		t.Fatalf("SeedCount = %d, want 0 (usenet carries no seeder concept)", got.SeedCount)
	}
	if got.UploadSpeed != 0 {
		t.Fatalf("UploadSpeed = %d, want 0 (usenet carries no upload concept)", got.UploadSpeed)
	}
}

// G-10 — a mixed queue tags each row by its source engine (the mapper that
// produced it), not by re-deriving protocol from a GID-prefix convention
// (D-2). A torrent GID deliberately shaped like the "nzb-" prefix routing
// uses elsewhere in this package proves the mapper never looks at the GID at
// all to decide protocol.
func TestMergedDownloads_ProtocolPerRow(t *testing.T) {
	dl := newTestDownloader("nzb-looks-like-usenet-but-isnt", t.TempDir())
	seedActive(dl, "nzb-looks-like-usenet-but-isnt")

	rows := mergedDownloads(dl, nil)
	if len(rows) != 1 {
		t.Fatalf("mergedDownloads returned %d rows, want 1", len(rows))
	}
	if rows[0].Protocol != apidto.DownloadProtocolTorrent {
		t.Fatalf("Protocol = %q for a torrent-engine row with a misleading GID prefix, want %q — "+
			"protocol must come from which mapper ran, not the GID string", rows[0].Protocol, apidto.DownloadProtocolTorrent)
	}
}

// G-11 — the wire-contract guard the frontend depends on: the JSON keys
// actually on the wire must be seedCount/uploadSpeed/protocol, and connections
// must be gone. A round-trip through apidto.Download (as G-8/G-9 do) would
// pass even if the old key were still present under a different Go field, so
// this asserts against the raw encoded bytes instead.
func TestDownloadJSONShape_HasNewFieldsNotConnections(t *testing.T) {
	dl := newTestDownloader("g3", t.TempDir())
	seedActive(dl, "g3")

	rows := mergedDownloads(dl, nil)
	body, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshalling rows: %v", err)
	}
	raw := string(body)

	for _, want := range []string{`"seedCount"`, `"uploadSpeed"`, `"protocol"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("encoded JSON missing %s: %s", want, raw)
		}
	}
	if strings.Contains(raw, `"connections"`) {
		t.Errorf("encoded JSON still carries the old \"connections\" key: %s", raw)
	}
}
