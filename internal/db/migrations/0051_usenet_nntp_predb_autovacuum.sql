-- +goose Up
-- Claude 2026-10-22: per-table autovacuum on usenet_nntp_predb (not global).
-- Reason: default vacuum_scale_factor 0.2 waits for ~1.44M dead rows; live
--   sat at 185k with last_vacuum empty. Dump import already VACUUM ANALYZE;
--   IRC ON CONFLICT between dumps never hit the trip point. Do not set
--   autovacuum_vacuum_cost_delay=0 (resume-state emergency; this heap is 2GB
--   on iSCSI). Do not VACUUM FULL. Review if: dump import is the only writer
--   (then defaults are fine again).
-- Related files: internal/usenetsearch/predb/dump.go, internal/api/usenetnntp_predb.go

ALTER TABLE usenet_nntp_predb SET (
    autovacuum_vacuum_scale_factor = 0.02,
    autovacuum_vacuum_threshold = 1000,
    autovacuum_analyze_scale_factor = 0.02,
    autovacuum_analyze_threshold = 5000,
    autovacuum_vacuum_insert_scale_factor = 0.05,
    autovacuum_vacuum_insert_threshold = 1000
);

-- +goose Down
ALTER TABLE usenet_nntp_predb RESET (
    autovacuum_vacuum_scale_factor,
    autovacuum_vacuum_threshold,
    autovacuum_analyze_scale_factor,
    autovacuum_analyze_threshold,
    autovacuum_vacuum_insert_scale_factor,
    autovacuum_vacuum_insert_threshold
);
