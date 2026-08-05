DROP FUNCTION gl_cash_accounts();

CREATE OR REPLACE FUNCTION gl_cash_account(p_provider TEXT) RETURNS UUID
LANGUAGE sql STABLE AS $$
    SELECT CASE lower(coalesce(p_provider, ''))
        WHEN 'stripe' THEN gl_account('1100')
        WHEN 'paypal' THEN gl_account('1200')
        ELSE gl_account('1000')
    END $$;

CREATE OR REPLACE FUNCTION gl_payment_credit_account(p_invoice UUID, p_currency TEXT) RETURNS UUID
LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN p_invoice IS NOT NULL
                 AND EXISTS (SELECT 1 FROM dues_invoices i
                             WHERE i.id = p_invoice AND i.currency = p_currency)
           THEN gl_account('1300') ELSE gl_account('4000') END $$;

CREATE OR REPLACE FUNCTION gl_post_invoice() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM gl_post(NEW.created_at::date, 'Invoice ' || NEW.period_label,
        'invoice', NEW.id, gl_account('1300'), gl_account('4000'), NEW.amount, NEW.currency);
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION gl_post_waive() RETURNS trigger
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

DROP FUNCTION gl_rule(TEXT);
DROP TABLE posting_rules;
