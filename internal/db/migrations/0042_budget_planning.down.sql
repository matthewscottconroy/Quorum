ALTER TABLE plans DROP COLUMN cost_currency, DROP COLUMN estimated_cost_minor;
ALTER TABLE budget_scenarios DROP CONSTRAINT budget_scenarios_period_order;
ALTER TABLE budget_scenarios DROP COLUMN ends_on, DROP COLUMN starts_on;
DROP INDEX idx_budget_lines_account;
ALTER TABLE budget_lines DROP COLUMN account_id;
