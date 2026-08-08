-- Counterparty billing: invoices to outside customers (contacts) and vendor
-- bills (accounts payable). Completes the four money quadrants:
-- member->org (dues), org->member (fund purchases), customer->org (this:
-- contact invoices), org->vendor (this: bills).

-- Invoices attach to EXACTLY ONE counterparty: a member (dues) or a contact
-- (services). Existing rows keep member_id; nothing changes for them.
ALTER TABLE dues_invoices
    ALTER COLUMN member_id DROP NOT NULL,
    ADD COLUMN contact_id UUID REFERENCES contacts(id),
    ADD CONSTRAINT invoice_one_counterparty CHECK ((member_id IS NULL) <> (contact_id IS NULL));
CREATE INDEX idx_invoices_contact ON dues_invoices (contact_id);

-- The frozen identity now includes the counterparty pair.
CREATE OR REPLACE FUNCTION invoice_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'dues_invoices cannot be deleted: cancel the invoice by status instead';
    END IF;
    IF NEW.amount IS DISTINCT FROM OLD.amount
       OR NEW.currency IS DISTINCT FROM OLD.currency
       OR NEW.member_id IS DISTINCT FROM OLD.member_id
       OR NEW.contact_id IS DISTINCT FROM OLD.contact_id
       OR NEW.period_label IS DISTINCT FROM OLD.period_label THEN
        RAISE EXCEPTION 'invoice identity (amount/currency/counterparty/period) is frozen after creation';
    END IF;
    RETURN NEW;
END $$;

-- Accounts + posting rules for the new flows, reusing existing accounts if
-- the org already created those codes.
DO $$
DECLARE ap UUID; svc UUID;
BEGIN
    SELECT id INTO ap FROM accounts WHERE code = '2000';
    IF ap IS NULL THEN
        INSERT INTO accounts (code, name, type) VALUES ('2000', 'Accounts Payable', 'liability')
        RETURNING id INTO ap;
    END IF;
    SELECT id INTO svc FROM accounts WHERE code = '4100';
    IF svc IS NULL THEN
        INSERT INTO accounts (code, name, type) VALUES ('4100', 'Service Income', 'income')
        RETURNING id INTO svc;
    END IF;
    INSERT INTO posting_rules (key, account_id) VALUES ('payable', ap)
        ON CONFLICT (key) DO NOTHING;
    INSERT INTO posting_rules (key, account_id) VALUES ('income.services', svc)
        ON CONFLICT (key) DO NOTHING;
END $$;

-- Contact invoices post to income.services; member invoices stay income.dues.
CREATE OR REPLACE FUNCTION gl_post_invoice() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM gl_post(NEW.created_at::date, 'Invoice ' || NEW.period_label,
        'invoice', NEW.id, gl_rule('receivable'),
        CASE WHEN NEW.contact_id IS NULL THEN gl_rule('income.dues') ELSE gl_rule('income.services') END,
        NEW.amount, NEW.currency);
    RETURN NEW;
END $$;

-- Vendor bills: accrue on creation (DR expense / CR A/P), pay later
-- (DR A/P / CR cash), or void (reversing entry). Cash-basis presentation is
-- unaffected: the accrual entry touches no cash account, so the cash-basis
-- statement ignores a bill until it is actually paid.
CREATE TABLE bills (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    contact_id         UUID NOT NULL REFERENCES contacts(id),
    amount             BIGINT NOT NULL CHECK (amount > 0),
    currency           TEXT NOT NULL,
    memo               TEXT CHECK (memo IS NULL OR length(memo) <= 300),
    expense_account_id UUID NOT NULL REFERENCES accounts(id),
    bill_date          DATE NOT NULL DEFAULT current_date,
    due_date           DATE,
    status             TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'paid', 'void')),
    accrual_entry_id   UUID REFERENCES journal_entries(id),
    payment_entry_id   UUID REFERENCES journal_entries(id),
    paid_at            TIMESTAMPTZ,
    created_by         UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_bills_status ON bills (status, due_date);

CREATE FUNCTION bill_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'bills are permanent records: void instead of deleting';
    END IF;
    IF OLD.status IN ('paid', 'void') THEN
        RAISE EXCEPTION 'bill is % and frozen', OLD.status;
    END IF;
    IF NEW.status = 'paid' AND NEW.payment_entry_id IS NULL THEN
        RAISE EXCEPTION 'a paid bill must reference its payment journal entry';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER trg_bill_guard BEFORE UPDATE OR DELETE ON bills
    FOR EACH ROW EXECUTE FUNCTION bill_guard();

ALTER TABLE journal_entries DROP CONSTRAINT journal_entries_source_type_check;
ALTER TABLE journal_entries ADD CONSTRAINT journal_entries_source_type_check
    CHECK (source_type IN ('invoice', 'payment', 'writeoff', 'unwaive', 'manual', 'backfill',
                           'purchase', 'fund_transfer', 'bill', 'bill_payment', 'bill_void'));
