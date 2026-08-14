-- +goose Up
-- Claude 2026-08-14: pruning_rules.criteria JSON list of AND'd {field,op,value,unit}
--   rows. The five scalar columns stay (never DROP); Match uses criteria when
--   the list is non-empty and falls back to the scalars otherwise.
-- Reason: the Clean-up Rules UI is now an ordered row builder — two ages,
--   contains + does-not-contain tags, etc. Five scalars cannot express that.
-- Troubleshooting: tag backfill is one `contains` row per stored tag, which
--   changes the old tags-OR-any into AND. Split into separate rules to keep
--   OR. Quality floor "lossless" cannot be expressed with only gt/lt/eq
--   (would mean every ranked tier); those rows keep empty criteria and the
--   legacy Match path. Age/size backfill use strict `gt` (old was >=).
-- Review if: gte/lte operators are added, or tags regain OR-within-rule.

ALTER TABLE pruning_rules
    ADD COLUMN criteria text NOT NULL DEFAULT '[]';

-- Backfill one criterion per configured scalar, in age/size/quality/tag/rating
-- order. Quality "at or below floor" maps onto gt/lt/eq as: low → eq low;
-- medium → lt high; high → lt lossless; lossless → omitted (legacy path).
UPDATE pruning_rules
SET criteria = (
    SELECT COALESCE(jsonb_agg(c ORDER BY ord), '[]'::jsonb)::text
    FROM (
        SELECT 1 AS ord,
               jsonb_build_object('field', 'age', 'op', 'gt', 'value', age_days::text, 'unit', 'days') AS c
        WHERE age_days > 0
        UNION ALL
        SELECT 2,
               jsonb_build_object(
                   'field', 'size',
                   'op', 'gt',
                   'value', trim(trailing '.' from trim(trailing '0' from (round(size_bytes::numeric / 1073741824, 6))::text)),
                   'unit', 'gb'
               )
        WHERE size_bytes > 0
        UNION ALL
        SELECT 3,
               CASE quality_tier_floor
                   WHEN 'low' THEN jsonb_build_object('field', 'quality', 'op', 'eq', 'value', 'low')
                   WHEN 'medium' THEN jsonb_build_object('field', 'quality', 'op', 'lt', 'value', 'high')
                   WHEN 'high' THEN jsonb_build_object('field', 'quality', 'op', 'lt', 'value', 'lossless')
                   ELSE NULL
               END
        WHERE quality_tier_floor IN ('low', 'medium', 'high')
        UNION ALL
        SELECT 10 + (ord::int),
               jsonb_build_object('field', 'tag', 'op', 'contains', 'value', tag)
        FROM jsonb_array_elements_text(
                 CASE WHEN tags IS NULL OR btrim(tags) = '' THEN '[]'::jsonb ELSE tags::jsonb END
             ) WITH ORDINALITY AS t(tag, ord)
        WHERE btrim(tag) <> ''
        UNION ALL
        SELECT 1000,
               jsonb_build_object('field', 'rating', 'op', 'lt', 'value', min_rating::text, 'unit', 'stars')
        WHERE min_rating > 0
    ) parts
    WHERE c IS NOT NULL
);

-- +goose Down
ALTER TABLE pruning_rules DROP COLUMN IF EXISTS criteria;
