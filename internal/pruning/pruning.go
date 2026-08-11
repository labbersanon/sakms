// Package pruning persists operator-authored propose-only pruning rules —
// a rule is a NAMED set of AND'd conditions (age/size/quality-tier-floor/tags)
// scoped to one mode; an item matching ANY enabled rule for its mode gets
// staged as a Purge proposal. Nothing in this package ever deletes: rule
// evaluation runs only in Purge's existing propose phase, and every match
// still requires a human Apply. This package is persistence + validation
// only (this file) plus the match/reason engine (evaluate.go) — no HTTP
// handlers and no internal/apidto types, the same boundary
// internal/discoversliders' package doc states explicitly.
package pruning

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/quality"
)

// ErrNotFound is returned by Update/Delete when id has no stored rule.
var ErrNotFound = errors.New("pruning: no rule with that id")

// ErrNameRequired is returned when Name is blank.
var ErrNameRequired = errors.New("pruning: name is required")

// ErrInvalidMode is returned when Mode isn't one of movies/series/adult.
var ErrInvalidMode = errors.New("pruning: invalid mode")

// ErrNoConditions is returned when all four condition fields are at their
// unset sentinel (age_days == 0, size_bytes == 0, quality_tier_floor == "",
// len(tags) == 0) — AC1's "at least one condition required".
//
// The string enumerates all four deliberately: it is mirrored client-side by
// PurgeRulesCard.tsx's hasCondition(), so a future fifth condition that
// updates only one side is caught by TestCreate_RejectsRuleWithNoConditions
// and the card's own validation test together.
var ErrNoConditions = errors.New("pruning: at least one condition (age, size, quality tier floor, or tags) is required")

// ErrBlankTag is returned when Tags contains an empty or whitespace-only
// entry. This is the guard the retired addAllowlistTagHandler used to apply at
// the HTTP layer; it moves here with the mechanism.
var ErrBlankTag = errors.New("pruning: a tag must not be blank")

// ErrInvalidTierFloor is returned when QualityTierFloor is non-empty but
// isn't one of the four real quality tiers.
var ErrInvalidTierFloor = errors.New("pruning: invalid quality tier floor")

// ErrNegativeThreshold is returned when AgeDays or SizeBytes is negative.
var ErrNegativeThreshold = errors.New("pruning: threshold must not be negative")

// Rule is one operator-authored pruning rule. A zero value in a condition
// field means "this condition is not configured" — 0 for the two numeric
// thresholds, "" for the tier floor, and an empty slice for Tags (see
// internal/db/migrations/0008_pruning_rule_tags.sql, and 0001_baseline.sql
// for the other three).
type Rule struct {
	ID               int64    `json:"id"`
	Name             string   `json:"name"`
	Mode             string   `json:"mode"`
	AgeDays          int      `json:"ageDays,omitempty"`
	SizeBytes        int64    `json:"sizeBytes,omitempty"`
	QualityTierFloor string   `json:"qualityTierFloor,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Enabled          bool     `json:"enabled"`
	CreatedAt        string   `json:"createdAt"`
	UpdatedAt        string   `json:"updatedAt"`
}

// validModes is the fixed set of mode.Mode values a rule may scope to.
// There is deliberately no "all modes" value — see plan §1.4.
var validModes = map[string]bool{
	string(mode.Movies): true,
	string(mode.Series): true,
	string(mode.Adult):  true,
}

// validTierFloors is the fixed set of real quality tiers a rule's floor may
// reference. quality.Tier defines no ordering of its own — evaluate.go's
// tierRank owns the ladder used at match time; this set exists only to
// reject garbage input at write time.
var validTierFloors = map[string]bool{
	string(quality.Low):      true,
	string(quality.Medium):   true,
	string(quality.High):     true,
	string(quality.Lossless): true,
}

// Validate checks r against the fixed mode/tier enums, rejects negative
// thresholds and blank tags, and requires at least one of the four conditions
// to be configured (AC1). Called by both Create and Update.
func Validate(r Rule) error {
	if r.Name == "" {
		return ErrNameRequired
	}
	if !validModes[r.Mode] {
		return fmt.Errorf("%w: %q", ErrInvalidMode, r.Mode)
	}
	if r.AgeDays < 0 || r.SizeBytes < 0 {
		return ErrNegativeThreshold
	}
	if r.QualityTierFloor != "" && !validTierFloors[r.QualityTierFloor] {
		return fmt.Errorf("%w: %q", ErrInvalidTierFloor, r.QualityTierFloor)
	}
	for _, tag := range r.Tags {
		if strings.TrimSpace(tag) == "" {
			return ErrBlankTag
		}
	}
	if r.AgeDays == 0 && r.SizeBytes == 0 && r.QualityTierFloor == "" && len(r.Tags) == 0 {
		return ErrNoConditions
	}
	return nil
}

// Store persists pruning rules against a database.
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Create validates and inserts a new rule, returning the stored row with
// its assigned id and timestamps.
func (s *Store) Create(ctx context.Context, r Rule) (Rule, error) {
	if err := Validate(r); err != nil {
		return Rule{}, err
	}
	tagsJSON, err := marshalStringSlice(r.Tags)
	if err != nil {
		return Rule{}, fmt.Errorf("encoding tags for pruning rule %q: %w", r.Name, err)
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO pruning_rules (name, mode, age_days, size_bytes, quality_tier_floor, tags, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at
	`, r.Name, r.Mode, r.AgeDays, r.SizeBytes, r.QualityTierFloor, tagsJSON, r.Enabled)

	if err := row.Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return Rule{}, fmt.Errorf("creating pruning rule %q: %w", r.Name, err)
	}
	return r, nil
}

// Update validates and overwrites every editable field of the rule with
// r.ID, setting updated_at. Returns ErrNotFound if no rule has that id.
func (s *Store) Update(ctx context.Context, r Rule) (Rule, error) {
	if err := Validate(r); err != nil {
		return Rule{}, err
	}
	tagsJSON, err := marshalStringSlice(r.Tags)
	if err != nil {
		return Rule{}, fmt.Errorf("encoding tags for pruning rule %d: %w", r.ID, err)
	}
	row := s.db.QueryRowContext(ctx, `
		UPDATE pruning_rules SET
			name = ?, mode = ?, age_days = ?, size_bytes = ?, quality_tier_floor = ?, tags = ?, enabled = ?,
			updated_at = sakms_now()
		WHERE id = ?
		RETURNING id, created_at, updated_at
	`, r.Name, r.Mode, r.AgeDays, r.SizeBytes, r.QualityTierFloor, tagsJSON, r.Enabled, r.ID)

	if err := row.Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Rule{}, ErrNotFound
		}
		return Rule{}, fmt.Errorf("updating pruning rule %d: %w", r.ID, err)
	}
	return r, nil
}

// Delete removes the rule with the given id. Unlike
// internal/discoversliders' Delete (idempotent, no error), a missing id
// here IS an error — an operator deleting a rule from the list they're
// looking at expects a stale/already-gone row to be surfaced, not silently
// swallowed.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM pruning_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting pruning rule %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("deleting pruning rule %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns every rule ordered by id, ascending.
func (s *Store) List(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, mode, age_days, size_bytes, quality_tier_floor, COALESCE(tags, '[]'), enabled, created_at, updated_at
		FROM pruning_rules
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing pruning rules: %w", err)
	}
	defer rows.Close()
	return scanRules(rows)
}

// ListEnabledForMode returns every enabled rule scoped to m, ordered by id
// ascending — the hot path purge.ScanLibrary* calls once per scan.
func (s *Store) ListEnabledForMode(ctx context.Context, m mode.Mode) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, mode, age_days, size_bytes, quality_tier_floor, COALESCE(tags, '[]'), enabled, created_at, updated_at
		FROM pruning_rules
		WHERE mode = ? AND enabled = true
		ORDER BY id ASC
	`, string(m))
	if err != nil {
		return nil, fmt.Errorf("listing enabled pruning rules for mode %q: %w", m, err)
	}
	defer rows.Close()
	return scanRules(rows)
}

// scanRules drains rows into a []Rule. []Rule{}, not var out []Rule — a
// blank/no-match result should serialize as [] over the API, not null (same
// convention as discoversliders.Store.List).
func scanRules(rows *sql.Rows) ([]Rule, error) {
	out := []Rule{}
	for rows.Next() {
		var r Rule
		var tagsJSON string
		// Argument order MUST track the SELECT lists in List/ListEnabledForMode
		// exactly — tags sits immediately after quality_tier_floor, before
		// enabled. A mismatch here is a runtime scan error, not a compile
		// error, so nothing catches it until a test runs.
		if err := rows.Scan(&r.ID, &r.Name, &r.Mode, &r.AgeDays, &r.SizeBytes, &r.QualityTierFloor, &tagsJSON, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning pruning rule: %w", err)
		}
		// Deliberately the OPPOSITE call from out's []Rule{} above: an empty
		// tag list decodes to nil, not []string{}, because Tags carries
		// omitempty on the DTO (tags?: string[] client-side) — an absent key is
		// the correct wire shape for "condition not configured". The []T{} rule
		// above is about the LIST endpoint's own top-level JSON, not a nested
		// field. Do not "align" them.
		if err := unmarshalStringSlice(tagsJSON, &r.Tags); err != nil {
			return nil, fmt.Errorf("decoding tags for pruning rule %d: %w", r.ID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// marshalStringSlice encodes ss as a JSON array — always "[]" for the
// nil/empty case, never "null", matching pruning_rules.tags' DEFAULT '[]'.
//
// A local copy rather than a shared helper: internal/library and
// internal/proposals each already carry their own, per this repo's
// no-premature-abstraction convention.
func marshalStringSlice(ss []string) (string, error) {
	if len(ss) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(ss)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// unmarshalStringSlice decodes a JSON array column into out, leaving it nil
// for an empty array so the omitempty on Rule.Tags fires.
func unmarshalStringSlice(s string, out *[]string) error {
	if s == "" || s == "[]" {
		*out = nil
		return nil
	}
	var decoded []string
	if err := json.Unmarshal([]byte(s), &decoded); err != nil {
		return err
	}
	if len(decoded) == 0 {
		*out = nil
		return nil
	}
	*out = decoded
	return nil
}
