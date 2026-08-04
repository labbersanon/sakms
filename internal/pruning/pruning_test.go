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
