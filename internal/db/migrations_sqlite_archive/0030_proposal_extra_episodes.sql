-- +goose Up
ALTER TABLE proposals ADD COLUMN extra_episode_numbers TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE proposals DROP COLUMN extra_episode_numbers;
