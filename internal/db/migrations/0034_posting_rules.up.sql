-- Admin-configurable posting rules: the account mappings the automatic
-- posting triggers use, moved from code into data so an accountant (via an
-- admin) reconfigures the books without code edits. Rules affect FUTURE
-- postings only — history is immutable as always.
--
-- Keys: receivable, income.dues, income.unlinked, writeoff, cash.operating,
-- expense.default, and cash.provider.<provider> for any payment provider
-- label (free text elsewhere in the system). Missing provider keys fall
-- back to cash.operating.

CREATE TABLE posting_rules (
    key        TEXT PRIMARY KEY CHECK (key ~ '^[a-z0-9_.]{3,64}$'),
    account_id UUID NOT NULL REFERENCES accounts(id),
    updated_by UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO posting_rules (key, account_id) VALUES
    ('receivable',           gl_account('1300')),
    ('income.dues',          gl_account('4000')),
    ('income.unlinked',      gl_account('4000')),
    ('writeoff',             gl_account('4900')),
    ('cash.operating',       gl_account('1000')),
    ('expense.default',      gl_account('5000')),
    ('cash.provider.stripe', gl_account('1100')),
    ('cash.provider.paypal', gl_account('1200'));

CREATE FUNCTION gl_rule(p_key TEXT) RETURNS UUID
LANGUAGE sql STABLE AS $$ SELECT account_id FROM posting_rules WHERE key = p_key $$;

-- Rewire the posting helpers onto rules (same signatures; old behavior is
-- the seed data, so nothing changes until an admin edits a rule).
CREATE OR REPLACE FUNCTION gl_cash_account(p_provider TEXT) RETURNS UUID
LANGUAGE sql STABLE AS $$
    SELECT coalesce(gl_rule('cash.provider.' || lower(coalesce(p_provider, ''))),
                    gl_rule('cash.operating')) $$;

CREATE OR REPLACE FUNCTION gl_payment_credit_account(p_invoice UUID, p_currency TEXT) RETURNS UUID
LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN p_invoice IS NOT NULL
                 AND EXISTS (SELECT 1 FROM dues_invoices i
                             WHERE i.id = p_invoice AND i.currency = p_currency)
           THEN gl_rule('receivable') ELSE gl_rule('income.unlinked') END $$;

CREATE OR REPLACE FUNCTION gl_post_invoice() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM gl_post(NEW.created_at::date, 'Invoice ' || NEW.period_label,
        'invoice', NEW.id, gl_rule('receivable'), gl_rule('income.dues'), NEW.amount, NEW.currency);
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION gl_post_waive() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE remaining BIGINT;
BEGIN
    remaining := gl_invoice_remaining(NEW.id);
    IF NEW.status = 'waived' AND OLD.status <> 'waived' AND remaining > 0 THEN
        PERFORM gl_post(current_date, 'Write-off: invoice ' || NEW.period_label,
            'writeoff', NEW.id, gl_rule('writeoff'), gl_rule('receivable'), remaining, NEW.currency);
    ELSIF OLD.status = 'waived' AND NEW.status <> 'waived' AND remaining > 0 THEN
        PERFORM gl_post(current_date, 'Reinstate: invoice ' || NEW.period_label,
            'unwaive', NEW.id, gl_rule('receivable'), gl_rule('writeoff'), remaining, NEW.currency);
    END IF;
    RETURN NEW;
END $$;

-- Cash-set helper for cash-basis reporting: every account referenced by a
-- cash.* rule plus every fund cash account.
CREATE FUNCTION gl_cash_accounts() RETURNS SETOF UUID
LANGUAGE sql STABLE AS $$
    SELECT account_id FROM posting_rules WHERE key LIKE 'cash.%'
    UNION SELECT cash_account_id FROM funds $$;
