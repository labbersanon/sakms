package usenetsearch

import (
	"context"
	"strings"

	"github.com/labbersanon/sakms/internal/release"
)

// Search runs q against the local index. Never contacts NNTP.
// Groups nil ⇒ search all crawled groups; empty non-nil ⇒ no matches.
func (s *Service) Search(ctx context.Context, q Query) ([]Candidate, error) {
	if s == nil || s.idx == nil {
		return nil, nil
	}
	if q.Groups == nil {
		q.Groups = s.cfg.AllGroups()
	}
	if len(q.Groups) == 0 {
		return nil, nil
	}
	cs, err := s.idx.SearchReleases(ctx, q)
	if err != nil || len(cs) == 0 {
		return cs, err
	}
	langs, filter := s.preferredLanguages(ctx)
	if !filter {
		return cs, nil
	}
	out := make([]Candidate, 0, len(cs))
	for _, c := range cs {
		if release.TitleLanguageAllowed(c.Name, langs) {
			out = append(out, c)
		}
	}
	return out, nil
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
