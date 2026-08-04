package api

// Claude 2026-08-03: new file — HTTP coverage for the pruning-rules CRUD,
// preview, and the purge-scan end-to-end path (B5, plan §9.6).
// Reason: driven through the REAL mux (template:
// scanschedule_settings_test.go) rather than by calling handlers directly, so
// the route patterns registered in handler.go are exercised too — a handler
// that works but was never registered is the failure mode a direct call hides.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/allowlist"
	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/pruning"
	"github.com/labbersanon/sakms/internal/settings"
)

// pruningEnv is a mux wired with only the stores the pruning routes and the
// purge-scan path touch, all against ONE database — testStores(t) predates
// *pruning.Store and does not hand back the *sql.DB needed to build one
// alongside its library store.
type pruningEnv struct {
	srv          *httptest.Server
	libStore     *library.Store
	pruningStore *pruning.Store
	propStore    *proposals.Store
	sqlDB        *sql.DB
}

func newPruningEnv(t *testing.T) *pruningEnv {
	t.Helper()
	sqlDB := dbtest.New(t)

	libStore := library.New(sqlDB)
	pruningStore := pruning.New(sqlDB)
	propStore := proposals.New(sqlDB)

	srv := httptest.NewServer(NewMux(testHTTPClient(), nil, nil, propStore, allowlist.New(sqlDB),
		testProber(t), testPHasher(t), testVideoHasher(t), settings.New(sqlDB), nil, libStore,
		nil, nil, nil, nil, testFeedHealth(), nil, nil, nil, nil, nil, nil, nil, nil, pruningStore))
	t.Cleanup(srv.Close)

	return &pruningEnv{srv: srv, libStore: libStore, pruningStore: pruningStore, propStore: propStore, sqlDB: sqlDB}
}

func (e *pruningEnv) do(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshalling body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, reader)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func validUpsert() apidto.PruningRuleUpsertRequest {
	return apidto.PruningRuleUpsertRequest{
		Name: "Old low-quality movies", Mode: string(mode.Movies),
		AgeDays: 365, SizeBytes: 1 << 30, QualityTierFloor: "low", Enabled: true,
	}
}

func TestPruningRules_CRUD_RoundTrip(t *testing.T) {
	env := newPruningEnv(t)

	status, body := env.do(t, http.MethodGet, "/api/pruning-rules", nil)
	if status != http.StatusOK {
		t.Fatalf("GET on a blank install = %d (%s), want 200", status, body)
	}
	var empty []apidto.PruningRule
	if err := json.Unmarshal(body, &empty); err != nil {
		t.Fatalf("decoding empty list: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no rules on a blank install, got %+v", empty)
	}
	// [] rather than null — the frontend iterates the response directly.
	if string(bytes.TrimSpace(body)) != "[]" {
		t.Errorf("blank list serialized as %q, want []", body)
	}

	status, body = env.do(t, http.MethodPost, "/api/pruning-rules", validUpsert())
	if status != http.StatusOK {
		t.Fatalf("POST = %d (%s), want 200", status, body)
	}
	var created apidto.PruningRule
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decoding created rule: %v", err)
	}
	if created.ID == 0 || created.CreatedAt == "" {
		t.Fatalf("created rule is missing its assigned id/timestamps: %+v", created)
	}
	if created.AgeDays != 365 || created.SizeBytes != 1<<30 || created.QualityTierFloor != "low" || !created.Enabled {
		t.Errorf("created rule did not round-trip its conditions: %+v", created)
	}

	status, body = env.do(t, http.MethodGet, "/api/pruning-rules", nil)
	if status != http.StatusOK {
		t.Fatalf("GET after create = %d (%s), want 200", status, body)
	}
	var listed []apidto.PruningRule
	json.Unmarshal(body, &listed)
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("expected exactly the created rule in the list, got %+v", listed)
	}

	path := "/api/pruning-rules/" + strconv.FormatInt(created.ID, 10)
	edited := validUpsert()
	edited.Name = "Renamed"
	edited.SizeBytes = 0 // clearing a condition is expressible via its sentinel
	edited.Enabled = false
	status, body = env.do(t, http.MethodPut, path, edited)
	if status != http.StatusOK {
		t.Fatalf("PUT = %d (%s), want 200", status, body)
	}
	var updated apidto.PruningRule
	json.Unmarshal(body, &updated)
	if updated.Name != "Renamed" || updated.SizeBytes != 0 || updated.Enabled {
		t.Errorf("PUT did not overwrite every editable field: %+v", updated)
	}

	status, body = env.do(t, http.MethodDelete, path, nil)
	if status != http.StatusNoContent {
		t.Fatalf("DELETE = %d (%s), want 204", status, body)
	}
	status, body = env.do(t, http.MethodGet, "/api/pruning-rules", nil)
	json.Unmarshal(body, &listed)
	if len(listed) != 0 {
		t.Fatalf("expected an empty list after delete, got %+v", listed)
	}
}

// AC1's server half: a rule with all three conditions at their unset sentinel
// is refused, on both create and update.
func TestPruningRules_RejectsRuleWithNoConditions_400(t *testing.T) {
	env := newPruningEnv(t)

	bare := apidto.PruningRuleUpsertRequest{Name: "Everything", Mode: string(mode.Movies), Enabled: true}
	if status, body := env.do(t, http.MethodPost, "/api/pruning-rules", bare); status != http.StatusBadRequest {
		t.Fatalf("POST with no conditions = %d (%s), want 400", status, body)
	}

	_, body := env.do(t, http.MethodPost, "/api/pruning-rules", validUpsert())
	var created apidto.PruningRule
	json.Unmarshal(body, &created)
	path := "/api/pruning-rules/" + strconv.FormatInt(created.ID, 10)
	if status, out := env.do(t, http.MethodPut, path, bare); status != http.StatusBadRequest {
		t.Fatalf("PUT clearing every condition = %d (%s), want 400", status, out)
	}
}

func TestPruningRules_RejectsInvalidMode_400(t *testing.T) {
	env := newPruningEnv(t)
	for _, badMode := range []string{"", "all", "tv"} {
		req := validUpsert()
		req.Mode = badMode
		if status, body := env.do(t, http.MethodPost, "/api/pruning-rules", req); status != http.StatusBadRequest {
			t.Errorf("POST with mode %q = %d (%s), want 400", badMode, status, body)
		}
	}
}

func TestPruningRules_RejectsInvalidTierFloorAndNegativeThresholds_400(t *testing.T) {
	env := newPruningEnv(t)

	badTier := validUpsert()
	badTier.QualityTierFloor = "unknown" // the backfill sentinel is NOT a floor
	if status, body := env.do(t, http.MethodPost, "/api/pruning-rules", badTier); status != http.StatusBadRequest {
		t.Errorf("POST with a %q tier floor = %d (%s), want 400", badTier.QualityTierFloor, status, body)
	}

	negative := validUpsert()
	negative.AgeDays = -1
	if status, body := env.do(t, http.MethodPost, "/api/pruning-rules", negative); status != http.StatusBadRequest {
		t.Errorf("POST with a negative ageDays = %d (%s), want 400", status, body)
	}
}

func TestPruningRules_DeleteNotFound_404(t *testing.T) {
	env := newPruningEnv(t)
	if status, body := env.do(t, http.MethodDelete, "/api/pruning-rules/424242", nil); status != http.StatusNotFound {
		t.Fatalf("DELETE of an unknown id = %d (%s), want 404", status, body)
	}
	if status, body := env.do(t, http.MethodPut, "/api/pruning-rules/424242", validUpsert()); status != http.StatusNotFound {
		t.Fatalf("PUT of an unknown id = %d (%s), want 404", status, body)
	}
}

// --- §13.1 preview -------------------------------------------------------

// backdateRow sets a library row's created_at so age conditions have something
// real to compare against.
func backdateRow(t *testing.T, sqlDB *sql.DB, table string, id int64, days int) {
	t.Helper()
	createdAt := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	if _, err := sqlDB.Exec("UPDATE "+table+" SET created_at = ? WHERE id = ?", createdAt, id); err != nil {
		t.Fatalf("backdating %s %d: %v", table, id, err)
	}
}

// The preview counts what the draft WOULD match and stages nothing — it is a
// soft warning, so the count is the whole contract and the Purge queue must
// still be empty afterwards.
func TestPruningRules_Preview_CountsMatchesWithoutStaging(t *testing.T) {
	env := newPruningEnv(t)
	ctx := context.Background()

	for i, ageDays := range []int{500, 400, 10} {
		item, err := env.libStore.Upsert(ctx, library.Item{
			Mode: mode.Movies, TMDBID: i + 1, Title: "Movie " + strconv.Itoa(i),
			RootFolderPath: "/media/Movies",
		})
		if err != nil {
			t.Fatalf("seeding item %d: %v", i, err)
		}
		backdateRow(t, env.sqlDB, "library_items", item.ID, ageDays)
	}

	draft := apidto.PruningRuleUpsertRequest{
		Name: "Stale", Mode: string(mode.Movies), AgeDays: 365, Enabled: true,
	}
	status, body := env.do(t, http.MethodPost, "/api/pruning-rules/preview", draft)
	if status != http.StatusOK {
		t.Fatalf("preview = %d (%s), want 200", status, body)
	}
	var got apidto.PruningRulePreviewResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decoding preview: %v", err)
	}
	if got.MatchCount != 2 {
		t.Errorf("matchCount = %d, want 2 (the two items older than 365 days)", got.MatchCount)
	}

	// Nothing staged, and no rule persisted: preview never writes.
	queued, err := env.propStore.List(ctx, mode.Movies, proposals.Purge)
	if err != nil {
		t.Fatalf("listing the purge queue: %v", err)
	}
	if len(queued) != 0 {
		t.Errorf("preview staged %d proposals — it must only count", len(queued))
	}
	stored, err := env.pruningStore.List(ctx)
	if err != nil {
		t.Fatalf("listing rules: %v", err)
	}
	if len(stored) != 0 {
		t.Errorf("preview persisted %d rules — it previews DRAFTS", len(stored))
	}
}

// A high count is reported, never refused — §13.1's "save is always allowed"
// has its server half here: the preview returns 200 with the number and the
// create that follows still succeeds.
func TestPruningRules_Preview_LargeCountStillAllowsSave(t *testing.T) {
	env := newPruningEnv(t)
	ctx := context.Background()

	const seeded = 30
	for i := 0; i < seeded; i++ {
		item, err := env.libStore.Upsert(ctx, library.Item{
			Mode: mode.Movies, TMDBID: i + 1, Title: "Movie " + strconv.Itoa(i), RootFolderPath: "/media/Movies",
		})
		if err != nil {
			t.Fatalf("seeding item %d: %v", i, err)
		}
		backdateRow(t, env.sqlDB, "library_items", item.ID, 500)
	}

	draft := apidto.PruningRuleUpsertRequest{Name: "Stale", Mode: string(mode.Movies), AgeDays: 365, Enabled: true}
	status, body := env.do(t, http.MethodPost, "/api/pruning-rules/preview", draft)
	if status != http.StatusOK {
		t.Fatalf("preview = %d (%s), want 200", status, body)
	}
	var got apidto.PruningRulePreviewResponse
	json.Unmarshal(body, &got)
	if got.MatchCount != seeded {
		t.Fatalf("matchCount = %d, want %d", got.MatchCount, seeded)
	}

	if status, out := env.do(t, http.MethodPost, "/api/pruning-rules", draft); status != http.StatusOK {
		t.Fatalf("saving a rule matching %d items = %d (%s) — the preview is a SOFT warning and must not block", got.MatchCount, status, out)
	}
}

// A draft with no name still previews (a name has no bearing on what matches),
// but a structurally invalid one is refused with the same 400 a save gets.
func TestPruningRules_Preview_AllowsBlankNameRejectsNoConditions(t *testing.T) {
	env := newPruningEnv(t)

	unnamed := apidto.PruningRuleUpsertRequest{Mode: string(mode.Movies), AgeDays: 365}
	if status, body := env.do(t, http.MethodPost, "/api/pruning-rules/preview", unnamed); status != http.StatusOK {
		t.Errorf("preview of an unnamed draft = %d (%s), want 200", status, body)
	}

	bare := apidto.PruningRuleUpsertRequest{Name: "Everything", Mode: string(mode.Movies)}
	if status, body := env.do(t, http.MethodPost, "/api/pruning-rules/preview", bare); status != http.StatusBadRequest {
		t.Errorf("preview of a no-condition draft = %d (%s), want 400", status, body)
	}
}

// --- end to end through purgeScanHandler ---------------------------------

// TestPurgeScan_WithEnabledRule_StagesProposal is the whole feature in one
// request: a saved enabled rule, a manual Purge Scan, and the matching item
// lands in the queue as a Pending proposal with the rule's Reason.
func TestPurgeScan_WithEnabledRule_StagesProposal(t *testing.T) {
	env := newPruningEnv(t)
	ctx := context.Background()

	old, err := env.libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "Ancient", RootFolderPath: "/media/Movies"})
	if err != nil {
		t.Fatalf("seeding item: %v", err)
	}
	backdateRow(t, env.sqlDB, "library_items", old.ID, 500)
	fresh, err := env.libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 2, Title: "Fresh", RootFolderPath: "/media/Movies"})
	if err != nil {
		t.Fatalf("seeding item: %v", err)
	}
	backdateRow(t, env.sqlDB, "library_items", fresh.ID, 1)

	if status, body := env.do(t, http.MethodPost, "/api/pruning-rules", apidto.PruningRuleUpsertRequest{
		Name: "Stale", Mode: string(mode.Movies), AgeDays: 365, Enabled: true,
	}); status != http.StatusOK {
		t.Fatalf("creating the rule = %d (%s), want 200", status, body)
	}

	status, body := env.do(t, http.MethodPost, "/api/modes/movies/purge/scan", nil)
	if status != http.StatusOK {
		t.Fatalf("purge scan = %d (%s), want 200", status, body)
	}
	var staged []proposals.Proposal
	if err := json.Unmarshal(body, &staged); err != nil {
		t.Fatalf("decoding the scan response: %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("expected exactly the stale item staged, got %d: %+v", len(staged), staged)
	}
	if staged[0].TrackedID != int(old.ID) || staged[0].Status != proposals.Pending {
		t.Errorf("unexpected staged proposal: %+v", staged[0])
	}
	if staged[0].Reason != "Matched rule 'Stale': 500 days old" {
		t.Errorf("Reason = %q, want the rule fragment naming the actual age", staged[0].Reason)
	}
}

// A DISABLED rule is never evaluated — the scan behaves as if it did not exist.
func TestPurgeScan_DisabledRule_StagesNothing(t *testing.T) {
	env := newPruningEnv(t)
	ctx := context.Background()

	item, err := env.libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "Ancient", RootFolderPath: "/media/Movies"})
	if err != nil {
		t.Fatalf("seeding item: %v", err)
	}
	backdateRow(t, env.sqlDB, "library_items", item.ID, 500)

	if status, body := env.do(t, http.MethodPost, "/api/pruning-rules", apidto.PruningRuleUpsertRequest{
		Name: "Stale", Mode: string(mode.Movies), AgeDays: 365, Enabled: false,
	}); status != http.StatusOK {
		t.Fatalf("creating the rule = %d (%s), want 200", status, body)
	}

	_, body := env.do(t, http.MethodPost, "/api/modes/movies/purge/scan", nil)
	var staged []proposals.Proposal
	json.Unmarshal(body, &staged)
	if len(staged) != 0 {
		t.Fatalf("a disabled rule staged %d proposals: %+v", len(staged), staged)
	}
}

// A rule scoped to another mode is never evaluated for this one — §1.4's
// one-mode-per-rule scoping, asserted at the HTTP boundary.
func TestPurgeScan_RuleForAnotherMode_StagesNothing(t *testing.T) {
	env := newPruningEnv(t)
	ctx := context.Background()

	item, err := env.libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "Ancient", RootFolderPath: "/media/Movies"})
	if err != nil {
		t.Fatalf("seeding item: %v", err)
	}
	backdateRow(t, env.sqlDB, "library_items", item.ID, 500)

	if status, body := env.do(t, http.MethodPost, "/api/pruning-rules", apidto.PruningRuleUpsertRequest{
		Name: "Stale series", Mode: string(mode.Series), AgeDays: 365, Enabled: true,
	}); status != http.StatusOK {
		t.Fatalf("creating the rule = %d (%s), want 200", status, body)
	}

	_, body := env.do(t, http.MethodPost, "/api/modes/movies/purge/scan", nil)
	var staged []proposals.Proposal
	json.Unmarshal(body, &staged)
	if len(staged) != 0 {
		t.Fatalf("a series-scoped rule staged %d movies proposals: %+v", len(staged), staged)
	}
}
