package usenetsearch

import (
	"context"
	"strings"
)

// Search runs q against the local index. Never contacts NNTP.
func (s *Service) Search(ctx context.Context, q Query) ([]Candidate, error) {
	if s == nil || s.idx == nil {
		return nil, nil
	}
	if len(q.Groups) == 0 {
		q.Groups = append([]string(nil), s.cfg.Groups...)
	}
	return s.idx.SearchReleases(ctx, q)
}

// NormalizeTitleTerms splits a grab title into AND terms for Query.
func NormalizeTitleTerms(title string) []string {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	// Replace common separators with spaces, drop empty.
	r := strings.NewReplacer(".", " ", "_", " ", "-", " ", "(", " ", ")", " ")
	parts := strings.Fields(r.Replace(title))
	var out []string
	for _, p := range parts {
		if len(p) < 2 {
			continue
		}
		// skip tiny noise tokens
		low := strings.ToLower(p)
		if low == "the" || low == "and" || low == "of" {
			continue
		}
		out = append(out, p)
	}
	return out
}
