-- +goose Up
-- One-time cleanup: delete every performer/studio row in adult_newest_releases
-- that currently has ZERO linked scene/movie rows -- the orphans the pre-fix
-- pipeline created. Before the US-1 fix (internal/adultnewest/scan.go),
-- identifyStudioPerformers ran UNCONDITIONALLY after the match switch in both
-- matchRelease (browse pass) and processFeedItem (feed pass), so a confirmable
-- studio/performer could be cached even when no scene/movie row was persisted
-- alongside it -- producing Performers/Studios browse-strip cards with no
-- content behind them (confirmed live: "Madison"). Owner-approved to delete
-- outright (deep-interview spec: no soft-delete, no flag, no retention).
--
-- "Linked" is the EXACT same definition the Go query layer uses
-- (adultnewest.ReleaseStore.ScenesLinkedToEntity for the drill-down, and
-- listFiltered's browse-strip filter): a performer is linked iff some
-- scene/movie row's performers JSON array contains a case/whitespace-
-- insensitive match of its name (via json_each -- not a substring/LIKE,
-- still a full-name match); a studio iff some scene/movie row's
-- entity_studio case/whitespace-insensitively equals its name. entity_id
-- holds the name for a performer/studio row. Case/whitespace-insensitive
-- (TRIM(LOWER(...)), not byte-exact) because a scene row's
-- entity_studio/performers come from the matched box scene object itself,
-- while a performer/studio row's entity_id comes from the independent
-- verifyStudio/verifyPerformers result for the SAME name -- two different
-- sources confirmed (internal/identify/identify_test.go's
-- TestIdentifyDetailed_SceneMatchAlsoReturnsCorrectedStudioAndPerformers) to
-- sometimes diverge only in casing (e.g. scene-side "Vixen Media" vs
-- entity-side "vixen media"); a byte-exact join here would delete rows that
-- do have a real linked scene. This SQL is hand-mirrored from those Go
-- helpers because a goose migration can't call Go -- keep the two in sync if
-- the linkage definition ever changes. SQLite's json1 (json_each) is
-- confirmed available on both the production DB and the test driver
-- (modernc.org/sqlite).
--
-- The DELETE target can't be aliased in SQLite, so the correlation is the full
-- table name (adult_newest_releases.entity_id) with the inner scene instance
-- aliased sc.
DELETE FROM adult_newest_releases
WHERE row_type = 'performer'
  AND NOT EXISTS (
    SELECT 1 FROM adult_newest_releases sc
    WHERE sc.row_type IN ('scene', 'movie')
      AND EXISTS (
        SELECT 1 FROM json_each(sc.performers) je
        WHERE TRIM(LOWER(je.value)) = TRIM(LOWER(adult_newest_releases.entity_id))
      )
  );

DELETE FROM adult_newest_releases
WHERE row_type = 'studio'
  AND NOT EXISTS (
    SELECT 1 FROM adult_newest_releases sc
    WHERE sc.row_type IN ('scene', 'movie')
      AND TRIM(LOWER(sc.entity_studio)) = TRIM(LOWER(adult_newest_releases.entity_id))
  );

-- +goose Down
-- Irreversible by design: this migration DELETES orphaned data outright
-- (owner-approved -- see the deep-interview spec). The deleted performer/studio
-- rows are NOT restorable here; they can only be regenerated organically by the
-- background scan re-identifying a release that resolves to them alongside a
-- real scene. A no-op Down, matching this project's convention for irreversible
-- data-deleting migrations (cf. 0049's "cached CONTENT is not restored" note).
SELECT 1;
