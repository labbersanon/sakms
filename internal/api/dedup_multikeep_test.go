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

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
)

// TestApplyBatch_MultiKeepDedup_ThreadsAdditionalKeepIndices is AC13, the
// critical bulk-path regression guard: a batched multi-keep Dedup group must
// leave its additional keepers on disk. This exercises the exact spot an earlier
// draft of the plan missed — applyBatchHandler reconstructing an
// applyProposalRequest from the batch item. If AdditionalKeepIndices is dropped
// in that reconstruction, the file the operator checked as "keep" gets silently
// deleted here. Goes through the real apply-batch HTTP handler, not
// dedup.ApplyLibrary directly, precisely because the reconstruction (not the
// delete loop) is the code under test.
func TestApplyBatch_MultiKeepDedup_ThreadsAdditionalKeepIndices(t *testing.T) {
	dir := t.TempDir()
	winnerPath := writeTestVideoFile(t, dir, "winner.mkv", 10)
	keeperPath := writeTestVideoFile(t, dir, "additional-keeper.mkv", 10)
	loserPath := writeTestVideoFile(t, dir, "loser.mkv", 10)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	// Three untracked orphans of the same TMDB id staged as one Dedup group.
	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Dedup, []proposals.Proposal{
		{Status: proposals.Pending, Title: "X", TMDBID: 7, RootFolderPath: dir, Candidates: []proposals.Candidate{
			{Label: "winner", Path: winnerPath, Winner: true},
			{Label: "keeper", Path: keeperPath},
			{Label: "loser", Path: loserPath},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	keep := 0
	body, _ := json.Marshal(applyBatchRequest{Items: []applyBatchItem{
		{ID: saved[0].ID, KeepIndex: &keep, AdditionalKeepIndices: []int{1}},
	}})
	out := postApplyBatch(t, srv, body)

	if len(out.Results) != 1 || !out.Results[0].OK {
		t.Fatalf("expected the batched multi-keep apply to succeed, got %+v", out.Results)
	}

	// The regression assertion: the additional keeper the operator checked must
	// still be on disk after the BULK apply. If the batch handler dropped
	// AdditionalKeepIndices, this file would have been deleted.
	if _, err := os.Stat(keeperPath); err != nil {
		t.Errorf("expected the additional keeper to survive the bulk apply, got %v", err)
	}
	if _, err := os.Stat(winnerPath); err != nil {
		t.Errorf("expected the primary winner to survive, got %v", err)
	}
	if _, err := os.Stat(loserPath); !os.IsNotExist(err) {
		t.Errorf("expected the unchecked loser to be deleted, got %v", err)
	}

	// Only the primary is tracked — no row for the additional keeper.
	items, err := libStore.List(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].FilePath != winnerPath {
		t.Errorf("expected exactly the primary winner tracked, got %+v", items)
	}
}

// TestApplyProposal_ValidationRejections is the structural-validation gate
// (Stage 1 step 5a): each illegal keep-set combination must be rejected with a
// 400 before any file is touched. One sub-case per rejected combination. The
// proposal is a 3-candidate Dedup group (indices 0..2 valid, 5 out of range),
// reused across sub-cases since a rejected apply mutates nothing.
func TestApplyProposal_ValidationRejections(t *testing.T) {
	dir := t.TempDir()
	c0 := writeTestVideoFile(t, dir, "c0.mkv", 10)
	c1 := writeTestVideoFile(t, dir, "c1.mkv", 10)
	c2 := writeTestVideoFile(t, dir, "c2.mkv", 10)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore := testStores(t)
	ctx := context.Background()

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Dedup, []proposals.Proposal{
		{Status: proposals.Pending, Title: "X", TMDBID: 7, RootFolderPath: dir, Candidates: []proposals.Candidate{
			{Label: "c0", Path: c0, Winner: true},
			{Label: "c1", Path: c1},
			{Label: "c2", Path: c2},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore, slidersStore, traktStore, adultNewestRowStore, adultNewestReleaseStore, rssFeedsStore, nil, nil, nil, nil, nil))
	defer srv.Close()

	applyURL := srv.URL + "/api/proposals/" + strconv.FormatInt(saved[0].ID, 10) + "/apply"
	post := func(t *testing.T, body string) (int, string) {
		t.Helper()
		resp, err := http.Post(applyURL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("apply POST failed: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	cases := []struct {
		name string
		body string
	}{
		{"keepAll combined with additionalKeepIndices", `{"keepAll":true,"additionalKeepIndices":[1]}`},
		{"additionalKeepIndices without a keepIndex", `{"additionalKeepIndices":[1]}`},
		{"keepIndex out of range", `{"keepIndex":5}`},
		{"additionalKeepIndices entry out of range", `{"keepIndex":0,"additionalKeepIndices":[5]}`},
		{"additionalKeepIndices entry equals keepIndex", `{"keepIndex":0,"additionalKeepIndices":[0]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, resp := post(t, tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %q, got %d: %s", tc.body, code, resp)
			}
		})
	}

	// The proposal must remain Pending (no illegal request ever mutated it) and
	// every candidate file must survive untouched.
	still, err := propStore.Get(ctx, saved[0].ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if still.Status != proposals.Pending {
		t.Errorf("expected the proposal to stay Pending after rejected applies, got %q", still.Status)
	}
	for _, p := range []string{c0, c1, c2} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected candidate %q untouched after rejected applies, got %v", filepath.Base(p), err)
		}
	}
}
