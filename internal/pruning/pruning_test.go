package pruning

import (
	"context"
	"errors"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/mode"
)

// newTestStore builds a Store against a real, freshly migrated SQLite file —
// exercising the actual schema/migration, not mocks, the same way every
// other store in this repo is tested (see discoversliders_test.go).
func newTestStore(t *testing.T) *Store {
	t.Helper()
	sqlDB := dbtest.New(t)

	return New(sqlDB)
}

func TestCreate_ThenList_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Rule{Name: "Stale movies", Mode: string(mode.Movies), AgeDays: 412, Enabled: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("expected a non-zero id")
	}
	if created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Errorf("expected timestamps to be populated, got %+v", created)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].Name != "Stale movies" || list[0].AgeDays != 412 {
		t.Errorf("unexpected list: %+v", list)
	}
}

func TestCreate_RejectsBlankName(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), Rule{Name: "", Mode: string(mode.Movies), AgeDays: 30})
	if !errors.Is(err, ErrNameRequired) {
		t.Fatalf("expected ErrNameRequired, got %v", err)
	}
}

func TestCreate_RejectsInvalidMode(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), Rule{Name: "Bogus", Mode: "bogus", AgeDays: 30})
	if !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("expected ErrInvalidMode, got %v", err)
	}
}

func TestCreate_RejectsInvalidTierFloor(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), Rule{Name: "Bogus", Mode: string(mode.Movies), QualityTierFloor: "bogus"})
	if !errors.Is(err, ErrInvalidTierFloor) {
		t.Fatalf("expected ErrInvalidTierFloor, got %v", err)
	}
}

func TestCreate_RejectsNegativeThresholds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, Rule{Name: "Negative age", Mode: string(mode.Movies), AgeDays: -1}); !errors.Is(err, ErrNegativeThreshold) {
		t.Errorf("negative AgeDays: expected ErrNegativeThreshold, got %v", err)
	}
	if _, err := s.Create(ctx, Rule{Name: "Negative size", Mode: string(mode.Movies), SizeBytes: -1}); !errors.Is(err, ErrNegativeThreshold) {
		t.Errorf("negative SizeBytes: expected ErrNegativeThreshold, got %v", err)
	}
}

func TestCreate_RejectsRuleWithNoConditions(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), Rule{Name: "No conditions", Mode: string(mode.Movies), Enabled: true})
	if !errors.Is(err, ErrNoConditions) {
		t.Fatalf("expected ErrNoConditions, got %v", err)
	}
}

func TestUpdate_NotFound_ReturnsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Update(context.Background(), Rule{ID: 999, Name: "X", Mode: string(mode.Movies), AgeDays: 30})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdate_OverwritesFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Rule{Name: "First", Mode: string(mode.Movies), AgeDays: 30, Enabled: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := s.Update(ctx, Rule{ID: created.ID, Name: "Renamed", Mode: string(mode.Series), SizeBytes: 1024, Enabled: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Name != "Renamed" || updated.Mode != string(mode.Series) || updated.SizeBytes != 1024 || updated.AgeDays != 0 || updated.Enabled {
		t.Errorf("unexpected updated rule: %+v", updated)
	}
}

func TestDelete_NotFound_ReturnsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.Delete(context.Background(), 999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete_RemovesRule(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Rule{Name: "First", Mode: string(mode.Movies), AgeDays: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.Delete(ctx, created.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected no rules after delete, got %+v", list)
	}
}

func TestListEnabledForMode_ExcludesDisabledRules(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, Rule{Name: "Enabled", Mode: string(mode.Movies), AgeDays: 30, Enabled: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Create(ctx, Rule{Name: "Disabled", Mode: string(mode.Movies), AgeDays: 30, Enabled: false}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rules, err := s.ListEnabledForMode(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 || rules[0].Name != "Enabled" {
		t.Errorf("expected only the enabled rule, got %+v", rules)
	}
}

func TestListEnabledForMode_ExcludesOtherModes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, Rule{Name: "Movies rule", Mode: string(mode.Movies), AgeDays: 30, Enabled: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Create(ctx, Rule{Name: "Series rule", Mode: string(mode.Series), AgeDays: 30, Enabled: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.Create(ctx, Rule{Name: "Adult rule", Mode: string(mode.Adult), AgeDays: 30, Enabled: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rules, err := s.ListEnabledForMode(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 || rules[0].Name != "Movies rule" {
		t.Errorf("expected only the movies rule, got %+v", rules)
	}
}

func TestList_EmptyReturnsEmptySliceNotNil(t *testing.T) {
	s := newTestStore(t)
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if list == nil {
		t.Fatal("expected an empty slice, got nil")
	}
	if len(list) != 0 {
		t.Errorf("expected no rules, got %+v", list)
	}
}

// --- Tags, the 4th condition (Claude 2026-08-11) ---------------------------

// TestTags_RoundTripThroughCreateListUpdate is the column-position guard:
// tags sits between quality_tier_floor and enabled in all four statements, and
// a SELECT-list / rows.Scan mismatch there is a RUNTIME error, not a compile
// error. Nothing but a test that actually reads a row back catches it.
func TestTags_RoundTripThroughCreateListUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Rule{
		Name: "Tagged", Mode: string(mode.Movies), Tags: []string{"BDSM", "Rope"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(created.Tags) != 2 || created.Tags[0] != "BDSM" || created.Tags[1] != "Rope" {
		t.Fatalf("Create returned tags %v, want [BDSM Rope]", created.Tags)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || len(list[0].Tags) != 2 || list[0].Tags[0] != "BDSM" {
		t.Fatalf("List returned %+v, want one rule tagged [BDSM Rope]", list)
	}
	// Every other field must still line up — the whole point of the guard.
	if list[0].Name != "Tagged" || list[0].Mode != string(mode.Movies) || !list[0].Enabled {
		t.Errorf("column positions drifted: %+v", list[0])
	}

	enabled, err := s.ListEnabledForMode(ctx, mode.Movies)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(enabled) != 1 || len(enabled[0].Tags) != 2 {
		t.Fatalf("ListEnabledForMode returned %+v, want the tagged rule", enabled)
	}

	updated, err := s.Update(ctx, Rule{
		ID: created.ID, Name: "Tagged", Mode: string(mode.Movies), Tags: []string{"Trailer"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(updated.Tags) != 1 || updated.Tags[0] != "Trailer" {
		t.Fatalf("Update returned tags %v, want [Trailer]", updated.Tags)
	}
	after, _ := s.List(ctx)
	if len(after) != 1 || len(after[0].Tags) != 1 || after[0].Tags[0] != "Trailer" {
		t.Fatalf("after Update, List returned %+v, want tags [Trailer]", after)
	}
}

// A tags-only rule satisfies the four-way ErrNoConditions check — this is what
// migration 0008's "Legacy allowlist" rules are, so if this regressed every
// migrated rule would fail on the operator's next edit.
func TestCreate_TagsOnlyRuleIsValid(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(context.Background(), Rule{
		Name: "Legacy allowlist", Mode: string(mode.Movies), Tags: []string{"Trailer"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("a tags-only rule must be valid, got %v", err)
	}
	if created.AgeDays != 0 || created.SizeBytes != 0 || created.QualityTierFloor != "" {
		t.Errorf("expected the other conditions unset, got %+v", created)
	}
}

func TestCreate_MinRatingOnlyRuleIsValid(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(context.Background(), Rule{
		Name: "Low stars", Mode: string(mode.Movies), MinRating: 3, Enabled: true,
	})
	if err != nil {
		t.Fatalf("a min-rating-only rule must be valid, got %v", err)
	}
	if created.MinRating != 3 {
		t.Errorf("MinRating = %d, want 3", created.MinRating)
	}
}

func TestCreate_RejectsInvalidMinRating(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), Rule{Name: "Bogus", Mode: string(mode.Movies), MinRating: 6})
	if !errors.Is(err, ErrInvalidMinRating) {
		t.Fatalf("expected ErrInvalidMinRating, got %v", err)
	}
}

func TestCreate_RejectsBlankTag(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for name, tags := range map[string][]string{
		"empty string":    {""},
		"whitespace only": {"   "},
		"blank alongside": {"BDSM", "\t"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.Create(ctx, Rule{Name: "Bad", Mode: string(mode.Movies), Tags: tags, Enabled: true})
			if !errors.Is(err, ErrBlankTag) {
				t.Fatalf("expected ErrBlankTag, got %v", err)
			}
		})
	}
}

// An empty Tags on Update CLEARS stored tags — the upsert body is deliberately
// whole-rule, so an empty list must be expressible as "remove the condition",
// exactly like qualityTierFloor: "".
func TestUpdate_EmptyTagsClearsStoredTags(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, Rule{
		Name: "Tagged", Mode: string(mode.Movies), AgeDays: 30, Tags: []string{"BDSM"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := s.Update(ctx, Rule{
		ID: created.ID, Name: "Tagged", Mode: string(mode.Movies), AgeDays: 30, Tags: nil, Enabled: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	after, _ := s.List(ctx)
	if len(after) != 1 || len(after[0].Tags) != 0 {
		t.Fatalf("expected tags cleared, got %+v", after)
	}
	if after[0].AgeDays != 30 {
		t.Errorf("clearing tags must not disturb the other conditions, got %+v", after[0])
	}
}

func TestCreate_CriteriaOnlyRuleIsValid(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(context.Background(), Rule{
		Name: "Window", Mode: string(mode.Movies), Enabled: true,
		Criteria: []Criterion{
			{Field: FieldAge, Op: OpGT, Value: "30", Unit: UnitDays},
			{Field: FieldAge, Op: OpLT, Value: "365", Unit: UnitDays},
		},
	})
	if err != nil {
		t.Fatalf("a criteria-only rule must be valid, got %v", err)
	}
	if created.AgeDays != 0 || created.MinRating != 0 {
		t.Errorf("expected scalars unset, got %+v", created)
	}
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || len(list[0].Criteria) != 2 {
		t.Fatalf("List criteria = %+v, want 2 rows", list)
	}
	want0 := Criterion{Field: FieldAge, Op: OpGT, Value: "30", Unit: UnitDays}
	want1 := Criterion{Field: FieldAge, Op: OpLT, Value: "365", Unit: UnitDays}
	if list[0].Criteria[0].Field != want0.Field || list[0].Criteria[0].Op != want0.Op || list[0].Criteria[0].Value != want0.Value || list[0].Criteria[0].Unit != want0.Unit {
		t.Errorf("criteria[0] = %+v", list[0].Criteria[0])
	}
	if list[0].Criteria[1].Field != want1.Field || list[0].Criteria[1].Op != want1.Op || list[0].Criteria[1].Value != want1.Value || list[0].Criteria[1].Unit != want1.Unit {
		t.Errorf("criteria[1] = %+v", list[0].Criteria[1])
	}
}

func TestCreate_RejectsInvalidCriterion(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), Rule{
		Name: "Bogus", Mode: string(mode.Movies), Enabled: true,
		Criteria: []Criterion{{Field: "nope", Op: OpGT, Value: "1", Unit: UnitDays}},
	})
	if !errors.Is(err, ErrInvalidCriterion) {
		t.Fatalf("expected ErrInvalidCriterion, got %v", err)
	}
}

func TestCreate_RejectsTagCompareOp(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), Rule{
		Name: "Bogus", Mode: string(mode.Movies), Enabled: true,
		Criteria: []Criterion{{Field: FieldTag, Op: OpGT, Value: "BDSM"}},
	})
	if !errors.Is(err, ErrInvalidCriterion) {
		t.Fatalf("expected ErrInvalidCriterion, got %v", err)
	}
}

func TestCreate_RejectsTagCriterionWithZeroChips(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), Rule{
		Name: "Empty tags", Mode: string(mode.Adult), Enabled: true,
		Criteria: []Criterion{{Field: FieldTag, Op: OpContains, MatchMode: MatchModeAny}},
	})
	if !errors.Is(err, ErrBlankTag) {
		t.Fatalf("expected ErrBlankTag, got %v", err)
	}
}

func TestCreate_RejectsInvalidTagMatchMode(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(context.Background(), Rule{
		Name: "Bogus mode", Mode: string(mode.Adult), Enabled: true,
		Criteria: []Criterion{{Field: FieldTag, Op: OpContains, Values: []string{"Bondage"}, MatchMode: "xor"}},
	})
	if !errors.Is(err, ErrInvalidCriterion) {
		t.Fatalf("expected ErrInvalidCriterion, got %v", err)
	}
}

func TestCreate_TagValuesAndMatchModeRoundTrip(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(context.Background(), Rule{
		Name: "BDSM any", Mode: string(mode.Adult), Enabled: true,
		Criteria: []Criterion{{
			Field: FieldTag, Op: OpContains, MatchMode: MatchModeAny,
			Values: []string{"Bondage", "Bound", "Dungeon", "Pee", "Peeing"},
		}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || created.ID != list[0].ID {
		t.Fatalf("list = %+v", list)
	}
	got := list[0].Criteria
	if len(got) != 1 || got[0].MatchMode != MatchModeAny || len(got[0].Values) != 5 || got[0].Values[0] != "Bondage" {
		t.Fatalf("criteria = %+v, want contains+any with five chips", got)
	}
}
