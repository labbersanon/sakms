-- +goose Up
-- Claude 2026-09-21: aggressive autovacuum on usenet_resume_state
-- Reason: per-segment full-JSON SaveResume creates rapid TOAST dead versions;
--   default autovacuum lags and the 8G sakms_db LUN filled (2026-09-21 outage).
-- Troubleshooting: sakms-db 100% / healthz 503; pair with sakms-resume-vacuum.timer
-- Review if: DB resume mirror is removed or throttled (settings can loosen)
-- Related: deploy/sakms/sakms-resume-vacuum.*; internal/downloadstate/store.go

ALTER TABLE usenet_resume_state SET (
    autovacuum_enabled = true,
    autovacuum_vacuum_threshold = 5,
    autovacuum_vacuum_scale_factor = 0.01,
    autovacuum_analyze_threshold = 5,
    autovacuum_analyze_scale_factor = 0.01,
    autovacuum_vacuum_cost_delay = 0
);

-- +goose Down
ALTER TABLE usenet_resume_state RESET (
    autovacuum_enabled,
    autovacuum_vacuum_threshold,
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_threshold,
    autovacuum_analyze_scale_factor,
    autovacuum_vacuum_cost_delay
);
