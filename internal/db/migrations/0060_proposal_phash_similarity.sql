-- Claude 2026-08-04: persist Proposal.PHashSimilarity, which ScanLibraryPHash /
-- ScanLibrarySeriesPHash have computed since 50dd970 but which was dropped at
-- the persistence boundary (no column existed) — silently defeating the
-- similarity badge/confidence label AC6 specifies (spec AC6 was rendering
-- from a GET path that never carried the value).
-- Reason: proposals.go's INSERT/SELECT/scanProposal never had this column, so
-- the value died in-memory after Scan and could never be read back by List/Get.
-- Troubleshooting: see .omc/plans/autopilot-impl-phash-grouping.md §2 for the
-- full traced break (dedup_phash_primary.go → api/dedup.go → proposals.go →
-- api/proposals.go → Dedup.tsx).
-- Review if: never — DEFAULT 0 keeps every pre-existing row (legacy/Adult
-- proposals with no similarity score) reading exactly as before, which is
-- the same omitempty sentinel apidto/dto.go already documents.
--
-- +goose Up
ALTER TABLE proposals ADD COLUMN phash_similarity REAL NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE proposals DROP COLUMN phash_similarity;
