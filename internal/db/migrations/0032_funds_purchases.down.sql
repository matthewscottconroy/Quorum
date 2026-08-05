DROP FUNCTION gl_balance(UUID, TEXT);
DROP FUNCTION gl_next_fund_code();
ALTER TABLE journal_entries DROP CONSTRAINT journal_entries_source_type_check;
ALTER TABLE journal_entries ADD CONSTRAINT journal_entries_source_type_check
    CHECK (source_type IN ('invoice', 'payment', 'writeoff', 'unwaive', 'manual', 'backfill'));
DROP TRIGGER trg_purchase_guard ON purchase_requests;
DROP FUNCTION purchase_guard();
DROP TABLE purchase_approvals;
DROP TABLE purchase_requests;
DROP TABLE fund_signers;
DROP TABLE funds;
