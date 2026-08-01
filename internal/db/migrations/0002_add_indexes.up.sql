CREATE INDEX IF NOT EXISTS idx_refresh_tokens_hash    ON refresh_tokens (token_hash);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON refresh_tokens (user_id);
CREATE INDEX IF NOT EXISTS idx_transactions_member_id ON transactions   (member_id);
CREATE INDEX IF NOT EXISTS idx_dues_invoices_due_date ON dues_invoices  (due_date);
