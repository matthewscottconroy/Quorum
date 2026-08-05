-- CPA-accounting Phase A: the double-entry general ledger core.
-- (roadmap/cpa-accounting.md §2.1–2.2)
--
-- Design decisions, deliberate:
--  * Posting rules live in DATABASE TRIGGERS on the existing dues tables, so
--    every invoice/payment/waiver posts balanced journal lines in the same
--    transaction that creates it — completeness is structural. Nothing can
--    reach the subledger without reaching the books, even via direct SQL.
--  * The journal is append-only (like the transactions ledger); corrections
--    are reversing entries. Debits must equal credits per entry per currency,
--    enforced by a deferred constraint trigger at COMMIT.
--  * Tamper-evidence v1 piggybacks on the existing audit hash chain (the
--    actions that cause postings are already chained); a dedicated GL chain
--    is a Phase D hardening item.
--  * Lines carry their natural currency; the trial balance reports per
--    currency. Functional-currency translation is a CPA decision (Phase C).

CREATE TABLE accounts (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code       TEXT NOT NULL CHECK (code ~ '^[0-9]{4}$'),
    name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    type       TEXT NOT NULL CHECK (type IN ('asset', 'liability', 'net_assets', 'income', 'expense')),
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_accounts_code ON accounts (code);

CREATE TABLE journal_entries (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    seq         BIGSERIAL,
    entry_date  DATE NOT NULL DEFAULT current_date,
    memo        TEXT NOT NULL CHECK (length(memo) BETWEEN 1 AND 300),
    source_type TEXT NOT NULL CHECK (source_type IN ('invoice', 'payment', 'writeoff', 'unwaive', 'manual', 'backfill')),
    source_id   UUID,
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_journal_entries_source ON journal_entries (source_type, source_id);
CREATE INDEX idx_journal_entries_date ON journal_entries (entry_date);

CREATE TABLE journal_lines (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entry_id   UUID NOT NULL REFERENCES journal_entries(id) ON DELETE RESTRICT,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    currency   TEXT NOT NULL,
    debit      BIGINT NOT NULL DEFAULT 0 CHECK (debit >= 0),
    credit     BIGINT NOT NULL DEFAULT 0 CHECK (credit >= 0),
    CHECK ((debit = 0) <> (credit = 0))  -- exactly one side, never both/neither
);
CREATE INDEX idx_journal_lines_entry ON journal_lines (entry_id);
CREATE INDEX idx_journal_lines_account ON journal_lines (account_id, currency);

-- Append-only: the journal is history, not state.
CREATE FUNCTION journal_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'the journal is append-only: % on % is not allowed; post a reversing entry instead', TG_OP, TG_TABLE_NAME;
END $$;
CREATE TRIGGER trg_journal_entries_guard BEFORE UPDATE OR DELETE ON journal_entries
    FOR EACH ROW EXECUTE FUNCTION journal_guard();
CREATE TRIGGER trg_journal_lines_guard BEFORE UPDATE OR DELETE ON journal_lines
    FOR EACH ROW EXECUTE FUNCTION journal_guard();

-- Debits = credits per entry per currency, checked at COMMIT so multi-line
-- entries can be assembled freely inside a transaction.
CREATE FUNCTION journal_balance_check() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE bad INT;
BEGIN
    SELECT count(*) INTO bad FROM (
        SELECT currency FROM journal_lines WHERE entry_id = NEW.entry_id
        GROUP BY currency HAVING sum(debit) <> sum(credit)
    ) x;
    IF bad > 0 THEN
        RAISE EXCEPTION 'journal entry % does not balance: debits must equal credits per currency', NEW.entry_id;
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER trg_journal_balance
    AFTER INSERT ON journal_lines
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION journal_balance_check();

-- Starter chart of accounts (editable later; codes are stable anchors).
INSERT INTO accounts (code, name, type) VALUES
    ('1000', 'Cash - Operating',       'asset'),
    ('1100', 'Cash - Stripe',          'asset'),
    ('1200', 'Cash - PayPal',          'asset'),
    ('1300', 'Accounts Receivable',    'asset'),
    ('3000', 'Net Assets',             'net_assets'),
    ('4000', 'Dues Income',            'income'),
    ('4900', 'Dues Written Off',       'income'),
    ('5000', 'General Expenses',       'expense');

CREATE FUNCTION gl_account(p_code TEXT) RETURNS UUID
LANGUAGE sql STABLE AS $$ SELECT id FROM accounts WHERE code = p_code $$;

CREATE FUNCTION gl_cash_account(p_provider TEXT) RETURNS UUID
LANGUAGE sql STABLE AS $$
    SELECT CASE lower(coalesce(p_provider, ''))
        WHEN 'stripe' THEN gl_account('1100')
        WHEN 'paypal' THEN gl_account('1200')
        ELSE gl_account('1000')
    END $$;

-- One entry + two lines, the only way postings are written.
CREATE FUNCTION gl_post(p_date DATE, p_memo TEXT, p_source_type TEXT, p_source_id UUID,
                        p_debit_acct UUID, p_credit_acct UUID, p_amount BIGINT, p_currency TEXT)
RETURNS UUID
LANGUAGE plpgsql AS $$
DECLARE eid UUID;
BEGIN
    IF p_amount <= 0 THEN
        RAISE EXCEPTION 'gl_post amount must be positive (got %)', p_amount;
    END IF;
    INSERT INTO journal_entries (entry_date, memo, source_type, source_id)
        VALUES (p_date, left(p_memo, 300), p_source_type, p_source_id) RETURNING id INTO eid;
    INSERT INTO journal_lines (entry_id, account_id, currency, debit)  VALUES (eid, p_debit_acct,  p_currency, p_amount);
    INSERT INTO journal_lines (entry_id, account_id, currency, credit) VALUES (eid, p_credit_acct, p_currency, p_amount);
    RETURN eid;
END $$;

-- Posting rule: invoice created -> DR Accounts Receivable / CR Dues Income.
CREATE FUNCTION gl_post_invoice() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM gl_post(NEW.created_at::date, 'Invoice ' || NEW.period_label,
        'invoice', NEW.id, gl_account('1300'), gl_account('4000'), NEW.amount, NEW.currency);
    RETURN NEW;
END $$;
CREATE TRIGGER trg_gl_invoice AFTER INSERT ON dues_invoices
    FOR EACH ROW EXECUTE FUNCTION gl_post_invoice();

-- Posting rule: payment recorded -> DR Cash(provider) / CR A/R when linked
-- to an invoice IN THE INVOICE'S CURRENCY; otherwise CR Dues Income (an
-- unlinked payment, or one in a different currency, never reduces a
-- receivable - mirroring the application's same-currency paid rule).
-- Negative amounts are correcting entries and post reversed.
CREATE FUNCTION gl_payment_credit_account(p_invoice UUID, p_currency TEXT) RETURNS UUID
LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN p_invoice IS NOT NULL
                 AND EXISTS (SELECT 1 FROM dues_invoices i
                             WHERE i.id = p_invoice AND i.currency = p_currency)
           THEN gl_account('1300') ELSE gl_account('4000') END $$;

CREATE FUNCTION gl_post_payment() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE cash UUID; other UUID; amt BIGINT;
BEGIN
    cash := gl_cash_account(NEW.provider);
    other := gl_payment_credit_account(NEW.invoice_id, NEW.currency);
    amt := NEW.amount;
    IF amt = 0 THEN RETURN NEW; END IF;
    IF amt > 0 THEN
        PERFORM gl_post(NEW.occurred_at::date, 'Payment via ' || NEW.provider,
            'payment', NEW.id, cash, other, amt, NEW.currency);
    ELSE
        PERFORM gl_post(NEW.occurred_at::date, 'Payment correction via ' || NEW.provider,
            'payment', NEW.id, other, cash, -amt, NEW.currency);
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER trg_gl_payment AFTER INSERT ON transactions
    FOR EACH ROW EXECUTE FUNCTION gl_post_payment();

-- Remaining receivable of one invoice (same-currency payments only, matching
-- the application's paid-status rule).
CREATE FUNCTION gl_invoice_remaining(p_invoice UUID) RETURNS BIGINT
LANGUAGE sql STABLE AS $$
    SELECT i.amount - coalesce((SELECT sum(t.amount) FROM transactions t
                                WHERE t.invoice_id = i.id AND t.currency = i.currency), 0)
    FROM dues_invoices i WHERE i.id = p_invoice $$;

-- Posting rule: waive -> write off the remaining balance (DR contra-income /
-- CR A/R); un-waive -> reverse it. Symmetric so the invariant survives
-- status flip-flops.
CREATE FUNCTION gl_post_waive() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE remaining BIGINT;
BEGIN
    remaining := gl_invoice_remaining(NEW.id);
    IF NEW.status = 'waived' AND OLD.status <> 'waived' AND remaining > 0 THEN
        PERFORM gl_post(current_date, 'Write-off: invoice ' || NEW.period_label,
            'writeoff', NEW.id, gl_account('4900'), gl_account('1300'), remaining, NEW.currency);
    ELSIF OLD.status = 'waived' AND NEW.status <> 'waived' AND remaining > 0 THEN
        PERFORM gl_post(current_date, 'Reinstate: invoice ' || NEW.period_label,
            'unwaive', NEW.id, gl_account('1300'), gl_account('4900'), remaining, NEW.currency);
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER trg_gl_waive AFTER UPDATE OF status ON dues_invoices
    FOR EACH ROW EXECUTE FUNCTION gl_post_waive();

-- Backfill: derive opening entries from the existing subledger so the books
-- start complete. Ordered by original timestamps; memos say Backfill.
DO $$
DECLARE r RECORD;
BEGIN
    FOR r IN SELECT * FROM dues_invoices ORDER BY created_at, id LOOP
        PERFORM gl_post(r.created_at::date, 'Backfill invoice ' || r.period_label,
            'backfill', r.id, gl_account('1300'), gl_account('4000'), r.amount, r.currency);
    END LOOP;
    FOR r IN SELECT * FROM transactions WHERE amount <> 0 ORDER BY occurred_at, id LOOP
        IF r.amount > 0 THEN
            PERFORM gl_post(r.occurred_at::date, 'Backfill payment via ' || r.provider, 'backfill', r.id,
                gl_cash_account(r.provider),
                gl_payment_credit_account(r.invoice_id, r.currency),
                r.amount, r.currency);
        ELSE
            PERFORM gl_post(r.occurred_at::date, 'Backfill correction via ' || r.provider, 'backfill', r.id,
                gl_payment_credit_account(r.invoice_id, r.currency),
                gl_cash_account(r.provider), -r.amount, r.currency);
        END IF;
    END LOOP;
    FOR r IN SELECT i.* FROM dues_invoices i WHERE i.status = 'waived'
             AND gl_invoice_remaining(i.id) > 0 ORDER BY i.created_at, i.id LOOP
        PERFORM gl_post(current_date, 'Backfill write-off: invoice ' || r.period_label,
            'backfill', r.id, gl_account('4900'), gl_account('1300'), gl_invoice_remaining(r.id), r.currency);
    END LOOP;
END $$;

-- The perpetual invariant: GL Accounts Receivable == subledger receivable,
-- per currency. Zero rows from this view = books reconcile.
CREATE VIEW gl_reconcile_ar AS
WITH gl AS (
    SELECT l.currency, sum(l.debit) - sum(l.credit) AS gl_ar
    FROM journal_lines l WHERE l.account_id = gl_account('1300')
    GROUP BY l.currency
), sub AS (
    SELECT i.currency,
           sum(CASE WHEN i.status = 'waived' THEN 0 ELSE gl_invoice_remaining(i.id) END) AS sub_ar
    FROM dues_invoices i GROUP BY i.currency
)
SELECT coalesce(gl.currency, sub.currency) AS currency,
       coalesce(gl.gl_ar, 0) AS gl_ar, coalesce(sub.sub_ar, 0) AS sub_ar
FROM gl FULL OUTER JOIN sub ON sub.currency = gl.currency
WHERE coalesce(gl.gl_ar, 0) <> coalesce(sub.sub_ar, 0);
