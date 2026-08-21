-- Third GL integrity pass, closing three gaps the audits surfaced.

-- 1) gl_reconcile_ar was hard-wired to account 1300 while every posting flow
--    was rewired to the admin-repointable 'receivable' rule (0034). Repoint
--    the rule and the flagship reconcile verdict silently reported phantom
--    mismatches forever. The view now follows the same rule the postings use.
DROP VIEW gl_reconcile_ar;
CREATE VIEW gl_reconcile_ar AS
WITH gl AS (
    SELECT l.currency, sum(l.debit) - sum(l.credit) AS gl_ar
    FROM journal_lines l WHERE l.account_id = gl_rule('receivable')
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

-- 2) provider_status was an unconstrained nullable column read with opposite
--    NULL polarity by the app ("!= 'failed'" excludes NULL) and by the GL
--    ("IS DISTINCT FROM 'failed'" includes NULL). One NULL row would desync
--    the two ledgers permanently. Money that was recorded counts: normalize
--    NULLs to 'succeeded' and forbid new ones.
-- The append-only trigger guards business writes; this is a schema
-- normalization of a column the trigger predates, done inside the same
-- migration transaction — disable and restore around the backfill.
ALTER TABLE transactions DISABLE TRIGGER trg_transactions_append_only;
UPDATE transactions SET provider_status = 'succeeded' WHERE provider_status IS NULL;
ALTER TABLE transactions ENABLE TRIGGER trg_transactions_append_only;
ALTER TABLE transactions ALTER COLUMN provider_status SET DEFAULT 'succeeded';
ALTER TABLE transactions ALTER COLUMN provider_status SET NOT NULL;

-- 3) The GL posted every inserted transaction — including FAILED ones, where
--    no money moved. 0055 taught gl_invoice_remaining to exclude them but
--    left the posting trigger blind, so each failed row desynced GL cash and
--    receivable from the app's paid math.
CREATE OR REPLACE FUNCTION gl_post_payment() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE cash UUID; other UUID; amt BIGINT;
BEGIN
    IF NEW.provider_status = 'failed' THEN RETURN NEW; END IF;
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

-- 4) Close-month vs in-flight postings raced: an entry dated July 15 could
--    pass the period check while July's close committed, landing a posting
--    in a "closed" month. The period guard takes a SHARED advisory lock and
--    ClosePeriod (repo) takes the EXCLUSIVE side, so a close waits out
--    in-flight postings and postings after the close see the period row.
CREATE OR REPLACE FUNCTION journal_period_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_advisory_xact_lock_shared(hashtext('quorum.period_close'));
    IF EXISTS (SELECT 1 FROM accounting_periods
               WHERE month = date_trunc('month', NEW.entry_date)::date) THEN
        RAISE EXCEPTION 'accounting period % is closed: post into an open period instead',
            to_char(NEW.entry_date, 'YYYY-MM');
    END IF;
    RETURN NEW;
END $$;
