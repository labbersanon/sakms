-- +goose Up
CREATE TABLE library_collections (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    tmdb_collection_id INTEGER NOT NULL UNIQUE,
    name               TEXT    NOT NULL
);

ALTER TABLE library_items ADD COLUMN collection_id INTEGER REFERENCES library_collections(id);

-- +goose Down
ALTER TABLE library_items DROP COLUMN collection_id;
DROP TABLE library_collections;
