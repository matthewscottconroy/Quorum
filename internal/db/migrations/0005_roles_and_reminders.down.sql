DROP INDEX IF EXISTS idx_dues_invoices_reminder;

ALTER TABLE dues_invoices
    DROP COLUMN IF EXISTS last_reminder_at,
    DROP COLUMN IF EXISTS reminder_stage;

-- Collapse the new roles back into the original three so the narrower CHECK
-- can be restored.
UPDATE users SET role = 'admin'  WHERE role = 'superadmin';
UPDATE users SET role = 'member' WHERE role = 'restricted';

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_chk;
ALTER TABLE users
    ADD CONSTRAINT users_role_chk
        CHECK (role IN ('admin', 'officer', 'member'));
