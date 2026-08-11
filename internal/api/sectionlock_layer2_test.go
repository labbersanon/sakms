package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
	"github.com/labbersanon/sakms/internal/auth"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/sectionlock"
)

// sectionlock_layer2_test.go — W1-c: SL-17, the row/body-addressed Adult
// cases Layer 1 structurally CANNOT see.
//
// Both routes exercised here classify as something OTHER than adult-content:
// POST /api/autograb-batch is {discover}, POST /api/proposals/{id}/apply is
// {organize}. With only adult-content locked, Layer 1 lets both through by
// design — the Adult scope lives in a request-body item's mode and in a
// proposal ROW's own p.Mode, neither of which appears in a URL. So whatever
// denies them is Layer 2 inside mode.Build, which is exactly the claim.
//
// Every case is a PAIR: the same request with a correct X-Section-Pin must
// stop being denied FOR THE LOCK. That is what separates "the lock denied
// it" from "this request was going to fail anyway."

// layer2Fixture wraps the REAL NewMux in auth.Middleware with the section
// gate installed, the way cmd/sakms/main.go wraps it. A test that called the
// handlers directly would carry no decision in the request context at all,
// and Layer 2 treats an absent decision as ALLOW — so it would pass against
// a Build that had never been touched.
type layer2Fixture struct {
	srv       *httptest.Server
	propStore *proposals.Store
	lockStore *sectionlock.Store
	prowlarr  *atomic.Int64
}

func newLayer2Fixture(t *testing.T) *layer2Fixture {
	t.Helper()
	ctx := context.Background()

	var prowlarrHits atomic.Int64
	prowlarrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		prowlarrHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(prowlarrSrv.Close)

	connStore, propStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, rssFeedsStore := testStores(t)
	if err := connStore.Upsert(ctx, "prowlarr", prowlarrSrv.URL, "key"); err != nil {
		t.Fatalf("seeding prowlarr: %v", err)
	}

	authStore, secretStore, _ := testAuthStoreWithDB(t)
	if err := authStore.SetAuthMode(ctx, auth.ModePassword); err != nil {
		t.Fatalf("SetAuthMode: %v", err)
	}
	authStore.UseEnvAPIKey(sectionLockTestAPIKey)

	lockStore := sectionlock.NewStore(settingsStore)
	if err := lockStore.SetPin(ctx, sectionLockTestPin); err != nil {
		t.Fatalf("SetPin: %v", err)
	}
	if err := lockStore.SetSections(ctx, []string{sectionlock.SectionAdultContent}); err != nil {
		t.Fatalf("SetSections: %v", err)
	}

	apiMux := NewMux(testHTTPClient(), connStore, nil, propStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, rowStore, releaseStore, testFeedHealth(), rssFeedsStore, nil, nil, nil, nil, nil, nil, nil, nil)
	gated := auth.Middleware(secretStore, authStore, apiMux, auth.WithSectionGate(sectionlock.NewGate(lockStore, secretStore)))

	srv := httptest.NewServer(gated)
	t.Cleanup(srv.Close)

	return &layer2Fixture{srv: srv, propStore: propStore, lockStore: lockStore, prowlarr: &prowlarrHits}
}

func (f *layer2Fixture) post(t *testing.T, path string, body any, decorate ...func(*http.Request)) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshalling body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, d := range decorate {
		d(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// --- SL-17a: POST /api/autograb-batch with an Adult item ----------------

// The batch handler builds one session per distinct item mode and turns a
// Build failure into that item's own error (skip-and-continue is the batch
// contract — a locked Adult item must not abort a Mainstream sibling). So
// "denied" here means: the Adult item is refused, NOTHING was dispatched,
// and no indexer query ever fired.
func TestSectionLockLayer2_AutoGrabBatchAdultItemDenied(t *testing.T) {
	f := newLayer2Fixture(t)

	body := apidto.AutoGrabBatchRequest{Items: []apidto.AutoGrabBatchItem{
		{Mode: "adult", Request: apidto.AutoGrabRequest{Title: "Some Scene"}},
	}}

	resp := f.post(t, "/api/autograb-batch", body, withKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/autograb-batch = %d, want 200 — Layer 1 classifies this route as {discover}, which is NOT locked here", resp.StatusCode)
	}

	var out apidto.AutoGrabBatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding batch response: %v", err)
	}
	if len(out.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out.Results))
	}
	got := out.Results[0]
	if got.Grabbed {
		t.Fatal("the Adult item was GRABBED while adult-content is locked")
	}
	if !strings.Contains(got.Error, sectionlock.ErrSectionLocked.Error()) {
		t.Fatalf("expected the Adult item to be refused by the section lock, got error %q", got.Error)
	}
	if n := f.prowlarr.Load(); n != 0 {
		t.Errorf("the denial must happen before any indexer traffic; Prowlarr saw %d request(s)", n)
	}
}

// The pair. With the PIN header the same item stops being lock-denied — it
// fails later, on its own merits (no TMDB id, nothing to search), which is
// what proves the first half's error came from the lock and not from the
// request being malformed to begin with.
func TestSectionLockLayer2_AutoGrabBatchAdultItemAllowedWithThePin(t *testing.T) {
	f := newLayer2Fixture(t)

	body := apidto.AutoGrabBatchRequest{Items: []apidto.AutoGrabBatchItem{
		{Mode: "adult", Request: apidto.AutoGrabRequest{Title: "Some Scene"}},
	}}

	resp := f.post(t, "/api/autograb-batch", body, withKey, withPinHeader(sectionLockTestPin))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/autograb-batch with the PIN = %d, want 200", resp.StatusCode)
	}

	var out apidto.AutoGrabBatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding batch response: %v", err)
	}
	if len(out.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out.Results))
	}
	if err := out.Results[0].Error; strings.Contains(err, sectionlock.ErrSectionLocked.Error()) {
		t.Fatalf("a correct PIN must clear the section lock, still got %q", err)
	}
}

// A mixed batch is where the batch contract and the lock meet: skip-and-
// continue means one item's refusal never blocks the rest, so a locked
// adult-content must refuse the Adult item and leave the Movies sibling
// alone. Locking Adult must not become a de-facto "no bulk grabs at all".
func TestSectionLockLayer2_MixedBatchRefusesOnlyTheAdultItem(t *testing.T) {
	f := newLayer2Fixture(t)

	body := apidto.AutoGrabBatchRequest{Items: []apidto.AutoGrabBatchItem{
		{Mode: "movies", Request: apidto.AutoGrabRequest{Title: "Some Movie", TMDBID: 42}},
		{Mode: "adult", Request: apidto.AutoGrabRequest{Title: "Some Scene"}},
	}}

	resp := f.post(t, "/api/autograb-batch", body, withKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/autograb-batch = %d, want 200", resp.StatusCode)
	}
	var out apidto.AutoGrabBatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding batch response: %v", err)
	}
	if len(out.Results) != 2 {
		t.Fatalf("expected 2 results (skip-and-continue, never an abort), got %d", len(out.Results))
	}
	if err := out.Results[0].Error; strings.Contains(err, sectionlock.ErrSectionLocked.Error()) {
		t.Errorf("the Movies item was refused by a locked adult-content: %q", err)
	}
	if err := out.Results[1].Error; !strings.Contains(err, sectionlock.ErrSectionLocked.Error()) {
		t.Errorf("expected the Adult item to be refused by the section lock, got %q", err)
	}
}

// --- SL-17b: POST /api/proposals/{id}/apply on an Adult proposal --------

// A single-item handler, so the denial IS the response: 403 with the same
// section_locked body Layer 1 writes. The frontend keys its PIN overlay on
// that code, so a generic 400 here would render as an unexplained failure.
func TestSectionLockLayer2_ApplyAdultProposalDenied(t *testing.T) {
	f := newLayer2Fixture(t)
	p := f.insertAdultProposal(t)

	resp := f.post(t, "/api/proposals/"+strconv.FormatInt(p.ID, 10)+"/apply", map[string]any{}, withKey)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("applying an Adult proposal while adult-content is locked = %d, want 403", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding rejection body: %v", err)
	}
	if body["code"] != "section_locked" {
		t.Fatalf("rejection body = %+v, want code section_locked so the frontend raises the PIN overlay", body)
	}
	if body["section"] != sectionlock.SectionAdultContent {
		t.Errorf("rejection body named section %q, want %q", body["section"], sectionlock.SectionAdultContent)
	}
}

// The pair: with the PIN the request stops being lock-denied and proceeds
// into the real Apply path (where it fails on its own, since the proposal's
// paths do not exist on disk).
func TestSectionLockLayer2_ApplyAdultProposalAllowedWithThePin(t *testing.T) {
	f := newLayer2Fixture(t)
	p := f.insertAdultProposal(t)

	resp := f.post(t, "/api/proposals/"+strconv.FormatInt(p.ID, 10)+"/apply", map[string]any{},
		withKey, withPinHeader(sectionLockTestPin))
	if resp.StatusCode == http.StatusForbidden {
		t.Fatal("a correct PIN must clear the section lock, still got 403")
	}
}

// A Mainstream proposal is untouched by a locked adult-content — AC6's
// "without locking Mainstream", asserted on Layer 2 rather than assumed.
func TestSectionLockLayer2_ApplyMoviesProposalUnaffected(t *testing.T) {
	f := newLayer2Fixture(t)
	saved, err := f.propStore.ReplacePending(context.Background(), mode.Movies, proposals.Rename, []proposals.Proposal{{
		Status:     proposals.Pending,
		SourceName: "Some Movie",
		SourcePath: "/movies/Some Movie",
	}})
	if err != nil {
		t.Fatalf("inserting the Movies proposal: %v", err)
	}

	resp := f.post(t, "/api/proposals/"+strconv.FormatInt(saved[0].ID, 10)+"/apply", map[string]any{}, withKey)
	if resp.StatusCode == http.StatusForbidden {
		t.Fatal("a locked adult-content must not deny a Movies proposal")
	}
}

func (f *layer2Fixture) insertAdultProposal(t *testing.T) proposals.Proposal {
	t.Helper()
	saved, err := f.propStore.ReplacePending(context.Background(), mode.Adult, proposals.Rename, []proposals.Proposal{{
		Status:     proposals.Pending,
		SourceName: "Some Scene",
		SourcePath: "/adult/Some Scene",
	}})
	if err != nil {
		t.Fatalf("inserting the Adult proposal: %v", err)
	}
	return saved[0]
}
