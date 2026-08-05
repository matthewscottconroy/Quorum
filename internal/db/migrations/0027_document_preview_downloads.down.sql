DROP TABLE document_downloads;
ALTER TABLE resources DROP COLUMN file_preview_only;
DROP INDEX idx_folders_name;
CREATE UNIQUE INDEX idx_folders_name ON folders (lower(name));
ALTER TABLE folders DROP COLUMN parent_id;
