-- +goose Up
-- Claude 2026-08-14: rewrite pruning_rules.criteria tag rows to values[] +
--   matchMode, and collapse adjacent 0014 one-contains-per-tag clusters into
--   one contains+any list (live Adult BDSM id=4: Bondage/Bound/Dungeon/Pee/Peeing).
-- Reason: 0014 turned the old tags[] OR-any list into AND of separate contains
--   rows. Tag criteria now hold a chip list plus any (OR) / all (AND).
-- Troubleshooting: only consecutive field=tag, op=contains, single `value`,
--   no matchMode rows merge. Mixed contains+notContains and already-authored
--   values[]/matchMode rows stay separate. notContains clusters are not merged
--   (0014 never created them). Scalar columns are not DROPped.
-- Review if: gte/lte operators are added, or drag-and-drop rule ordering ships.

-- +goose StatementBegin
DO $$
DECLARE
  rec RECORD;
  arr jsonb;
  elem jsonb;
  out_arr jsonb;
  cluster jsonb;
  i int;
  n int;
  field text;
  op text;
  match_mode text;
  raw_vals jsonb;
  vals jsonb;
  val text;
BEGIN
  FOR rec IN SELECT id, criteria FROM pruning_rules LOOP
    arr := CASE
      WHEN rec.criteria IS NULL OR btrim(rec.criteria) = '' THEN '[]'::jsonb
      ELSE rec.criteria::jsonb
    END;
    out_arr := '[]'::jsonb;
    cluster := '[]'::jsonb;
    n := jsonb_array_length(arr);
    i := 0;
    WHILE i < n LOOP
      elem := arr->i;
      field := COALESCE(elem->>'field', '');
      op := COALESCE(elem->>'op', '');
      match_mode := COALESCE(elem->>'matchMode', '');
      raw_vals := elem->'values';
      val := COALESCE(elem->>'value', '');

      -- 0014 backfill cluster: adjacent tag+contains with a single value and
      -- no matchMode / values[]. Operator-authored rows that already carry
      -- matchMode or values stay as their own criterion.
      IF field = 'tag' AND op = 'contains'
         AND match_mode = ''
         AND (raw_vals IS NULL OR jsonb_typeof(raw_vals) <> 'array' OR jsonb_array_length(raw_vals) = 0)
         AND btrim(val) <> '' THEN
        cluster := cluster || jsonb_build_array(val);
      ELSE
        IF jsonb_array_length(cluster) > 0 THEN
          out_arr := out_arr || jsonb_build_array(
            jsonb_build_object(
              'field', 'tag',
              'op', 'contains',
              'matchMode', 'any',
              'values', cluster
            )
          );
          cluster := '[]'::jsonb;
        END IF;

        IF field = 'tag' THEN
          IF raw_vals IS NOT NULL AND jsonb_typeof(raw_vals) = 'array' AND jsonb_array_length(raw_vals) > 0 THEN
            vals := raw_vals;
          ELSIF btrim(val) <> '' THEN
            vals := jsonb_build_array(val);
          ELSE
            vals := '[]'::jsonb;
          END IF;
          IF match_mode = '' THEN
            match_mode := 'any';
          END IF;
          out_arr := out_arr || jsonb_build_array(
            jsonb_build_object(
              'field', 'tag',
              'op', op,
              'matchMode', match_mode,
              'values', vals
            )
          );
        ELSE
          out_arr := out_arr || jsonb_build_array(elem);
        END IF;
      END IF;
      i := i + 1;
    END LOOP;

    IF jsonb_array_length(cluster) > 0 THEN
      out_arr := out_arr || jsonb_build_array(
        jsonb_build_object(
          'field', 'tag',
          'op', 'contains',
          'matchMode', 'any',
          'values', cluster
        )
      );
    END IF;

    UPDATE pruning_rules SET criteria = out_arr::text WHERE id = rec.id;
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Claude 2026-08-14: 0015 rewrites criteria JSON in place (no new column).
-- Reason: reversing would re-AND the live BDSM rule; homelab never deletes
--   retired product data and this rewrite is not a DROP.
-- Troubleshooting: goose requires a Down section; this is deliberately a no-op.
-- Review if: a lossless reverse of values[]+matchMode is actually needed.
SELECT 1;
