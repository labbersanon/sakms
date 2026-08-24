package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dedupscan"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/sectionlock"
)

func videoURL(srv, m string, id int64) string {
	return srv + "/api/modes/" + m + "/proposals/" + strconv.FormatInt(id, 10) + "/video"
}

func candidateVideoURL(srv, m string, id int64, i int) string {
	return videoURL(srv, m, id) + "?candidateIndex=" + strconv.Itoa(i)
}

// insertRenameProposal seeds a single-source proposal whose SourcePath is a REAL
// file, which insertProposal's shared "/library/group" fixture cannot be.
func insertRenameProposal(t *testing.T, propStore *proposals.Store, m mode.Mode, path string) proposals.Proposal {
	t.Helper()
	saved, err := propStore.ReplacePending(context.Background(), m, proposals.Rename, []proposals.Proposal{{
		Status:     proposals.Pending,
		SourceName: filepath.Base(path),
		SourcePath: path,
	}})
	if err != nil {
		t.Fatalf("inserting rename proposal: %v", err)
	}
	return saved[0]
}

// newProposalVideoMux returns the UNWRAPPED mux so a test can install its own
// middleware — specifically withLockedAdult, which must sit between the client
// and NewMux to inject a sectionlock.Decision.
func newProposalVideoMux(t *testing.T) (http.Handler, *proposals.Store) {
	t.Helper()
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	mux := NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, dedupscan.New(), nil, nil, nil, nil)
	return mux, propStore
}

// AC14(a): an out-of-range candidateIndex or an unknown proposalId is rejected
// with an explicit 400 — never a silent empty 200 response.
func TestProposalVideoHandler_RejectsBadRequests(t *testing.T) {
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
		{"unknown mode", "/api/modes/bogus/proposals/" + propID(dedup) + "/video?candidateIndex=0", http.StatusBadRequest},
		{"non-numeric id", "/api/modes/movies/proposals/abc/video?candidateIndex=0", http.StatusBadRequest},
		{"a candidate-carrying proposal with no candidateIndex", "/api/modes/movies/proposals/" + propID(dedup) + "/video", http.StatusBadRequest},
		{"candidateIndex out of range (high)", "/api/modes/movies/proposals/" + propID(dedup) + "/video?candidateIndex=5", http.StatusBadRequest},
		{"candidateIndex out of range (negative)", "/api/modes/movies/proposals/" + propID(dedup) + "/video?candidateIndex=-1", http.StatusBadRequest},
		{"unknown proposal id", "/api/modes/movies/proposals/999999/video?candidateIndex=0", http.StatusBadRequest},
		{"mode mismatch", "/api/modes/series/proposals/" + propID(dedup) + "/video?candidateIndex=0", http.StatusBadRequest},
		{"candidateIndex present but empty", "/api/modes/movies/proposals/" + propID(dedup) + "/video?candidateIndex=", http.StatusBadRequest},
		{"single-source proposal with a candidateIndex", "/api/modes/movies/proposals/" + propID(rename) + "/video?candidateIndex=0", http.StatusBadRequest},
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
func TestProposalVideoHandler_ServesEveryCandidateAcrossDirectories(t *testing.T) {
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
		resp, err := http.Get(candidateVideoURL(srv.URL, "movies", dedup.ID, i))
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
		req, err := http.NewRequest(http.MethodGet, candidateVideoURL(srv.URL, "movies", dedup.ID, i), nil)
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

// T-1 (§8.3): the feature itself — a single-source (Rename) proposal now
// serves 200 + bytes instead of 400 "not a dedup proposal". Table-driven over
// all three modes per AC #6; the Range assertion is repeated here rather than
// inherited from the Dedup test, because inheriting it would prove nothing
// about this new code path even though http.ServeContent is shared.
func TestProposalVideoHandler_ServesRenameProposal(t *testing.T) {
	modes := []mode.Mode{mode.Movies, mode.Series, mode.Adult}
	for _, m := range modes {
		t.Run(string(m), func(t *testing.T) {
			srv, propStore, _ := newVMAFTestMux(t)

			dir := t.TempDir()
			want := []byte("rename-proposal-video-bytes-" + string(m) + "-0123456789")
			path := filepath.Join(dir, "video.mkv")
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			prop := insertRenameProposal(t, propStore, m, path)

			// Full GET: 200 + the complete, correct bytes.
			resp, err := http.Get(videoURL(srv.URL, string(m), prop.ID))
			if err != nil {
				t.Fatalf("full GET: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("full GET got %d, want 200; body=%q", resp.StatusCode, body)
			}
			if string(body) != string(want) {
				t.Fatalf("full GET body = %q, want %q", body, want)
			}

			// Range GET: 206 + exactly the requested slice.
			req, err := http.NewRequest(http.MethodGet, videoURL(srv.URL, string(m), prop.ID), nil)
			if err != nil {
				t.Fatalf("build range req: %v", err)
			}
			req.Header.Set("Range", "bytes=2-5")
			rResp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("range GET: %v", err)
			}
			rBody, _ := io.ReadAll(rResp.Body)
			rResp.Body.Close()
			if rResp.StatusCode != http.StatusPartialContent {
				t.Fatalf("range GET got %d, want 206", rResp.StatusCode)
			}
			if string(rBody) != string(want[2:6]) {
				t.Fatalf("range body = %q, want %q", rBody, want[2:6])
			}
		})
	}
}

// T-2 (§8.3): the Dedup (candidate-indexed) shape in Adult mode specifically.
// The existing multi-directory regression test (above) is Movies-only; AC #6
// requires all three modes.
func TestProposalVideoHandler_ServesDedupAdultProposal(t *testing.T) {
	srv, propStore, _ := newVMAFTestMux(t)

	dir := t.TempDir()
	want := []byte("adult-dedup-candidate-video-bytes-0123456789")
	path := filepath.Join(dir, "video.mkv")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	dedup := insertProposal(t, propStore, mode.Adult, proposals.Dedup, []proposals.Candidate{
		{Label: "a", Path: path},
	})

	resp, err := http.Get(candidateVideoURL(srv.URL, "adult", dedup.ID, 0))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200; body=%q", resp.StatusCode, body)
	}
	if string(body) != string(want) {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

// T-3 (§8.3): THE security test — the Adult PIN-lock check genuinely refuses
// to serve either proposal shape while Adult is locked, proven by BYTE
// ABSENCE, not just the status code.
//
// Both sub-cases MUST seed a REAL on-disk file containing a distinct marker
// string. Reusing the existing Dedup fixtures' non-existent literal paths
// (/a.mkv, /b.mkv) would make the byte-absence assertion structurally
// incapable of firing: if a future refactor moved denyIfAdultLocked below
// os.Open, the open would fail on a path that never existed and the handler
// would answer 500 — never 200-with-bytes — so this test would still pass
// while proving only a status code. A distinct marker per sub-case makes a
// crossed fixture fail loudly instead of passing on the other's bytes.
func TestProposalVideoHandler_AdultLockedRefusesBeforeAnyBytes(t *testing.T) {
	const (
		dedupMarker  = "ADULT-DEDUP-BYTES-DO-NOT-SERVE-0123456789"
		renameMarker = "ADULT-RENAME-BYTES-DO-NOT-SERVE-9876543210"
	)

	tests := []struct {
		name   string
		marker string
		// seed writes the marker into a real on-disk file and inserts a
		// proposal pointing at it, returning the request URL to hit.
		seed func(t *testing.T, srv string, propStore *proposals.Store) string
	}{
		{
			name:   "dedup/adult",
			marker: dedupMarker,
			seed: func(t *testing.T, srv string, propStore *proposals.Store) string {
				t.Helper()
				path := filepath.Join(t.TempDir(), "video.mkv")
				if err := os.WriteFile(path, []byte(dedupMarker), 0o644); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
				prop := insertProposal(t, propStore, mode.Adult, proposals.Dedup, []proposals.Candidate{
					{Label: "a", Path: path},
				})
				return candidateVideoURL(srv, "adult", prop.ID, 0)
			},
		},
		{
			name:   "rename/adult",
			marker: renameMarker,
			seed: func(t *testing.T, srv string, propStore *proposals.Store) string {
				t.Helper()
				path := filepath.Join(t.TempDir(), "video.mkv")
				if err := os.WriteFile(path, []byte(renameMarker), 0o644); err != nil {
					t.Fatalf("write fixture: %v", err)
				}
				prop := insertRenameProposal(t, propStore, mode.Adult, path)
				return videoURL(srv, "adult", prop.ID)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// newProposalVideoMux returns the BARE mux (§8.1) precisely so
			// withLockedAdult can sit between the client and NewMux —
			// newVMAFTestMux returns an already-started *httptest.Server and
			// cannot be wrapped. withLockedAdult (delete_batch_test.go, same
			// package) is reused as-is rather than duplicated: both files are
			// package api, so it is already visible here.
			mux, propStore := newProposalVideoMux(t)
			srv := httptest.NewServer(withLockedAdult(mux))
			defer srv.Close()

			resp, err := http.Get(tc.seed(t, srv.URL, propStore))
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("got %d, want 403; body=%q", resp.StatusCode, body)
			}
			// The whole point: a status-only assertion passes against a
			// handler that streams first and reports the lock afterwards.
			if strings.Contains(string(body), tc.marker) {
				t.Fatalf("the locked Adult section served file bytes: %q", body)
			}
			var out map[string]string
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("unmarshal body: %v; body=%q", err, body)
			}
			if out["code"] != "section_locked" || out["section"] != sectionlock.SectionAdultContent {
				t.Fatalf("expected the section_locked body, got %q", body)
			}
		})
	}
}

// T-4 (§8.3): the false-positive guard. A ticket-unlocked request must still
// be served even though the section is in the Locked set — without this, a
// future "simplify" that tests Locked.Has(...) directly instead of
// Decision.Allows would lock out every legitimate unlocked operator and no
// test would notice.
func TestProposalVideoHandler_AdultUnlockedTicketStillServes(t *testing.T) {
	mux, propStore := newProposalVideoMux(t)
	withUnlockedAdult := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d := sectionlock.Decision{
				Enforcing: true,
				Locked:    sectionlock.NewSet(sectionlock.SectionAdultContent),
				Unlocked:  true,
			}
			h.ServeHTTP(w, r.WithContext(sectionlock.WithDecision(r.Context(), d)))
		})
	}
	srv := httptest.NewServer(withUnlockedAdult(mux))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "video.mkv")
	want := []byte("adult-unlocked-ticket-still-serves-bytes")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	prop := insertRenameProposal(t, propStore, mode.Adult, path)

	resp, err := http.Get(videoURL(srv.URL, "adult", prop.ID))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200; body=%q", resp.StatusCode, body)
	}
	if string(body) != string(want) {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

// T-5 (§8.3): pins the §2.4 ordering decision that the PIN check runs BEFORE
// proposalVideoPath — an out-of-range candidateIndex against a locked Adult
// Dedup proposal must still answer 403 section_locked, never 400
// "candidateIndex out of range", which would be a candidate-count oracle for
// a locked duplicate group.
func TestProposalVideoHandler_AdultLockedRefusesBeforeLeakingShape(t *testing.T) {
	mux, propStore := newProposalVideoMux(t)
	srv := httptest.NewServer(withLockedAdult(mux))
	defer srv.Close()

	dedup := insertProposal(t, propStore, mode.Adult, proposals.Dedup, []proposals.Candidate{
		{Label: "a", Path: "/a.mkv"}, {Label: "b", Path: "/b.mkv"},
	})

	resp, err := http.Get(candidateVideoURL(srv.URL, "adult", dedup.ID, 99))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d, want 403 section_locked (not a 400 candidateIndex-out-of-range oracle); body=%q", resp.StatusCode, body)
	}
	var out map[string]string
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal body: %v; body=%q", err, body)
	}
	if out["code"] != "section_locked" || out["section"] != sectionlock.SectionAdultContent {
		t.Fatalf("expected the section_locked body, got %q", body)
	}
}

// T-5b (§8.3): the last unpinned ordering from §2.4 — the PIN check runs
// BEFORE the mode-mismatch check specifically, not just before file-serving.
// An Adult proposal addressed via a Movies path while locked must still
// answer 403 section_locked, never the 400 "proposal does not belong to that
// mode" — which would confirm the id exists and is not a Movies proposal, an
// id-existence-and-mode oracle for a locked section.
func TestProposalVideoHandler_AdultLockedRefusesBeforeModeMismatch(t *testing.T) {
	mux, propStore := newProposalVideoMux(t)
	srv := httptest.NewServer(withLockedAdult(mux))
	defer srv.Close()

	prop := insertProposal(t, propStore, mode.Adult, proposals.Dedup, []proposals.Candidate{
		{Label: "a", Path: "/a.mkv"},
	})

	resp, err := http.Get(candidateVideoURL(srv.URL, "movies", prop.ID, 0))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d, want 403 section_locked (not a 400 mode-mismatch); body=%q", resp.StatusCode, body)
	}
	var out map[string]string
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal body: %v; body=%q", err, body)
	}
	if out["code"] != "section_locked" || out["section"] != sectionlock.SectionAdultContent {
		t.Fatalf("expected the section_locked body, got %q", body)
	}
}
