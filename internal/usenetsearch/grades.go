package usenetsearch

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"
)

// Claude 2026-10-15: passive group grades — demote on failure, promote on success.
// Reason: no active OVER re-rank; start grade 1; +1 per failed grab (per GID);
//   −1 after promoteEverySuccesses successful imports. Grade 5 sticky on demote;
//   grade 1 floor on promote. Crawl/search require grade ≤ package ceiling;
//   crossing the ceiling deletes that group's header index rows.
// Troubleshooting: demote/promote log lines; missing row ⇒ grade 1.
// Review if: multi-mode shared groups should avoid index delete when another
//   mode still allows the group.
// Related files: 0040/0041 migrations, api usenet_nntp_demote.go, import.go

// promoteEverySuccesses is how many successful native imports promote one tier.
const promoteEverySuccesses = 2

// GroupGrade is one mode's reliability grade for a newsgroup.
type GroupGrade struct {
	Mode          string
	Group         string
	Grade         int
	FailCount     int64
	LastFailedAt  int64
	SuccessStreak int
	LastSuccessAt int64
}

// GetGrade returns the stored grade or 1 when no row exists.
func (idx *Index) GetGrade(ctx context.Context, mode, group string) (int, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	group = strings.TrimSpace(group)
	if mode == "" || group == "" {
		return 1, nil
	}
	var g int
	err := idx.db.QueryRowContext(ctx,
		`SELECT grade FROM usenet_nntp_group_grades WHERE mode=? AND group_name=?`,
		mode, group).Scan(&g)
	if err == sql.ErrNoRows {
		return 1, nil
	}
	if err != nil {
		return 1, err
	}
	if g < PackageStepMin {
		return PackageStepMin, nil
	}
	if g > PackageStepMax {
		return PackageStepMax, nil
	}
	return g, nil
}

// ListGrades returns all stored grades for mode.
func (idx *Index) ListGrades(ctx context.Context, mode string) ([]GroupGrade, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	rows, err := idx.db.QueryContext(ctx, `
SELECT mode, group_name, grade, fail_count, last_failed_at,
       COALESCE(success_streak, 0), COALESCE(last_success_at, 0)
FROM usenet_nntp_group_grades WHERE mode=?`, mode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupGrade
	for rows.Next() {
		var g GroupGrade
		if err := rows.Scan(&g.Mode, &g.Group, &g.Grade, &g.FailCount, &g.LastFailedAt,
			&g.SuccessStreak, &g.LastSuccessAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// demoteCooldownLimitSec skips a second demote for the same group within this window.
const demoteCooldownLimitSec int64 = 600

// DemoteGroup bumps grade by one (cap 5) for mode/group. Returns new grade.
// Resets success_streak. First failure inserts grade=2 (implicit prior grade 1).
// Rate-limited: another demote within demoteCooldownLimitSec is a no-op.
func (idx *Index) DemoteGroup(ctx context.Context, mode, group string) (int, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	group = strings.TrimSpace(group)
	if mode == "" || group == "" {
		return 1, nil
	}
	now := time.Now().Unix()
	var lastFail sql.NullInt64
	_ = idx.db.QueryRowContext(ctx,
		`SELECT last_failed_at FROM usenet_nntp_group_grades WHERE mode=? AND group_name=?`,
		mode, group).Scan(&lastFail)
	if lastFail.Valid && lastFail.Int64 > 0 && now-lastFail.Int64 < demoteCooldownLimitSec {
		return idx.GetGrade(ctx, mode, group)
	}
	_, err := idx.db.ExecContext(ctx, `
INSERT INTO usenet_nntp_group_grades (mode, group_name, grade, fail_count, last_failed_at, success_streak, last_success_at)
VALUES (?, ?, 2, 1, ?, 0, 0)
ON CONFLICT (mode, group_name) DO UPDATE SET
  grade = CASE
    WHEN usenet_nntp_group_grades.grade >= ? THEN usenet_nntp_group_grades.grade
    ELSE usenet_nntp_group_grades.grade + 1
  END,
  fail_count = usenet_nntp_group_grades.fail_count + 1,
  last_failed_at = EXCLUDED.last_failed_at,
  success_streak = 0
`, mode, group, now, PackageStepMax)
	if err != nil {
		return 0, err
	}
	return idx.GetGrade(ctx, mode, group)
}

// NoteSuccess records one successful native import. After promoteEverySuccesses
// successes, grade decreases by one (floor 1) and the streak resets.
// Returns (newGrade, promoted). Atomic upsert — safe against concurrent demote.
func (idx *Index) NoteSuccess(ctx context.Context, mode, group string) (grade int, promoted bool, err error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	group = strings.TrimSpace(group)
	if mode == "" || group == "" {
		return 1, false, nil
	}
	now := time.Now().Unix()
	var prevGrade sql.NullInt64
	_ = idx.db.QueryRowContext(ctx,
		`SELECT grade FROM usenet_nntp_group_grades WHERE mode=? AND group_name=?`, mode, group).Scan(&prevGrade)
	err = idx.db.QueryRowContext(ctx, `
INSERT INTO usenet_nntp_group_grades (mode, group_name, grade, fail_count, last_failed_at, success_streak, last_success_at)
VALUES (?, ?, 1, 0, 0, 1, ?)
ON CONFLICT (mode, group_name) DO UPDATE SET
  grade = CASE
    WHEN usenet_nntp_group_grades.success_streak + 1 >= ? THEN
      CASE WHEN usenet_nntp_group_grades.grade <= ? THEN ?
      ELSE usenet_nntp_group_grades.grade - 1 END
    ELSE usenet_nntp_group_grades.grade
  END,
  success_streak = CASE
    WHEN usenet_nntp_group_grades.success_streak + 1 >= ? THEN 0
    ELSE usenet_nntp_group_grades.success_streak + 1
  END,
  last_success_at = EXCLUDED.last_success_at
RETURNING grade
`, mode, group, now, promoteEverySuccesses, PackageStepMin, PackageStepMin, promoteEverySuccesses).Scan(&grade)
	if err != nil {
		return 0, false, err
	}
	old := 1
	if prevGrade.Valid {
		old = int(prevGrade.Int64)
	}
	return grade, grade < old, nil
}

// ResetGrade sets grade back to 1 and clears counters (operator escape).
func (idx *Index) ResetGrade(ctx context.Context, mode, group string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	group = strings.TrimSpace(group)
	_, err := idx.db.ExecContext(ctx, `
INSERT INTO usenet_nntp_group_grades (mode, group_name, grade, fail_count, last_failed_at, success_streak, last_success_at)
VALUES (?, ?, 1, 0, 0, 0, 0)
ON CONFLICT (mode, group_name) DO UPDATE SET
  grade=1, fail_count=0, last_failed_at=0, success_streak=0, last_success_at=0
`, mode, group)
	return err
}

// DeleteGroupIndex removes all headers and the watermark for group.
func (idx *Index) DeleteGroupIndex(ctx context.Context, group string) (int64, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return 0, nil
	}
	res, err := idx.db.ExecContext(ctx, `DELETE FROM usenet_nntp_headers WHERE group_name=?`, group)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	_, _ = idx.db.ExecContext(ctx, `DELETE FROM usenet_nntp_watermarks WHERE group_name=?`, group)
	return n, nil
}

// EligibleGroups filters candidates to those with grade ≤ ceiling.
func (idx *Index) EligibleGroups(ctx context.Context, mode string, ceiling int, candidates []string) ([]string, error) {
	ceiling = ClampPackageStep(ceiling)
	var out []string
	for _, g := range candidates {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		grade, err := idx.GetGrade(ctx, mode, g)
		if err != nil {
			return nil, err
		}
		if grade <= ceiling {
			out = append(out, g)
		}
	}
	return out, nil
}

// PruneGroupsBeyondCeiling is a no-op placeholder kept for call sites that
// used to delete per-mode. Index deletion is owned by Service.PruneBeyondCeilings
// which only deletes when no enabled mode still allows the group.
func (idx *Index) PruneGroupsBeyondCeiling(ctx context.Context, mode string, ceiling int, candidates []string) error {
	_ = ctx
	_ = mode
	_ = ceiling
	_ = candidates
	return nil
}
