-- +goose Up
-- Claude 2026-10-15: title/year on library_quality_prefs for Discover-only
-- NNTP crawl-admission matchability (approved plan
-- nntp-crawl-quality-admission.md, locked decision #5/open-question 1).
-- Reason: the crawler only ever sees a release's subject line, never a
--   tmdb_id — matching a title override at OVER time requires the title
--   text and year to already live on this row. A series override already
--   resolves through library.Series (title/year available at PUT time via
--   the existing row), but a MOVIE override set from Discover before the
--   movie is in the library had no title/year anywhere in this table.
-- Troubleshooting: api.NewNNTPAdmissionPolicy (Step 4) reads these two
--   columns via ListTitleQualityPrefs; a blank title means the row predates
--   this migration or was never resolvable (falls back to mode defaults at
--   crawl time, same as before this migration existed).
-- Review if: a title override's underlying library title is renamed —
--   these columns are a point-in-time snapshot taken at PUT, not a live
--   join, so a rename only takes effect on the next PUT.
-- Related files: internal/library/quality_prefs.go, internal/api/
--   titlequality.go, internal/api/usenetadmission.go

ALTER TABLE library_quality_prefs
    ADD COLUMN title text NOT NULL DEFAULT '';
ALTER TABLE library_quality_prefs
    ADD COLUMN year INTEGER;

-- +goose Down
ALTER TABLE library_quality_prefs DROP COLUMN IF EXISTS title;
ALTER TABLE library_quality_prefs DROP COLUMN IF EXISTS year;
