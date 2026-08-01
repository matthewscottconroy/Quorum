DROP INDEX IF EXISTS idx_audit_log_entity;
ALTER TABLE audit_log DROP COLUMN IF EXISTS entity_type;
