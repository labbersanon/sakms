package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/connections"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/pruning"
	"github.com/labbersanon/sakms/internal/secrets"
	"github.com/labbersanon/sakms/internal/settings"
)

// purgeE2EEnv is a mux carrying BOTH a real connStore (the Apply path runs
// mode.Build, which dereferences it) and a real pruningStore (the rules CRUD
// routes 503 without one), all against ONE database.
//
// Neither testStores nor newPruningEnv can serve this test on its own:
// testStores predates *pruning.Store and does not hand back the *sql.DB needed
// to build one alongside its library store, while newPruningEnv passes a nil
// connStore and so cannot reach Apply. Same one-database reasoning as
// rename_undo_test.go's own env.
type purgeE2EEnv struct {
	srv      *httptest.Server
	libStore *library.Store
	dir      string
}

func newPurgeE2EEnv(t *testing.T) *purgeE2EEnv {
	t.Helper()
	sqlDB := dbtest.New(t)
	secretStore, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("building secret store: %v", err)
	}
	libStore := library.New(sqlDB)

	srv := httptest.NewServer(NewMux(testHTTPClient(), connections.New(sqlDB, secretStore), nil,
		proposals.New(sqlDB), testProber(t), testPHasher(t), testVideoHasher(t), settings.New(sqlDB),
		nil, libStore, nil, nil, nil, nil, testFeedHealth(), nil,
		nil, nil, nil, nil, nil, nil, nil, pruning.New(sqlDB)))
	t.Cleanup(srv.Close)

	return &purgeE2EEnv{srv: srv, libStore: libStore, dir: t.TempDir()}
}

// TestPurgeWorkflow_TagsRuleThenScanThenApply_EndToEnd exercises the full
// Purge loop for Movies: create a TAGS-ONLY pruning rule through the API, Scan
// evaluates it against libStore's own tagged items (no Radarr involved
// anymore), and Apply deletes exactly the one approved proposal — hitting
// SAK's real HTTP handlers, a real migrated Postgres database, and a real
// on-disk file.
//
// Claude 2026-08-11: re-targeted from the retired per-mode tag allowlist
// (POST/GET/DELETE /api/modes/{mode}/purge/allowlist) onto /api/pruning-rules.
// Reason: the allowlist mechanism is gone; tags are now a fourth AND'd
// condition on a pruning rule. Only the "configure matching" head of this test
// changed — the Scan -> proposal-appears -> Apply -> file-deleted -> re-list
// tail is preserved verbatim, because it is the repo's ONLY HTTP-level
// end-to-end coverage of that sequence for Purge.
// Review if: Purge's propose phase ever gains a second matching mechanism.
func TestPurgeWorkflow_TagsRuleThenScanThenApply_EndToEnd(t *testing.T) {
	env := newPurgeE2EEnv(t)
	ctx := context.Background()

	vanillaPath := env.dir + "/vanilla.mkv"
	flaggedPath := env.dir + "/flagged.mkv"
	if err := os.WriteFile(vanillaPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(flaggedPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vanilla, err := env.libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "Vanilla Movie", FilePath: vanillaPath, RootFolderPath: env.dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := env.libStore.AddTag(ctx, vanilla.ID, "family-friendly"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	flagged, err := env.libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 2, Title: "Flagged Movie", FilePath: flaggedPath, RootFolderPath: env.dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := env.libStore.AddTag(ctx, flagged.ID, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Configure matching through the API, not directly on the store: a
	// tags-only rule, which is exactly what migration 0008 converts a legacy
	// allowlist into.
	ruleBody, _ := json.Marshal(apidto.PruningRuleUpsertRequest{
		Name: "Flagged tags", Mode: string(mode.Movies), Tags: []string{"BDSM"}, Enabled: true,
	})
	createResp, err := http.Post(env.srv.URL+"/api/pruning-rules", "application/json", bytes.NewReader(ruleBody))
	if err != nil {
		t.Fatalf("create rule failed: %v", err)
	}
	if createResp.StatusCode != http.StatusCreated && createResp.StatusCode != http.StatusOK {
		createResp.Body.Close()
		t.Fatalf("expected 200/201 creating a tags-only rule, got %d", createResp.StatusCode)
	}
	var created apidto.PruningRule
	json.NewDecoder(createResp.Body).Decode(&created)
	createResp.Body.Close()
	if created.ID == 0 || len(created.Tags) != 1 || created.Tags[0] != "BDSM" {
		t.Fatalf("expected the created rule to carry its tag, got %+v", created)
	}

	var listedRules []apidto.PruningRule
	getJSON(t, env.srv.URL+"/api/pruning-rules", &listedRules)
	if len(listedRules) != 1 || listedRules[0].ID != created.ID {
		t.Fatalf("expected the rules list to reflect the created rule, got %+v", listedRules)
	}

	scanResp, err := http.Post(env.srv.URL+"/api/modes/movies/purge/scan", "application/json", nil)
	if err != nil {
		t.Fatalf("scan POST failed: %v", err)
	}
	defer scanResp.Body.Close()
	if scanResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from scan, got %d", scanResp.StatusCode)
	}
	var scanned []proposals.Proposal
	json.NewDecoder(scanResp.Body).Decode(&scanned)
	if len(scanned) != 1 || scanned[0].Title != "Flagged Movie" || scanned[0].TrackedID != int(flagged.ID) {
		t.Fatalf("unexpected scan result: %+v", scanned)
	}
	if want := "Matched rule 'Flagged tags': tags: BDSM"; scanned[0].Reason != want {
		t.Errorf("Reason = %q, want %q", scanned[0].Reason, want)
	}

	listResp, err := http.Get(env.srv.URL + "/api/modes/movies/purge/proposals")
	if err != nil {
		t.Fatalf("list proposals failed: %v", err)
	}
	defer listResp.Body.Close()
	var listedPage struct {
		Items []proposals.Proposal `json:"items"`
	}
	json.NewDecoder(listResp.Body).Decode(&listedPage)
	listed := listedPage.Items
	if len(listed) != 1 || listed[0].ID != scanned[0].ID {
		t.Fatalf("expected the purge queue to reflect what scan staged, got %+v", listed)
	}

	applyResp, err := http.Post(
		env.srv.URL+"/api/proposals/"+strconv.FormatInt(scanned[0].ID, 10)+"/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("apply POST failed: %v", err)
	}
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from apply, got %d", applyResp.StatusCode)
	}
	var applied proposals.Proposal
	json.NewDecoder(applyResp.Body).Decode(&applied)
	if applied.Status != proposals.Applied {
		t.Fatalf("expected the proposal to come back Applied, got %+v", applied)
	}
	if _, err := os.Stat(flaggedPath); !os.IsNotExist(err) {
		t.Errorf("expected the flagged movie's file to be deleted, stat returned: %v", err)
	}
	if _, err := os.Stat(vanillaPath); err != nil {
		t.Errorf("expected the vanilla movie's file to survive untouched, got: %v", err)
	}
	if _, err := env.libStore.Get(ctx, flagged.ID); err != library.ErrNotFound {
		t.Errorf("expected the flagged movie's library record to be deleted, got err=%v", err)
	}

	// Retire the matching rule again via the API — the direct counterpart of
	// the allowlist removal this test used to end on.
	delReq, _ := http.NewRequest(http.MethodDelete,
		env.srv.URL+"/api/pruning-rules/"+strconv.FormatInt(created.ID, 10), nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete rule failed: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 deleting a rule, got %d", delResp.StatusCode)
	}
	var after []apidto.PruningRule
	getJSON(t, env.srv.URL+"/api/pruning-rules", &after)
	if len(after) != 0 {
		t.Fatalf("expected no rules after deletion, got %v", after)
	}
}

// TestPurgeScanHandler_NoConnectionNeeded proves no mode needs any *arr
// connection for Purge anymore — Movies/Series since their eliminations, and
// Adult since Stage 4's Whisparr elimination (its Scan is now the pure
// library-backed purge.ScanLibraryAdult, returning an empty queue for an
// empty library rather than 400-ing on a missing Whisparr).
func TestPurgeScanHandler_NoConnectionNeeded(t *testing.T) {
	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil))
	defer srv.Close()

	for _, m := range []string{"movies", "series", "adult"} {
		resp, err := http.Post(srv.URL+"/api/modes/"+m+"/purge/scan", "application/json", nil)
		if err != nil {
			t.Fatalf("POST failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for %s with no *arr connection at all, got %d", m, resp.StatusCode)
		}
	}
}
