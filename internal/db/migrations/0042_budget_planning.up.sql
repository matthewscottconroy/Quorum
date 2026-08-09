-- Budgeting & planning upgrades:
--   * budget lines can link to a GL account, enabling per-category
--     budget-vs-actual and budget context at spend-approval time;
--   * budget scenarios get real dates, enabling fiscal-year defaults and
--     time-elapsed proration in variance views;
--   * plans can carry a cost estimate, connecting planning to money.
-- All additive and nullable: existing rows are untouched.

ALTER TABLE budget_lines
    ADD COLUMN account_id UUID REFERENCES accounts(id) ON DELETE SET NULL;
CREATE INDEX idx_budget_lines_account ON budget_lines (account_id)
    WHERE account_id IS NOT NULL;

ALTER TABLE budget_scenarios
    ADD COLUMN starts_on DATE,
    ADD COLUMN ends_on   DATE,
    ADD CONSTRAINT budget_scenarios_period_order
        CHECK (starts_on IS NULL OR ends_on IS NULL OR ends_on >= starts_on);

ALTER TABLE plans
    ADD COLUMN estimated_cost_minor BIGINT CHECK (estimated_cost_minor IS NULL OR estimated_cost_minor >= 0),
    ADD COLUMN cost_currency TEXT CHECK (cost_currency IS NULL OR length(cost_currency) = 3);
