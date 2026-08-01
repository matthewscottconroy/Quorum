-- Restore the pre-0017 ON DELETE SET NULL behavior (as set by 0003).
ALTER TABLE audit_log
    DROP CONSTRAINT IF EXISTS audit_log_user_id_fkey,
    ADD CONSTRAINT audit_log_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_recorded_by_fkey,
    ADD CONSTRAINT transactions_recorded_by_fkey
        FOREIGN KEY (recorded_by) REFERENCES users(id) ON DELETE SET NULL;

DROP TRIGGER IF EXISTS trg_invoice_guard ON dues_invoices;
DROP FUNCTION IF EXISTS invoice_guard();
DROP TRIGGER IF EXISTS trg_transactions_append_only ON transactions;
DROP FUNCTION IF EXISTS ledger_block_mutation();
DROP TRIGGER IF EXISTS trg_audit_log_guard_delete ON audit_log;
DROP FUNCTION IF EXISTS audit_log_guard_delete();
DROP TRIGGER IF EXISTS trg_audit_log_no_update ON audit_log;
DROP FUNCTION IF EXISTS audit_log_block_update();
DROP TRIGGER IF EXISTS trg_audit_log_chain ON audit_log;
DROP FUNCTION IF EXISTS audit_log_chain();
DROP INDEX IF EXISTS idx_audit_log_seq;
ALTER TABLE audit_log
    DROP COLUMN IF EXISTS entry_hash,
    DROP COLUMN IF EXISTS prev_hash,
    DROP COLUMN IF EXISTS seq;
DROP SEQUENCE IF EXISTS audit_log_seq_seq;
DROP FUNCTION IF EXISTS audit_entry_digest(bigint, uuid, text, text, text, timestamptz, text);
DROP EXTENSION IF EXISTS pgcrypto;
