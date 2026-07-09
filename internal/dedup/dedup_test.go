package dedup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curtiswtaylorjr/sak/internal/identify"
	"github.com/curtiswtaylorjr/sak/internal/mediainfo"
	"github.com/curtiswtaylorjr/sak/internal/mode"
	"github.com/curtiswtaylorjr/sak/internal/proposals"
	"github.com/curtiswtaylorjr/sak/internal/servarr"
	"github.com/curtiswtaylorjr/sak/internal/stashbox"
	"github.com/curtiswtaylorjr/sak/internal/throttle"
)

// fakeProber maps a video file path to a canned mediainfo.Probe result, so
// tests never need a real ffprobe binary.
type fakeProber struct {
	byPath map[string]*mediainfo.Probe
}

func (f *fakeProber) Probe(ctx context.Context, path string) (*mediainfo.Probe, error) {
	p, ok := f.byPath[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return p, nil
}

func newTestSession(t *testing.T, app servarr.App, handler http.HandlerFunc) *mode.Session {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	m := mode.Movies
	if app == servarr.Sonarr {
		m = mode.Series
	}
	return &mode.Session{
		Mode:    m,
		Servarr: servarr.New(servarr.Config{BaseURL: srv.URL, APIKey: "test-key", App: app}, srv.Client()),
	}
}

// writeVideoFile creates dir (if needed) and a dummy video file inside it,
// returning the file's full path.
func writeVideoFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestFindVideoFile_PathIsAlreadyAFile(t *testing.T) {
	dir := t.TempDir()
	f := writeVideoFile(t, dir, "movie.mkv", 100)

	got, err := findVideoFile(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != f {
		t.Errorf("expected %q, got %q", f, got)
	}
}

func TestFindVideoFile_DirectoryPicksLargestVideoFile(t *testing.T) {
	dir := t.TempDir()
	writeVideoFile(t, dir, "sample.mkv", 10)
	big := writeVideoFile(t, dir, "movie.mkv", 1000)
	writeVideoFile(t, dir, "poster.jpg", 5000) // bigger, but not a video extension

	got, err := findVideoFile(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != big {
		t.Errorf("expected the largest video file %q, got %q", big, got)
	}
}

func TestFindVideoFile_NoVideoFilesErrors(t *testing.T) {
	dir := t.TempDir()
	writeVideoFile(t, dir, "readme.txt", 10)

	if _, err := findVideoFile(dir); err == nil {
		t.Error("expected an error when no video file exists in the directory")
	}
}

func TestFindVideoFile_MissingPathErrors(t *testing.T) {
	if _, err := findVideoFile(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected an error for a nonexistent path")
	}
}

func TestScan_RefusesSeries(t *testing.T) {
	sess := newTestSession(t, servarr.Sonarr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Scan must not make any HTTP call for an unsupported app")
	})
	if _, err := Scan(context.Background(), sess, &fakeProber{}); err == nil {
		t.Fatal("expected Scan to refuse a Series (Sonarr) session")
	}
}

func TestScan_TrackedItemPlusOrphan_ProposesWithCorrectWinner(t *testing.T) {
	dir := t.TempDir()
	trackedDir := filepath.Join(dir, "Movies", "Some Movie (2020)")
	orphanDir := filepath.Join(dir, "Movies", "Some.Movie.2020.1080p.BluRay.x264-GROUP")
	trackedFile := writeVideoFile(t, trackedDir, "movie.mkv", 100)
	orphanFile := writeVideoFile(t, orphanDir, "movie.mkv", 100)

	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/rootfolder":
			w.Write([]byte(`[{"id":1,"path":"` + filepath.Join(dir, "Movies") + `","accessible":true,"freeSpace":1,"unmappedFolders":[
				{"name":"Some.Movie.2020.1080p.BluRay.x264-GROUP","path":"` + orphanDir + `","relativePath":"Some.Movie.2020.1080p.BluRay.x264-GROUP"}
			]}]`))
		case "/api/v3/movie":
			json.NewEncoder(w).Encode([]servarr.TrackedItem{
				{ID: 9, Title: "Some Movie", Path: trackedDir, RootFolderPath: filepath.Join(dir, "Movies"), TMDBID: 42, QualityProfileID: 4},
			})
		case "/api/v3/movie/lookup":
			w.Write([]byte(`[{"title":"Some Movie","year":2020,"tmdbId":42}]`))
		case "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":4,"name":"HD-1080p"}]`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	})

	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}

	got, err := Scan(context.Background(), sess, prober)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 duplicate group, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.TMDBID != 42 || len(p.Candidates) != 2 {
		t.Fatalf("unexpected proposal: %+v", p)
	}

	var winner, loser proposals.Candidate
	for _, c := range p.Candidates {
		if c.Winner {
			winner = c
		} else {
			loser = c
		}
	}
	if winner.Path != orphanFile {
		t.Errorf("expected the higher-resolution orphan to win, got winner=%+v", winner)
	}
	if loser.Path != trackedFile || loser.TrackedID != 9 {
		t.Errorf("expected the tracked file to be the loser, got %+v", loser)
	}
}

func TestScan_SingleNewOrphanIsNotADuplicate(t *testing.T) {
	dir := t.TempDir()
	orphanDir := filepath.Join(dir, "Movies", "New.Movie.2020")
	writeVideoFile(t, orphanDir, "movie.mkv", 100)

	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/rootfolder":
			w.Write([]byte(`[{"id":1,"path":"` + filepath.Join(dir, "Movies") + `","accessible":true,"freeSpace":1,"unmappedFolders":[
				{"name":"New.Movie.2020","path":"` + orphanDir + `","relativePath":"New.Movie.2020"}
			]}]`))
		case "/api/v3/movie":
			w.Write([]byte(`[]`))
		case "/api/v3/movie/lookup":
			w.Write([]byte(`[{"title":"New Movie","year":2020,"tmdbId":99}]`))
		case "/api/v3/qualityprofile":
			w.Write([]byte(`[]`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	})

	got, err := Scan(context.Background(), sess, &fakeProber{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no duplicate groups for a single new item, got %+v", got)
	}
}

func TestApply_KeepsWinnerByDefault_DeletesOrphanLoser(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeVideoFile(t, dir, "loser.mkv", 10)

	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "X", TMDBID: 1,
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: "/winner.mkv", TrackedID: 9, Winner: true},
			{Label: "loser", Path: loserPath},
		},
	}
	id, err := Apply(context.Background(), sess, p, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 9 {
		t.Errorf("expected the already-tracked winner's id (9), got %d", id)
	}
	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Error("expected the losing orphan file to be deleted")
	}
}

func TestApply_WinnerIsOrphan_DeletesTrackedLoserAndRegistersWinner(t *testing.T) {
	dir := t.TempDir()
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	var deletedTrackedID int
	var addedBody map[string]any
	var scanTriggered bool
	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v3/movie/9":
			deletedTrackedID = 9
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/movie":
			json.NewDecoder(r.Body).Decode(&addedBody)
			w.Write([]byte(`{"id":55}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			scanTriggered = true
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Some Movie", TMDBID: 42,
		RootFolderPath: "/media/Movies", QualityProfileID: 4,
		Candidates: []proposals.Candidate{
			{Label: "tracked", Path: "/tracked.mkv", TrackedID: 9},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	id, err := Apply(context.Background(), sess, p, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 55 {
		t.Errorf("expected the newly registered id (55), got %d", id)
	}
	if deletedTrackedID != 9 {
		t.Error("expected the losing tracked item to be deleted")
	}
	if addedBody["tmdbId"] != float64(42) || addedBody["title"] != "Some Movie" {
		t.Errorf("unexpected Add request body: %+v", addedBody)
	}
	if !scanTriggered {
		t.Error("expected a downloaded-files scan to be triggered after registering the winner")
	}
}

func TestApply_KeepAll_NoMutation(t *testing.T) {
	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("keepAll must not make any HTTP or filesystem mutation")
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending,
		Candidates: []proposals.Candidate{
			{Label: "a", Path: "/a.mkv", TrackedID: 9},
			{Label: "b", Path: "/b.mkv"},
		},
	}
	id, err := Apply(context.Background(), sess, p, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 9 {
		t.Errorf("expected keepAll to still report the existing tracked id, got %d", id)
	}
}

func TestApply_ExplicitKeepIndexOverridesWinner(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeVideoFile(t, dir, "b.mkv", 10)

	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("keeping the already-tracked candidate should need no HTTP call")
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending,
		Candidates: []proposals.Candidate{
			{Label: "a", Path: "/a.mkv", TrackedID: 9},
			{Label: "b", Path: loserPath, Winner: true}, // Scan's pick, overridden below
		},
	}
	keepA := 0
	id, err := Apply(context.Background(), sess, p, &keepA, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 9 {
		t.Errorf("expected the explicitly kept candidate's tracked id (9), got %d", id)
	}
	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Error("expected the explicitly-not-kept file to be deleted")
	}
}

func TestApply_RejectsNonPendingProposal(t *testing.T) {
	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Apply must not make any HTTP call for a non-pending proposal")
	})
	p := proposals.Proposal{
		Status:     proposals.Applied,
		Candidates: []proposals.Candidate{{Path: "/a.mkv"}, {Path: "/b.mkv"}},
	}
	if _, err := Apply(context.Background(), sess, p, nil, false); err == nil {
		t.Fatal("expected Apply to refuse an already-applied proposal")
	}
}

func TestApply_RejectsFewerThanTwoCandidates(t *testing.T) {
	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Apply must not make any HTTP call with too few candidates")
	})
	p := proposals.Proposal{Status: proposals.Pending, Candidates: []proposals.Candidate{{Path: "/a.mkv"}}}
	if _, err := Apply(context.Background(), sess, p, nil, false); err == nil {
		t.Fatal("expected Apply to refuse a proposal with fewer than 2 candidates")
	}
}

func TestApply_RejectsOutOfRangeKeepIndex(t *testing.T) {
	sess := newTestSession(t, servarr.Radarr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Apply must not make any HTTP call for an invalid keepIndex")
	})
	p := proposals.Proposal{
		Status:     proposals.Pending,
		Candidates: []proposals.Candidate{{Path: "/a.mkv"}, {Path: "/b.mkv"}},
	}
	bad := 5
	if _, err := Apply(context.Background(), sess, p, &bad, false); err == nil {
		t.Fatal("expected Apply to refuse an out-of-range keepIndex")
	}
}

// Two well-formed UUIDs so the identify pipeline resolves via its direct
// UUID-lookup path (skipping Ollama, which these tests never configure).
const (
	sceneUUIDA = "a29768db-b3cd-4a71-a75e-4294373207bb"
	sceneUUIDB = "f47ac10b-58cc-4372-a567-0e02b2c3d479"
)

// fakeStashboxByID serves StashDB's findScene-by-id GraphQL query, returning a
// scene for each UUID present in titles and null for anything else.
func fakeStashboxByID(t *testing.T, titles map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				ID string `json:"id"`
			} `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		title, ok := titles[req.Variables.ID]
		if !ok {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": nil}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"findScene": map[string]any{
			"id": req.Variables.ID, "title": title, "release_date": "2021-01-01",
			"studio": map[string]any{"name": "Some Studio", "parent": nil},
		}}})
	}
}

// newAdultTestSession builds a Whisparr-backed Adult session whose Identify
// resolves scenes via the UUID path against a fake StashDB — no Ollama needed
// (the UUID path resolves before ParseFilename), throttle disabled for speed.
func newAdultTestSession(t *testing.T, whisparrHandler, stashboxHandler http.HandlerFunc) *mode.Session {
	t.Helper()
	wsrv := httptest.NewServer(whisparrHandler)
	t.Cleanup(wsrv.Close)
	ssrv := httptest.NewServer(stashboxHandler)
	t.Cleanup(ssrv.Close)
	boxes := map[string]*stashbox.Client{
		"stashdb": stashbox.New(stashbox.Config{Endpoint: ssrv.URL, APIKey: "k", HasVoteField: true}, ssrv.Client()),
	}
	return &mode.Session{
		Mode:    mode.Adult,
		Servarr: servarr.New(servarr.Config{BaseURL: wsrv.URL, APIKey: "test-key", App: servarr.Whisparr}, wsrv.Client()),
		Identify: &identify.Identifier{
			Boxes:    identify.NewBoxSearcher(boxes, nil),
			Throttle: throttle.New(0),
		},
	}
}

// newAdultApplySession builds a Whisparr Adult session with no Identify — Apply
// never touches it, and the guard/passthrough only need AppType()==Whisparr.
func newAdultApplySession(t *testing.T, handler http.HandlerFunc) *mode.Session {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &mode.Session{
		Mode:    mode.Adult,
		Servarr: servarr.New(servarr.Config{BaseURL: srv.URL, APIKey: "test-key", App: servarr.Whisparr}, srv.Client()),
	}
}

func TestScan_Adult_RefusesWhenIdentifyNil(t *testing.T) {
	sess := newAdultApplySession(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("Scan must fail fast before any HTTP call, got %s %s", r.Method, r.URL.Path)
	})
	_, err := Scan(context.Background(), sess, &fakeProber{})
	if err == nil {
		t.Fatal("expected Scan to refuse an Adult session with no identify backbone")
	}
	if !strings.Contains(err.Error(), "identification isn't configured") {
		t.Errorf("expected an actionable config error, got %v", err)
	}
}

func TestScan_Adult_TrackedPlusOrphan_GroupsByForeignID(t *testing.T) {
	dir := t.TempDir()
	adultRoot := filepath.Join(dir, "Adult")
	trackedDir := filepath.Join(adultRoot, "Some Scene")
	orphanName := "Some.Scene." + sceneUUIDA
	orphanDir := filepath.Join(adultRoot, orphanName)
	trackedFile := writeVideoFile(t, trackedDir, "scene.mkv", 100)
	orphanFile := writeVideoFile(t, orphanDir, "scene.mkv", 100)

	sess := newAdultTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/rootfolder":
			w.Write([]byte(`[{"id":1,"path":"` + adultRoot + `","accessible":true,"freeSpace":1,"unmappedFolders":[
				{"name":"` + orphanName + `","path":"` + orphanDir + `","relativePath":"` + orphanName + `"}
			]}]`))
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode([]servarr.TrackedItem{
				{ID: 7, Title: "Some Scene", Path: trackedDir, RootFolderPath: adultRoot, ForeignID: sceneUUIDA, QualityProfileID: 4},
			})
		case r.URL.Path == "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":4,"name":"HD"}]`))
		default:
			t.Fatalf("unexpected whisparr request: %s %s", r.Method, r.URL.Path)
		}
	}, fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))

	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		trackedFile: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		orphanFile:  {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}

	got, err := Scan(context.Background(), sess, prober)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 duplicate group, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.Status != proposals.Pending || p.ForeignID != sceneUUIDA || p.ItemType != "scene" {
		t.Fatalf("expected a Pending scene proposal keyed by foreignID, got %+v", p)
	}
	if p.TMDBID != 0 {
		t.Errorf("expected no TMDBID on an Adult proposal, got %d", p.TMDBID)
	}
	if len(p.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %+v", p.Candidates)
	}
	var winner, loser proposals.Candidate
	for _, c := range p.Candidates {
		if c.Winner {
			winner = c
		} else {
			loser = c
		}
	}
	if winner.Path != orphanFile {
		t.Errorf("expected the higher-quality orphan to win, got winner=%+v", winner)
	}
	if loser.Path != trackedFile || loser.TrackedID != 7 {
		t.Errorf("expected the tracked file to be the loser, got %+v", loser)
	}
}

func TestScan_Adult_TwoOrphans_GroupByForeignID(t *testing.T) {
	dir := t.TempDir()
	adultRoot := filepath.Join(dir, "Adult")
	nameA := "Some.Scene.SD." + sceneUUIDA
	nameB := "Some.Scene.HD." + sceneUUIDA
	dirA := filepath.Join(adultRoot, nameA)
	dirB := filepath.Join(adultRoot, nameB)
	fileA := writeVideoFile(t, dirA, "scene.mkv", 100)
	fileB := writeVideoFile(t, dirB, "scene.mkv", 100)

	sess := newAdultTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/rootfolder":
			w.Write([]byte(`[{"id":1,"path":"` + adultRoot + `","accessible":true,"freeSpace":1,"unmappedFolders":[
				{"name":"` + nameA + `","path":"` + dirA + `","relativePath":"` + nameA + `"},
				{"name":"` + nameB + `","path":"` + dirB + `","relativePath":"` + nameB + `"}
			]}]`))
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodGet:
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v3/qualityprofile":
			w.Write([]byte(`[{"id":4,"name":"HD"}]`))
		default:
			t.Fatalf("unexpected whisparr request: %s %s", r.Method, r.URL.Path)
		}
	}, fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))

	prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
		fileA: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
		fileB: {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
	}}

	got, err := Scan(context.Background(), sess, prober)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || len(got[0].Candidates) != 2 {
		t.Fatalf("expected 1 orphan-vs-orphan group of 2, got %+v", got)
	}
	if got[0].ForeignID != sceneUUIDA || got[0].ItemType != "scene" {
		t.Errorf("expected the group keyed by foreignID with itemType scene, got %+v", got[0])
	}
}

// TestScan_Adult_NoForeignIDOnTracked_DegradesGracefully is the load-bearing
// test for §0.2: if Whisparr's GET /movie doesn't report foreignId, the
// tracked side is skipped and the code degrades to orphan-vs-orphan dedup — no
// crash, no misgroup.
func TestScan_Adult_NoForeignIDOnTracked_DegradesGracefully(t *testing.T) {
	// (a) tracked item WITHOUT foreignId + a single orphan of that scene → no
	// group at all (tracked side skipped; a lone orphan isn't a duplicate).
	t.Run("single orphan produces no group", func(t *testing.T) {
		dir := t.TempDir()
		adultRoot := filepath.Join(dir, "Adult")
		trackedDir := filepath.Join(adultRoot, "Some Scene")
		orphanName := "Some.Scene." + sceneUUIDA
		orphanDir := filepath.Join(adultRoot, orphanName)
		writeVideoFile(t, trackedDir, "scene.mkv", 100)
		orphanFile := writeVideoFile(t, orphanDir, "scene.mkv", 100)

		sess := newAdultTestSession(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/api/v3/rootfolder":
				w.Write([]byte(`[{"id":1,"path":"` + adultRoot + `","accessible":true,"freeSpace":1,"unmappedFolders":[
					{"name":"` + orphanName + `","path":"` + orphanDir + `","relativePath":"` + orphanName + `"}
				]}]`))
			case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodGet:
				// A tracked item reported WITHOUT a foreignId (the unverified-
				// assumption failure mode) — no "foreignId" key at all.
				json.NewEncoder(w).Encode([]servarr.TrackedItem{
					{ID: 7, Title: "Some Scene", Path: trackedDir, RootFolderPath: adultRoot, QualityProfileID: 4},
				})
			case r.URL.Path == "/api/v3/qualityprofile":
				w.Write([]byte(`[{"id":4,"name":"HD"}]`))
			default:
				t.Fatalf("unexpected whisparr request: %s %s", r.Method, r.URL.Path)
			}
		}, fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))

		prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
			orphanFile: {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
		}}
		got, err := Scan(context.Background(), sess, prober)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected no group when the tracked side has no foreignId and only one orphan exists, got %+v", got)
		}
	})

	// (b) same missing-foreignId tracked item, but TWO orphans of the scene →
	// orphan-vs-orphan group still emitted (degradation, not breakage).
	t.Run("two orphans still group", func(t *testing.T) {
		dir := t.TempDir()
		adultRoot := filepath.Join(dir, "Adult")
		trackedDir := filepath.Join(adultRoot, "Some Scene")
		nameA := "Some.Scene.SD." + sceneUUIDA
		nameB := "Some.Scene.HD." + sceneUUIDA
		dirA := filepath.Join(adultRoot, nameA)
		dirB := filepath.Join(adultRoot, nameB)
		writeVideoFile(t, trackedDir, "scene.mkv", 100)
		fileA := writeVideoFile(t, dirA, "scene.mkv", 100)
		fileB := writeVideoFile(t, dirB, "scene.mkv", 100)

		sess := newAdultTestSession(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/api/v3/rootfolder":
				w.Write([]byte(`[{"id":1,"path":"` + adultRoot + `","accessible":true,"freeSpace":1,"unmappedFolders":[
					{"name":"` + nameA + `","path":"` + dirA + `","relativePath":"` + nameA + `"},
					{"name":"` + nameB + `","path":"` + dirB + `","relativePath":"` + nameB + `"}
				]}]`))
			case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodGet:
				json.NewEncoder(w).Encode([]servarr.TrackedItem{
					{ID: 7, Title: "Some Scene", Path: trackedDir, RootFolderPath: adultRoot, QualityProfileID: 4},
				})
			case r.URL.Path == "/api/v3/qualityprofile":
				w.Write([]byte(`[{"id":4,"name":"HD"}]`))
			default:
				t.Fatalf("unexpected whisparr request: %s %s", r.Method, r.URL.Path)
			}
		}, fakeStashboxByID(t, map[string]string{sceneUUIDA: "Some Scene"}))

		prober := &fakeProber{byPath: map[string]*mediainfo.Probe{
			fileA: {CodecName: "h264", Width: 1280, Height: 720, BitRate: 3000},
			fileB: {CodecName: "h265", Width: 1920, Height: 1080, BitRate: 8000},
		}}
		got, err := Scan(context.Background(), sess, prober)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 || len(got[0].Candidates) != 2 {
			t.Fatalf("expected orphan-vs-orphan dedup to still fire when the tracked side degrades, got %+v", got)
		}
	})
}

// TestAdultForeignID covers the grouping-key decision dedup makes for each
// identify outcome — the web-only/nil/error skips (§5 item 6, "Dedup leaves it
// for Rename") and the delegation to identify.MatchResult.WhisparrForeignID so
// dedup's key can never diverge from what rename.classifyAdultMatch derives
// (§5 item 9 parity; the derivation itself is unit-tested in the identify
// package's TestWhisparrForeignID).
func TestAdultForeignID(t *testing.T) {
	cases := []struct {
		name         string
		res          *identify.MatchResult
		err          error
		wantOK       bool
		wantFID      string
		wantItemType string
		wantTitle    string
	}{
		{name: "identify error", err: os.ErrNotExist, wantOK: false},
		{name: "nil result", res: nil, wantOK: false},
		{name: "web-only (no scene id)", res: &identify.MatchResult{Source: "web_search", SceneID: "", Box: ""}, wantOK: false},
		{name: "stashdb raw uuid", res: &identify.MatchResult{Box: "stashdb", SceneID: "u1", Type: "scene", Title: "T"}, wantOK: true, wantFID: "u1", wantItemType: "scene", wantTitle: "T"},
		{name: "tpdb gets prefix", res: &identify.MatchResult{Box: "tpdb", SceneID: "77", Type: "scene", Title: "P"}, wantOK: true, wantFID: "tpdbId:77", wantItemType: "scene", wantTitle: "P"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fid, itemType, title, ok := adultForeignID(tc.res, tc.err)
			if ok != tc.wantOK {
				t.Fatalf("ok: got %v, want %v", ok, tc.wantOK)
			}
			if fid != tc.wantFID || itemType != tc.wantItemType || title != tc.wantTitle {
				t.Errorf("got (%q,%q,%q), want (%q,%q,%q)", fid, itemType, title, tc.wantFID, tc.wantItemType, tc.wantTitle)
			}
		})
	}
}

func TestApply_Adult_RegistersUntrackedWinnerWithForeignID(t *testing.T) {
	dir := t.TempDir()
	winnerPath := writeVideoFile(t, dir, "winner.mkv", 10)

	var deletedTrackedID int
	var addBody map[string]any
	var scanTriggered bool
	sess := newAdultApplySession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v3/movie/9":
			deletedTrackedID = 9
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/movie":
			json.NewDecoder(r.Body).Decode(&addBody)
			w.Write([]byte(`{"id":88}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			scanTriggered = true
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Some Scene",
		ForeignID: sceneUUIDA, ItemType: "scene",
		RootFolderPath: "/media/Adult", QualityProfileID: 4,
		Candidates: []proposals.Candidate{
			{Label: "tracked", Path: "/tracked.mkv", TrackedID: 9},
			{Label: "winner", Path: winnerPath, Winner: true},
		},
	}
	id, err := Apply(context.Background(), sess, p, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 88 {
		t.Errorf("expected the newly registered id (88), got %d", id)
	}
	if deletedTrackedID != 9 {
		t.Error("expected the losing tracked scene to be deleted")
	}
	if addBody["foreignId"] != sceneUUIDA || addBody["itemType"] != "scene" {
		t.Errorf("expected the scene identifiers on the Add body, got %+v", addBody)
	}
	if !scanTriggered {
		t.Error("expected a downloaded-files scan after registering the winner")
	}
}

// TestApply_Adult_GuardRejectsBlankForeignID proves the §0.3 guard fires BEFORE
// any mutation: a blank-identifier Whisparr proposal is refused with no HTTP
// call AND no file removal — not deleted-then-refused.
func TestApply_Adult_GuardRejectsBlankForeignID(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeVideoFile(t, dir, "loser.mkv", 10)

	sess := newAdultApplySession(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("guard must refuse before any HTTP call, but got %s %s", r.Method, r.URL.Path)
	})

	p := proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Some Scene",
		ForeignID: "", ItemType: "scene", // blank identifier — must be refused
		Candidates: []proposals.Candidate{
			{Label: "winner", Path: "/winner.mkv", Winner: true}, // untracked winner
			{Label: "loser", Path: loserPath},                    // untracked orphan with a real file
		},
	}
	if _, err := Apply(context.Background(), sess, p, nil, false); err == nil {
		t.Fatal("expected Apply to refuse a blank-identifier Whisparr proposal")
	}
	if _, err := os.Stat(loserPath); err != nil {
		t.Errorf("expected the losing orphan's file to survive a refused Apply (guard before removal), got %v", err)
	}
}
