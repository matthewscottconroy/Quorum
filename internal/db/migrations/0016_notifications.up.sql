-- In-app notifications with per-user email preferences. In-app notices are
-- always recorded (the bell shows them); email delivery is opt-out per category.

CREATE TABLE notifications (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Dotted event type, e.g. 'motion.opened', 'meeting.scheduled',
    -- 'action_item.assigned', 'dues.invoice_created'.
    type       TEXT NOT NULL,
    title      TEXT NOT NULL,
    body       TEXT,
    -- In-app hash route to open when the notice is clicked, e.g. '#/meetings'.
    link       TEXT,
    read_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- List a user's notices newest-first; partial index makes the unread badge cheap.
CREATE INDEX idx_notifications_user    ON notifications (user_id, created_at DESC);
CREATE INDEX idx_notifications_unread  ON notifications (user_id) WHERE read_at IS NULL;

-- One row per user; email delivery is enabled per category by default. Absence
-- of a row is treated as "all enabled" so existing users need no backfill.
CREATE TABLE notification_preferences (
    user_id           UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    governance_email  BOOLEAN NOT NULL DEFAULT TRUE,  -- motions, ballots
    meetings_email    BOOLEAN NOT NULL DEFAULT TRUE,  -- scheduled meetings
    dues_email        BOOLEAN NOT NULL DEFAULT TRUE,  -- invoices, overdue
    assignments_email BOOLEAN NOT NULL DEFAULT TRUE,  -- action items assigned to me
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
