ALTER TABLE action_items  DROP COLUMN IF EXISTS due_reminded_at;
ALTER TABLE meetings      DROP COLUMN IF EXISTS reminder_sent_at;
ALTER TABLE dues_invoices DROP COLUMN IF EXISTS late_fee_invoice_id;
ALTER TABLE bills         DROP COLUMN IF EXISTS resource_id;
ALTER TABLE transactions  DROP COLUMN IF EXISTS resource_id;
