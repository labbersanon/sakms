package proposals

import (
	"context"
	"errors"
	"testing"

	"github.com/labbersanon/sakms/internal/mode"
)

// TestRepickAdultScene_FlipsTogglesStatusAndSetsIdentity verifies the core
// behaviour: an Adult Unmatched proposal is promoted to Pending with the
// supplied catalog identity and the reason is cleared.
func TestRepickAdultScene_FlipsTogglesStatusAndSetsIdentity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.ReplacePending(ctx, mode.Adult, Rename, []Proposal{
		{
			Status: Unmatched, Mode: mode.Adult, Workflow: Rename,
			SourceName: "scene.mp4", SourcePath: "/adult/scene.mp4",
			RootFolderPath: "/adult",
			Title:          "Web ID Title", Studio: "Studio A", Date: "2024",
			Reason: "web-identified only — no catalog scene id yet; use Review to name and track it",
		},
	})
	if err != nil || len(saved) != 1 {
		t.Fatalf("seeding: %v / %d", err, len(saved))
	}
	p := saved[0]

	if err := s.RepickAdultScene(ctx, p.ID, "Catalog Title", "Studio B", "2023-05-01", "stashdb", "scene-uuid"); err != nil {
		t.Fatalf("RepickAdultScene: %v", err)
	}

	got, err := s.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get after repick: %v", err)
	}
	if got.Status != Pending {
		t.Errorf("expected status Pending, got %q", got.Status)
	}
	if got.Title != "Catalog Title" {
		t.Errorf("expected title %q, got %q", "Catalog Title", got.Title)
	}
	if got.Studio != "Studio B" {
		t.Errorf("expected studio %q, got %q", "Studio B", got.Studio)
	}
	if got.Date != "2023-05-01" {
		t.Errorf("expected date %q, got %q", "2023-05-01", got.Date)
	}
	if got.GiveBackBox != "stashdb" {
		t.Errorf("expected give_back_box=stashdb, got %q", got.GiveBackBox)
	}
	if got.GiveBackSceneID != "scene-uuid" {
		t.Errorf("expected give_back_scene_id=scene-uuid, got %q", got.GiveBackSceneID)
	}
	if got.Reason != "" {
		t.Errorf("expected reason to be cleared, got %q", got.Reason)
	}
}

// TestRepickAdultScene_ReturnsErrNotFoundForUnknownID verifies that repicking
// a non-existent proposal id returns ErrNotFound.
func TestRepickAdultScene_ReturnsErrNotFoundForUnknownID(t *testing.T) {
	s := newTestStore(t)
	err := s.RepickAdultScene(context.Background(), 9999, "T", "S", "D", "stashdb", "u1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
