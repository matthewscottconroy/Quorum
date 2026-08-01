-- Data-integrity constraints and indexes that the initial schema omitted.
-- Handlers already validate these enums in Go; the CHECK constraints make the
-- database the authoritative backstop against direct SQL or a future code path.

ALTER TABLE users
    ADD CONSTRAINT users_role_chk
        CHECK (role IN ('admin', 'officer', 'member'));

ALTER TABLE members
    ADD CONSTRAINT members_status_chk
        CHECK (status IN ('active', 'inactive', 'suspended'));

ALTER TABLE dues_invoices
    ADD CONSTRAINT dues_invoices_status_chk
        CHECK (status IN ('pending', 'paid', 'partial', 'overdue', 'waived'));

ALTER TABLE meetings
    ADD CONSTRAINT meetings_status_chk
        CHECK (status IN ('scheduled', 'completed', 'cancelled'));

ALTER TABLE meeting_decisions
    ADD CONSTRAINT meeting_decisions_outcome_chk
        CHECK (outcome IN ('passed', 'failed', 'tabled', 'noted'));

ALTER TABLE action_items
    ADD CONSTRAINT action_items_status_chk
        CHECK (status IN ('open', 'in_progress', 'done', 'cancelled'));

ALTER TABLE action_items
    ADD CONSTRAINT action_items_priority_chk
        CHECK (priority IN ('low', 'normal', 'high'));

ALTER TABLE plans
    ADD CONSTRAINT plans_status_chk
        CHECK (status IN ('draft', 'active', 'completed', 'archived'));

-- Refresh-token hashes must be unique for lookup correctness; promote the
-- non-unique index from migration 0002 to a unique one.
DROP INDEX IF EXISTS idx_refresh_tokens_hash;
CREATE UNIQUE INDEX idx_refresh_tokens_hash ON refresh_tokens (token_hash);

-- Support pruning of expired/revoked refresh tokens and old bookkeeping rows.
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_expires ON refresh_tokens (expires_at);
CREATE INDEX IF NOT EXISTS idx_processed_events_at    ON processed_events (processed_at);
CREATE INDEX IF NOT EXISTS idx_audit_log_created_at   ON audit_log (created_at);

-- Webhook dedup looks up transactions by provider_reference_id; index it so the
-- lookup is not a sequential scan (partial: most rows for manual entries are null).
CREATE INDEX IF NOT EXISTS idx_transactions_provider_ref
    ON transactions (provider_reference_id)
    WHERE provider_reference_id IS NOT NULL;

-- Tag filters use `$1 = ANY(tags)`; GIN indexes make array membership fast.
CREATE INDEX IF NOT EXISTS idx_contacts_tags  ON contacts  USING gin (tags);
CREATE INDEX IF NOT EXISTS idx_resources_tags ON resources USING gin (tags);
