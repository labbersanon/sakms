-- +goose Up
-- Claude 2026-08-27: btree indexes on denormalized library file_path columns.
-- Reason: Organize Browse tracked badges (and RemapPath/HasTrackedUnder)
--   filter with file_path = $dir OR starts_with(file_path, $dir || '/').
--   library_items / library_episodes / library_scenes had no file_path
--   index; library_episode_files UNIQUE is (episode_id, file_path), which
--   cannot serve a prefix-only scan. library_item_files and
--   library_scene_files already UNIQUE(file_path).
-- Troubleshooting: Browse /tracked still slow after this — check EXPLAIN
--   for starts_with; Postgres 16's ^@ is sargable if we rewrite later.
-- Review if: library_*_files become the sole path source (drop these).

CREATE INDEX idx_library_items_file_path ON library_items (file_path);
CREATE INDEX idx_library_episodes_file_path ON library_episodes (file_path);
CREATE INDEX idx_library_scenes_file_path ON library_scenes (file_path);
CREATE INDEX idx_library_episode_files_file_path ON library_episode_files (file_path);

-- +goose Down
DROP INDEX IF EXISTS idx_library_episode_files_file_path;
DROP INDEX IF EXISTS idx_library_scenes_file_path;
DROP INDEX IF EXISTS idx_library_episodes_file_path;
DROP INDEX IF EXISTS idx_library_items_file_path;
