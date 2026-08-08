DROP TRIGGER trg_bill_guard ON bills;
DROP FUNCTION bill_guard();
DROP TABLE bills;

-- Bill-sourced journal entries would violate the restored CHECK below. The
-- journal is append-only by design; a schema rollback is the sanctioned bulk
-- rewrite, so the guards are disabled visibly for this step.
ALTER TABLE journal_entries DISABLE TRIGGER trg_journal_entries_guard;
ALTER TABLE journal_lines DISABLE TRIGGER trg_journal_lines_guard;
DELETE FROM journal_lines WHERE entry_id IN
    (SELECT id FROM journal_entries WHERE source_type IN ('bill', 'bill_payment', 'bill_void'));
DELETE FROM journal_entries WHERE source_type IN ('bill', 'bill_payment', 'bill_void');
ALTER TABLE journal_lines ENABLE TRIGGER trg_journal_lines_guard;
ALTER TABLE journal_entries ENABLE TRIGGER trg_journal_entries_guard;

ALTER TABLE journal_entries DROP CONSTRAINT journal_entries_source_type_check;
ALTER TABLE journal_entries ADD CONSTRAINT journal_entries_source_type_check
    CHECK (source_type IN ('invoice', 'payment', 'writeoff', 'unwaive', 'manual', 'backfill',
                           'purchase', 'fund_transfer'));

CREATE OR REPLACE FUNCTION gl_post_invoice() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM gl_post(NEW.created_at::date, 'Invoice ' || NEW.period_label,
        'invoice', NEW.id, gl_rule('receivable'), gl_rule('income.dues'), NEW.amount, NEW.currency);
    RETURN NEW;
END $$;

DELETE FROM posting_rules WHERE key IN ('payable', 'income.services');

CREATE OR REPLACE FUNCTION invoice_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'dues_invoices cannot be deleted: cancel the invoice by status instead';
    END IF;
    IF NEW.amount IS DISTINCT FROM OLD.amount
       OR NEW.currency IS DISTINCT FROM OLD.currency
       OR NEW.member_id IS DISTINCT FROM OLD.member_id
       OR NEW.period_label IS DISTINCT FROM OLD.period_label THEN
        RAISE EXCEPTION 'invoice identity (amount/currency/member/period) is frozen after creation';
    END IF;
    RETURN NEW;
END $$;

-- invoice_guard forbids deletes; a schema rollback is the sanctioned bulk
-- rewrite, so disable it visibly for this step (0017's documented pattern).
ALTER TABLE dues_invoices DISABLE TRIGGER trg_invoice_guard;
ALTER TABLE transactions DISABLE TRIGGER trg_transactions_append_only;
ALTER TABLE journal_entries DISABLE TRIGGER trg_journal_entries_guard;
ALTER TABLE journal_lines DISABLE TRIGGER trg_journal_lines_guard;
-- The GL trace of contact invoices (and their payments) goes with them, so
-- the AR invariant still reconciles against the member-only subledger.
DELETE FROM journal_lines WHERE entry_id IN (
    SELECT id FROM journal_entries
    WHERE (source_type = 'invoice' AND source_id IN
              (SELECT id FROM dues_invoices WHERE contact_id IS NOT NULL))
       OR (source_type IN ('payment', 'writeoff', 'unwaive') AND source_id IN (
              SELECT t.id FROM transactions t
              JOIN dues_invoices di ON di.id = t.invoice_id WHERE di.contact_id IS NOT NULL))
       OR (source_type IN ('writeoff', 'unwaive') AND source_id IN
              (SELECT id FROM dues_invoices WHERE contact_id IS NOT NULL)));
DELETE FROM journal_entries WHERE id NOT IN (SELECT DISTINCT entry_id FROM journal_lines);
DELETE FROM transactions WHERE invoice_id IN (SELECT id FROM dues_invoices WHERE contact_id IS NOT NULL);
DELETE FROM dues_invoices WHERE contact_id IS NOT NULL;
ALTER TABLE journal_lines ENABLE TRIGGER trg_journal_lines_guard;
ALTER TABLE journal_entries ENABLE TRIGGER trg_journal_entries_guard;
ALTER TABLE transactions ENABLE TRIGGER trg_transactions_append_only;
ALTER TABLE dues_invoices ENABLE TRIGGER trg_invoice_guard;
ALTER TABLE dues_invoices
    DROP CONSTRAINT invoice_one_counterparty,
    DROP COLUMN contact_id;
UPDATE dues_invoices SET member_id = member_id; -- no-op keeps ordering clear
ALTER TABLE dues_invoices ALTER COLUMN member_id SET NOT NULL;
