DROP VIEW gl_reconcile_ar;
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

ALTER TABLE transactions ALTER COLUMN provider_status DROP NOT NULL;
ALTER TABLE transactions ALTER COLUMN provider_status DROP DEFAULT;

CREATE OR REPLACE FUNCTION gl_post_payment() RETURNS trigger
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

CREATE OR REPLACE FUNCTION journal_period_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounting_periods
               WHERE month = date_trunc('month', NEW.entry_date)::date) THEN
        RAISE EXCEPTION 'accounting period % is closed: post into an open period instead',
            to_char(NEW.entry_date, 'YYYY-MM');
    END IF;
    RETURN NEW;
END $$;
