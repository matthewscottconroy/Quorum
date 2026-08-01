-- Expand the role ladder with 'restricted' (own-record-only) and 'superadmin'
-- (destructive deletes), and add dues reminder escalation tracking.

-- Widen the role CHECK before assigning any new role value.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_chk;
ALTER TABLE users
    ADD CONSTRAINT users_role_chk
        CHECK (role IN ('restricted', 'member', 'officer', 'admin', 'superadmin'));

-- Continuity for existing installs: the founding admin (earliest-created) is
-- promoted to superadmin so destructive deletes remain possible for someone.
-- No-op on a fresh database (bootstrap creates a superadmin directly).
UPDATE users
SET role = 'superadmin'
WHERE id = (
    SELECT id FROM users WHERE role = 'admin' ORDER BY created_at, id LIMIT 1
);

-- Reminder escalation state per invoice:
--   reminder_stage 0 = none sent, 1 = first notice, 2 = 7-day, 3 = final (30-day)
ALTER TABLE dues_invoices
    ADD COLUMN IF NOT EXISTS reminder_stage   INT         NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_reminder_at TIMESTAMPTZ;

-- Speeds the nightly scan for invoices due a reminder.
CREATE INDEX IF NOT EXISTS idx_dues_invoices_reminder
    ON dues_invoices (status, reminder_stage);
