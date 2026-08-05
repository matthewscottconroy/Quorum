DROP TABLE resource_files;
ALTER TABLE resources
    DROP COLUMN folder_id,
    DROP COLUMN file_name,
    DROP COLUMN file_size,
    DROP COLUMN file_sha256;
DROP TABLE folders;
