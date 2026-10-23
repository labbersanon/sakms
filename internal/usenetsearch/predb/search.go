package predb

import (
	"context"
	"strings"
)

// Search is NNTmux Search::searchPredb + match:prefiles without Manticore:
// exact/stem filename, title-md5/sha1, then exact title of the cleaned name.
// Local table only — never HTTP. Hashed queries skip the title step.
//
// Claude 2026-10-22: copy NNTmux match:prefiles / fixNamesWithFiles.
// Reason: live hash_hits=0 with 6.9M PreDB rows because namefix only did
//
//	ByHash(md5(title)) and skipped ByFilename on the BODY path. NNTmux
//	matches cleaned release_files.name to predb.filename and predb.title.
//
// Troubleshooting: Search misses a known dump filename → ByFilename
//
//	filename<>'' predicate or stem <16. Review if: pg_trgm is added.
func (s *Store) Search(ctx context.Context, query string) (Entry, bool, error) {
	if s == nil || s.db == nil {
		return Entry{}, false, nil
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return Entry{}, false, nil
	}
	cleaned := CleanForMatching(q)
	files := []string{q}
	if cleaned != "" && !strings.EqualFold(cleaned, q) {
		files = append(files, cleaned)
	}
	for _, fn := range files {
		if e, ok, err := s.ByFilename(ctx, fn); err != nil || ok {
			return e, ok, err
		}
	}
	for _, src := range []string{q, cleaned} {
		for _, h := range HashTokens(src) {
			if e, ok, err := s.ByHash(ctx, h); err != nil || ok {
				return e, ok, err
			}
		}
	}
	if LooksLikeHashed(q) || LooksLikeHashed(cleaned) {
		return Entry{}, false, nil
	}
	for _, title := range titleQueries(q, cleaned) {
		if e, ok, err := s.ByTitle(ctx, title); err != nil || ok {
			return e, ok, err
		}
	}
	return Entry{}, false, nil
}

func titleQueries(raw, cleaned string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(raw)
	add(cleaned)
	if cleaned != "" {
		add(strings.ReplaceAll(cleaned, " ", "."))
		add(strings.ReplaceAll(cleaned, "_", "."))
	}
	return out
}
