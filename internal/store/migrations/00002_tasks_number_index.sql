-- +goose Up
-- Metadata reuse looks a work up by its number (`Store.FindMetadataJSON`), once per
-- task run. Without this index that lookup scans every task the library has ever
-- had, which grows without bound.
CREATE INDEX ix_tasks_number ON tasks (number);
