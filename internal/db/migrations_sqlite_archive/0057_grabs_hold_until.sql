-- +goose Up
-- A pre-release request's hold. A COLUMN on `grabs`, not a new table, for the
-- same reason 0054_grabs_retry.sql gave for retry state: there is NO `requests`
-- table — internal/api/requests.go aggregates Requests live on read ("a grab IS
-- the request") — so a request's state belongs on `grabs`.
--
-- hold_until is ALSO this feature's ORIGIN MARKER, deliberately. A row with a
-- non-empty hold_until originated as a Calendar pre-release request and nothing
-- else can produce one. That is a queryable fact, not an inference from status +
-- retry_reason + retry_count — the pattern that caused a HIGH-severity
-- misclassification bug in series air-date monitoring (see airdatemonitor.go's
-- airDateShaped/airDateOriginated and their doc comments).
--
-- It is NEVER cleared, including after the row is promoted and dispatched:
-- provenance outlives the hold. DueForRelease's retry_after = '' guard, not a
-- clear, is what makes promotion fire exactly once.
ALTER TABLE grabs ADD COLUMN hold_until TEXT NOT NULL DEFAULT '';

-- At most ONE held request per (mode, tmdb_id), enforced by the DATABASE rather
-- than by the route's check-then-act.
--
-- POST /api/calendar/prerelease-request reads FindHeldRequest and, on a miss,
-- Creates. Two genuinely concurrent clicks (a double-click, or two tabs) can both
-- miss that read and both Create. Nothing downstream can repair it: the shared
-- nonHeldMovieWork predicate self-excludes EVERY held row, not just the row under
-- evaluation, so promotion-time suppression cannot tell two held rows apart —
-- both promote, both dispatch, one film downloads twice.
--
-- PARTIAL (WHERE hold_until != '') because the constraint belongs to Calendar
-- pre-release requests alone. Ordinary grabs legitimately repeat a (mode, tmdb_id)
-- pair — a re-grab, a retry that minted a fresh row — and a total unique index
-- would break every one of them.
--
-- No pre-existing-duplicate failure mode on an upgrading install: the ADD COLUMN
-- above lands '' on every existing row, so there are zero held rows when this
-- index is built.
CREATE UNIQUE INDEX idx_grabs_held_request ON grabs (mode, tmdb_id) WHERE hold_until != '';

-- +goose Down
-- DROP INDEX FIRST. SQLite's ALTER TABLE ... DROP COLUMN refuses a column named in
-- a partial index's WHERE clause, so dropping hold_until while this index exists
-- fails outright. Verified by swapping these two statements and running
-- migration_grabs_hold_until_test.go, which failed with:
--   SQL logic error: error in index idx_grabs_held_request after drop column:
--   no such column: hold_until (1)
-- so this ordering is load-bearing, not stylistic, and that test is what catches
-- a future edit that reverses it.
DROP INDEX idx_grabs_held_request;
ALTER TABLE grabs DROP COLUMN hold_until;
