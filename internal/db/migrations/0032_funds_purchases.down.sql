DROP FUNCTION gl_balance(UUID, TEXT);
DROP FUNCTION gl_next_fund_code();
DROP TRIGGER trg_purchase_guard ON purchase_requests;
DROP FUNCTION purchase_guard();
DROP TABLE purchase_approvals;
DROP TABLE purchase_requests;
DROP TABLE fund_signers;
DROP TABLE funds;

-- Purchase/transfer journal entries would violate the restored CHECK below
-- (and are deletable only once the tables referencing them are gone). The
-- journal is append-only by design; a schema rollback is the sanctioned
-- bulk rewrite, so the guards are disabled visibly for this step.
ALTER TABLE journal_entries DISABLE TRIGGER trg_journal_entries_guard;
ALTER TABLE journal_lines DISABLE TRIGGER trg_journal_lines_guard;
DELETE FROM journal_lines WHERE entry_id IN
    (SELECT id FROM journal_entries WHERE source_type IN ('purchase', 'fund_transfer'));
DELETE FROM journal_entries WHERE source_type IN ('purchase', 'fund_transfer');
ALTER TABLE journal_lines ENABLE TRIGGER trg_journal_lines_guard;
ALTER TABLE journal_entries ENABLE TRIGGER trg_journal_entries_guard;

ALTER TABLE journal_entries DROP CONSTRAINT journal_entries_source_type_check;
ALTER TABLE journal_entries ADD CONSTRAINT journal_entries_source_type_check
    CHECK (source_type IN ('invoice', 'payment', 'writeoff', 'unwaive', 'manual', 'backfill'));
