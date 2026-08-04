-- +goose Up
CREATE TABLE IF NOT EXISTS vmaf_scores (
    candidate_path       TEXT    NOT NULL,
    candidate_file_size  INTEGER NOT NULL DEFAULT 0,
    candidate_file_mtime TEXT    NOT NULL DEFAULT '',
    reference_path       TEXT    NOT NULL,
    score                REAL    NOT NULL,
    computed_at          TEXT    NOT NULL,
    PRIMARY KEY (candidate_path, reference_path)
);

-- +goose Down
DROP TABLE IF EXISTS vmaf_scores;
