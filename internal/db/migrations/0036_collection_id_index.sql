-- +goose Up
CREATE INDEX IF NOT EXISTS idx_library_items_collection_id ON library_items(collection_id);

-- +goose Down
DROP INDEX IF EXISTS idx_library_items_collection_id;
