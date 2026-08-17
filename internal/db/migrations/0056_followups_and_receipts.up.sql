-- Round-5 feature schema: closing the loops with members and the calendar.

-- Attachments: a transaction or bill can point at a supporting document
-- (check image, Zelle screenshot, vendor invoice PDF) in the resource
-- library — the same pattern purchase requests already use.
ALTER TABLE transactions ADD COLUMN resource_id UUID REFERENCES resources(id) ON DELETE SET NULL;
ALTER TABLE bills        ADD COLUMN resource_id UUID REFERENCES resources(id) ON DELETE SET NULL;

-- Late fees: at most one fee invoice per overdue invoice, tracked by a
-- pointer on the original so the nightly job is idempotent.
ALTER TABLE dues_invoices ADD COLUMN late_fee_invoice_id UUID REFERENCES dues_invoices(id) ON DELETE SET NULL;

-- Meeting reminders: sent once, ~two days ahead, marked here.
ALTER TABLE meetings ADD COLUMN reminder_sent_at TIMESTAMPTZ;

-- Action-item due reminders: emailed once per card, marked here.
ALTER TABLE action_items ADD COLUMN due_reminded_at TIMESTAMPTZ;
