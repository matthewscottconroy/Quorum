DROP INDEX IF EXISTS idx_resources_tags;
DROP INDEX IF EXISTS idx_contacts_tags;
DROP INDEX IF EXISTS idx_transactions_provider_ref;
DROP INDEX IF EXISTS idx_audit_log_created_at;
DROP INDEX IF EXISTS idx_processed_events_at;
DROP INDEX IF EXISTS idx_refresh_tokens_expires;

-- Restore the non-unique refresh-token hash index from migration 0002.
DROP INDEX IF EXISTS idx_refresh_tokens_hash;
CREATE INDEX idx_refresh_tokens_hash ON refresh_tokens (token_hash);

ALTER TABLE plans             DROP CONSTRAINT IF EXISTS plans_status_chk;
ALTER TABLE action_items      DROP CONSTRAINT IF EXISTS action_items_priority_chk;
ALTER TABLE action_items      DROP CONSTRAINT IF EXISTS action_items_status_chk;
ALTER TABLE meeting_decisions DROP CONSTRAINT IF EXISTS meeting_decisions_outcome_chk;
ALTER TABLE meetings          DROP CONSTRAINT IF EXISTS meetings_status_chk;
ALTER TABLE dues_invoices     DROP CONSTRAINT IF EXISTS dues_invoices_status_chk;
ALTER TABLE members           DROP CONSTRAINT IF EXISTS members_status_chk;
ALTER TABLE users             DROP CONSTRAINT IF EXISTS users_role_chk;
