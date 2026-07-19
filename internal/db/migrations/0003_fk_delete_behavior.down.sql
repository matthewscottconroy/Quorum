DROP INDEX IF EXISTS idx_users_email_lower;

ALTER TABLE plan_decisions
    DROP CONSTRAINT IF EXISTS plan_decisions_decided_by_fkey,
    ADD CONSTRAINT plan_decisions_decided_by_fkey
        FOREIGN KEY (decided_by) REFERENCES users(id);

ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_recorded_by_fkey,
    ADD CONSTRAINT transactions_recorded_by_fkey
        FOREIGN KEY (recorded_by) REFERENCES users(id);

ALTER TABLE audit_log
    DROP CONSTRAINT IF EXISTS audit_log_user_id_fkey,
    ADD CONSTRAINT audit_log_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES users(id);

ALTER TABLE action_items
    DROP CONSTRAINT IF EXISTS action_items_plan_id_fkey,
    ADD CONSTRAINT action_items_plan_id_fkey
        FOREIGN KEY (plan_id) REFERENCES plans(id);

ALTER TABLE action_items
    DROP CONSTRAINT IF EXISTS action_items_meeting_id_fkey,
    ADD CONSTRAINT action_items_meeting_id_fkey
        FOREIGN KEY (meeting_id) REFERENCES meetings(id);
