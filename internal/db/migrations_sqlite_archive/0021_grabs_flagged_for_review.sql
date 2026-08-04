-- +goose Up
-- Auto-grab's post-grab mislabel check (internal/autograb.RuntimeMismatch)
-- flags an imported grab whose actual file duration is wildly inconsistent
-- with the known TMDB/TPDB runtime, for the operator to review — the import
-- still succeeded, this is advisory, so it's a field rather than a lifecycle
-- status. flag_reason is a short human-readable explanation for the UI.
ALTER TABLE grabs ADD COLUMN flagged_for_review INTEGER NOT NULL DEFAULT 0;
ALTER TABLE grabs ADD COLUMN flag_reason TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE grabs DROP COLUMN flag_reason;
ALTER TABLE grabs DROP COLUMN flagged_for_review;
