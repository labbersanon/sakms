package api

// rename_undo_test.go — the HTTP surface of Rename Undo
// (deep-interview-rename-undo, implementation plan §5/§7).
//
// The reversal logic itself is covered in internal/rename's
// undo_apply_test.go; this file covers only what the endpoint adds: the status
// and workflow guards, the 200 round trip, and the 404 an EVICTED entry
// produces — which is the observable half of the plan's eviction requirement.
//
// Claude 2026-08-10: added TestRecentlyAppliedHandler_* for the READ half
//   (GET /api/modes/{mode}/rename/recently-applied), added in the frontend pass.
// Reason: the list endpoint's two load-bearing properties are that a fresh
//   Apply appears in it with the snapshot-derived fields populated, and that a
//   consumed (undone) entry leaves it — neither is provable from the mutation
//   handler's own tests.
// Review if: the list gains pagination or stops projecting from the snapshot.

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

	"github.com/labbersanon/sakms/internal/adultnewest"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/discoversliders"
	"github.com/labbersanon/sakms/internal/grabs"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/naming"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/rename"
	"github.com/labbersanon/sakms/internal/rssfeeds"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
	"github.com/labbersanon/sakms/internal/trakt"
)

type undoAPIEnv struct {
	srv       *httptest.Server
	propStore *proposals.Store
	libStore  *library.Store
	root      string
	ctx       context.Context
}

// newUndoAPIEnv builds a real mux over one isolated database and registers the
// archive store the same way main.go does, so an Apply run through this env's
// stores really archives.
func newUndoAPIEnv(t *testing.T, depth rename.DepthFunc) *undoAPIEnv {
	t.Helper()
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	// Every store is built against THIS database (rather than via testStores,
	// which opens its own): the handler's proposal lookup and the archive store
	// must agree on one database or the undo route can never find an entry.
	propStore := proposals.New(sqlDB)
	libStore := library.New(sqlDB)

	// The handler resolves the archive store from the package default at
	// request time, exactly as main.go registers it at boot.
	rename.SetDefaultUndoStore(rename.NewUndoStore(sqlDB, depth))
	t.Cleanup(func() { rename.SetDefaultUndoStore(nil) })

	srv := httptest.NewServer(NewMux(testHTTPClient(), connections.New(sqlDB, secretStore), nil, propStore,
		testProber(t), testPHasher(t), testVideoHasher(t), settings.New(sqlDB),
		grabs.New(sqlDB, secretStore), libStore, discoversliders.New(sqlDB),
		trakt.NewStore(sqlDB, secretStore), adultnewest.New(sqlDB), adultnewest.NewReleaseStore(sqlDB),
		testFeedHealth(), rssfeeds.NewStore(sqlDB, secretStore),
		nil, nil, nil, nil, nil, nil, nil, nil))
	t.Cleanup(srv.Close)

	return &undoAPIEnv{srv: srv, propStore: propStore, libStore: libStore,
		root: t.TempDir(), ctx: context.Background()}
}

func (e *undoAPIEnv) postUndo(t *testing.T, id int64) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(e.srv.URL+"/api/proposals/"+strconv.FormatInt(id, 10)+"/undo", "application/json", nil)
	if err != nil {
		t.Fatalf("POST undo: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading undo response: %v", err)
	}
	return resp, string(body)
}

// applyOneMovie seeds a Pending Movies rename proposal, applies it for real
// (which archives), and marks it Applied — the exact state the endpoint's
// status guard expects.
func (e *undoAPIEnv) applyOneMovie(t *testing.T, name string, tmdbID int) (proposals.Proposal, string, string) {
	t.Helper()
	src := filepath.Join(e.root, "inbox", name)
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(src, []byte("movie-bytes-"+name), 0o644); err != nil {
		t.Fatalf("writing %s: %v", src, err)
	}
	saved, err := e.propStore.ReplacePending(e.ctx, mode.Movies, proposals.Rename, []proposals.Proposal{{
		Status: proposals.Pending, SourceName: name, SourcePath: src, RootFolderPath: e.root,
		Title: "Movie " + name, Year: 2020, TMDBID: tmdbID,
	}})
	if err != nil {
		t.Fatalf("seeding the proposal: %v", err)
	}
	p := saved[0]
	itemID, changes, err := rename.ApplyLibrary(e.ctx, e.libStore, p, naming.Jellyfin, "high", nil)
	if err != nil {
		t.Fatalf("ApplyLibrary: %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, p.ID, int(itemID)); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	return p, src, changes[1].Path
}

func TestUndoProposalHandler_RoundTrip(t *testing.T) {
	e := newUndoAPIEnv(t, nil)
	p, src, dest := e.applyOneMovie(t, "roundtrip.mkv", 4001)

	resp, body := e.postUndo(t, p.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var got rename.UndoResult
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	if !got.FileRestored || got.RestoredPath != src {
		t.Errorf("expected the file restored to %q, got %+v", src, got)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("expected the applied destination gone, stat err = %v", err)
	}
	after, err := e.propStore.Get(e.ctx, p.ID)
	if err != nil {
		t.Fatalf("re-reading the proposal: %v", err)
	}
	if after.Status != proposals.Pending {
		t.Errorf("expected the proposal back in the Pending queue, got %q", after.Status)
	}

	// A second undo of the same proposal is refused by the status guard — the
	// row is Pending now, not Applied.
	resp2, body2 := e.postUndo(t, p.ID)
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 on a second undo, got %d: %s", resp2.StatusCode, body2)
	}
}

func TestUndoProposalHandler_RejectsNonAppliedProposal(t *testing.T) {
	e := newUndoAPIEnv(t, nil)
	saved, err := e.propStore.ReplacePending(e.ctx, mode.Movies, proposals.Rename, []proposals.Proposal{{
		Status: proposals.Pending, SourceName: "pending.mkv",
		SourcePath: filepath.Join(e.root, "pending.mkv"), RootFolderPath: e.root, Title: "Pending", TMDBID: 4002,
	}})
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	resp, body := e.postUndo(t, saved[0].ID)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a Pending proposal, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "only applied proposals can be undone") {
		t.Errorf("expected the status-guard message, got %q", body)
	}
}

func TestUndoProposalHandler_RejectsNonRenameWorkflow(t *testing.T) {
	e := newUndoAPIEnv(t, nil)
	saved, err := e.propStore.ReplacePending(e.ctx, mode.Movies, proposals.Purge, []proposals.Proposal{{
		Status: proposals.Pending, SourceName: "purge-me.mkv",
		SourcePath: filepath.Join(e.root, "purge-me.mkv"), RootFolderPath: e.root, Title: "Purge", TMDBID: 4003,
	}})
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := e.propStore.MarkApplied(e.ctx, saved[0].ID, 1); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	resp, body := e.postUndo(t, saved[0].ID)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-rename proposal, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "only rename proposals can be undone") {
		t.Errorf("expected the workflow-guard message, got %q", body)
	}
}

// TestUndoProposalHandler_RefusesWhenSourcePathIsLiveAgain covers the
// pre-condition guard against migration 0004's partial unique index on live
// proposals by source_path: a re-download landed at the same inbox path and a
// Scan minted a live row for it, so restoring this proposal there would put two
// live rows at one path. The refusal must happen BEFORE anything moves — a 500
// from the restore UPDATE would land after the file had already been moved back.
func TestUndoProposalHandler_RefusesWhenSourcePathIsLiveAgain(t *testing.T) {
	e := newUndoAPIEnv(t, nil)
	applied, src, dest := e.applyOneMovie(t, "recycled.mkv", 4020)

	// A fresh Scan discovers a new file that landed at the same inbox path.
	if err := os.WriteFile(src, []byte("a-different-download"), 0o644); err != nil {
		t.Fatalf("re-creating the source file: %v", err)
	}
	if _, err := e.propStore.ReplacePending(e.ctx, mode.Movies, proposals.Rename, []proposals.Proposal{{
		Status: proposals.Pending, SourceName: "recycled.mkv", SourcePath: src,
		RootFolderPath: e.root, Title: "Something Else", TMDBID: 4021,
	}}); err != nil {
		t.Fatalf("seeding the colliding live proposal: %v", err)
	}

	resp, body := e.postUndo(t, applied.ID)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for a source path that is live again, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "two live proposals at the same source path") {
		t.Errorf("expected the collision message, got %q", body)
	}
	// Nothing was moved and nothing was consumed.
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("the applied file must be untouched by a refused undo: %v", err)
	}
	still, err := e.propStore.Get(e.ctx, applied.ID)
	if err != nil {
		t.Fatalf("re-reading the proposal: %v", err)
	}
	if still.Status != proposals.Applied {
		t.Errorf("a refused undo must leave the proposal Applied, got %q", still.Status)
	}
}

// TestUndoProposalHandler_EvictedEntryIs404 is the observable half of the
// rolling-depth requirement: once the (N+1)th Apply evicts an entry, undoing it
// is indistinguishable from "nothing to undo" — a deterministic 404, not a
// vague failure.
func TestUndoProposalHandler_EvictedEntryIs404(t *testing.T) {
	e := newUndoAPIEnv(t, func(context.Context, mode.Mode) int { return 1 })
	first, _, _ := e.applyOneMovie(t, "first.mkv", 4010)
	second, _, _ := e.applyOneMovie(t, "second.mkv", 4011)

	// The second Apply pushed the first out of the window.
	resp, body := e.postUndo(t, first.ID)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for an evicted entry, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "nothing to undo") {
		t.Errorf("expected the nothing-to-undo message, got %q", body)
	}
	// The still-active one is undoable.
	resp2, body2 := e.postUndo(t, second.ID)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected the most recent Apply to still be undoable, got %d: %s", resp2.StatusCode, body2)
	}
}

// getRecentlyApplied fetches one mode's "Recently Applied" list.
func (e *undoAPIEnv) getRecentlyApplied(t *testing.T, m mode.Mode) (*http.Response, []apidto.RecentlyAppliedEntry, string) {
	t.Helper()
	resp, err := http.Get(e.srv.URL + "/api/modes/" + string(m) + "/rename/recently-applied")
	if err != nil {
		t.Fatalf("GET recently-applied: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading recently-applied response: %v", err)
	}
	var out []apidto.RecentlyAppliedEntry
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decoding %q: %v", raw, err)
		}
	}
	return resp, out, string(raw)
}

// TestRecentlyAppliedHandler_RoundTrip is the read half of the feature: an
// Apply enters the list carrying its snapshot-derived fields, and undoing it
// takes it back out (the entry is consumed, and ListActive filters consumed
// entries). It also pins the empty-list-not-404 contract, which is what keeps a
// healthy install that has applied nothing from rendering an error banner.
func TestRecentlyAppliedHandler_RoundTrip(t *testing.T) {
	e := newUndoAPIEnv(t, nil)

	// Nothing applied yet: 200 with an empty array, never a 404 or a 503.
	resp, empty, body := e.getRecentlyApplied(t, mode.Movies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for an empty list, got %d: %s", resp.StatusCode, body)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no entries before any Apply, got %+v", empty)
	}
	if strings.TrimSpace(body) != "[]" {
		t.Errorf("expected a literal empty JSON array, got %q", body)
	}

	p, _, _ := e.applyOneMovie(t, "listed.mkv", 4030)

	resp, listed, body := e.getRecentlyApplied(t, mode.Movies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if len(listed) != 1 {
		t.Fatalf("expected exactly one entry, got %+v", listed)
	}
	got := listed[0]
	if got.ProposalID != p.ID {
		t.Errorf("expected proposalId %d, got %d", p.ID, got.ProposalID)
	}
	if got.UndoID == 0 {
		t.Errorf("expected a non-zero undoId, got %+v", got)
	}
	if got.Mode != string(mode.Movies) {
		t.Errorf("expected mode %q, got %q", mode.Movies, got.Mode)
	}
	// Both come from the pre-Apply proposal SNAPSHOT, not a live re-read.
	if got.SourceName != "listed.mkv" {
		t.Errorf("expected the snapshot's sourceName, got %q", got.SourceName)
	}
	if got.Title != "Movie listed.mkv" {
		t.Errorf("expected the snapshot's title, got %q", got.Title)
	}
	if got.ViaAlternateFold {
		t.Errorf("a plain Apply must not be flagged as an alternate fold: %+v", got)
	}
	if got.AppliedAt == "" {
		t.Errorf("expected an appliedAt timestamp, got %+v", got)
	}

	// Undoing consumes the entry, which removes it from the list.
	if undoResp, undoBody := e.postUndo(t, p.ID); undoResp.StatusCode != http.StatusOK {
		t.Fatalf("expected the undo to succeed, got %d: %s", undoResp.StatusCode, undoBody)
	}
	resp, after, body := e.getRecentlyApplied(t, mode.Movies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after the undo, got %d: %s", resp.StatusCode, body)
	}
	if len(after) != 0 {
		t.Errorf("an undone entry must leave the list, got %+v", after)
	}
}

// TestRecentlyAppliedHandler_IsPerModeAndNewestFirst pins the two ordering /
// scoping properties the UI depends on: the list is scoped to the {mode} in the
// URL, and it is newest-first (the Undo an operator most likely wants is at the
// top).
func TestRecentlyAppliedHandler_IsPerModeAndNewestFirst(t *testing.T) {
	e := newUndoAPIEnv(t, nil)
	first, _, _ := e.applyOneMovie(t, "older.mkv", 4040)
	second, _, _ := e.applyOneMovie(t, "newer.mkv", 4041)

	_, movies, _ := e.getRecentlyApplied(t, mode.Movies)
	if len(movies) != 2 {
		t.Fatalf("expected two Movies entries, got %+v", movies)
	}
	if movies[0].ProposalID != second.ID || movies[1].ProposalID != first.ID {
		t.Errorf("expected newest-first (%d then %d), got %d then %d",
			second.ID, first.ID, movies[0].ProposalID, movies[1].ProposalID)
	}

	// Another mode's list never shows them.
	_, series, _ := e.getRecentlyApplied(t, mode.Series)
	if len(series) != 0 {
		t.Errorf("Movies entries must not appear in the Series list, got %+v", series)
	}
}

// TestRecentlyAppliedHandler_EmptyWhenUndoUnavailable covers the deliberate
// asymmetry with the mutation handler: with no store registered, POST /undo
// answers 503 but this list answers 200 [] — an empty list view, not a failure.
func TestRecentlyAppliedHandler_EmptyWhenUndoUnavailable(t *testing.T) {
	e := newUndoAPIEnv(t, nil)
	rename.SetDefaultUndoStore(nil)

	resp, out, body := e.getRecentlyApplied(t, mode.Movies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with undo unavailable, got %d: %s", resp.StatusCode, body)
	}
	if len(out) != 0 {
		t.Errorf("expected an empty list, got %+v", out)
	}
}
