-- +goose Up
-- Performer names for a matched Scene/Movie entity, JSON-array-encoded TEXT —
-- same shape/convention as the existing genres column. Sourced from the
-- matched box scene's OWN performer list (identify.MatchResult.Performers,
-- TPDB's SceneResource.performers[].name), not from the AI's filename-parse
-- guess (detail.Performers in scan.go's matchRelease, which only ever spins
-- off separate standalone Performer cache rows) — see MatchResult.Performers'
-- doc comment for why that distinction matters. Empty for Studio/Performer
-- rows and for entities matched before this column existed.
ALTER TABLE adult_newest_releases ADD COLUMN performers TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE adult_newest_releases DROP COLUMN performers;
