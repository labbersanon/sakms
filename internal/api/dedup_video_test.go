package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

func videoURL(srv string, m string, id int64, candidateIndex int) string {
	return srv + "/api/modes/" + m + "/dedup/proposals/" + strconv.FormatInt(id, 10) +
		"/video?candidateIndex=" + strconv.Itoa(candidateIndex)
}

// AC14(a): an out-of-range candidateIndex or an unknown proposalId is rejected
// with an explicit 400 — never a silent empty 200 response.
func TestDedupVideoHandler_RejectsBadRequests(t *testing.T) {
	srv, propStore, _ := newVMAFTestMux(t)
	dedup := insertProposal(t, propStore, mode.Movies, proposals.Dedup, []proposals.Candidate{
		{Label: "a", Path: "/a.mkv"}, {Label: "b", Path: "/b.mkv"},
	})
	rename := insertProposal(t, propStore, mode.Movies, proposals.Rename, nil)

	tests := []struct {
		name string
		path string
		want int
	}{
		{"unknown mode", "/api/modes/bogus/dedup/proposals/" + propID(dedup) + "/video?candidateIndex=0", http.StatusBadRequest},
		{"non-numeric id", "/api/modes/movies/dedup/proposals/abc/video?candidateIndex=0", http.StatusBadRequest},
		{"missing candidateIndex", "/api/modes/movies/dedup/proposals/" + propID(dedup) + "/video", http.StatusBadRequest},
		{"candidateIndex out of range (high)", "/api/modes/movies/dedup/proposals/" + propID(dedup) + "/video?candidateIndex=5", http.StatusBadRequest},
		{"candidateIndex out of range (negative)", "/api/modes/movies/dedup/proposals/" + propID(dedup) + "/video?candidateIndex=-1", http.StatusBadRequest},
		{"unknown proposal id", "/api/modes/movies/dedup/proposals/999999/video?candidateIndex=0", http.StatusBadRequest},
		{"not a dedup proposal", "/api/modes/movies/dedup/proposals/" + propID(rename) + "/video?candidateIndex=0", http.StatusBadRequest},
		{"mode mismatch", "/api/modes/series/dedup/proposals/" + propID(dedup) + "/video?candidateIndex=0", http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("got %d, want %d", resp.StatusCode, tc.want)
			}
			// AC14(a) explicitly: the rejection must NOT be a silent empty
			// response — a 400 carries an error message, never zero bytes.
			if len(body) == 0 {
				t.Errorf("expected a non-empty error body, got an empty response")
			}
		})
	}
}

// AC14(b): the regression gate that would have caught every abandoned
// lexical-root confinement scheme. Every candidate in a multi-directory
// duplicate group must be served successfully (200 + full bytes) AND support a
// working range request (206 + the exact requested slice) — not just the first
// candidate, and not candidates that happen to share a directory. The fixture
// deliberately places each candidate in a genuinely different, unrelated
// subtree (a library dir, a downloads dir, an external-drive dir) so no single
// root folder could ever cover all three.
func TestDedupVideoHandler_ServesEveryCandidateAcrossDirectories(t *testing.T) {
	srv, propStore, _ := newVMAFTestMux(t)
	base := t.TempDir()

	dirs := []string{
		filepath.Join(base, "media", "Movies", "Movie A (2020)"),
		filepath.Join(base, "downloads", "completed", "movie.a.1080p.bluray"),
		filepath.Join(base, "mnt", "external-usb", "old-backup"),
	}
	// Distinct, range-able contents per candidate so a mixed-up path serves the
	// wrong bytes and fails loudly.
	contents := [][]byte{
		[]byte("candidate-A-video-bytes-0123456789ABCDEF"),
		[]byte("candidate-B-different-length-video-payload-9876543210"),
		[]byte("candidate-C-yet-another-distinct-video-blob-ZZZZ"),
	}

	cands := make([]proposals.Candidate, len(dirs))
	for i, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
		p := filepath.Join(d, "video.mkv")
		if err := os.WriteFile(p, contents[i], 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		cands[i] = proposals.Candidate{Label: filepath.Base(d), Path: p}
	}

	dedup := insertProposal(t, propStore, mode.Movies, proposals.Dedup, cands)

	for i := range cands {
		want := contents[i]

		// Full GET: 200 + the complete, correct bytes.
		resp, err := http.Get(videoURL(srv.URL, "movies", dedup.ID, i))
		if err != nil {
			t.Fatalf("candidate %d full GET: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("candidate %d: full GET got %d, want 200", i, resp.StatusCode)
		}
		if string(body) != string(want) {
			t.Fatalf("candidate %d: full GET body = %q, want %q", i, body, want)
		}

		// Range GET: 206 Partial Content + exactly the requested slice, proving
		// seek/scrub support (http.ServeContent over an *os.File).
		req, err := http.NewRequest(http.MethodGet, videoURL(srv.URL, "movies", dedup.ID, i), nil)
		if err != nil {
			t.Fatalf("candidate %d build range req: %v", i, err)
		}
		req.Header.Set("Range", "bytes=2-5")
		rResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("candidate %d range GET: %v", i, err)
		}
		rBody, _ := io.ReadAll(rResp.Body)
		rResp.Body.Close()
		if rResp.StatusCode != http.StatusPartialContent {
			t.Fatalf("candidate %d: range GET got %d, want 206", i, rResp.StatusCode)
		}
		if string(rBody) != string(want[2:6]) {
			t.Fatalf("candidate %d: range body = %q, want %q", i, rBody, want[2:6])
		}
	}
}
