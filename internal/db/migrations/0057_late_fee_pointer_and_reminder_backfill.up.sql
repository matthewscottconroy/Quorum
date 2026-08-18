-- Late fees must never compound: a fee invoice carries a pointer to the
-- invoice it penalizes, and the nightly candidate query excludes anything
-- with the pointer set. The prior label-prefix convention ("Late fee — …")
-- was not machine-checked, so a fee invoice going overdue would itself be
-- fee'd — chaining fees forever and eventually overflowing the 200-char
-- period_label bound, which aborted the whole batch.
ALTER TABLE dues_invoices ADD COLUMN late_fee_for UUID REFERENCES dues_invoices(id) ON DELETE SET NULL;

-- Belt and braces for any pre-pointer fee invoices (dev databases only —
-- this ships in the same release as the feature): mark them by label.
UPDATE dues_invoices fee SET late_fee_for = orig.id
FROM dues_invoices orig
WHERE orig.late_fee_invoice_id = fee.id AND fee.late_fee_for IS NULL;

-- Action-item due reminders were added with no backfill, so the first
-- nightly run would email every assignee of every HISTORICALLY overdue
-- item ("Due today: …" for a card overdue since last year). Items already
-- past due at upgrade time are marked reminded; only items that come due
-- from here on trigger mail.
UPDATE action_items SET due_reminded_at = now()
WHERE due_date < CURRENT_DATE AND due_reminded_at IS NULL;
