-- Uploaded documents in the resource library. A resource with file_name set
-- is a document; url stays for link resources (a resource may carry both).
-- Bytes live in PostgreSQL so "all state is in the database" stays true:
-- pg_dump backups, restore-verification, and disaster recovery cover
-- documents with no new moving parts. The blob sits in its own table so
-- list queries never touch file data.
CREATE TABLE folders (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_folders_name ON folders (lower(name));

ALTER TABLE resources
    ADD COLUMN folder_id   UUID REFERENCES folders(id) ON DELETE SET NULL,
    ADD COLUMN file_name   TEXT   CHECK (file_name IS NULL OR length(file_name) BETWEEN 1 AND 255),
    ADD COLUMN file_size   BIGINT CHECK (file_size IS NULL OR file_size >= 0),
    ADD COLUMN file_sha256 TEXT   CHECK (file_sha256 IS NULL OR file_sha256 ~ '^[0-9a-f]{64}$');
CREATE INDEX idx_resources_folder ON resources (folder_id);

CREATE TABLE resource_files (
    resource_id  UUID PRIMARY KEY REFERENCES resources(id) ON DELETE CASCADE,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    data         BYTEA NOT NULL
);
