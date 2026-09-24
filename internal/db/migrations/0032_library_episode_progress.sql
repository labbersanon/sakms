-- +goose Up
-- Claude 2026-09-24: per-episode watch position for owned Series playback.
-- Reason: Play Show vs Resume and episode progress bars cannot be honest
--   without a table. Movies/Adult stay untouched this pass.
-- Troubleshooting: header Resume would otherwise guess or stay hidden.
-- Review if: Movies/Adult join this table or progress syncs with a player.
-- Related files: internal/library/library_episode_progress.go,
--   frontend/src/screens/seriesPlay.ts

CREATE TABLE library_episode_progress (
    episode_id bigint PRIMARY KEY,
    position_seconds double precision NOT NULL DEFAULT 0,
    duration_seconds double precision NOT NULL DEFAULT 0,
    watched boolean NOT NULL DEFAULT false,
    updated_at text NOT NULL DEFAULT sakms_now(),
    FOREIGN KEY (episode_id) REFERENCES library_episodes (id) ON DELETE CASCADE ON UPDATE NO ACTION DEFERRABLE INITIALLY IMMEDIATE
);

-- +goose Down
DROP TABLE IF EXISTS library_episode_progress;
