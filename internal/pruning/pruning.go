// Package pruning persists operator-authored propose-only pruning rules —
// a rule is a NAMED ordered list of AND'd criteria (age/size/quality/tag/rating
// with an operator and value) scoped to one mode; an item matching ANY enabled
// rule for its mode gets staged as a Purge proposal. Nothing in this package
// ever deletes: rule evaluation runs only in Purge's existing propose phase,
// and every match still requires a human Apply. This package is persistence +
// validation only (this file) plus the match/reason engine (evaluate.go) — no
// HTTP handlers and no internal/apidto types, the same boundary
// internal/discoversliders' package doc states explicitly.
package pruning

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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

// ErrNoConditions is returned when a rule has no Criteria and all five
// legacy scalar fields are at their unset sentinel (age_days == 0,
// size_bytes == 0, quality_tier_floor == "", len(tags) == 0, min_rating == 0)
// — AC1's "at least one condition required".
//
// A non-empty Criteria list satisfies this even when the scalars are all
// unset (the HTTP frontend always sends criteria and zeros the scalars).
var ErrNoConditions = errors.New("pruning: at least one condition (age, size, quality tier floor, tags, or min rating) is required")

// ErrInvalidCriterion is returned when a Criteria entry has an unknown
// field/op, a missing required unit, or a value that cannot be parsed for
// that field. Wrapped with the 1-based row index so the form can point at
// the bad row.
var ErrInvalidCriterion = errors.New("pruning: invalid criterion")

// ErrBlankTag is returned when Tags contains an empty or whitespace-only
// entry. This is the guard the retired addAllowlistTagHandler used to apply at
// the HTTP layer; it moves here with the mechanism.
var ErrBlankTag = errors.New("pruning: a tag must not be blank")

// ErrInvalidTierFloor is returned when QualityTierFloor is non-empty but
// isn't one of the four real quality tiers.
var ErrInvalidTierFloor = errors.New("pruning: invalid quality tier floor")

// ErrNegativeThreshold is returned when AgeDays or SizeBytes is negative.
var ErrNegativeThreshold = errors.New("pruning: threshold must not be negative")

// ErrInvalidMinRating is returned when MinRating is outside 0–5.
// 0 is the unset sentinel; 1–5 are keep-floors (match if 0 < rating < min).
var ErrInvalidMinRating = errors.New("pruning: min rating must be 0 through 5")

// Criterion field/op wire values. Keep these identical to the frontend
// dropdown values and to apidto.PruningCriterion — a typo here is a silent
// match miss, not a compile error.
const (
	FieldAge     = "age"
	FieldSize    = "size"
	FieldQuality = "quality"
	FieldTag     = "tag"
	FieldRating  = "rating"

	OpGT          = "gt"
	OpLT          = "lt"
	OpEQ          = "eq"
	OpContains    = "contains"
	OpNotContains = "notContains"

	UnitDays  = "days"
	UnitKB    = "kb"
	UnitMB    = "mb"
	UnitGB    = "gb"
	UnitTB    = "tb"
	UnitStars = "stars"
)

// Criterion is one AND'd row on a Rule: field + operator + free-fill value
// plus a unit when the field needs one (age/size/rating).
type Criterion struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
	Unit  string `json:"unit,omitempty"`
}

// Rule is one operator-authored pruning rule. Criteria is the current
// matching surface: an ordered list of AND'd rows. The five scalar fields
// (AgeDays/SizeBytes/QualityTierFloor/Tags/MinRating) are the pre-0014
// shape — Match uses them only when Criteria is empty, so existing Go tests
// that construct Rule{AgeDays: 365} stay green. A zero value in a scalar
// still means "this condition is not configured".
type Rule struct {
	ID               int64       `json:"id"`
	Name             string      `json:"name"`
	Mode             string      `json:"mode"`
	AgeDays          int         `json:"ageDays,omitempty"`
	SizeBytes        int64       `json:"sizeBytes,omitempty"`
	QualityTierFloor string      `json:"qualityTierFloor,omitempty"`
	Tags             []string    `json:"tags,omitempty"`
	MinRating        int         `json:"minRating,omitempty"`
	Criteria         []Criterion `json:"criteria,omitempty"`
	Enabled          bool        `json:"enabled"`
	CreatedAt        string      `json:"createdAt"`
	UpdatedAt        string      `json:"updatedAt"`
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
// thresholds, out-of-range min ratings, and blank tags, and requires at least
// one condition — either a non-empty Criteria list or one of the five legacy
// scalars (AC1). Called by both Create and Update.
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
	if r.MinRating < 0 || r.MinRating > 5 {
		return fmt.Errorf("%w: %d", ErrInvalidMinRating, r.MinRating)
	}
	if r.QualityTierFloor != "" && !validTierFloors[r.QualityTierFloor] {
		return fmt.Errorf("%w: %q", ErrInvalidTierFloor, r.QualityTierFloor)
	}
	for _, tag := range r.Tags {
		if strings.TrimSpace(tag) == "" {
			return ErrBlankTag
		}
	}
	if len(r.Criteria) > 0 {
		for i, c := range r.Criteria {
			if err := validateCriterion(c); err != nil {
				if errors.Is(err, ErrBlankTag) {
					return err
				}
				return fmt.Errorf("%w: criterion %d: %v", ErrInvalidCriterion, i+1, err)
			}
		}
		return nil
	}
	if r.AgeDays == 0 && r.SizeBytes == 0 && r.QualityTierFloor == "" && len(r.Tags) == 0 && r.MinRating == 0 {
		return ErrNoConditions
	}
	return nil
}

func validateCriterion(c Criterion) error {
	switch c.Field {
	case FieldAge:
		if !isCompareOp(c.Op) {
			return fmt.Errorf("age operator must be gt, lt, or eq")
		}
		if strings.ToLower(c.Unit) != UnitDays {
			return fmt.Errorf("age unit must be days")
		}
		if _, err := parseNonNegInt(c.Value); err != nil {
			return fmt.Errorf("age value must be a non-negative integer")
		}
	case FieldSize:
		if !isCompareOp(c.Op) {
			return fmt.Errorf("size operator must be gt, lt, or eq")
		}
		n, ok := criterionSizeBytes(c.Value, c.Unit)
		if !ok || n <= 0 {
			return fmt.Errorf("size value must be a positive number with unit kb, mb, gb, or tb")
		}
	case FieldQuality:
		if !isCompareOp(c.Op) {
			return fmt.Errorf("quality operator must be gt, lt, or eq")
		}
		if strings.TrimSpace(c.Unit) != "" {
			return fmt.Errorf("quality has no unit")
		}
		if !validTierFloors[strings.ToLower(strings.TrimSpace(c.Value))] {
			return fmt.Errorf("quality value must be low, medium, high, or lossless")
		}
	case FieldTag:
		if c.Op != OpContains && c.Op != OpNotContains {
			return fmt.Errorf("tag operator must be contains or notContains")
		}
		if strings.TrimSpace(c.Unit) != "" {
			return fmt.Errorf("tag has no unit")
		}
		if strings.TrimSpace(c.Value) == "" {
			return ErrBlankTag
		}
	case FieldRating:
		if !isCompareOp(c.Op) {
			return fmt.Errorf("rating operator must be gt, lt, or eq")
		}
		if strings.ToLower(c.Unit) != UnitStars {
			return fmt.Errorf("rating unit must be stars")
		}
		n, err := parseNonNegInt(c.Value)
		if err != nil || n < 1 || n > 5 {
			return fmt.Errorf("rating value must be 1 through 5")
		}
	default:
		return fmt.Errorf("unknown field %q", c.Field)
	}
	return nil
}

func isCompareOp(op string) bool {
	return op == OpGT || op == OpLT || op == OpEQ
}

func parseNonNegInt(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0, errInvalidInt
	}
	return n, nil
}

var errInvalidInt = errors.New("not a non-negative integer")

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
	criteriaJSON, err := marshalCriteria(r.Criteria)
	if err != nil {
		return Rule{}, fmt.Errorf("encoding criteria for pruning rule %q: %w", r.Name, err)
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO pruning_rules (name, mode, age_days, size_bytes, quality_tier_floor, tags, min_rating, criteria, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at
	`, r.Name, r.Mode, r.AgeDays, r.SizeBytes, r.QualityTierFloor, tagsJSON, r.MinRating, criteriaJSON, r.Enabled)

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
	criteriaJSON, err := marshalCriteria(r.Criteria)
	if err != nil {
		return Rule{}, fmt.Errorf("encoding criteria for pruning rule %d: %w", r.ID, err)
	}
	row := s.db.QueryRowContext(ctx, `
		UPDATE pruning_rules SET
			name = ?, mode = ?, age_days = ?, size_bytes = ?, quality_tier_floor = ?, tags = ?, min_rating = ?, criteria = ?, enabled = ?,
			updated_at = sakms_now()
		WHERE id = ?
		RETURNING id, created_at, updated_at
	`, r.Name, r.Mode, r.AgeDays, r.SizeBytes, r.QualityTierFloor, tagsJSON, r.MinRating, criteriaJSON, r.Enabled, r.ID)

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
		SELECT id, name, mode, age_days, size_bytes, quality_tier_floor, COALESCE(tags, '[]'), min_rating, COALESCE(criteria, '[]'), enabled, created_at, updated_at
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
		SELECT id, name, mode, age_days, size_bytes, quality_tier_floor, COALESCE(tags, '[]'), min_rating, COALESCE(criteria, '[]'), enabled, created_at, updated_at
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
		var criteriaJSON string
		// Argument order MUST track the SELECT lists in List/ListEnabledForMode
		// exactly — tags, min_rating, criteria, then enabled. A mismatch here
		// is a runtime scan error, not a compile error, so nothing catches it
		// until a test runs.
		if err := rows.Scan(&r.ID, &r.Name, &r.Mode, &r.AgeDays, &r.SizeBytes, &r.QualityTierFloor, &tagsJSON, &r.MinRating, &criteriaJSON, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
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
		if err := unmarshalCriteria(criteriaJSON, &r.Criteria); err != nil {
			return nil, fmt.Errorf("decoding criteria for pruning rule %d: %w", r.ID, err)
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

func marshalCriteria(cc []Criterion) (string, error) {
	if len(cc) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(cc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalCriteria(s string, out *[]Criterion) error {
	if s == "" || s == "[]" {
		*out = nil
		return nil
	}
	var decoded []Criterion
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
