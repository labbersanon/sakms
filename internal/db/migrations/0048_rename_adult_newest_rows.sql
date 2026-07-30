-- +goose Up
-- Rename the seeded admin rows now that RSS-derived rows are the ONLY Performers/Studios
-- rows. GUARDED on the exact seeded default title so a name the operator already customized
-- via the admin UI is never clobbered. Idempotent; a no-op if already renamed.
UPDATE adult_newest_rows SET title = 'Performers'
  WHERE row_type = 'performer' AND title = 'New Performers';
UPDATE adult_newest_rows SET title = 'Studios'
  WHERE row_type = 'studio' AND title = 'New Studios';

-- +goose Down
UPDATE adult_newest_rows SET title = 'New Performers'
  WHERE row_type = 'performer' AND title = 'Performers';
UPDATE adult_newest_rows SET title = 'New Studios'
  WHERE row_type = 'studio' AND title = 'Studios';
