ALTER TABLE purchase_requests DROP COLUMN expense_account_id;
DROP TRIGGER trg_account_guard ON accounts;
DROP FUNCTION account_guard();
DROP TRIGGER trg_journal_period ON journal_entries;
DROP FUNCTION journal_period_guard();
DROP TABLE accounting_periods;
DROP TABLE org_settings;
