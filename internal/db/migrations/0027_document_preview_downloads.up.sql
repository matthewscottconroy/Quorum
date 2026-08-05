-- Nested folders, preview-only documents, and the download ledger.

-- Folders become a tree: a deleted parent releases its children to the root
-- (never cascades into their documents). Cycle prevention is enforced in the
-- application with a recursive ancestor check before any re-parenting.
ALTER TABLE folders
    ADD COLUMN parent_id UUID REFERENCES folders(id) ON DELETE SET NULL;
CREATE INDEX idx_folders_parent ON folders (parent_id);

-- Names must be unique among siblings, not globally (two subtrees may both
-- contain "Minutes"). The zero UUID stands in for "root" so NULL parents
-- still collide with each other.
DROP INDEX idx_folders_name;
CREATE UNIQUE INDEX idx_folders_name
    ON folders (coalesce(parent_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name));

-- Preview-only documents can be rendered in the app but not downloaded.
ALTER TABLE resources
    ADD COLUMN file_preview_only BOOLEAN NOT NULL DEFAULT false;

-- Forensic ledger of every document download: who, when, from where, and the
-- SHA-256 of the exact bytes served (downloads of stampable text formats are
-- watermarked, so their hash differs per download — which is the point: a
-- file found in the wild maps back to the one download event that produced
-- it). References null out if the resource or user goes away; the copied
-- name and hash remain. Deliberately NOT subject to audit-log retention
-- pruning: provenance questions arrive years later.
CREATE TABLE document_downloads (
    id            UUID PRIMARY KEY,
    resource_id   UUID REFERENCES resources(id) ON DELETE SET NULL,
    user_id       UUID REFERENCES users(id) ON DELETE SET NULL,
    file_name     TEXT NOT NULL,
    sha256        TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    ip            TEXT NOT NULL DEFAULT '',
    downloaded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_doc_downloads_sha ON document_downloads (sha256);
CREATE INDEX idx_doc_downloads_resource ON document_downloads (resource_id, downloaded_at DESC);
