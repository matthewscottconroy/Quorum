ALTER TABLE dues_invoices DROP COLUMN IF EXISTS late_fee_for;
-- The reminder backfill is not reversed: un-marking would re-arm the
-- first-night email blast the up migration exists to prevent.
