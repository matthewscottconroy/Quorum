-- Recurring dues automation. A schedule defines the dues amount and billing
-- cadence for a member tier; the nightly job (and a manual "generate now"
-- action) materializes invoices for each active member of that tier for the
-- current period, skipping anyone who already has one — so it is idempotent.
CREATE TABLE dues_schedules (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tier         TEXT   NOT NULL,
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    currency     TEXT   NOT NULL DEFAULT 'USD',
    cadence      TEXT   NOT NULL CHECK (cadence IN ('annual', 'quarterly', 'monthly')),
    -- Days after the period start until an invoice is due (e.g. 30 → due Jan 31
    -- for an annual period beginning Jan 1).
    due_days     INTEGER NOT NULL DEFAULT 30 CHECK (due_days >= 0),
    active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_dues_schedules_active ON dues_schedules (active) WHERE active;
