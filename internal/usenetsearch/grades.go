package usenetsearch

import (
	"context"
	"database/sql"
	"log"
	"strings"
	"time"
)

// Claude 2026-10-15: passive group grades — demote on Usenet failure only.
// Reason: no active OVER re-rank; start grade 1, +1 per failed grab (per GID).
//   Grade 5 is sticky. Crawl/search require grade ≤ package ceiling; crossing
//   the ceiling deletes that group's header index rows.
// Troubleshooting: demotions log "usenetsearch: demote"; missing row ⇒ grade 1.
// Review if: multi-mode shared groups should avoid index delete when another
//   mode still allows the group.
// Related files: 0040_usenet_nntp_group_grades.sql, crawler.go, api usenetretry

// GroupGrade is one mode's reliability grade for a newsgroup.
type GroupGrade struct {
	Mode         string
	Group        string
	Grade        int
	FailCount    int64
	LastFailedAt int64
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
SELECT mode, group_name, grade, fail_count, last_failed_at
FROM usenet_nntp_group_grades WHERE mode=?`, mode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupGrade
	for rows.Next() {
		var g GroupGrade
		if err := rows.Scan(&g.Mode, &g.Group, &g.Grade, &g.FailCount, &g.LastFailedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// DemoteGroup bumps grade by one (cap 5) for mode/group. Returns new grade.
// firstFailure inserts grade=2 when no row existed (implicit prior grade 1).
func (idx *Index) DemoteGroup(ctx context.Context, mode, group string) (int, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	group = strings.TrimSpace(group)
	if mode == "" || group == "" {
		return 1, nil
	}
	now := time.Now().Unix()
	res, err := idx.db.ExecContext(ctx, `
INSERT INTO usenet_nntp_group_grades (mode, group_name, grade, fail_count, last_failed_at)
VALUES (?, ?, 2, 1, ?)
ON CONFLICT (mode, group_name) DO UPDATE SET
  grade = CASE
    WHEN usenet_nntp_group_grades.grade >= ? THEN usenet_nntp_group_grades.grade
    ELSE usenet_nntp_group_grades.grade + 1
  END,
  fail_count = usenet_nntp_group_grades.fail_count + 1,
  last_failed_at = EXCLUDED.last_failed_at
`, mode, group, now, PackageStepMax)
	if err != nil {
		return 0, err
	}
	_, _ = res.RowsAffected()
	return idx.GetGrade(ctx, mode, group)
}

// ResetGrade sets grade back to 1 (operator escape).
func (idx *Index) ResetGrade(ctx context.Context, mode, group string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	group = strings.TrimSpace(group)
	_, err := idx.db.ExecContext(ctx, `
INSERT INTO usenet_nntp_group_grades (mode, group_name, grade, fail_count, last_failed_at)
VALUES (?, ?, 1, 0, 0)
ON CONFLICT (mode, group_name) DO UPDATE SET grade=1, fail_count=0, last_failed_at=0
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

// PruneGroupsBeyondCeiling deletes index rows for candidates whose grade > ceiling.
func (idx *Index) PruneGroupsBeyondCeiling(ctx context.Context, mode string, ceiling int, candidates []string) error {
	ceiling = ClampPackageStep(ceiling)
	for _, g := range candidates {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		grade, err := idx.GetGrade(ctx, mode, g)
		if err != nil {
			return err
		}
		if grade <= ceiling {
			continue
		}
		n, err := idx.DeleteGroupIndex(ctx, g)
		if err != nil {
			return err
		}
		if n > 0 {
			log.Printf("usenetsearch: pruned %d headers for %s (grade %d > ceiling %d, mode=%s)",
				n, g, grade, ceiling, mode)
		}
	}
	return nil
}
