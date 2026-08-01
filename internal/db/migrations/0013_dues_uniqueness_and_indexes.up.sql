-- Prevent duplicate invoices for the same member+period (backstops the
-- recurring-dues generator's idempotency, which previously relied only on a
-- non-atomic NOT EXISTS check and could double-bill under concurrency).
CREATE UNIQUE INDEX idx_dues_invoices_member_period ON dues_invoices (member_id, period_label);

-- Indexes for the analytics/generation hot paths on tables that grow over time.
-- transactions.occurred_at: the reporting-currency probe (ORDER BY ... LIMIT 1),
-- the YTD sum, and the monthly collection series all filter/scan on it.
CREATE INDEX idx_transactions_occurred_at ON transactions (occurred_at);
-- members.joined_at: the monthly new-member growth series.
CREATE INDEX idx_members_joined_at ON members (joined_at);
-- Active members by tier: dues generation and the tier breakdown filter on both.
CREATE INDEX idx_members_active_tier ON members (tier) WHERE status = 'active';
