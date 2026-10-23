-- +goose Up
-- Claude 2026-10-22: dirty named-hidden multiword subjects after ident relax.
-- Reason: identNameSQL now matches multiword titles without 1080p. Existing
--   named_hidden rows stay searchable=FALSE until recomputeOne runs.
-- Troubleshooting: Searchable unchanged after deploy → this UPDATE matched
--   0 rows, or RecomputeDirty backlog has not drained them.
-- Review if: identNameSQL third OR is removed (then goose down + recompute).
-- Related files: internal/usenetsearch/completeness.go

UPDATE usenet_nntp_releases SET dirty=TRUE
WHERE state='named' AND searchable=FALSE AND display_name<>''
  AND NOT (state='named' AND name_source='subject' AND (display_name ~ '^\(' OR display_name ~* '^description' OR (display_name !~ '[ ._]' AND display_name !~* '(1080|720|2160|480)p|1920x1080')))
  AND (name_source IN ('par2','parhash','rar','ybegin','nfo','file','sfv','srrdb','nzb') OR (display_name ~* '(1080|720|2160|480)p|1280x720|3840x2160|1920x[0-9]+' AND (display_name ~ '[12][0-9]{3}' OR display_name ~* 'S[0-9]{1,2}([^0-9]|$)' OR (display_name ~ '[A-Za-z]{3,}' AND display_name ~ '[ .]'))) OR (display_name ~ '[A-Za-z]{3,}' AND display_name ~ '[ ._]' AND display_name !~ '^\('));

-- +goose Down
UPDATE usenet_nntp_releases SET dirty=TRUE
WHERE state='named' AND searchable=TRUE AND name_source='subject'
  AND display_name !~* '(1080|720|2160|480)p|1280x720|3840x2160|1920x[0-9]+';
