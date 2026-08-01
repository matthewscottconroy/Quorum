-- Fix foreign keys that made legitimate deletes impossible:
--   * deleting a meeting/plan failed once any action item referenced it
--   * deleting a user failed once they had audit_log rows (written on every
--     successful write request) or recorded transactions
-- These references are informational, so detach them on delete instead.

ALTER TABLE action_items
    DROP CONSTRAINT IF EXISTS action_items_meeting_id_fkey,
    ADD CONSTRAINT action_items_meeting_id_fkey
        FOREIGN KEY (meeting_id) REFERENCES meetings(id) ON DELETE SET NULL;

ALTER TABLE action_items
    DROP CONSTRAINT IF EXISTS action_items_plan_id_fkey,
    ADD CONSTRAINT action_items_plan_id_fkey
        FOREIGN KEY (plan_id) REFERENCES plans(id) ON DELETE SET NULL;

ALTER TABLE audit_log
    DROP CONSTRAINT IF EXISTS audit_log_user_id_fkey,
    ADD CONSTRAINT audit_log_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_recorded_by_fkey,
    ADD CONSTRAINT transactions_recorded_by_fkey
        FOREIGN KEY (recorded_by) REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE plan_decisions
    DROP CONSTRAINT IF EXISTS plan_decisions_decided_by_fkey,
    ADD CONSTRAINT plan_decisions_decided_by_fkey
        FOREIGN KEY (decided_by) REFERENCES users(id) ON DELETE SET NULL;

-- Case-insensitive email uniqueness. Fails loudly if two accounts already
-- differ only by case — the operator must merge them first.
UPDATE users SET email = lower(email);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_lower ON users (lower(email));
