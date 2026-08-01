-- Strengthen the audit trail so it can answer "who changed which record":
-- add a typed entity column and index (entity_id was previously untyped and
-- unindexed). The middleware now also captures the id of POST-created rows.
ALTER TABLE audit_log ADD COLUMN entity_type TEXT;
CREATE INDEX idx_audit_log_entity ON audit_log (entity_type, entity_id);
