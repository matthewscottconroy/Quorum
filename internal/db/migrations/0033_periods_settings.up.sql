-- Accounting Phase C foundations + org-agnostic configuration.
-- Nothing here assumes an org type, tax status, or calendar: periods are
-- plain months, statements take arbitrary ranges, and the fiscal-year start
-- is a setting that only drives defaults and labels.

-- Generic org settings (admin-managed, allowlisted in the handler).
CREATE TABLE org_settings (
    key        TEXT PRIMARY KEY CHECK (length(key) BETWEEN 1 AND 64),
    value      TEXT NOT NULL CHECK (length(value) <= 4000),
    updated_by UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Accounting periods: month granularity, created when first closed.
-- Closing locks POSTING DATES in that month; reopening (admin, audited) is
-- allowed because small orgs make late corrections - the CPA workflow is
-- close -> adjust via reopen or post into the open period.
CREATE TABLE accounting_periods (
    month     DATE PRIMARY KEY CHECK (month = date_trunc('month', month)::date),
    closed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_by UUID REFERENCES users(id) ON DELETE SET NULL
);

CREATE FUNCTION journal_period_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounting_periods
               WHERE month = date_trunc('month', NEW.entry_date)::date) THEN
        RAISE EXCEPTION 'accounting period % is closed: post into an open period instead',
            to_char(NEW.entry_date, 'YYYY-MM');
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER trg_journal_period BEFORE INSERT ON journal_entries
    FOR EACH ROW EXECUTE FUNCTION journal_period_guard();

-- The chart is editable, but history is not: once an account has lines, its
-- code and type are frozen (rename and deactivate stay allowed), and it can
-- never be deleted (RESTRICT already guarantees that with lines; forbid
-- outright for uniformity).
CREATE FUNCTION account_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'accounts cannot be deleted: deactivate instead';
    END IF;
    IF (NEW.code <> OLD.code OR NEW.type <> OLD.type)
       AND EXISTS (SELECT 1 FROM journal_lines l WHERE l.account_id = OLD.id) THEN
        RAISE EXCEPTION 'account % has postings: code and type are frozen', OLD.code;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER trg_account_guard BEFORE UPDATE OR DELETE ON accounts
    FOR EACH ROW EXECUTE FUNCTION account_guard();

-- Purchases may target a specific expense account (defaults to 5000 at
-- completion when unset) - org-defined spending categories.
ALTER TABLE purchase_requests
    ADD COLUMN expense_account_id UUID REFERENCES accounts(id);
