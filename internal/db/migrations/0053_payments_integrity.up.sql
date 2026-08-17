-- Payments integrity round.
--
-- 1) One ACTIVE dues schedule per tier. The nightly generator creates
--    invoices per active schedule, so two active schedules for one tier
--    bill every member in it twice (and budget seeding double-counts the
--    projected income). Deactivate all but the newest active schedule per
--    tier, then lock the invariant in with a partial unique index.
UPDATE dues_schedules s SET active = FALSE, updated_at = now()
WHERE s.active AND EXISTS (
    SELECT 1 FROM dues_schedules newer
    WHERE newer.tier = s.tier AND newer.active
      AND (newer.created_at, newer.id) > (s.created_at, s.id)
);

CREATE UNIQUE INDEX idx_dues_schedules_one_active_per_tier
    ON dues_schedules (tier) WHERE active;
