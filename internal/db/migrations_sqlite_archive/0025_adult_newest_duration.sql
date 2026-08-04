-- +goose Up
-- Adds the matched entity's runtime, threaded through from
-- identify.MatchResult.RuntimeSeconds (internal/identify/boxlookup.go) —
-- fixes a real live bug: Adult's auto-grab bitrate-quality-floor scorer
-- (internal/autograb.GradeCandidate) never re-fetches a real runtime the
-- way Movies/Series do, so a grab request built from a matched entity with
-- no duration data silently failed to auto-qualify anything (every
-- candidate landed in the manual fallback pick list, reading as "no
-- available downloads"). 0 means unknown, same convention as
-- tpdbrest.Scene.Duration/stashbox.Scene.Duration.
ALTER TABLE adult_newest_releases ADD COLUMN entity_duration_seconds INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE adult_newest_releases DROP COLUMN entity_duration_seconds;
