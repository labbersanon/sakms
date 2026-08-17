package purge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/library"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/proposals"
	"github.com/labbersanon/sakms/internal/pruning"
)

func TestScanLibraryAdult_ProposesOnlyScenesMatchingATagsRule(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()

	vanilla, err := libStore.UpsertScene(ctx, library.Scene{Box: "stashdb", SceneID: "s-1", Title: "Vanilla Scene", RootFolderPath: "/media/Adult"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSceneTag(ctx, vanilla.ID, "romance"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	flagged, err := libStore.UpsertScene(ctx, library.Scene{Box: "stashdb", SceneID: "s-2", Title: "Flagged Scene", RootFolderPath: "/media/Adult"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSceneTag(ctx, flagged.ID, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSceneTag(ctx, flagged.ID, "unrelated"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibraryAdult(ctx, libStore, []pruning.Rule{
		{Name: "Flagged", Mode: string(mode.Adult), Tags: []string{"BDSM"}, Enabled: true},
	}, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 matched proposal, got %d: %+v", len(got), got)
	}
	p := got[0]
	if p.TrackedID != int(flagged.ID) || p.Title != "Flagged Scene" || p.Mode != mode.Adult || p.Status != proposals.Pending {
		t.Errorf("unexpected proposal: %+v", p)
	}
	if want := "Matched rule 'Flagged': tags: BDSM"; p.Reason != want {
		t.Errorf("Reason = %q, want %q", p.Reason, want)
	}
}

func TestScanLibraryAdult_NoRulesMatchNothing(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	scene, err := libStore.UpsertScene(ctx, library.Scene{Box: "stashdb", SceneID: "s-1", Title: "X", RootFolderPath: "/media/Adult"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSceneTag(ctx, scene.ID, "BDSM"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibraryAdult(ctx, libStore, nil, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no proposals with no rules configured, got %+v", got)
	}
}

func TestScanLibraryAdult_AspectFilterSkipsOtherClass(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	vert, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "movie-1", Title: "Movie", RootFolderPath: "/media/Adult",
		PosterAspectClass: library.PosterAspectVertical,
	})
	if err != nil {
		t.Fatal(err)
	}
	horiz, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "scene-1", Title: "Scene", RootFolderPath: "/media/Adult",
		PosterAspectClass: library.PosterAspectHorizontal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := libStore.AddSceneTag(ctx, vert.ID, "gone"); err != nil {
		t.Fatal(err)
	}
	if err := libStore.AddSceneTag(ctx, horiz.ID, "gone"); err != nil {
		t.Fatal(err)
	}
	rules := []pruning.Rule{
		{Name: "Drop", Mode: string(mode.Adult), Tags: []string{"gone"}, Enabled: true},
	}
	all, err := ScanLibraryAdult(ctx, libStore, rules, "", nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %d err=%v, want 2", len(all), err)
	}
	movies, err := ScanLibraryAdult(ctx, libStore, rules, library.PosterAspectVertical, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 1 || movies[0].TrackedID != int(vert.ID) {
		t.Fatalf("vertical = %+v", movies)
	}
	scenes, err := ScanLibraryAdult(ctx, libStore, rules, library.PosterAspectHorizontal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenes) != 1 || scenes[0].TrackedID != int(horiz.ID) {
		t.Fatalf("horizontal = %+v", scenes)
	}
}

func TestApplyLibraryAdult_DeletesFileAndScene(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "scene.mkv")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	scene, err := libStore.UpsertScene(ctx, library.Scene{Box: "stashdb", SceneID: "s-1", Title: "Flagged Scene", FilePath: filePath, RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// SourcePath is load-bearing here: ApplyLibraryAdult removes the file it
	// names (there's no GetScene-by-id to re-derive the path), so the
	// hand-built proposal must carry it exactly as ScanLibraryAdult would.
	changes, err := ApplyLibraryAdult(ctx, libStore, proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Flagged Scene",
		SourcePath: filePath, TrackedID: int(scene.ID),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("expected the file to be deleted, stat returned: %v", err)
	}
	if _, err := libStore.GetScene(ctx, "stashdb", "s-1"); err != library.ErrNotFound {
		t.Errorf("expected the scene row to be deleted, got err=%v", err)
	}
	if len(changes) != 1 || changes[0].Path != filePath || changes[0].Kind != mode.Deleted {
		t.Errorf("expected exactly one Deleted PathChange for %q, got %+v", filePath, changes)
	}
}

func TestApplyLibraryAdult_DeletesSceneAlternateFiles(t *testing.T) {
	root := t.TempDir()
	sceneDir := filepath.Join(root, "Flagged Scene")
	if err := os.MkdirAll(sceneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(sceneDir, "scene.mkv")
	alt := filepath.Join(sceneDir, "scene - 1080p.mp4")
	for _, p := range []string{primary, alt} {
		if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	scene, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s-alts", Title: "Flagged Scene",
		FilePath: primary, RootFolderPath: root,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertSceneFile(ctx, library.SceneFile{
		SceneID: scene.ID, FilePath: alt, IsPrimary: false,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	changes, err := ApplyLibraryAdult(ctx, libStore, proposals.Proposal{
		ID: 1, Status: proposals.Pending, Title: "Flagged Scene",
		SourcePath: primary, RootFolderPath: root, TrackedID: int(scene.ID),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected PathChanges for primary+alternate, got %+v", changes)
	}
	if _, err := os.Stat(sceneDir); !os.IsNotExist(err) {
		t.Errorf("expected scene folder gone, stat: %v", err)
	}
	if _, err := libStore.GetScene(ctx, "stashdb", "s-alts"); err != library.ErrNotFound {
		t.Errorf("expected the scene row to be deleted, got err=%v", err)
	}
}

func TestApplyLibraryAdult_RejectsNonPendingProposal(t *testing.T) {
	libStore := newTestLibraryStore(t)
	for _, status := range []proposals.Status{proposals.Applied, proposals.Dismissed, proposals.Unmatched} {
		_, err := ApplyLibraryAdult(context.Background(), libStore, proposals.Proposal{Status: status, TrackedID: 5})
		if err == nil {
			t.Errorf("expected ApplyLibraryAdult to refuse a %q proposal", status)
		}
	}
}

func TestApplyLibraryAdult_RejectsMissingTrackedID(t *testing.T) {
	libStore := newTestLibraryStore(t)
	_, err := ApplyLibraryAdult(context.Background(), libStore, proposals.Proposal{Status: proposals.Pending, TrackedID: 0})
	if err == nil {
		t.Fatal("expected ApplyLibraryAdult to refuse a proposal with no scene id")
	}
}

type fakeCatalogTags struct {
	byPHash map[string]CatalogTagHit
	byID    map[string][]string
}

func (f fakeCatalogTags) TagsByPHash(_ context.Context, phashes []string) (map[string]CatalogTagHit, error) {
	out := map[string]CatalogTagHit{}
	for _, ph := range phashes {
		if hit, ok := f.byPHash[ph]; ok {
			out[ph] = hit
		}
	}
	return out, nil
}

func (f fakeCatalogTags) TagsByID(_ context.Context, box, sceneID string) ([]string, error) {
	return f.byID[box+"/"+sceneID], nil
}

func TestScanLibraryAdult_BackfillsCatalogTagsAndMatchesRule(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()

	flagged, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s-bdsm", Title: "Flagged Scene", RootFolderPath: "/media/Adult",
		PHash: "phash-bdsm",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	vanilla, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s-vanilla", Title: "Vanilla Scene", RootFolderPath: "/media/Adult",
		PHash: "phash-vanilla",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	local, err := libStore.UpsertScene(ctx, library.Scene{
		Box: library.LocalSceneBox, SceneID: library.LocalSceneID("phash-local"), Title: "Local Scene",
		RootFolderPath: "/media/Adult", PHash: "phash-local",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	src := fakeCatalogTags{
		byPHash: map[string]CatalogTagHit{
			"phash-bdsm":    {Box: "stashdb", SceneID: "s-bdsm", Tags: []string{"Bondage", "Dungeon"}},
			"phash-vanilla": {Box: "stashdb", SceneID: "s-vanilla", Tags: []string{"Romance"}},
			"phash-local":   {Box: "stashdb", SceneID: "should-not-apply", Tags: []string{"Bondage"}},
		},
	}

	got, err := ScanLibraryAdult(ctx, libStore, []pruning.Rule{
		{Name: "BDSM", Mode: string(mode.Adult), Tags: []string{"Bondage", "Dungeon"}, Enabled: true},
	}, "", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 matched proposal, got %d: %+v", len(got), got)
	}
	if got[0].TrackedID != int(flagged.ID) {
		t.Errorf("matched %d (%s), want flagged scene %d", got[0].TrackedID, got[0].Title, flagged.ID)
	}

	tags, err := libStore.SceneTags(ctx, flagged.ID)
	if err != nil {
		t.Fatalf("SceneTags flagged: %v", err)
	}
	if len(tags) != 2 || tags[0] != "Bondage" || tags[1] != "Dungeon" {
		t.Errorf("flagged tags = %v, want [Bondage Dungeon]", tags)
	}
	vanillaTags, err := libStore.SceneTags(ctx, vanilla.ID)
	if err != nil {
		t.Fatalf("SceneTags vanilla: %v", err)
	}
	if len(vanillaTags) != 1 || vanillaTags[0] != "Romance" {
		t.Errorf("vanilla tags = %v, want [Romance]", vanillaTags)
	}
	localTags, err := libStore.SceneTags(ctx, local.ID)
	if err != nil {
		t.Fatalf("SceneTags local: %v", err)
	}
	if len(localTags) != 0 {
		t.Errorf("local scene must not receive catalog tags, got %v", localTags)
	}
}

func TestScanLibraryAdult_BackfillFallsBackToSceneByID(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	scene, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "tpdb", SceneID: "77", Title: "TPDB Scene", RootFolderPath: "/media/Adult",
		PHash: "phash-other-box",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	src := fakeCatalogTags{
		byPHash: map[string]CatalogTagHit{
			"phash-other-box": {Box: "stashdb", SceneID: "different", Tags: []string{"Wrong"}},
		},
		byID: map[string][]string{"tpdb/77": {"Bondage"}},
	}

	got, err := ScanLibraryAdult(ctx, libStore, []pruning.Rule{
		{Name: "BDSM", Mode: string(mode.Adult), Tags: []string{"Bondage"}, Enabled: true},
	}, "", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].TrackedID != int(scene.ID) {
		t.Fatalf("got %+v, want scene %d", got, scene.ID)
	}
}

func TestScanLibraryAdult_DoesNotLookupWhenNoTagRules(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	if _, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s-1", Title: "X", RootFolderPath: "/media/Adult", PHash: "h1",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	src := fakeCatalogTags{
		byPHash: map[string]CatalogTagHit{"h1": {Box: "stashdb", SceneID: "s-1", Tags: []string{"Bondage"}}},
	}
	got, err := ScanLibraryAdult(ctx, libStore, []pruning.Rule{
		{Name: "Big", Mode: string(mode.Adult), SizeBytes: 1, Enabled: true},
	}, "", src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("size rule with Size=0-captured scene should not match, got %+v", got)
	}
	scenes, err := libStore.ListScenes(ctx)
	if err != nil {
		t.Fatalf("ListScenes: %v", err)
	}
	tags, err := libStore.SceneTags(ctx, scenes[0].ID)
	if err != nil {
		t.Fatalf("SceneTags: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("size-only rule must not backfill catalog tags, got %v", tags)
	}
}

type blockingCatalogTags struct{}

func (blockingCatalogTags) TagsByPHash(ctx context.Context, _ []string) (map[string]CatalogTagHit, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingCatalogTags) TagsByID(ctx context.Context, _, _ string) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestScanLibraryAdult_BackfillDeadlineStillMatchesTaggedScenes(t *testing.T) {
	prev := catalogTagBackfillBudget
	catalogTagBackfillBudget = 20 * time.Millisecond
	t.Cleanup(func() { catalogTagBackfillBudget = prev })

	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	tagged, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s-tagged", Title: "Already Tagged", RootFolderPath: "/media/Adult",
		PHash: "phash-tagged",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := libStore.AddSceneTag(ctx, tagged.ID, "Bondage"); err != nil {
		t.Fatalf("AddSceneTag: %v", err)
	}
	if _, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s-slow", Title: "Needs Lookup", RootFolderPath: "/media/Adult",
		PHash: "phash-slow",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := ScanLibraryAdult(ctx, libStore, []pruning.Rule{
		{Name: "BDSM", Mode: string(mode.Adult), Tags: []string{"Bondage"}, Enabled: true},
	}, "", blockingCatalogTags{})
	if err != nil {
		t.Fatalf("scan must not fail when backfill hits its budget, got %v", err)
	}
	if len(got) != 1 || got[0].TrackedID != int(tagged.ID) {
		t.Fatalf("got %+v, want the already-tagged scene %d", got, tagged.ID)
	}
}

func TestScanLibraryAdult_CriteriaTagRuleBackfillsCatalogTags(t *testing.T) {
	libStore := newTestLibraryStore(t)
	ctx := context.Background()
	scene, err := libStore.UpsertScene(ctx, library.Scene{
		Box: "stashdb", SceneID: "s-1", Title: "X", RootFolderPath: "/media/Adult", PHash: "h1",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	src := fakeCatalogTags{
		byPHash: map[string]CatalogTagHit{"h1": {Box: "stashdb", SceneID: "s-1", Tags: []string{"Bondage"}}},
	}
	got, err := ScanLibraryAdult(ctx, libStore, []pruning.Rule{{
		Name: "BDSM", Mode: string(mode.Adult), Enabled: true,
		Criteria: []pruning.Criterion{{
			Field: pruning.FieldTag, Op: pruning.OpContains, MatchMode: pruning.MatchModeAny,
			Values: []string{"Bondage", "Bound"},
		}},
	}}, "", src)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 1 || got[0].TrackedID != int(scene.ID) {
		t.Fatalf("got %+v, want scene %d (criteria tag rules must still backfill)", got, scene.ID)
	}
}
