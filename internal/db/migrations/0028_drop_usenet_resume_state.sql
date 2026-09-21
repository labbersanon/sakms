-- +goose Up
-- Claude 2026-09-21: DROP TABLE usenet_resume_state (sidecar-only resume).
-- Reason: per-segment full-JSON DB mirrors filled the 8G sakms_db LUN via MVCC
--   TOAST (~6.5G from one REMUX). Staging .sakms-resume.json is SoT, like
--   NZBGet/SABnzbd; torrent seed state stays in torrent_seed_state.
-- Troubleshooting: sakms-db 100% / healthz 503; do not VACUUM this table after drop.
-- Review if: a UI/debug resume view is required (read sidecar; do not re-create this table).
-- Related: internal/usenet/resume.go; internal/downloadstate/store.go;
--   deploy/sakms/sakms-resume-vacuum.* (retired)

DROP TABLE IF EXISTS usenet_resume_state;

-- +goose Down
-- Recreate the last known schema so goose down of an unrelated later migration
-- can still reverse this one. Autovacuum reloptions from 0027 are not restored.
CREATE TABLE IF NOT EXISTS usenet_resume_state (
    download_gid TEXT PRIMARY KEY NOT NULL,
    state_json   TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL DEFAULT ''
);
